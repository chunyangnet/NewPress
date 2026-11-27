// pluginmarket builds the public, signed official catalog. It never publishes
// files or sends signing keys to a remote service.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/licensing"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	keygen := flag.Bool("keygen", false, "create a signing key; never overwrite")
	keyPath := flag.String("key", "", "private key file outside source/public repositories")
	out := flag.String("out", "", "new output directory")
	sequence := flag.Int64("sequence", 0, "monotonically increasing catalog sequence")
	expiry := flag.String("expires", "", "UTC expiry, RFC3339")
	extraPackages := flag.String("packages", "", "comma-separated reviewed plugin ZIP files (bundled host plugins require schema 2)")
	themePackages := flag.String("themes", "", "comma-separated reviewed theme ZIP files (schema 2)")
	schema := flag.Int("schema", 2, "catalog schema: 2 for extension OTA")
	channel := flag.String("channel", "stable", "default release channel: stable or preview (schema 2)")
	compatible := flag.Bool("data-compatible", false, "publisher confirms updates preserve data compatibility (schema 2)")
	notes := flag.String("notes", "", "UTF-8 release notes file, used as the default for schema 2")
	metadata := flag.String("metadata", "", "per-release JSON metadata keyed by kind/slug@version (schema 2)")
	catalogName := flag.String("catalog-name", "catalog.extensions-v5.json", "catalog filename")
	themesOnly := flag.Bool("themes-only", false, "publish a theme-only catalog without bundled plugins (schema 2)")
	noBundled := flag.Bool("no-bundled", false, "omit the generated comment-origin package; preserve reviewed historical ZIPs with -packages")
	flag.Parse()
	if *themesOnly && (*schema != 2 || *extraPackages != "") {
		return errors.New("-themes-only requires schema 2 and no plugin packages")
	}
	if *schema != 2 {
		return errors.New("catalog schema must be 2")
	}
	if *keyPath == "" {
		return errors.New("-key is required")
	}
	if filepath.Base(*catalogName) != *catalogName || !strings.HasSuffix(*catalogName, ".json") || strings.ContainsAny(*catalogName, "/\\") {
		return errors.New("invalid catalog filename")
	}
	if *keygen {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		if err = writeNew(*keyPath, []byte(base64.StdEncoding.EncodeToString(private)+"\n"), 0o600); err != nil {
			return err
		}
		if err = writeNew(*keyPath+".pub", []byte(base64.StdEncoding.EncodeToString(public)+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Println("Created private key and .pub file. Keep the private key outside every public repository.")
		return nil
	}
	expiration, err := time.Parse(time.RFC3339, *expiry)
	if err != nil || !expiration.After(time.Now()) || *sequence < 1 || *out == "" {
		return errors.New("-out, positive -sequence and future -expires RFC3339 are required")
	}
	key, err := os.ReadFile(*keyPath)
	if err != nil {
		return err
	}
	private, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(key)))
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return errors.New("invalid Ed25519 private key")
	}
	if err = os.Mkdir(*out, 0o750); err != nil {
		return err
	}
	catalog := plugin.Catalog{Schema: *schema, Sequence: *sequence, Expires: expiration.UTC(), Plugins: []plugin.Release{}}
	identity := func(m plugin.Manifest) string {
		return m.Slug + "@" + m.Version
	}
	seen := map[string]bool{}
	if !*themesOnly && !*noBundled {
		manifest, archive, err := commentOriginPackage()
		if err != nil {
			return err
		}
		packagePath := "packages/" + manifest.Slug + "-" + manifest.Version + ".zip"
		if err = packagefs.MkdirAllNoSymlink(filepath.Join(*out, "packages"), 0o750); err != nil {
			return err
		}
		if err = writeNew(filepath.Join(*out, filepath.FromSlash(packagePath)), archive, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(archive)
		catalog.Plugins = append(catalog.Plugins, plugin.Release{Manifest: manifest, Path: packagePath, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(archive)), Notes: "官方评论来源插件；补齐 Apache-2.0 全文及宿主第三方声明，业务功能不变。需要已注册 1.0.1 描述的配套主程序。"})
		seen[identity(manifest)] = true
	}
	for _, filename := range strings.Split(*extraPackages, ",") {
		filename = strings.TrimSpace(filename)
		if filename == "" {
			continue
		}
		releaseManifest, err := packagefs.ReadJSON[plugin.Manifest](filename, "plugin.json")
		if err != nil {
			return err
		}
		if err = plugin.ValidateManifest(releaseManifest); err != nil {
			return err
		}
		bundled := isReviewedBuiltin(releaseManifest)
		if seen[identity(releaseManifest)] || !bundled && (releaseManifest.Runtime != "wasm/core-v1" || len(releaseManifest.Hooks) == 0) {
			return errors.New("extra packages must be distinct executable WASM plugins or exact registered host plugins")
		}
		seen[identity(releaseManifest)] = true
		f, info, err := packagefs.OpenRegularNoSymlink(filename)
		if err != nil {
			return err
		}
		if info.Size() > 32<<20 {
			f.Close()
			return errors.New("plugin package exceeds 32 MiB")
		}
		payload, err := io.ReadAll(io.LimitReader(f, 32<<20+1))
		f.Close()
		if err != nil || len(payload) > 32<<20 {
			return errors.New("read plugin package failed")
		}
		temp, err := os.MkdirTemp("", "newpress-release-probe-")
		if err != nil {
			return err
		}
		snapshot := filepath.Join(temp, "package.zip")
		if err = os.WriteFile(snapshot, payload, 0600); err != nil {
			os.RemoveAll(temp)
			return err
		}
		verified, err := plugin.NewManager(temp).InstallPackage(snapshot)
		os.RemoveAll(temp)
		if err != nil || !reflect.DeepEqual(verified, releaseManifest) {
			return errors.New("package runtime validation failed or package changed")
		}
		digest := sha256.Sum256(payload)
		path := "packages/" + releaseManifest.Slug + "-" + releaseManifest.Version + ".zip"
		releaseNotes := "独立 WASM 插件；安装后保存设置与权限，再启用。"
		if bundled {
			path = "packages/" + releaseManifest.Slug + "/" + releaseManifest.Version + "/" + releaseManifest.Slug + "-" + releaseManifest.Version + ".zip"
			releaseNotes = "宿主插件；需要已注册对应运行时的 NewPress 主程序及配套主题，安装后默认停用。业务功能随主程序升级。"
		}
		if releaseManifest.Slug == plugin.CommentOrigin {
			// Keep the public path of historical comment-origin packages stable.
			path = "packages/" + releaseManifest.Slug + "-" + releaseManifest.Version + ".zip"
		}
		if err = packagefs.MkdirAllNoSymlink(filepath.Dir(filepath.Join(*out, filepath.FromSlash(path))), 0750); err != nil {
			return err
		}
		if err = writeNew(filepath.Join(*out, filepath.FromSlash(path)), payload, 0644); err != nil {
			return err
		}
		catalog.Plugins = append(catalog.Plugins, plugin.Release{Manifest: releaseManifest, Path: path, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload)), Notes: releaseNotes})
	}
	themeSeen := map[string]bool{}
	for _, filename := range strings.Split(*themePackages, ",") {
		if strings.TrimSpace(filename) == "" {
			continue
		}
		release, payload, err := readThemePackage(strings.TrimSpace(filename))
		if err != nil {
			return err
		}
		key := release.Manifest.Slug + "@" + release.Manifest.Version
		if themeSeen[key] {
			return errors.New("duplicate theme release")
		}
		themeSeen[key] = true
		if err := packagefs.MkdirAllNoSymlink(filepath.Join(*out, "packages", "themes"), 0750); err != nil {
			return err
		}
		if err := writeNew(filepath.Join(*out, filepath.FromSlash(release.Path)), payload, 0644); err != nil {
			return err
		}
		catalog.Themes = append(catalog.Themes, release)
		if release.Cover != nil {
			cover, err := themePackageCover(payload, release.Manifest.Cover)
			if err != nil {
				return err
			}
			filename := filepath.Join(*out, filepath.FromSlash(release.Cover.Path))
			if err := packagefs.MkdirAllNoSymlink(filepath.Dir(filename), 0750); err != nil {
				return err
			}
			if err := writeNew(filename, cover, 0644); err != nil {
				return err
			}
		}
	}
	if err := decorateReleases(&catalog, *channel, *compatible, *notes, *metadata); err != nil {
		return err
	}
	payload, _ := json.Marshal(catalog)
	if len(payload) > 1<<20 {
		return errors.New("catalog payload exceeds the client's 1 MiB limit")
	}
	signed := plugin.SignedCatalog{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))}
	body, _ := json.MarshalIndent(signed, "", "  ")
	if err = writeNew(filepath.Join(*out, *catalogName), body, 0o644); err != nil {
		return err
	}
	if err = writeNew(filepath.Join(*out, strings.TrimSuffix(*catalogName, ".json")+".readable.json"), payload, 0o644); err != nil {
		return err
	}
	fmt.Println("Built official catalog sequence " + strconv.FormatInt(*sequence, 10) + " in " + *out + ". Publish " + *catalogName + " and packages together; do not publish the signing key.")
	return nil
}

func isReviewedBuiltin(manifest plugin.Manifest) bool {
	for _, registered := range plugin.BundledManifests() {
		if reflect.DeepEqual(manifest, registered) {
			return true
		}
	}
	return false
}

func commentOriginPackage() (plugin.Manifest, []byte, error) {
	manifest := plugin.BuiltinManifest()
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return plugin.Manifest{}, nil, err
	}
	files := map[string][]byte{
		"plugin.json":            manifestJSON,
		"builtin.json":           []byte(`{"runtime":"builtin/comment-origin-v1"}`),
		"LICENSE":                []byte(licensing.Apache20),
		"THIRD_PARTY_NOTICES.md": []byte(licensing.HostPluginNotices),
		"flag-icons-MIT.txt":     []byte(licensing.FlagIconsMIT),
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			return plugin.Manifest{}, nil, err
		}
		if _, err := entry.Write(files[name]); err != nil {
			return plugin.Manifest{}, nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return plugin.Manifest{}, nil, err
	}
	return manifest, archive.Bytes(), nil
}

func readThemePackage(filename string) (theme.Release, []byte, error) {
	f, info, err := packagefs.OpenRegularNoSymlink(filename)
	if err != nil {
		return theme.Release{}, nil, err
	}
	defer f.Close()
	if info.Size() > 64<<20 {
		return theme.Release{}, nil, errors.New("theme package exceeds 64 MiB")
	}
	payload, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if err != nil || len(payload) > 64<<20 {
		return theme.Release{}, nil, errors.New("read theme package failed")
	}
	temp, err := os.MkdirTemp("", "newpress-theme-release-")
	if err != nil {
		return theme.Release{}, nil, err
	}
	defer os.RemoveAll(temp)
	snapshot := filepath.Join(temp, "package.zip")
	if err := os.WriteFile(snapshot, payload, 0600); err != nil {
		return theme.Release{}, nil, err
	}
	manifest, err := theme.NewManager(temp, "").InstallPackage(snapshot)
	if err != nil {
		return theme.Release{}, nil, err
	}
	sum := sha256.Sum256(payload)
	release := theme.Release{Manifest: manifest, Path: "packages/themes/" + manifest.Slug + "-" + manifest.Version + ".zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(payload)), Notes: "官方主题独立发行。"}
	if manifest.Cover != "" {
		cover, err := themePackageCover(payload, manifest.Cover)
		if err != nil {
			return theme.Release{}, nil, err
		}
		digest := sha256.Sum256(cover)
		release.Cover = &theme.CoverFile{Path: "previews/themes/" + manifest.Slug + "-" + manifest.Version + filepath.Ext(manifest.Cover), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(cover))}
	}
	return release, payload, nil
}

func themePackageCover(payload []byte, name string) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, err
	}
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		data, err := io.ReadAll(io.LimitReader(reader, theme.MaxCoverBytes+1))
		if err != nil {
			return nil, err
		}
		if _, err := theme.ValidateCover(data); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, errors.New("theme cover missing from package")
}
func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
