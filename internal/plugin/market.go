package plugin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/safehttp"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/version"
)

// Trust roots belong to the official distribution/deployment, never a browser
// form. Both mirrors must publish the same Ed25519-signed catalog.
type MarketSource struct {
	URLs      []string
	PublicKey string
}

func EnvironmentSource() MarketSource {
	var urls []string
	for _, raw := range strings.Split(os.Getenv("NEWPRESS_OFFICIAL_PLUGIN_CATALOGS"), ",") {
		if v := strings.TrimSpace(raw); v != "" {
			urls = append(urls, v)
		}
	}
	key := strings.TrimSpace(os.Getenv("NEWPRESS_OFFICIAL_PLUGIN_PUBLIC_KEY"))
	if len(urls) == 0 && key == "" {
		return officialSource()
	}
	// An override must provide the complete trust root. Never silently combine
	// custom repository URLs with the distribution's key (or vice versa).
	return MarketSource{URLs: urls, PublicKey: key}
}

type Release struct {
	Manifest Manifest            `json:"manifest"`
	Path     string              `json:"path"`
	SHA256   string              `json:"sha256"`
	Size     int64               `json:"size"`
	Notes    string              `json:"notes"`
	Release  *extension.Metadata `json:"release,omitempty"`
}
type Catalog struct {
	Schema   int             `json:"schema"`
	Sequence int64           `json:"sequence"`
	Expires  time.Time       `json:"expires"`
	Plugins  []Release       `json:"plugins"`
	Themes   []theme.Release `json:"themes,omitempty"`
}
type SignedCatalog struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}
type MarketView struct {
	Schema     int                  `json:"schema"`
	Configured bool                 `json:"configured"`
	Source     string               `json:"source"`
	Checked    time.Time            `json:"checked"`
	Expires    time.Time            `json:"expires"`
	Plugins    []Release            `json:"plugins"`
	Themes     []theme.Release      `json:"themes"`
	Error      string               `json:"error,omitempty"`
	Sequence   int64                `json:"sequence"`
	Sources    []MarketSourceStatus `json:"sources"`
}
type MarketSourceStatus struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	LatencyMS int64  `json:"latency_ms"`
	Available bool   `json:"available"`
	Selected  bool   `json:"selected"`
	Error     string `json:"error,omitempty"`
}
type marketTrust struct {
	Sequence int64  `json:"sequence"`
	Hash     string `json:"hash"`
}
type marketProbe struct {
	index    int
	source   string
	catalog  Catalog
	hash     string
	duration time.Duration
	err      error
}
type Market struct {
	opMu     sync.Mutex // Serialize mutations without blocking View during network requests.
	mu       sync.Mutex
	manager  *Manager
	stateDir string
	source   MarketSource
	client   *http.Client
	view     MarketView
	sequence int64
	hash     string
	mirrors  []string // Only mirrors verified to serve the selected catalog.
}

func NewMarket(manager *Manager, source MarketSource) (*Market, error) {
	return newMarket(manager, manager.root, source)
}

// NewThemeMarket uses an independent trust high-water mark and download area.
// The signed catalog protocol is shared; theme operations never invoke plugins.
func NewThemeMarket(root string, source MarketSource) (*Market, error) {
	if len(source.URLs) == 0 || source.PublicKey == "" {
		return nil, errors.New("主题市场必须同时配置目录地址与 Ed25519 公钥")
	}
	return newMarket(nil, root, source)
}

func newMarket(manager *Manager, stateDir string, source MarketSource) (*Market, error) {
	source.URLs = append([]string(nil), source.URLs...)
	m := &Market{manager: manager, stateDir: stateDir, source: source, client: safehttp.Client(60 * time.Second)}
	if len(source.URLs) == 0 && source.PublicKey == "" {
		return m, nil
	}
	key, err := base64.StdEncoding.DecodeString(source.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || len(source.URLs) < 1 || len(source.URLs) > 2 {
		return nil, errors.New("official plugin sources require 1–2 URLs and an Ed25519 public key")
	}
	seen := map[string]bool{}
	for _, raw := range source.URLs {
		if _, err := repositoryURL(raw); err != nil {
			return nil, err
		}
		if seen[raw] {
			return nil, errors.New("official plugin sources must be distinct")
		}
		seen[raw] = true
		name := "GitHub"
		if strings.HasPrefix(raw, "https://gitee.com/") {
			name = "Gitee"
		}
		m.view.Sources = append(m.view.Sources, MarketSourceStatus{URL: raw, Name: name})
	}
	m.view.Configured = true
	// A persisted high-water mark prevents rollback to an older signed catalog.
	v, err := ReadPrivate[marketTrust](filepath.Join(stateDir, ".market-sequence.json"), 1024)
	if err == nil {
		if v.Sequence < 1 || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(v.Hash) {
			return nil, errors.New("official market trust state invalid")
		}
		m.sequence = v.Sequence
		m.hash = v.Hash
	} else if !os.IsNotExist(err) {
		return nil, errors.New("official market trust state unreadable")
	}
	return m, nil
}

func (m *Market) View() MarketView {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Both manifests and release metadata are mutable maps/slices/pointers.
	b, _ := json.Marshal(m.view)
	var v MarketView
	_ = json.Unmarshal(b, &v)
	return v
}

func repositoryURL(raw string) (*url.URL, error) {
	u, err := safehttp.ValidateURL(raw)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || u.RawQuery != "" || strings.Contains(u.Path, "%") || path.Clean(u.Path) != u.Path {
		return nil, errors.New("invalid official repository URL")
	}
	if u.Host == "raw.githubusercontent.com" {
		return u, nil
	}
	if u.Host == "gitee.com" && len(parts) >= 5 && parts[2] == "raw" {
		return u, nil
	}
	return nil, errors.New("official catalog must use a public GitHub raw or Gitee raw repository URL")
}

func (m *Market) fetch(ctx context.Context, raw string, limit int64) ([]byte, error) {
	return m.fetchFile(ctx, raw, limit, nil, true)
}

var errCatalogNotFound = errors.New("官方仓库返回 HTTP 404")

func (m *Market) fetchProgress(ctx context.Context, raw string, limit int64, progress extension.Progress) ([]byte, error) {
	return m.fetchFile(ctx, raw, limit, progress, false)
}

func (m *Market) fetchFile(ctx context.Context, raw string, limit int64, progress extension.Progress, revalidate bool) ([]byte, error) {
	origin, err := repositoryURL(raw)
	if err != nil {
		return nil, err
	}
	request := func(raw string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, errors.New("invalid official request")
		}
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("User-Agent", "NewPress-Official-Market/1")
		// Revalidate repository/CDN caches when checking the signed catalog.
		// Archive downloads remain cacheable and are verified by their digest.
		if revalidate {
			req.Header.Set("Cache-Control", "no-cache")
		}
		return m.client.Do(req)
	}
	resp, err := request(raw)
	if err != nil {
		return nil, marketConnectionError(ctx, err, "官方仓库")
	}
	// Gitee now serves public raw files through a signed CDN URL. Keep the
	// generic client redirect-free; authorize exactly one same-path official hop.
	if origin.Host == "gitee.com" && (resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308) {
		target, redirectErr := giteeDownloadURL(origin, resp.Header.Get("Location"))
		resp.Body.Close()
		if redirectErr != nil {
			return nil, redirectErr
		}
		resp, err = request(target)
		if err != nil {
			return nil, marketConnectionError(ctx, err, "Gitee 文件服务")
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errCatalogNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("官方仓库返回 HTTP %d", resp.StatusCode)
	}
	reader := &marketProgressReader{reader: resp.Body, total: limit, progress: progress}
	b, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("官方文件读取失败或超出大小限制")
	}
	return b, nil
}

func marketConnectionError(ctx context.Context, err error, service string) error {
	var networkError net.Error
	var dnsError *net.DNSError
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return context.Canceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout():
		return fmt.Errorf("连接%s超时，请检查服务器网络", service)
	case errors.As(err, &dnsError):
		return fmt.Errorf("%s域名解析失败，请检查服务器 DNS", service)
	default:
		// Transport errors may include a signed CDN URL. Never expose it.
		return fmt.Errorf("无法连接%s，请检查服务器网络后重试", service)
	}
}

type marketProgressReader struct {
	reader      io.Reader
	read, total int64
	progress    extension.Progress
}

func (r *marketProgressReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.read += int64(n)
	r.progress.Report("downloading", min(r.read, r.total), r.total)
	return n, err
}

func giteeDownloadURL(origin *url.URL, location string) (string, error) {
	u, err := safehttp.ValidateURL(location)
	invalid := errors.New("Gitee 文件跳转未通过官方域名与路径校验")
	if err != nil || origin.Host != "gitee.com" || u.Host != "raw.giteeusercontent.com" || u.EscapedPath() != origin.EscapedPath() || len(location) > 8192 {
		return "", invalid
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 1 || len(query["signature"]) != 1 {
		return "", invalid
	}
	signature := query.Get("signature")
	if len(signature) == 0 || len(signature) > 4096 || !regexp.MustCompile(`^[A-Za-z0-9_+/=-]+$`).MatchString(signature) {
		return "", invalid
	}
	return u.String(), nil
}

func (m *Market) Refresh(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.refresh(ctx)
}

// RefreshIfStale coalesces automatic page checks without holding the view lock
// during network I/O. Manual refresh still bypasses this short interval.
func (m *Market) RefreshIfStale(ctx context.Context, maxAge time.Duration) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	view := m.View()
	if !view.Checked.IsZero() && time.Since(view.Checked) < maxAge && (view.Error != "" || view.Expires.After(time.Now())) {
		if view.Error != "" {
			return errors.New(view.Error)
		}
		return nil
	}
	return m.refresh(ctx)
}

func (m *Market) refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	view := m.View()
	if !view.Configured {
		return errors.New("官方源待配置")
	}
	// Measure real catalog GETs from this server, including TLS, body and signature
	// verification. Concurrent deadlines cap the whole probe round at eight seconds.
	results := make(chan marketProbe, len(m.source.URLs))
	for index, source := range m.source.URLs {
		go func() {
			started := time.Now()
			fetchContext, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			raw, err := m.fetch(fetchContext, source, 2<<20)
			probe := marketProbe{index: index, source: source, err: err}
			if err == nil {
				probe.catalog, probe.hash, probe.err = m.verifyCatalog(raw)
			}
			probe.duration = time.Since(started)
			results <- probe
		}()
	}
	var candidates []marketProbe
	for range m.source.URLs {
		probe := <-results
		status := &view.Sources[probe.index]
		status.URL = probe.source
		status.LatencyMS = max(1, probe.duration.Milliseconds())
		status.Selected, status.Available, status.Error = false, probe.err == nil, ""
		if probe.err != nil {
			status.Error = probe.err.Error()
		} else {
			candidates = append(candidates, probe)
		}
	}
	// A closed tab or an aborted automatic check is not a source failure. Do not
	// cache it for other readers or suppress their next attempt to reach a mirror.
	if err := ctx.Err(); err != nil {
		return err
	}
	view.Checked = time.Now().UTC()
	fail := func(err error) error {
		view.Error = err.Error()
		m.mu.Lock()
		m.view = view
		m.mu.Unlock()
		return err
	}
	if len(candidates) == 0 {
		var failures []string
		for _, status := range view.Sources {
			failures = append(failures, status.Name+"："+status.Error)
		}
		return fail(fmt.Errorf("官方目录加载失败，已尝试所有配置源。%s", strings.Join(failures, "；")))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].catalog.Sequence != candidates[j].catalog.Sequence {
			return candidates[i].catalog.Sequence > candidates[j].catalog.Sequence
		}
		if candidates[i].duration == candidates[j].duration {
			return candidates[i].index < candidates[j].index
		}
		return candidates[i].duration < candidates[j].duration
	})
	best := candidates[0]
	var mirrors []string
	for _, probe := range candidates {
		status := &view.Sources[probe.index]
		if probe.catalog.Sequence < best.catalog.Sequence {
			status.Available, status.Error = false, "镜像待同步，暂不用于下载"
			continue
		}
		if probe.hash != best.hash {
			for _, candidate := range candidates {
				view.Sources[candidate.index].Available = false
				view.Sources[candidate.index].Error = "相同序号的目录内容不一致"
			}
			return fail(errors.New("官方镜像目录冲突，请等待发布者完成同步"))
		}
		mirrors = append(mirrors, probe.source)
	}
	if !best.catalog.Expires.After(time.Now()) {
		return fail(errors.New("官方目录已过期，请重新刷新"))
	}
	if err := WritePrivate(filepath.Join(m.stateDir, ".market-sequence.json"), marketTrust{best.catalog.Sequence, best.hash}); err != nil {
		return fail(errors.New("无法保存官方市场校验状态"))
	}
	m.sequence, m.hash, m.mirrors = best.catalog.Sequence, best.hash, mirrors
	view.Source, view.Expires, view.Plugins = mirrors[0], best.catalog.Expires, best.catalog.Plugins
	view.Schema, view.Themes = best.catalog.Schema, best.catalog.Themes
	view.Sequence, view.Error = best.catalog.Sequence, ""
	view.Sources[best.index].Selected = true
	m.mu.Lock()
	m.view = view
	m.mu.Unlock()
	return nil
}

func (m *Market) verify(raw []byte) (Catalog, error) {
	c, _, err := m.verifyCatalog(raw)
	return c, err
}

func (m *Market) verifyCatalog(raw []byte) (Catalog, string, error) {
	env, err := packagefs.DecodeJSON[SignedCatalog](bytes.NewReader(raw), 2<<20, "signed catalog")
	if err != nil {
		return Catalog{}, "", errors.New("官方目录格式无效")
	}
	payload, e1 := base64.StdEncoding.DecodeString(env.Payload)
	signature, e2 := base64.StdEncoding.DecodeString(env.Signature)
	key, _ := base64.StdEncoding.DecodeString(m.source.PublicKey)
	if e1 != nil || e2 != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, payload, signature) {
		return Catalog{}, "", errors.New("官方目录签名校验失败")
	}
	c, err := packagefs.DecodeJSON[Catalog](bytes.NewReader(payload), 1<<20, "catalog")
	if err != nil || c.Schema != 2 || c.Sequence < 1 || c.Sequence < m.sequence || !c.Expires.After(time.Now()) || len(c.Plugins) > 100 || len(c.Themes) > 100 {
		return Catalog{}, "", errors.New("官方目录已过期、回退或格式无效")
	}
	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	if c.Sequence == m.sequence && m.hash != "" && hash != m.hash {
		return Catalog{}, "", errors.New("官方目录同一序号的内容发生变化")
	}
	seen := map[string]bool{}
	for _, r := range c.Plugins {
		v, parseErr := version.Parse(r.Manifest.Version)
		if parseErr != nil || r.Release.Validate(r.Manifest.Version) != nil || !isBuiltin(r.Manifest) && version.ValidateRange(r.Manifest.NewPress) != nil {
			return Catalog{}, "", errors.New("官方目录包含无效插件发行信息")
		}
		identity := r.Manifest.Slug + "@" + v.Identity()
		if ValidateManifest(r.Manifest) != nil || seen[identity] || validateReleaseFile(r.Path, r.SHA256, r.Size, 32<<20, r.Notes) != nil {
			return Catalog{}, "", errors.New("官方目录包含无效插件")
		}
		seen[identity] = true
	}
	seen = map[string]bool{}
	for _, r := range c.Themes {
		if theme.ValidateCoverFile(r.Cover) != nil || r.Cover != nil && r.Manifest.Cover == "" {
			return Catalog{}, "", errors.New("官方目录包含无效主题封面")
		}
		v, parseErr := version.Parse(r.Manifest.Version)
		identity := r.Manifest.Slug + "@" + v.Identity()
		if parseErr != nil || theme.ValidateManifest(r.Manifest) != nil || r.Release.Validate(r.Manifest.Version) != nil || version.ValidateRange(r.Manifest.NewPress) != nil || seen[identity] || validateReleaseFile(r.Path, r.SHA256, r.Size, 64<<20, r.Notes) != nil {
			return Catalog{}, "", errors.New("官方目录包含无效主题发行信息")
		}
		seen[identity] = true
	}
	sort.SliceStable(c.Plugins, func(i, j int) bool {
		a, b := c.Plugins[i].Manifest, c.Plugins[j].Manifest
		if a.Slug != b.Slug {
			return a.Slug < b.Slug
		}
		cmp, _ := version.Compare(a.Version, b.Version)
		return cmp > 0
	})
	sort.SliceStable(c.Themes, func(i, j int) bool {
		a, b := c.Themes[i].Manifest, c.Themes[j].Manifest
		if a.Slug != b.Slug {
			return a.Slug < b.Slug
		}
		cmp, _ := version.Compare(a.Version, b.Version)
		return cmp > 0
	})
	return c, hash, nil
}

func validateReleaseFile(name, digest string, size, limit int64, notes string) error {
	if packagefs.ValidateRelativePath(name) != nil || !strings.HasSuffix(name, ".zip") || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(digest) || size < 1 || size > limit || !validManifestText(notes, 4000, false) {
		return errors.New("发行包路径、摘要、大小或说明无效")
	}
	return nil
}

func (m *Market) pluginRelease(slug, digest string) (Release, error) {
	view := m.View()
	if !view.Expires.After(time.Now()) {
		return Release{}, errors.New("请先刷新官方市场")
	}
	for _, release := range view.Plugins {
		if release.Manifest.Slug == slug && release.SHA256 == digest {
			return release, nil
		}
	}
	return Release{}, extension.ErrConflict
}

func (m *Market) themeRelease(slug, digest string) (theme.Release, error) {
	view := m.View()
	if !view.Expires.After(time.Now()) {
		return theme.Release{}, errors.New("请先刷新官方市场")
	}
	for _, release := range view.Themes {
		if release.Manifest.Slug == slug && release.SHA256 == digest {
			return release, nil
		}
	}
	return theme.Release{}, extension.ErrConflict
}

func (m *Market) Install(ctx context.Context, slug, digest string) error {
	return m.InstallWithProgress(ctx, slug, digest, nil)
}

func (m *Market) InstallWithProgress(ctx context.Context, slug, digest string, progress extension.Progress) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	release, err := m.pluginRelease(slug, digest)
	if err != nil {
		return err
	}
	if !isBuiltin(release.Manifest) {
		if err := version.Compatible(release.Manifest.NewPress); err != nil {
			return err
		}
	}
	name, err := m.download(ctx, release.Path, release.Size, release.SHA256, progress)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	progress.Report("validating", 0, 0)
	manifest, err := packagefs.ReadJSON[Manifest](name, "plugin.json")
	if err != nil || !reflectManifest(manifest, release.Manifest) {
		return errors.New("插件包与官方目录不一致")
	}
	if info, err := packagefs.LstatNoSymlink(m.manager.root); err != nil || !info.IsDir() {
		return errors.New("插件目录不可用")
	}
	staging, err := os.MkdirTemp(m.manager.root, ".install-official-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if _, err := NewManager(staging).InstallPackage(name); err != nil {
		return err
	}
	prepared := filepath.Join(staging, slug)
	if err := WritePrivate(filepath.Join(prepared, ".official.json"), release); err != nil {
		return err
	}
	m.manager.mu.Lock()
	defer m.manager.mu.Unlock()
	target := filepath.Join(m.manager.root, slug)
	if _, err := packagefs.LstatNoSymlink(target); !os.IsNotExist(err) {
		return extension.ErrConflict
	}
	progress.Report("switching", 0, 0)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(prepared, target); err != nil {
		return err
	}
	m.manager.nextGeneration(slug)
	return nil
}

func (m *Market) Update(ctx context.Context, slug, digest, expected, revision string, progress extension.Progress) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	release, err := m.pluginRelease(slug, digest)
	if err != nil {
		return err
	}
	m.manager.mu.Lock()
	target, err := m.manager.captureUpdate(slug, expected, revision)
	m.manager.mu.Unlock()
	if err != nil {
		return err
	}
	if expected == "" || revision == "" {
		return extension.ErrConflict
	}
	if release.Release == nil || !release.Release.DataCompatible || isBuiltin(target.manifest) {
		return errors.New("此发行版不支持独立 OTA 更新")
	}
	if err := version.Compatible(release.Manifest.NewPress); err != nil {
		return err
	}
	if cmp, err := version.Compare(release.Manifest.Version, expected); err != nil || cmp <= 0 {
		return errors.New("在线更新只接受更高的语义版本")
	}
	name, err := m.download(ctx, release.Path, release.Size, release.SHA256, progress)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	return m.manager.updatePackage(ctx, name, release, target, progress)
}

func (m *Market) InstallTheme(ctx context.Context, themes *theme.Manager, slug, digest string, progress extension.Progress) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	release, err := m.themeRelease(slug, digest)
	if err != nil {
		return err
	}
	if err := version.Compatible(release.Manifest.NewPress); err != nil {
		return err
	}
	name, err := m.download(ctx, release.Path, release.Size, release.SHA256, progress)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	return themes.InstallRelease(ctx, name, release, progress)
}

func (m *Market) UpdateTheme(ctx context.Context, themes *theme.Manager, slug, digest, expected, revision string, progress extension.Progress) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	release, err := m.themeRelease(slug, digest)
	if err != nil {
		return err
	}
	if expected == "" || revision == "" {
		return extension.ErrConflict
	}
	target, err := themes.CaptureUpdate(slug, expected, revision)
	if err != nil {
		return err
	}
	if release.Release == nil || !release.Release.DataCompatible {
		return errors.New("此发行版不支持独立 OTA 更新")
	}
	if err := version.Compatible(release.Manifest.NewPress); err != nil {
		return err
	}
	if cmp, err := version.Compare(release.Manifest.Version, expected); err != nil || cmp <= 0 {
		return errors.New("在线更新只接受更高的语义版本")
	}
	name, err := m.download(ctx, release.Path, release.Size, release.SHA256, progress)
	if err != nil {
		return err
	}
	defer os.Remove(name)
	return themes.UpdatePackage(ctx, name, release, target, progress)
}

// download is called with opMu held, binding the package to the exact signed
// catalog checked by the user. Only identical catalogs qualify as mirrors.
func (m *Market) download(ctx context.Context, packagePath string, size int64, digest string, progress extension.Progress) (string, error) {
	view := m.View()
	var b []byte
	var last error
	for _, source := range m.mirrors {
		base, _ := url.Parse(source)
		base.Path = path.Join(path.Dir(base.Path), packagePath)
		fetchContext, cancel := context.WithTimeout(ctx, 45*time.Second)
		progress.Report("downloading", 0, size)
		var err error
		b, err = m.fetchProgress(fetchContext, base.String(), size, progress)
		cancel()
		if err == nil {
			progress.Report("verifying", size, size)
			sum := sha256.Sum256(b)
			if int64(len(b)) != size || hex.EncodeToString(sum[:]) != digest {
				err = errors.New("扩展包完整性校验失败")
			}
		}
		if err == nil {
			view.Source = source
			for i := range view.Sources {
				view.Sources[i].Selected = view.Sources[i].URL == source
				if view.Sources[i].Selected {
					view.Sources[i].Available, view.Sources[i].Error = true, ""
				}
			}
			sort.SliceStable(m.mirrors, func(i, j int) bool { return m.mirrors[i] == source && m.mirrors[j] != source })
			last = nil
			break
		}
		last = err
		for i := range view.Sources {
			if view.Sources[i].URL == source {
				view.Sources[i].Available, view.Sources[i].Selected = false, false
				view.Sources[i].Error = "下载失败：" + err.Error()
			}
		}
		if ctx.Err() != nil {
			last = ctx.Err()
			break
		}
	}
	m.mu.Lock()
	m.view = view
	m.mu.Unlock()
	if last != nil {
		return "", last
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if len(b) == 0 || !view.Expires.After(time.Now()) {
		return "", errors.New("请先刷新官方市场")
	}
	f, err := os.CreateTemp(m.stateDir, ".download-*.zip")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func reflectManifest(a, b Manifest) bool { return reflect.DeepEqual(a, b) }
