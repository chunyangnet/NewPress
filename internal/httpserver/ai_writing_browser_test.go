package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/writingtemplate"
)

// Actual pages and DTOs, disposable stores, and a hard stop for escaped writes.
func TestAIWritingBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_AI_WRITING_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated AI writing browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, _, users, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "writing-reader", "writing-reader@example.test", "读者", "subscriber")
	paths := walletBrowserStores(t, s, owner.UserID)
	seedAIUsageBrowser(t, s)
	if err = s.plugins.InstallBundled(plugin.AIProMax); err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.SetEnabled(plugin.AIProMax, true); err != nil {
		t.Fatal(err)
	}
	s.media, err = media.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	image, err := s.media.Upload(t.Context(), bytes.NewReader(httpTestPNG(t, 120, 80)), media.UploadInput{Filename: "fixture-ai.png", Title: "隔离图片", AltText: "蓝绿色测试图", Caption: "隔离图片说明", UploadedBy: owner.Username, UploadSource: "ai-image"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	post := createOwnedTestPost(t, repository, owner.UserID, owner.Username, "ai-writing-fixture", "原始文章")
	storage, err := writingtemplate.NewFileStorage(filepath.Join(t.TempDir(), "templates.json"))
	if err != nil {
		t.Fatal(err)
	}
	library, err := writingtemplate.New(t.Context(), storage)
	if err != nil {
		t.Fatal(err)
	}
	s.SetWritingTemplates(library)
	definition := writingtemplate.Definition{BasePrompt: "article", Title: "团队写作范例", Description: "保留事实与引用", Instruction: "写清来源，保留事实；不要添加未提供的信息。", Example: "本周完成了三个功能，下面是具体材料。"}
	created, err := library.Apply(t.Context(), writingtemplate.Command{Action: "save", Definition: &definition, RequestKey: "1111111111111111111111111111111111111111111111111111111111111111", Actor: owner.Username})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := library.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	exported, err := library.Export(t.Context(), created.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	document, err := aieditor.PrepareDocument(aieditor.Document{Title: "已生成的文章", Slug: "generated-article", Excerpt: "生成的摘要", Markdown: "## 一段正文\n\n这是一份可核对的生成结果。", Tags: []string{"写作"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeEvidence := func(name string, value any) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	readState := func() []byte {
		accounts, err := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		posts, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		versions, err := repository.ListRevisions(t.Context(), post.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		templates, err := storage.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		queue, err := s.aiJobs.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		filter := aieditor.UsageFilter{Start: time.Now().AddDate(0, 0, -89).Format("2006-01-02"), End: time.Now().Format("2006-01-02"), Limit: 100}
		usage := s.aiTelemetry.Report(filter)
		records := append([]aieditor.RequestRecord{}, usage.Records...)
		for next := usage.Next; next != 0; {
			filter.Before = next
			page := s.aiTelemetry.Report(filter)
			records = append(records, page.Records...)
			next = page.Next
		}
		if len(records) != usage.Retained {
			t.Fatal("incomplete usage snapshot")
		}
		state := map[string]any{"accounts": accounts, "posts": posts, "revisions": versions, "templates": string(templates), "queue": queue, "media": s.media.Snapshot(), "settings": s.siteSettings.Snapshot(), "ai_settings": s.aiSettings.Snapshot(), "usage_records": records}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			state[filepath.Base(path)] = string(data)
		}
		values := s.themes.SettingsSnapshot().Values
		delete(values, "visual_style")
		state["theme_settings"] = values
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	var escaped atomic.Int64
	handler := s.routes()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := ownerCookie
		if r.PathValue("role") == "reader" {
			cookie = readerCookie
		} else if r.PathValue("role") == "guest" {
			expired := *ownerCookie
			expired.Value = ""
			expired.MaxAge = -1
			cookie = &expired
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/content/new/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.NotFound(w, r)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
			http.Error(w, "cannot set style", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			escaped.Add(1)
			http.Error(w, "browser mutation was not intercepted", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "post_id": post.ID, "template": initial, "template_result": created, "template_export": json.RawMessage(exported), "document": document, "media": mediaAPIItem(image), "metadata": aiEnrichment{Excerpt: "建议摘要", Slug: "suggested-slug", Tags: []string{"建议标签"}}})
	t.Log("AI writing fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(55 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("AI writing fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || escaped.Load() != 0 {
					_ = os.WriteFile(filepath.Join(directory, "state-before.json"), before, 0600)
					_ = os.WriteFile(filepath.Join(directory, "state-after.json"), after, 0600)
					t.Fatal(fmt.Sprintf("fixture changed or %d mutations escaped", escaped.Load()))
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "posts_and_revisions_unchanged": true, "templates_and_receipts_unchanged": true, "media_and_queue_unchanged": true, "settings_usage_ledger_orders_unchanged": true, "unintercepted_mutations": escaped.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
