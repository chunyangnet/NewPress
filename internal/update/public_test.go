package update

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func publicFixture(t *testing.T, handler http.HandlerFunc) *Manager {
	t.Helper()
	m := managerFixture(t)
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	m.client.public = s.Client()
	m.client.public.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	m.client.repositories = []publicRepository{{"GitHub", s.URL + "/github/api", s.URL + "/github/raw"}, {"Gitee", s.URL + "/gitee/api", s.URL + "/gitee/raw"}}
	return m
}

func publicManifest(payload []byte) Manifest {
	return Manifest{Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "packages/test.zip", Size: int64(len(payload)), SHA256: digest(payload)}}}
}

func writePublicCommit(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("a", 40)})
}

func TestOfficialPublicDefaultsAndNoPrivateCredentials(t *testing.T) {
	key := fixtureKey(t)
	signed := signFixture(t, key, publicManifest([]byte("archive")), 1)
	t.Setenv("NEWPRESS_GITEE_TOKEN", "private-environment-token")
	var calls atomic.Int32
	m := publicFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" || strings.Contains(r.URL.String(), "private-environment-token") {
			t.Error("private credential in public request")
		}
		if strings.Contains(r.URL.Path, "/commits/") {
			writePublicCommit(w)
		} else {
			_, _ = w.Write(signed)
		}
	})
	s, _, err := loadSource(m.root)
	if err != nil || !s.Official || s.validate() != nil {
		t.Fatal(s, err)
	}
	a, err := m.discover(t.Context(), s)
	if err != nil || len(a.Mirrors) != 2 || calls.Load() != 4 {
		t.Fatalf("public check: %+v %v calls=%d", a, err, calls.Load())
	}
	if err := m.Configure(Source{Official: true}, false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(m.root, "source.json"))
	if bytes.Contains(b, []byte("private-environment-token")) {
		t.Fatal("public configuration saved private credential")
	}
	view, err := m.Snapshot()
	if err != nil || !view.Configured || !view.Source.Official || view.Source.Token != "" {
		t.Fatal(view, err)
	}
}

func TestOfficialPublicGitHubUnavailableUsesGitee(t *testing.T) {
	key := fixtureKey(t)
	payload := zipFixture(t, "bin/"+binaryName())
	signed := signFixture(t, key, publicManifest(payload), 1)
	m := publicFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/github/") {
			http.Error(w, "offline", 503)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".zip") {
			_, _ = w.Write(payload)
		} else if strings.Contains(r.URL.Path, "/commits/") {
			writePublicCommit(w)
		} else {
			_, _ = w.Write(signed)
		}
	})
	a, err := m.discover(t.Context(), officialSource())
	if err != nil || len(a.Mirrors) != 1 || a.Mirrors[0].Source != "Gitee" {
		t.Fatal(a, err)
	}
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	view, err := m.Snapshot()
	if err != nil || view.Revision == "" {
		t.Fatal(view, err)
	}
	if _, err := m.Start("0.2.0", true, view.Revision); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	state, err := m.state()
	if err != nil || state.Phase != "queued" {
		t.Fatal(state, err)
	}
}

func TestOfficialPublicNewerMirrorAndPersistentRollbackGuard(t *testing.T) {
	key := fixtureKey(t)
	manifest := publicManifest([]byte("archive"))
	old, latest := signFixture(t, key, manifest, 1), signFixture(t, key, manifest, 2)
	var rollback atomic.Bool
	m := publicFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/") {
			writePublicCommit(w)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/github/") || rollback.Load() {
			_, _ = w.Write(old)
		} else {
			_, _ = w.Write(latest)
		}
	})
	a, err := m.discover(t.Context(), officialSource())
	if err != nil || a.Manifest.Sequence != 2 || len(a.Mirrors) != 1 || a.Mirrors[0].Source != "Gitee" {
		t.Fatal(a, err)
	}
	rollback.Store(true)
	if _, err := m.discover(t.Context(), officialSource()); err == nil || !strings.Contains(err.Error(), "序号") {
		t.Fatal("rollback accepted", err)
	}
}

func TestOfficialPublicConflictingSignedSequenceRejected(t *testing.T) {
	key := fixtureKey(t)
	manifest := publicManifest([]byte("archive"))
	first := signFixture(t, key, manifest, 7)
	manifest.Notes = "different signed content"
	second := signFixture(t, key, manifest, 7)
	m := publicFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/") {
			writePublicCommit(w)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/github/") {
			_, _ = w.Write(first)
		} else {
			_, _ = w.Write(second)
		}
	})
	if _, err := m.discover(t.Context(), officialSource()); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatal("conflict accepted", err)
	}
}

func TestOfficialPublicMissingReleaseNeverReportsCurrent(t *testing.T) {
	_ = fixtureKey(t)
	m := publicFixture(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	s, err := m.Snapshot()
	if err != nil || s.State.Phase != "failed" || s.Available != nil || !strings.Contains(s.Discovery.Message, "尚不可用") {
		t.Fatal(s, err)
	}
}

func TestOfficialPublicDownloadRetriesOnlyIdenticalVerifiedMirrors(t *testing.T) {
	key := fixtureKey(t)
	payload := zipFixture(t, "bin/"+binaryName())
	manifest := publicManifest(payload)
	signed := signFixture(t, key, manifest, 3)
	var github, gitee atomic.Int32
	m := publicFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/") {
			writePublicCommit(w)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".json") {
			_, _ = w.Write(signed)
			return
		}
		if !strings.Contains(r.URL.Path, "/"+strings.Repeat("a", 40)+"/") {
			t.Error("unpinned package request")
		}
		if strings.HasPrefix(r.URL.Path, "/github/") {
			github.Add(1)
			_, _ = w.Write(bytes.Repeat([]byte("x"), len(payload)))
			return
		}
		gitee.Add(1)
		_, _ = w.Write(payload)
	})
	a, err := m.discover(t.Context(), officialSource())
	if err != nil {
		t.Fatal(err)
	}
	a.Mirrors = []publicRevision{{"GitHub", strings.Repeat("a", 40)}, {"Gitee", strings.Repeat("a", 40)}}
	f, err := os.CreateTemp(m.root, "public-download-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	state := State{Total: int64(len(payload))}
	if err := m.downloadArchive(context.Background(), officialSource(), a, manifest.Artifacts[0], f, &state); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil || !bytes.Equal(b, payload) || github.Load() != 1 || gitee.Load() != 1 || state.Downloaded != int64(len(payload)) {
		t.Fatal("fallback did not preserve exact verified package", err)
	}
}

func TestOfficialPublicApprovalSurvivesMirrorSelectionButNotPayloadChange(t *testing.T) {
	a := Available{KeyID: "key", PayloadHash: "signed-bytes", Commit: strings.Repeat("a", 40), Mirrors: []publicRevision{{"GitHub", strings.Repeat("a", 40)}}}
	b := a
	b.Commit = strings.Repeat("b", 40)
	b.Mirrors = []publicRevision{{"Gitee", b.Commit}}
	if a.revision() != b.revision() {
		t.Fatal("mirror switch invalidated identical signed release")
	}
	b.PayloadHash = "other"
	if a.revision() == b.revision() {
		t.Fatal("changed signed release retained approval")
	}
}

func TestOfficialPublicRedirectIsOneSamePathGiteeCDNHop(t *testing.T) {
	origin := "https://gitee.com/chunyangnet_0/newpress/raw/main/updates/beta.json"
	valid := "https://raw.giteeusercontent.com/chunyangnet_0/newpress/raw/main/updates/beta.json?signature=abc_123="
	if _, err := publicRedirect(origin, valid); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{strings.Replace(valid, "https:", "http:", 1), strings.Replace(valid, "raw.giteeusercontent.com", "example.com", 1), strings.Replace(valid, "beta.json", "stable.json", 1), valid + "&access_token=secret", strings.Split(valid, "?")[0]} {
		if _, err := publicRedirect(origin, target); err == nil {
			t.Fatal("unsafe redirect accepted")
		}
	}
}
