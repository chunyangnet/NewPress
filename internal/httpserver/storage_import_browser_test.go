package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
	"newpress.local/newpress/internal/objectstorage"
	"newpress.local/newpress/internal/plugin"
)

func TestStorageImportMigrationBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_STORAGE_IMPORT_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated storage and import browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	if err = s.plugins.InstallBundled(plugin.ObjectStorage); err != nil {
		t.Fatal(err)
	}
	s.SetObjectStorage(objectstorage.New(s.plugins, s.media))
	settings := storageHTTPSettings()
	other := settings.Profiles[0]
	other.ID = "backup"
	other.Name = "备用存储"
	other.Bucket = "newpress-backup"
	settings.Profiles = append(settings.Profiles, other)
	if _, err = s.objectStorage.Save(settings, nil); err != nil {
		t.Fatal(err)
	}
	input := contentimport.Request{Source: "fixture-notes", Format: "markdown", State: "draft", Authors: map[string]int64{}, Media: map[string]string{}, Files: []contentimport.File{
		{Name: "one.md", Text: "---\ntitle: 第一篇迁入文章\nslug: fixture-import-one\nauthor: Writer\n---\n正文一\n\n[外部链接](https://example.invalid/read)"},
		{Name: "two.md", Text: "---\ntitle: 第二篇迁入文章\nslug: fixture-import-two\nauthor: Writer\n---\n正文二"},
	}}
	payload, _ := json.Marshal(map[string]any{"input": input})
	response := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/imports/preview", string(payload), authenticatedAPIHeaders(owner))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var preview importResponse
	if err = json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	applied := preview
	applied.Applied = true
	applied.Token = ""
	applied.Expires = ""
	applied.Plan.Items = append([]content.ImportRow{}, preview.Plan.Items...)
	for i := range applied.Plan.Items {
		applied.Plan.Items[i].Action = "created"
		applied.Plan.Items[i].PostID = int64(900 + i)
		applied.Plan.Items[i].Version = 1
	}
	snapshot, err := s.objectStorage.Status()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := objectstorage.Record{ID: strings.Repeat("a", 64), ProfileID: "archive", Identity: strings.Repeat("b", 64), ItemID: 1, Filename: "fixture.png", ObjectKey: "fixture.png", RemoteKey: "media/fixture.png", SHA256: strings.Repeat("c", 64), Size: 128, ContentType: "image/png", Status: "orphaned", Operation: "upload", Updated: now, Verified: now}
	migration := objectstorage.MigrationPreview{Source: "archive", Target: "backup", Total: 1, Ready: 0, Bytes: 128, Routes: []string{"媒体库"}, Default: true}
	files := objectstorage.RemotePage{Files: []objectstorage.RemoteFile{{Key: "media/example.txt", Size: 12}, {Key: "media/newpress-" + strings.Repeat("0", 32) + "/1/image.png", Size: 128, Managed: true}, {Key: "media/folder/", Directory: true}}}
	write := func(name string, value any) {
		data, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(directory, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	state := func() []byte {
		status, e := s.objectStorage.Status()
		if e != nil {
			t.Fatal(e)
		}
		private := map[string]string{}
		if e = s.plugins.WithData(plugin.ObjectStorage, false, 0, func(dir string, _ uint64) error {
			return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					return nil
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				rel, _ := filepath.Rel(dir, path)
				sum := sha256.Sum256(data)
				private[rel] = hex.EncodeToString(sum[:])
				return nil
			})
		}); e != nil {
			t.Fatal(e)
		}
		posts, e := s.repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		data, e := json.Marshal(map[string]any{"storage": status, "private": private, "posts": posts, "media": s.media.Snapshot(), "settings": s.siteSettings.Snapshot()})
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	before := state()
	var escaped atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/plugins/object-storage/", 303)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" || strings.HasPrefix(r.URL.Path, "/api/v1/admin/plugins/object-storage/files") {
			escaped.Add(1)
			http.Error(w, "unintercepted mutation or cloud access", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s.config.PublicURL = server.URL
	write("fixture.json", map[string]any{"url": server.URL, "login": server.URL + "/fixture/login", "owner_id": owner.UserID, "snapshot": snapshot, "record": record, "migration": migration, "files": files, "probe": objectstorage.ProbeResult{OK: true, Stages: []string{"上传", "校验", "清理"}, DurationMS: 12, CleanupOK: true}, "input": input, "preview": preview, "applied": applied, "notice": "Go projections; no storage worker, cloud requests or content writes during browser acceptance."})
	t.Log("Storage/import fixture ready", server.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(45 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("storage/import fixture timeout")
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(directory, "stop")); e == nil {
				after := state()
				if !bytes.Equal(before, after) || escaped.Load() != 0 {
					write("before.json", json.RawMessage(before))
					write("after.json", json.RawMessage(after))
					t.Fatalf("fixture changed, escaped requests %d", escaped.Load())
				}
				sum := sha256.Sum256(after)
				write("read-only-confirmation.json", map[string]any{"state_unchanged": true, "unintercepted_requests": escaped.Load(), "state_sha256": hex.EncodeToString(sum[:]), "worker_started": false})
				return
			}
		}
	}
}
