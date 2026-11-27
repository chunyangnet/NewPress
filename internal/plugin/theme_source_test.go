package plugin

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/theme"
)

func TestThemeSourceDefaultsAreIndependentOfPlugins(t *testing.T) {
	for _, name := range []string{"NEWPRESS_OFFICIAL_THEME_CATALOGS", "NEWPRESS_OFFICIAL_THEME_PUBLIC_KEY"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NEWPRESS_OFFICIAL_PLUGIN_CATALOGS", "https://raw.githubusercontent.com/example/plugins/main/catalog.json")
	t.Setenv("NEWPRESS_OFFICIAL_PLUGIN_PUBLIC_KEY", "unrelated-plugin-key")
	source, configured := EnvironmentThemeSource()
	if !configured || !reflect.DeepEqual(source, officialThemeSource()) || source.PublicKey == officialSource().PublicKey {
		t.Fatal("theme defaults must use an independent trust root")
	}
	market, err := NewThemeMarket(t.TempDir(), source)
	if err != nil || !market.View().Configured || len(market.View().Sources) != 2 {
		t.Fatal("official theme source is not usable", err)
	}
	// A deployment override replaces both URLs and key, never just one of them.
	customURL := "https://raw.githubusercontent.com/example/themes/main/catalog.json"
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	customKey := base64.StdEncoding.EncodeToString(pub)
	t.Setenv("NEWPRESS_OFFICIAL_THEME_CATALOGS", customURL)
	t.Setenv("NEWPRESS_OFFICIAL_THEME_PUBLIC_KEY", customKey)
	source, configured = EnvironmentThemeSource()
	if !configured || !reflect.DeepEqual(source, MarketSource{URLs: []string{customURL}, PublicKey: customKey}) {
		t.Fatal("custom theme source was mixed with defaults")
	}
	if _, err := NewThemeMarket(t.TempDir(), source); err != nil {
		t.Fatal(err)
	}
}

func TestSeparateThemeMarketAndVerifiedCovers(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	root := t.TempDir()
	source := "https://raw.githubusercontent.com/example/themes/main/catalog.themes-v1.json"
	m, err := NewThemeMarket(root, MarketSource{URLs: []string{source}, PublicKey: base64.StdEncoding.EncodeToString(pub)})
	if err != nil {
		t.Fatal(err)
	}
	release, _ := marketTheme(t, "1.0.0")
	var cover bytes.Buffer
	if err := png.Encode(&cover, image.NewRGBA(image.Rect(0, 0, 96, 60))); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(cover.Bytes())
	release.Manifest.Cover = "assets/preview.png"
	release.Cover = &theme.CoverFile{Path: "previews/themes/test.png", SHA256: hex.EncodeToString(digest[:]), Size: int64(cover.Len())}
	catalog := signCatalog(t, Catalog{Schema: 2, Sequence: 1, Expires: time.Now().Add(time.Hour), Themes: []theme.Release{release}}, key)
	corrupt := false
	m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		data := catalog
		if strings.HasSuffix(r.URL.Path, release.Cover.Path) {
			data = append([]byte(nil), cover.Bytes()...)
			if corrupt {
				data[0] ^= 1
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".market-sequence.json")); err != nil {
		t.Fatal("independent trust state missing", err)
	}
	if data, mime, err := m.ThemeCover(t.Context(), release.Manifest.Slug, release.Cover.SHA256); err != nil || mime != "image/png" || !bytes.Equal(data, cover.Bytes()) {
		t.Fatal(mime, err)
	}
	corrupt = true
	if _, _, err := m.ThemeCover(t.Context(), release.Manifest.Slug, release.Cover.SHA256); err == nil {
		t.Fatal("tampered cover accepted")
	}
	if _, _, err := m.ThemeCover(t.Context(), release.Manifest.Slug, strings.Repeat("a", 64)); err == nil {
		t.Fatal("unknown digest accepted")
	}
	for _, invalid := range []string{"https://evil.test/a.png", "../outside.png", "previews/themes/a.svg"} {
		release.Cover.Path = invalid
		catalog = signCatalog(t, Catalog{Schema: 2, Sequence: 2, Expires: time.Now().Add(time.Hour), Themes: []theme.Release{release}}, key)
		if err := m.Refresh(t.Context()); err == nil {
			t.Fatal("unsafe preview accepted", invalid)
		}
	}
}

func TestThemeSourceRequiresCompleteIndependentTrust(t *testing.T) {
	t.Setenv("NEWPRESS_OFFICIAL_THEME_CATALOGS", "https://raw.githubusercontent.com/example/themes/main/catalog.themes-v1.json")
	t.Setenv("NEWPRESS_OFFICIAL_THEME_PUBLIC_KEY", "")
	source, configured := EnvironmentThemeSource()
	if !configured {
		t.Fatal("partial override ignored")
	}
	if _, err := NewThemeMarket(t.TempDir(), source); err == nil {
		t.Fatal("partial override accepted")
	}
}
