package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEWPRESS_UPDATE_PUBLIC_KEY", base64.StdEncoding.EncodeToString(pub))
	return priv
}
func signFixture(t *testing.T, key ed25519.PrivateKey, m Manifest, seq int64) []byte {
	t.Helper()
	m.Schema = manifestSchema
	m.Product = CoreProduct
	m.Sequence = seq
	if m.Expires.IsZero() {
		m.Expires = time.Now().UTC().Add(time.Hour)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(SignedManifest{b, ed25519.Sign(key, b)})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestReleaseWithdrawalAndReplay(t *testing.T) {
	m := managerFixture(t)
	key := fixtureKey(t)
	payload := zipFixture(t, "bin/"+binaryName())
	manifest := Manifest{Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: int64(len(payload)), SHA256: digest(payload)}}}
	approved := signFixture(t, key, manifest, 2)
	manifest.DataCompatible = false
	withdrawn := signFixture(t, key, manifest, 3)
	var current atomic.Value
	current.Store(approved)
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commits/"):
			json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("a", 40)})
		case strings.HasSuffix(r.URL.Path, ".json"):
			w.Write(current.Load().([]byte))
		default:
			downloads.Add(1)
			w.Write(payload)
		}
	}))
	defer server.Close()
	m.client.base = server.URL
	if err := m.Configure(testSource(), false); err != nil {
		t.Fatal(err)
	}
	m.scheduledCheck(t.Context())
	v, err := m.Snapshot()
	if err != nil || v.Revision == "" || v.State.Phase != "idle" {
		t.Fatal(v, err)
	}
	current.Store(withdrawn)
	if _, err := m.Start(manifest.Version, true, v.Revision); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, "failed")
	m.wg.Wait()
	if downloads.Load() != 0 {
		t.Fatal("withdrawn package was downloaded")
	}
	current.Store(approved)
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, "failed")
	m.wg.Wait()
	v2, _ := m.Snapshot()
	if v2.Available != nil || !strings.Contains(v2.Discovery.Message, "序号") {
		t.Fatal(v2)
	}
	if _, err = m.Start(manifest.Version, true, v.Revision); err == nil {
		t.Fatal("stale approval accepted")
	}
}
func TestReleaseWithdrawnDuringDownloadNeverQueues(t *testing.T) {
	m := managerFixture(t)
	key := fixtureKey(t)
	payload := zipFixture(t, "bin/"+binaryName())
	manifest := Manifest{Version: "0.2.0", DataCompatible: true, Artifacts: []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Path: "pkg.zip", Size: int64(len(payload)), SHA256: digest(payload)}}}
	before, after := strings.Repeat("a", 40), strings.Repeat("b", 40)
	initial := signFixture(t, key, manifest, 7)
	manifest.DataCompatible = false
	revoked := signFixture(t, key, manifest, 8)
	var withdrawn, sawWithdrawal atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commits/"):
			head := before
			if withdrawn.Load() {
				head = after
			}
			json.NewEncoder(w).Encode(map[string]string{"sha": head})
		case strings.HasSuffix(r.URL.Path, "stable.json"):
			switch r.URL.Query().Get("ref") {
			case before:
				w.Write(initial)
			case after:
				sawWithdrawal.Store(true)
				w.Write(revoked)
			default:
				http.NotFound(w, r)
			}
		case strings.HasSuffix(r.URL.Path, "pkg.zip") && r.URL.Query().Get("ref") == before:
			mid := len(payload) / 2
			w.Write(payload[:mid])
			w.(http.Flusher).Flush()
			withdrawn.Store(true)
			w.Write(payload[mid:])
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m.client.base = server.URL
	if err := m.Configure(testSource(), false); err != nil {
		t.Fatal(err)
	}
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	checked, err := m.Snapshot()
	if err != nil || checked.Revision == "" {
		t.Fatal(checked, err)
	}
	ticket, err := m.Start(manifest.Version, true, checked.Revision)
	if err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	s, ok := m.Progress(ticket)
	if !ok || s.Phase != "failed" || s.Downloaded != int64(len(payload)) || !sawWithdrawal.Load() {
		t.Fatalf("final recheck: %+v", s)
	}
	for _, name := range []string{"request.json", "active.json", "available.json", filepath.Join("releases", s.ID)} {
		if _, err := os.Stat(filepath.Join(m.root, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected %s: %v", name, err)
		}
	}
}

func TestCorePublisherTrust(t *testing.T) {
	key := fixtureKey(t)
	public := key.Public().(ed25519.PublicKey)
	signed := signFixture(t, key, Manifest{}, 1)
	if _, err := decodeSigned(bytes.NewReader(signed), public); err != nil {
		t.Fatal(err)
	}
	other := fixtureKey(t)
	expired := signFixture(t, key, Manifest{Expires: time.Now().Add(-time.Hour)}, 2)
	for _, b := range [][]byte{[]byte(`{"schema":1}`), signFixture(t, other, Manifest{}, 2), expired} {
		if _, err := decodeSigned(bytes.NewReader(b), public); err == nil {
			t.Fatal("untrusted publication accepted")
		}
	}
}
func TestCorePackagePreservesThemeOwnership(t *testing.T) {
	m := managerFixture(t)
	theme := filepath.Join(m.themes, "starter")
	if err := os.Mkdir(theme, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"theme.json", ".official.json", "local.css"} {
		if err := os.WriteFile(filepath.Join(theme, name), []byte("site-version-1.1"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, includesTheme := range []bool{true, false} {
		names := []string{"bin/" + binaryName()}
		if includesTheme {
			names = append(names, "themes/starter/theme.json")
		}
		name := filepath.Join(t.TempDir(), "release.zip")
		if err := os.WriteFile(name, zipFixture(t, names...), 0600); err != nil {
			t.Fatal(err)
		}
		id := randomID()
		rel, err := stageArchive(t.Context(), name, m.root, id, m.themes)
		if includesTheme {
			if err == nil {
				t.Fatal("core overwrote installed theme")
			}
			if _, err = os.Stat(filepath.Join(m.root, "releases", id)); !os.IsNotExist(err) {
				t.Fatal("failed stage was retained")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"theme.json", ".official.json", "local.css"} {
			b, err := os.ReadFile(filepath.Join(releaseThemes(m.root, rel, ""), "starter", n))
			if err != nil || string(b) != "site-version-1.1" {
				t.Fatal(n, err)
			}
		}
	}
}
func TestDatabaseBackupReceipt(t *testing.T) {
	name := filepath.Join(t.TempDir(), "backup.json")
	t.Setenv("NEWPRESS_UPDATE_BACKUP_RECEIPT_FILE", name)
	now := time.Now().UTC()
	r := backupReceipt{1, "https://example.test", "1.0.0", "1.1.0", now.Add(-time.Hour), now.Add(-time.Minute), "pg-id", "files-id"}
	if err := writeJSON(name, r); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupReceipt(r.Site, r.From, r.To); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupReceipt(r.Site, r.From, "1.2.0"); err == nil {
		t.Fatal("wrong target accepted")
	}
	r.Captured = now.Add(-25 * time.Hour)
	if err := writeJSON(name, r); err != nil {
		t.Fatal(err)
	}
	if err := verifyBackupReceipt(r.Site, r.From, r.To); err == nil {
		t.Fatal("expired backup accepted")
	}
}
