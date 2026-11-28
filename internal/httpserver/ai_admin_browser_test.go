package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/auth"
)

// Real read contracts and disposable stores; the browser intercepts all writes.
func TestAIAdminBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_AI_ADMIN_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated AI administration browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "ai-reader", "ai-reader@example.test", "AI 读者", "subscriber")
	paths := walletBrowserStores(t, s, owner.UserID)
	seedAIUsageBrowser(t, s)
	reports, err := aiworkbench.NewStore(filepath.Join(t.TempDir(), "ai-reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetAIReports(reports)
	samples := map[string]aiworkbench.Preview{}
	for _, task := range aiworkbench.Tasks() {
		input := aiworkbench.Request{Task: task.ID, Days: 7}
		if task.ID == "summary" {
			input.Material = "隔离验收材料"
		}
		preview, err := s.workbenchPreview(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		preview, err = s.aiPreviews.Put(owner.UserID, preview, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		samples[task.ID] = preview
	}
	source := samples["operations"]
	source.Token = ""
	initial := aiworkbench.Report{ID: aiworkbench.NewID(), Owner: owner.UserID, Task: source.Task, Title: source.Title, CreatedAt: time.Now().UTC(), Provider: "主力写作", Model: "writer-thinking", Content: "## 隔离历史报告\n\n仅用于浏览器验收。", Source: &source}
	if err = reports.Save(initial, time.Now()); err != nil {
		t.Fatal(err)
	}
	readState := func() []byte {
		accounts, err := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		report, err := reports.Get(owner.UserID, initial.ID, time.Now())
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
			t.Fatal("incomplete fixture usage snapshot")
		}
		// Snapshot every stored record, including its original cost, instead of
		// comparing float sums accumulated from randomized Go map iteration.
		usageState := map[string]any{"config": usage.Config, "records": records, "retained": usage.Retained, "collapsed": usage.Collapsed, "persist_error": usage.PersistError, "persisted_at": usage.PersistedAt}
		state := map[string]any{"accounts": accounts, "settings": s.siteSettings.Snapshot(), "ai_settings": s.aiSettings.Snapshot(), "ai_reports": report, "usage": usageState}
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := ownerCookie
		if r.PathValue("role") == "reader" {
			cookie = readerCookie
		} else if r.PathValue("role") == "guest" {
			expired := *ownerCookie
			expired.Value, expired.MaxAge = "", -1
			cookie = &expired
		}
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/ai/", http.StatusSeeOther)
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
			http.Error(w, "cannot set fixture style", 500)
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
	writeEvidence := func(name string, value any) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "previews": samples, "report": initial})
	t.Log("AI admin browser fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(55 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("AI admin browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || escaped.Load() != 0 {
					_ = os.WriteFile(filepath.Join(directory, "state-before.json"), before, 0600)
					_ = os.WriteFile(filepath.Join(directory, "state-after.json"), after, 0600)
					t.Fatalf("fixture changed or %d mutations escaped interception", escaped.Load())
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "ledger_and_orders_unchanged": true, "settings_unchanged": true, "ai_settings_usage_and_reports_unchanged": true, "theme_settings_unchanged_except_fixture_style": true, "unintercepted_mutations": escaped.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
