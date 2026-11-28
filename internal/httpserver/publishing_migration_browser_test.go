package httpserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/questions"
)

// Actual Go pages and read APIs; browser business writes never reach the stores.
func TestPublishingMigrationBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_PUBLISHING_MIGRATION_BROWSER") != "1" {
		t.Skip("opt-in publishing migration fixture")
	}
	dir := os.Getenv("NEWPRESS_PUBLISHING_MIGRATION_BROWSER_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatal("explicit absolute evidence directory required")
	}
	s, handler, accounts, repo, adminCookie, owner := newPersistentAdminTestServer(t)
	reader, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "publishing-reader", "publishing-reader@example.test", "快报投稿者", "subscriber")
	storage := t.TempDir()
	circles, err := community.NewFileStore(filepath.Join(storage, "circles.json"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := community.NewFiles(filepath.Join(storage, "files"))
	if err != nil {
		t.Fatal(err)
	}
	qa, err := questions.NewFileStore(filepath.Join(storage, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunityStore(circles)
	s.SetCommunityFiles(files)
	s.SetQuestionsStore(qa)
	s.config.CommunityEnabled = true
	now := time.Now()
	circle, err := circles.Apply(t.Context(), "publishing", community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "create", Key: "publishing-create-circle", OwnerID: owner.UserID, Name: "写作与交流", Description: "社区草稿与发布的实际页面验收。", Rules: "友善交流，核对来源。", JoinPolicy: "open", Visibility: "public"}, now)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := circles.Apply(t.Context(), "publishing", community.Actor{ID: reader.ID}, community.Command{Action: "join", Key: "publishing-join-circle", Version: circle.Version}, now)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := circles.Apply(t.Context(), "publishing", community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "topic", Key: "publishing-first-topic", Version: joined.Version, Title: "分享写作经验", Body: "## 交流\n\n保留真实页面和请求边界。", Kind: "discussion", Format: "markdown"}, now)
	if err != nil {
		t.Fatal(err)
	}
	question, err := qa.Apply(t.Context(), 0, questions.Actor{ID: reader.ID}, questions.Command{Action: "create", Key: "publishing-first-question", Title: "如何保留发布中的新输入？", Body: "分享草稿恢复与提交时的经验。", Tags: []string{"写作"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	posts := map[string]content.Post{}
	for _, state := range []string{content.StatusDraft, content.StatusPending, content.StatusPublish} {
		body := "快报实际页面验收正文。"
		if state == content.StatusPublish {
			body = strings.Repeat(body, 35)
		}
		post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypeFlash, AuthorID: reader.ID, AuthorName: reader.DisplayName, Status: state, Title: "快报验收 " + state, Slug: "publishing-" + state, ContentSource: body, CommentStatus: content.CommentOpen, Flash: &content.FlashFields{SourceName: "公开资料", SourceURL: "https://example.test/source"}, PublishedAt: time.Date(2026, 11, 1, 2, 30, 42, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		posts[state] = post
	}
	readState := func() []byte {
		t.Helper()
		state := map[string]any{}
		for _, id := range []int64{owner.UserID, reader.ID} {
			account, err := accounts.GetUser(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			state[fmt.Sprintf("account-%d", id)] = account
		}
		all, err := repo.ListAdmin(t.Context(), content.AdminQuery{Type: content.TypeFlash, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state["posts"] = all
		for _, post := range all.Items {
			revisions, err := repo.ListRevisions(t.Context(), post.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			state[fmt.Sprintf("revisions-%d", post.ID)] = revisions
		}
		for _, id := range []int64{0, owner.UserID, reader.ID} {
			for _, kind := range []string{"like", "bookmark"} {
				values, err := s.engagements.States(t.Context(), id, kind, []int64{posts[content.StatusPublish].ID})
				if err != nil {
					t.Fatal(err)
				}
				state[fmt.Sprintf("engagement-%d-%s", id, kind)] = values
			}
		}
		if err := filepath.WalkDir(storage, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(storage, path)
			state[relative] = string(data)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		state["settings"] = s.siteSettings.Snapshot()
		state["theme"] = s.themes.SettingsSnapshot()
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(bytes[:])
	login, readerLogin, stop := "/__publishing_owner/"+nonce, "/__publishing_reader/"+nonce, "/__publishing_stop/"+nonce
	done, once := make(chan struct{}), sync.Once{}
	var escaped atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == login || r.URL.Path == readerLogin) {
			cookie := adminCookie
			if r.URL.Path == readerLogin {
				cookie = readerCookie
			}
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == stop {
			cookie, err := r.Cookie(auth.CookieName)
			if err != nil {
				http.Error(w, "session required", 401)
				return
			}
			principal, ok := accounts.ValidateSessionContext(r.Context(), cookie.Value)
			if !ok || principal.UserID != owner.UserID {
				http.Error(w, "owner required", 403)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			once.Do(func() { close(done) })
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			escaped.Add(1)
			http.Error(w, "browser business writes must be intercepted", 403)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(fixture.Close)
	s.config.PublicURL = fixture.URL
	write := func(name string, value any) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.Write(data)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
	write("fixture.json", map[string]any{"base": fixture.URL, "login": login, "reader_login": readerLogin, "stop": stop, "owner_id": owner.UserID, "reader_id": reader.ID, "flash_id": posts[content.StatusPublish].ID, "draft_id": posts[content.StatusDraft].ID, "pending_id": posts[content.StatusPending].ID, "question_id": question.ID, "topic_id": topic.TopicID, "paths": map[string]string{"flash": "/flash/", "single": "/flash/" + posts[content.StatusPublish].Slug + "/", "admin": fmt.Sprintf("/admin/flash/?edit=%d", posts[content.StatusDraft].ID), "new": "/admin/flash/?edit=new", "subscriber": fmt.Sprintf("/flash/submit/?edit=%d", posts[content.StatusPending].ID), "questions": "/questions/?tag=写作&scope=mine&status=unanswered", "question_new": "/questions/new/", "question": fmt.Sprintf("/questions/%d/", question.ID), "circle": "/community/publishing/", "circle_admin": "/admin/circles/publishing/", "topic": fmt.Sprintf("/community/publishing/topics/%d", topic.TopicID)}})
	t.Log("publishing migration fixture ready")
	select {
	case <-done:
	case <-time.After(25 * time.Minute):
		t.Fatal("explicit stop required within 25 minutes")
	case <-t.Context().Done():
		t.Fatal("fixture interrupted before stop")
	}
	after := readState()
	if !reflect.DeepEqual(before, after) || escaped.Load() != 0 {
		t.Fatalf("fixture state changed or %d writes escaped interception", escaped.Load())
	}
	digest := sha256.Sum256(after)
	write("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "content_and_revisions_unchanged": true, "questions_and_receipts_unchanged": true, "circles_and_attachments_unchanged": true, "engagement_unchanged": true, "settings_unchanged": true, "unintercepted_writes": escaped.Load(), "state_sha256": hex.EncodeToString(digest[:])})
}
