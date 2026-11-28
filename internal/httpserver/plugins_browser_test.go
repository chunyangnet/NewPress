package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/pluginjobs"
)

// Synthetic read store uses the production projection; no sender or worker runs.
type migrationWebhookStore struct{ items []plugindispatch.Summary }

func (s *migrationWebhookStore) List(_ context.Context, q plugindispatch.Query) (plugindispatch.Page, error) {
	items := []plugindispatch.Summary{}
	for _, item := range s.items {
		if (q.Plugin == "" || item.Plugin == q.Plugin) && (q.JobID == "" || item.JobID == q.JobID) && (q.State == "" || item.State == q.State) {
			items = append(items, item)
		}
	}
	start := min(q.Offset, len(items))
	end := min(start+q.Limit, len(items))
	return plugindispatch.Page{Items: items[start:end], Total: len(items), Limit: q.Limit, Offset: q.Offset}, nil
}
func (s *migrationWebhookStore) Review(context.Context, plugindispatch.ReviewRequest) (plugindispatch.Summary, error) {
	return plugindispatch.Summary{}, fmt.Errorf("browser write escaped interception")
}

func TestPluginsBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_PLUGINS_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated plugin browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, _ := createAndLoginTestUser(t, users, owner, "plugin-reader", "reader@example.test", "读者", "subscriber")
	paths := walletBrowserStores(t, s, owner.UserID)
	post := createOwnedTestPost(t, repository, owner.UserID, owner.Username, "plugin-fixture", "插件迁移示例文章")
	installTaskFixture(t, s)
	if err = s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	s.commentOrigin = commentorigin.New(s.plugins)
	shared := newSharedReviewPreviewStore(t)
	s.SetPluginTaskStore(shared)
	now := time.Now().UTC().Add(-time.Hour)
	epoch, err := s.plugins.CurrentTaskEpoch("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	source, err := shared.Enqueue(t.Context(), pluginjobs.Input{Plugin: "task-fixture", Epoch: epoch.Token, Kind: "task.execute.v1", Key: "fixture-source", ActorID: owner.UserID, Payload: json.RawMessage(`{"original":true}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = shared.CancelRestored(t.Context(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	details, err := shared.GetReview(t.Context(), "task-fixture", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := shared.Enqueue(t.Context(), pluginjobs.Input{Plugin: "task-fixture", Epoch: epoch.Token, Kind: "task.execute.v1", Key: "fixture-pending", ActorID: owner.UserID, Payload: json.RawMessage(`{"pending":true}`)}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	hooks := &migrationWebhookStore{}
	for i := 1; i <= 23; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		hooks.items = append(hooks.items, plugindispatch.Summary{ID: fmt.Sprintf("%064x", i), JobID: accepted.ID, Plugin: "task-fixture", Slot: "notify", Target: "https://example.test", Fingerprint: strings.Repeat("f", 64), Version: 3, State: "uncertain", CreatedAt: at, UpdatedAt: at, ResultAt: at, Outcome: plugindispatch.Outcome{Code: "transport_unconfirmed"}})
	}
	s.SetPluginWebhookStore(hooks)
	capture := func(method, path, body string) json.RawMessage {
		t.Helper()
		r := httptest.NewRequest(method, "https://example.test"+path, strings.NewReader(body))
		r.AddCookie(ownerCookie)
		r.Header.Set("Origin", "https://example.test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", owner.CSRFToken)
		r.Header.Set(sharedReviewUserHeader, strconv.FormatInt(owner.UserID, 10))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code < 200 || w.Code >= 300 {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return append(json.RawMessage{}, w.Body.Bytes()...)
	}
	encode := func(value any) string {
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	_, cfg, _, err := s.plugins.Configuration("task-fixture")
	if err != nil {
		t.Fatal(err)
	}
	configReceipt := capture("POST", "/api/v1/admin/plugins/task-fixture/configuration", encode(map[string]any{"expected_user_id": owner.UserID, "configuration": cfg, "subscribe_events": false}))
	originSettings, _ := s.commentOrigin.Status()
	originReceipt := capture("PUT", "/api/v1/admin/plugins/comment-origin/settings", encode(map[string]any{"expected_user_id": owner.UserID, "settings": originSettings.Settings, "clear_key": false}))
	taskReview := capture("GET", "/api/v1/admin/plugins/task-fixture/tasks/"+source.ID+"/review", "")
	child := capture("POST", "/api/v1/admin/plugins/task-fixture/tasks/"+source.ID+"/reissues", encode(map[string]any{"expected_user_id": owner.UserID, "source_version": details.Task.Version, "kind": details.Task.Kind, "key": "fixture-reissue", "payload": map[string]any{"reviewed": true}, "reason": "已核对原任务", "confirmed": true}))
	records := capture("GET", "/api/v1/admin/plugin-shared-records?type=content_effect&limit=20", "")
	quarantines := capture("GET", "/api/v1/admin/plugin-shared-quarantines?limit=20", "")
	webhookPage := capture("GET", "/api/v1/admin/plugin-webhooks?limit=20", "")
	var pluginRoot string
	if err = s.plugins.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error { pluginRoot = filepath.Dir(filepath.Dir(dir)); return nil }); err != nil {
		t.Fatal(err)
	}
	state := func() []byte {
		accounts, e := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		posts, e := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		revisions, e := repository.ListRevisions(t.Context(), post.ID, 100)
		if e != nil {
			t.Fatal(e)
		}
		queue, e := shared.Backend.Read(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		files := map[string]string{}
		if e = filepath.WalkDir(pluginRoot, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			name, err := filepath.Rel(pluginRoot, path)
			if err != nil {
				return err
			}
			files[name] = hex.EncodeToString(sum[:])
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		for _, path := range paths {
			bytes, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			files[filepath.Base(path)] = string(bytes)
		}
		result, e := json.Marshal(map[string]any{"accounts": accounts, "posts": posts, "revisions": revisions, "queue": queue, "shared_records": shared.records, "quarantines": shared.quarantines, "webhooks": hooks.items, "files": files, "media": s.media.Snapshot(), "settings": s.siteSettings.Snapshot()})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	before := state()
	var escaped atomic.Int64
	write := func(name string, value any) {
		bytes, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(directory, name), bytes, 0600); e != nil {
			t.Fatal(e)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := ownerCookie
		if r.PathValue("role") == "reader" {
			cookie = readerCookie
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/plugins/", http.StatusSeeOther)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			escaped.Add(1)
			http.Error(w, "browser mutation not intercepted", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	write("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "source_id": source.ID, "accepted": pluginjobs.Summarize(accepted), "task_review": taskReview, "reissue": child, "configuration_receipt": configReceipt, "origin_receipt": originReceipt, "records": records, "quarantines": quarantines, "webhooks": webhookPage, "webhook_items": hooks.items, "store": "synthetic shared/Webhook store; real Go router and projections; no PostgreSQL, sender or worker"})
	t.Log("Plugin migration fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(55 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("fixture timeout")
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(directory, "stop")); e == nil {
				after := state()
				if !reflect.DeepEqual(before, after) || escaped.Load() != 0 {
					write("state-before.json", json.RawMessage(before))
					write("state-after.json", json.RawMessage(after))
					t.Fatalf("fixture changed, escaped writes %d", escaped.Load())
				}
				sum := sha256.Sum256(after)
				write("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "posts_revisions_unchanged": true, "plugin_packages_configuration_unchanged": true, "queue_shared_webhooks_unchanged": true, "media_settings_ledger_orders_unchanged": true, "unintercepted_mutations": escaped.Load(), "state_sha256": hex.EncodeToString(sum[:])})
				return
			}
		}
	}
}
