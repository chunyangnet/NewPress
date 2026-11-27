package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/messaging"
	"newpress.local/newpress/internal/notification"
	"newpress.local/newpress/internal/theme"
)

// Opt-in visual QA against temporary stores; all writes stay in this fixture.
func TestMessagingModuleBrowserFixture(t *testing.T) {
	dir := os.Getenv("NEWPRESS_MESSAGING_BROWSER_FIXTURE")
	if dir == "" {
		t.Skip("set NEWPRESS_MESSAGING_BROWSER_FIXTURE for visual QA")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, h, _, repository, cookie, owner := newPersistentAdminTestServer(t)
	progressionHTTPStore(t, s)
	if slug := os.Getenv("NEWPRESS_MESSAGING_BROWSER_THEME"); slug != "" {
		root := os.Getenv("NEWPRESS_MESSAGING_BROWSER_THEMES")
		if root == "" {
			root = filepath.Join("..", "..", "themes")
		}
		s.themes = theme.NewManager(root, slug, theme.WithSettingsDir(t.TempDir()))
		if err := s.themes.Load(); err != nil {
			t.Fatal(err)
		}
	}
	_, err = repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Title: "把社区入口整理得更清楚", Slug: "messaging-layout", AuthorID: owner.UserID, AuthorName: owner.DisplayName, Status: content.StatusPublish, ContentSource: "<p>文章、收藏、关注与通知，各自回到适合的位置。</p>", PublishedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/account/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := s.themes.Load(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", h)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	data, _ := json.Marshal(map[string]any{"url": host.URL, "owner_id": owner.UserID})
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
				return
			}
		}
	}
}

func TestMessagingModuleLifecycle(t *testing.T) {
	s, h, users, repository, cookie, owner := newPersistentAdminTestServer(t)
	peer, _, _ := createAndLoginTestUser(t, users, owner, "message-switch-peer", "peer@example.test", "联系人", "subscriber")
	message, err := s.messages.Send(t.Context(), messaging.Input{SenderID: peer.ID, RecipientID: owner.UserID, Content: "关闭前的私信", Nonce: "messaging-switch-retained"})
	if err != nil {
		t.Fatal(err)
	}
	post, err := repository.Create(t.Context(), content.PostInput{Title: "模块开关测试", Slug: "messaging-switch", AuthorID: owner.UserID, AuthorName: owner.DisplayName, Status: content.StatusPublish, PublishedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := activity.New(activity.Publication, post.ID, post.ID, peer.ID, 1, "publish", now)
	if err := s.notifications.Store.Project(t.Context(), event.Key, []notification.Delivery{{UserID: owner.UserID, Kind: notification.Publication, Event: event}}, now); err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": owner.CSRFToken}
	setEnabled := func(enabled bool) {
		t.Helper()
		body := fmt.Sprintf(`{"revision":%q,"modules":{"messaging":%t}}`, s.siteSettings.Snapshot().Revision, enabled)
		if response := serveContentAPI(h, cookie, http.MethodPatch, "https://example.test/api/v1/admin/modules", body, headers); response.Code != http.StatusOK {
			t.Fatalf("save messaging switch: %d %s", response.Code, response.Body.String())
		}
	}
	readCenter := func(filter string) struct {
		Items   []centerNotice `json:"items"`
		Unread  int            `json:"unread"`
		Through noticeBoundary `json:"through"`
	} {
		t.Helper()
		response := serveContentAPI(h, cookie, http.MethodGet, "https://example.test/api/v1/social/notice-center?filter="+filter, "", nil)
		var result struct {
			Items   []centerNotice `json:"items"`
			Unread  int            `json:"unread"`
			Through noticeBoundary `json:"through"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatalf("notification center: %d %s", response.Code, response.Body.String())
		}
		return result
	}
	before := readCenter("all")
	if len(before.Items) != 2 || before.Unread != 2 || before.Through.MessageID == 0 {
		t.Fatalf("incomplete fixture: %+v", before)
	}
	setEnabled(false)
	if s.moduleEnabled("messaging") || !s.moduleEnabled("notifications") {
		t.Fatal("private messaging and activity notifications must be independent")
	}
	for _, request := range []struct{ method, path string }{
		{"GET", "contacts"}, {"GET", "inbox"}, {"GET", "messages/2"}, {"POST", "messages/2"}, {"POST", "messages/2/read"}, {"GET", "notifications"}, {"POST", "notifications"},
	} {
		assertAPIError(t, serveContentAPI(h, cookie, request.method, "https://example.test/api/v1/social/"+request.path, `{}`, headers), 404, "module.disabled")
	}
	for _, filter := range []string{"all", "unread", "activity", "interaction"} {
		result := readCenter(filter)
		if result.Through.MessageID != 0 || result.Unread != 1 {
			t.Fatalf("private unread state leaked: %+v", result)
		}
		for _, item := range result.Items {
			if item.Kind == "message" || item.PeerID != 0 {
				t.Fatalf("private notice leaked: %+v", item)
			}
		}
		if filter == "interaction" && len(result.Items) != 0 || filter != "interaction" && len(result.Items) != 1 {
			t.Fatalf("unexpected %s notices: %+v", filter, result)
		}
	}
	for _, body := range []string{
		fmt.Sprintf(`{"action":"read","id":"m%d"}`, before.Through.MessageID),
		fmt.Sprintf(`{"action":"clear","through":{"message_id":%d,"activity_id":%d}}`, before.Through.MessageID, before.Through.ActivityID),
	} {
		assertAPIError(t, serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/notice-center", body, headers), 404, "module.disabled")
	}
	result := readCenter("all")
	body := fmt.Sprintf(`{"action":"clear","through":{"message_id":0,"activity_id":%d}}`, result.Through.ActivityID)
	if response := serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/notice-center", body, headers); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	setEnabled(true)
	result = readCenter("all")
	if len(result.Items) != 1 || result.Items[0].Kind != "message" || result.Unread != 1 {
		t.Fatalf("private notice was not retained: %+v", result)
	}
	history, err := s.messages.Messages(t.Context(), owner.UserID, peer.ID, messaging.MessageQuery{})
	if err != nil || len(history.Items) != 1 || history.Items[0].ID != message.ID || history.Items[0].ReadAt != nil {
		t.Fatalf("private history changed while disabled: %+v %v", history, err)
	}
}

func TestMessagingModuleRenderedEntries(t *testing.T) {
	s, h, _, repository, cookie, owner := newPersistentAdminTestServer(t)
	_, err := repository.Create(t.Context(), content.PostInput{Title: "可见入口测试", Slug: "messaging-visibility", AuthorID: owner.UserID, AuthorName: owner.DisplayName, Status: content.StatusPublish, PublishedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"chunnel", "starter", "nexora", "docshub"} {
		t.Run(slug, func(t *testing.T) {
			s.themes = theme.NewManager(filepath.Join("..", "..", "themes"), slug, theme.WithSettingsDir(t.TempDir()))
			if err := s.themes.Load(); err != nil {
				t.Fatal(err)
			}
			for _, enabled := range []bool{false, true} {
				snapshot := s.siteSettings.Snapshot()
				snapshot.Settings.Modules = map[string]bool{"messaging": enabled}
				if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) != 0 {
					t.Fatalf("save: %v %v", fields, err)
				}
				for _, path := range []string{"/", "/account/", "/account/access/", "/posts/messaging-visibility", fmt.Sprintf("/authors/%d/", owner.UserID)} {
					for _, visitor := range []*http.Cookie{nil, cookie} {
						response := serveContentAPI(h, visitor, "GET", "https://example.test"+path, "", nil)
						if response.Code != 200 {
							t.Fatalf("%s enabled=%t: %d %s", path, enabled, response.Code, response.Body.String())
						}
						body := response.Body.String()
						if !enabled {
							for _, entry := range []string{`data-drawer-open="message-drawer"`, `id="message-drawer"`, `data-message-user=`, `data-message-to=`, `data-notification-filter="interaction"`, `data-message-unread-count`} {
								if strings.Contains(body, entry) {
									t.Errorf("%s still renders %s", path, entry)
								}
							}
						}
						if path == "/account/" && visitor != nil {
							if strings.Contains(body, `data-drawer-open="message-drawer"`) != enabled {
								t.Errorf("account message entry did not follow switch")
							}
							if !strings.Contains(body, `data-drawer-open="notification-drawer"`) {
								t.Error("notification entry was removed")
							}
						}
					}
				}
			}
		})
	}
}
