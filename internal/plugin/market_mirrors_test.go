package plugin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/extension"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const githubCatalog = "https://raw.githubusercontent.com/official/plugins/main/catalog.json"
const giteeCatalog = "https://gitee.com/official/plugins/raw/main/catalog.json"

func TestEnvironmentSourceDefaultsAndOverridesRemainSeparate(t *testing.T) {
	t.Setenv("NEWPRESS_OFFICIAL_PLUGIN_CATALOGS", "")
	t.Setenv("NEWPRESS_OFFICIAL_PLUGIN_PUBLIC_KEY", "")
	source := EnvironmentSource()
	if len(source.URLs) != 2 || source.URLs[0] != "https://raw.githubusercontent.com/Orangefen/newpress_Plug/main/catalog.extensions-v5.json" || source.URLs[1] != "https://gitee.com/chunyangnet_0/newpress_plug/raw/main/catalog.extensions-v5.json" {
		t.Fatalf("wrong official defaults: %+v", source)
	}
	if _, err := NewMarket(NewManager(t.TempDir()), source); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_OFFICIAL_PLUGIN_CATALOGS", githubCatalog)
	custom := EnvironmentSource()
	if custom.PublicKey != "" || len(custom.URLs) != 1 || custom.URLs[0] != githubCatalog {
		t.Fatal("override inherited default trust root")
	}
	if _, err := NewMarket(NewManager(t.TempDir()), custom); err == nil {
		t.Fatal("partial override accepted")
	}
	t.Setenv("NEWPRESS_OFFICIAL_PLUGIN_PUBLIC_KEY", source.PublicKey)
	if _, err := NewMarket(NewManager(t.TempDir()), EnvironmentSource()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_OFFICIAL_PLUGIN_CATALOGS", "")
	if _, err := NewMarket(NewManager(t.TempDir()), EnvironmentSource()); err == nil {
		t.Fatal("key-only override silently used default repositories")
	}
}

func mirrorFixture(t *testing.T) (*Market, Catalog, ed25519.PrivateKey, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMarket(NewManager(t.TempDir()), MarketSource{[]string{githubCatalog, giteeCatalog}, base64.StdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Schema: 1, Slug: "mirror-test", Name: "Mirror test", Version: "1.0.0", Runtime: "wasm/core-v1", NewPress: ">=0.1.0", Entry: "plugin.wasm", Capabilities: []string{"content.read"}}
	b, err := os.ReadFile(createPluginArchive(t, manifest, abiV1Module()))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	c := Catalog{Schema: 2, Sequence: 1, Expires: time.Now().Add(time.Hour), Plugins: []Release{{Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now().UTC(), DataCompatible: true}, Manifest: manifest, Path: "packages/test.zip", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}}}
	return m, c, private, b
}

func catalogResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}
}

func waitRequest(r *http.Request, delay time.Duration) error {
	select {
	case <-time.After(delay):
		return nil
	case <-r.Context().Done():
		return r.Context().Err()
	}
}

func TestMarketProbesConcurrentlyAndReelectsFastestSource(t *testing.T) {
	m, c, key, _ := mirrorFixture(t)
	signed := signCatalog(t, c, key)
	for _, slow := range []string{githubCatalog, giteeCatalog} {
		started := make(chan struct{}, 2)
		ready := make(chan struct{})
		go func() { <-started; <-started; close(ready) }()
		m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
			started <- struct{}{}
			select {
			case <-ready:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			if r.URL.String() == slow {
				if err := waitRequest(r, 40*time.Millisecond); err != nil {
					return nil, err
				}
			}
			return catalogResponse(signed), nil
		})}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		err := m.Refresh(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		v := m.View()
		if v.Source == slow || len(v.Sources) != 2 || len(m.mirrors) != 2 {
			t.Fatalf("wrong selection: %+v", v)
		}
		for _, s := range v.Sources {
			if !s.Available || s.LatencyMS < 1 || s.Selected != (s.URL == v.Source) {
				t.Fatalf("wrong probe state: %+v", s)
			}
		}
	}
}

func TestMarketLoadsFromGiteeWhenGitHubFails(t *testing.T) {
	for _, failure := range []string{"connection", "dns", "timeout", "http", "invalid-catalog"} {
		t.Run(failure, func(t *testing.T) {
			m, catalog, key, _ := mirrorFixture(t)
			signed := signCatalog(t, catalog, key)
			m.client = &http.Client{
				Timeout:       200 * time.Millisecond,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
				Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
					switch r.URL.Host {
					case "raw.githubusercontent.com":
						switch failure {
						case "dns":
							return nil, &net.DNSError{Err: "no such host", Name: r.URL.Host}
						case "timeout":
							<-r.Context().Done()
							return nil, r.Context().Err()
						case "http":
							return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("forbidden"))}, nil
						case "invalid-catalog":
							return catalogResponse([]byte("<html>unavailable</html>")), nil
						default:
							return nil, errors.New("connection refused")
						}
					case "gitee.com":
						return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"https://raw.giteeusercontent.com/official/plugins/raw/main/catalog.json?signature=public-download"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
					case "raw.giteeusercontent.com":
						return catalogResponse(signed), nil
					default:
						t.Errorf("unexpected host: %s", r.URL.Host)
						return nil, errors.New("unexpected host")
					}
				}),
			}
			if err := m.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			view := m.View()
			if view.Error != "" || view.Source != giteeCatalog || len(view.Plugins) != 1 || len(m.mirrors) != 1 || view.Sources[0].Available || !view.Sources[1].Selected {
				t.Fatalf("GitHub failure prevented Gitee loading: %+v", view)
			}
			if failure == "dns" && !strings.Contains(view.Sources[0].Error, "域名解析失败") || failure == "timeout" && !strings.Contains(view.Sources[0].Error, "超时") {
				t.Fatalf("missing network diagnostic: %s", view.Sources[0].Error)
			}
		})
	}
}

func TestMarketFailureNamesEveryAttemptedSource(t *testing.T) {
	m, _, _, _ := mirrorFixture(t)
	m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "raw.githubusercontent.com" {
			return nil, &net.DNSError{Err: "no such host", Name: r.URL.Host}
		}
		return nil, errors.New("transport detail with signature=secret")
	})}
	err := m.Refresh(t.Context())
	if err == nil {
		t.Fatal("unavailable mirrors accepted")
	}
	for _, text := range []string{"已尝试所有配置源", "GitHub：", "域名解析失败", "Gitee："} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("missing diagnostic %q: %v", text, err)
		}
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("raw transport error exposed")
	}
}

func TestMarketNewestCatalogWinsAndTrustSurvivesRestart(t *testing.T) {
	m, c, key, _ := mirrorFixture(t)
	old := signCatalog(t, c, key)
	c.Sequence = 2
	newest := signCatalog(t, c, key)
	m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == giteeCatalog {
			if err := waitRequest(r, 30*time.Millisecond); err != nil {
				return nil, err
			}
			return catalogResponse(newest), nil
		}
		return catalogResponse(old), nil
	})}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	v := m.View()
	if v.Source != giteeCatalog || v.Sequence != 2 || len(m.mirrors) != 1 || v.Sources[0].Available || !strings.Contains(v.Sources[0].Error, "待同步") {
		t.Fatalf("stale but faster source chosen: %+v", v)
	}
	restarted, err := NewMarket(m.manager, m.source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.verify(old); err == nil {
		t.Fatal("persisted rollback protection was lost")
	}
	c.Plugins[0].Notes = "different content at the same sequence"
	if _, err = restarted.verify(signCatalog(t, c, key)); err == nil {
		t.Fatal("same-sequence catalog replacement accepted")
	}
	if _, err = restarted.verify(newest); err != nil {
		t.Fatal(err)
	}
	if err = WritePrivate(filepath.Join(m.manager.root, ".market-sequence.json"), map[string]int64{"sequence": 2}); err != nil {
		t.Fatal(err)
	}
	if _, err = NewMarket(m.manager, m.source); err == nil {
		t.Fatal("accepted incomplete market trust checkpoint")
	}
}

func TestMarketRejectsMirrorEquivocationAndIgnoresInvalidSignatures(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "conflicting signed catalogs", true: "unsigned faster mirror"}[invalid], func(t *testing.T) {
			m, c, key, _ := mirrorFixture(t)
			good := signCatalog(t, c, key)
			c.Plugins[0].Notes = "conflicting publication"
			other := signCatalog(t, c, key)
			if invalid {
				other = []byte("<html>login required</html>")
			}
			m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() == giteeCatalog {
					return catalogResponse(other), nil
				}
				if err := waitRequest(r, 20*time.Millisecond); err != nil {
					return nil, err
				}
				return catalogResponse(good), nil
			})}
			err := m.Refresh(t.Context())
			if invalid {
				if err != nil || m.View().Source != githubCatalog || m.View().Sources[1].Available {
					t.Fatalf("invalid source accepted: %+v %v", m.View(), err)
				}
			} else if err == nil || len(m.mirrors) != 0 || m.sequence != 0 {
				t.Fatal("conflicting publication became trusted")
			}
		})
	}
}

func TestMarketPackageFailureSwitchesToIdenticalMirror(t *testing.T) {
	for _, failure := range []string{"unreachable", "checksum"} {
		t.Run(failure, func(t *testing.T) {
			m, c, key, archive := mirrorFixture(t)
			signed := signCatalog(t, c, key)
			var requests []string
			m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(r.URL.Path, ".zip") {
					if r.URL.String() == giteeCatalog {
						if err := waitRequest(r, 20*time.Millisecond); err != nil {
							return nil, err
						}
					}
					return catalogResponse(signed), nil
				}
				requests = append(requests, r.URL.Host)
				if r.URL.Host == "raw.githubusercontent.com" {
					if failure == "unreachable" {
						return nil, errors.New("network unavailable")
					}
					corrupt := append([]byte(nil), archive...)
					corrupt[0] ^= 1
					return catalogResponse(corrupt), nil
				}
				return catalogResponse(archive), nil
			})}
			if err := m.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := m.Install(t.Context(), c.Plugins[0].Manifest.Slug, c.Plugins[0].SHA256); err != nil {
				t.Fatal(err)
			}
			items, err := m.manager.List()
			if err != nil || len(items) != 1 || !items[0].Official || items[0].Enabled {
				t.Fatalf("install=%+v err=%v", items, err)
			}
			if len(requests) != 2 || requests[1] != "gitee.com" || m.View().Source != giteeCatalog || m.View().Sources[0].Available {
				t.Fatalf("fallback=%+v %+v", requests, m.View())
			}
			if err := m.manager.Uninstall(c.Plugins[0].Manifest.Slug); err != nil {
				t.Fatal(err)
			}
			requests = nil
			if err := m.Install(t.Context(), c.Plugins[0].Manifest.Slug, c.Plugins[0].SHA256); err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 || requests[0] != "gitee.com" {
				t.Fatalf("working source not remembered: %v", requests)
			}
		})
	}
}

func TestMarketNeverDownloadsFromOutdatedMirror(t *testing.T) {
	m, c, key, archive := mirrorFixture(t)
	old := signCatalog(t, c, key)
	c.Sequence = 2
	newest := signCatalog(t, c, key)
	m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			if r.URL.Host == "gitee.com" {
				t.Error("used a mirror with a different catalog")
				return catalogResponse(archive), nil
			}
			return nil, errors.New("offline")
		}
		if r.URL.String() == giteeCatalog {
			return catalogResponse(old), nil
		}
		return catalogResponse(newest), nil
	})}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(t.Context(), c.Plugins[0].Manifest.Slug, c.Plugins[0].SHA256); err == nil {
		t.Fatal("installation unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(m.manager.root, c.Plugins[0].Manifest.Slug)); !os.IsNotExist(err) {
		t.Fatal("failed install left a plugin")
	}
}

func TestMarketCancellationKeepsViewResponsiveAndTrustUntouched(t *testing.T) {
	m, _, _, _ := mirrorFixture(t)
	started := make(chan struct{}, 2)
	var done sync.WaitGroup
	done.Add(2)
	m.client = &http.Client{Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
		defer done.Done()
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.Refresh(ctx) }()
	<-started
	<-started
	viewDone := make(chan struct{})
	go func() { _ = m.View(); close(viewDone) }()
	select {
	case <-viewDone:
	case <-time.After(time.Second):
		t.Fatal("View blocked on network probes")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("probes did not cancel")
	}
	done.Wait()
	if m.sequence != 0 || len(m.mirrors) != 0 {
		t.Fatal("cancelled probes changed trust")
	}
}

func TestMarketSnapshotsDoNotMutateTrustedManifestOrSource(t *testing.T) {
	m, c, key, _ := mirrorFixture(t)
	signed := signCatalog(t, c, key)
	m.client = &http.Client{Transport: marketTransport(func(*http.Request) (*http.Response, error) { return catalogResponse(signed), nil })}
	if err := m.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	v := m.View()
	v.Plugins[0].Manifest.Capabilities[0] = "content.write"
	v.Sources[0].URL = "https://example.test/untrusted"
	if m.View().Plugins[0].Manifest.Capabilities[0] != "content.read" || m.View().Sources[0].URL != githubCatalog {
		t.Fatal("View aliased trusted state")
	}
	urls := append([]string(nil), m.source.URLs...)
	copyMarket, err := NewMarket(m.manager, MarketSource{urls, m.source.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	urls[0] = "https://example.test/untrusted"
	if copyMarket.source.URLs[0] != githubCatalog {
		t.Fatal("source list aliases caller input")
	}
}

func TestGiteeRawRedirectAllowsOnlyOneOfficialSamePathHop(t *testing.T) {
	m, _, _, _ := mirrorFixture(t)
	publicBody := []byte("public package bytes")
	for _, scenario := range []string{"valid", "wrong-host", "wrong-path", "duplicate-signature", "extra-query", "http", "second-hop", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			var requests []string
			m.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: marketTransport(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.URL.Host)
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
					t.Error("credentials forwarded to raw CDN")
				}
				if r.URL.Host == "gitee.com" {
					target := "https://raw.giteeusercontent.com/official/plugins/raw/main/catalog.json?signature=public-download-signature%3D"
					switch scenario {
					case "wrong-host":
						target = strings.Replace(target, "raw.giteeusercontent.com", "evil.example", 1)
					case "wrong-path":
						target = strings.Replace(target, "/official/plugins/", "/another/repository/", 1)
					case "duplicate-signature":
						target += "&signature=other"
					case "extra-query":
						target += "&redirect=https://evil.example"
					case "http":
						target = strings.Replace(target, "https:", "http:", 1)
					}
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{target}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if r.URL.Host != "raw.giteeusercontent.com" {
					t.Errorf("untrusted request to %s", r.URL.Host)
				}
				if scenario == "second-hop" {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.example/"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				return catalogResponse(publicBody), nil
			})}
			limit := int64(len(publicBody))
			if scenario == "oversize" {
				limit--
			}
			body, err := m.fetch(t.Context(), giteeCatalog, limit)
			if scenario == "valid" {
				if err != nil || !bytes.Equal(body, publicBody) || len(requests) != 2 {
					t.Fatalf("official hop failed: %v %v", requests, err)
				}
			} else if err == nil {
				t.Fatal("unsafe redirect or body was accepted")
			}
			if scenario != "valid" && scenario != "second-hop" && scenario != "oversize" && len(requests) != 1 {
				t.Fatalf("unsafe hop requested: %v", requests)
			}
			if len(requests) > 2 {
				t.Fatal("followed more than one redirect")
			}
		})
	}
	origin, _ := url.Parse(giteeCatalog)
	for _, target := range []string{
		"https://raw.giteeusercontent.com/official/plugins/raw/main/catalog.json",
		"https://raw.giteeusercontent.com/official/plugins/raw/main/catalog.json?signature=x#fragment",
		"https://raw.giteeusercontent.com/official/plugins/raw/main/catalog.json?signature=x%0A",
		"https://user:password@raw.giteeusercontent.com/official/plugins/raw/main/catalog.json?signature=x",
		"https://raw.giteeusercontent.com/official/plugins/raw/main/%63atalog.json?signature=x",
	} {
		if _, err := giteeDownloadURL(origin, target); err == nil {
			t.Fatal("unsafe Gitee redirect accepted")
		}
	}
	if _, err := repositoryURL("https://raw.giteeusercontent.com/official/plugins/raw/main/catalog.json?signature=x"); err == nil {
		t.Fatal("ephemeral CDN URL accepted as a trust root")
	}
}
