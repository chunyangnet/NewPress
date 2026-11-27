package httpserver

import (
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

	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/linkpage"
)

// Real templates and reads use only disposable accounts and stores. Browser
// mutations are intercepted, and the complete fixture state is checked on stop.
func TestCollectionsBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_COLLECTIONS_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated circles, links and series browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, repository, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "collections-reader", "collections-reader@example.test", "圈内读者", "subscriber")
	storage := t.TempDir()
	circlePath, linksPath := filepath.Join(storage, "circles.json"), filepath.Join(storage, "links.json")
	circleStore, err := community.NewFileStore(circlePath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := community.NewFiles(filepath.Join(storage, "files"))
	if err != nil {
		t.Fatal(err)
	}
	s.config.CommunityEnabled = true
	s.SetCommunityStore(circleStore)
	s.SetCommunityFiles(files)
	for _, slug := range []string{"makers", "private"} {
		visibility := "public"
		if slug == "private" {
			visibility = "members"
		}
		command := community.Command{Action: "create", Key: "collections-create-" + slug, OwnerID: owner.UserID, Name: "创作与实践", Description: "一起交流创作中的经验。", Rules: "尊重彼此。", JoinPolicy: "open", Visibility: visibility}
		created, err := circleStore.Apply(t.Context(), slug, community.Actor{ID: owner.UserID, Admin: true}, command, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		joined, err := circleStore.Apply(t.Context(), slug, community.Actor{ID: reader.UserID}, community.Command{Action: "join", Key: "collections-join-" + slug, Version: created.Version}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		topic, err := circleStore.Apply(t.Context(), slug, community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "topic", Key: "collections-topic-" + slug, Version: joined.Version, Title: "分享写作中的经验", Body: "## 一起开始\n\n保留真实页面的数据和交互。", Format: "markdown", Kind: "discussion"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = circleStore.Apply(t.Context(), slug, community.Actor{ID: reader.UserID}, community.Command{Action: "reply", Key: "collections-reply-" + slug, Version: topic.Version, TopicID: topic.TopicID, Body: "读者的第一条回复。", Format: "plain"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	links, err := linkpage.NewFileStore(linksPath)
	if err != nil {
		t.Fatal(err)
	}
	s.SetLinkPageStore(links)
	settings := linkpage.Defaults()
	settings.Name, settings.Description = "朋友们的独立站点", "常常拜访的博客与创作空间。"
	board, err := links.Apply(t.Context(), "friends", linkpage.Actor{ID: owner.UserID, Admin: true}, linkpage.Command{Action: "create", Key: "collections-links-create", ExpectedUserID: owner.UserID, Settings: &settings}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"创作手记", "未公开站点"} {
		board, err = links.Apply(t.Context(), "friends", linkpage.Actor{ID: owner.UserID, Admin: true}, linkpage.Command{Action: "save_item", Key: fmt.Sprintf("collections-link-%d", index), ExpectedUserID: owner.UserID, Version: board.Version, Item: &linkpage.Item{Name: name, URL: fmt.Sprintf("https://example.test/site-%d", index), Description: "记录过程，分享经验。", Enabled: index == 0}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	var postIDs []int64
	for index := 0; index < 26; index++ {
		status := content.StatusPublish
		if index == 0 {
			status = content.StatusPrivate
		} else if index == 1 {
			status = content.StatusDraft
		}
		post, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: status, Title: fmt.Sprintf("选集文章 %02d", index), Slug: fmt.Sprintf("collections-article-%d", index), AuthorID: owner.UserID, AuthorName: owner.DisplayName, ContentSource: "<p>选集编排验收文章。</p>", PublishedAt: time.Now().Add(-time.Duration(index+1) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		postIDs = append(postIDs, post.ID)
	}
	series, err := repository.SaveSeries(t.Context(), 0, content.Mutation{Actor: owner.Username}, content.SeriesInput{Kind: content.SeriesKindSequence, Status: content.StatusPublish, Title: "从想法到作品", Slug: "collections-reading", Description: "整理内容，编排一条阅读路径。", PostIDs: postIDs[:3]})
	if err != nil {
		t.Fatal(err)
	}
	readState := func() []byte {
		state := map[string]any{}
		for _, id := range []int64{owner.UserID, reader.UserID} {
			account, err := users.GetUser(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			state[fmt.Sprintf("account-%d", id)] = account
		}
		allPosts, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state["posts"] = allPosts
		for _, post := range allPosts.Items {
			revisions, err := repository.ListRevisions(t.Context(), post.ID, 100)
			if err != nil {
				t.Fatal(err)
			}
			state[fmt.Sprintf("revisions-%d", post.ID)] = revisions
		}
		allSeries, err := repository.ListSeries(t.Context(), content.SeriesQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		state["series"] = allSeries
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
		values := s.themes.SettingsSnapshot().Values
		delete(values, "visual_style")
		state["settings"] = values
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	var blocked atomic.Int64
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
		http.Redirect(w, r, "/community/makers/", http.StatusSeeOther)
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
			http.Error(w, "cannot change fixture style", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.URL.Path != "/api/v1/community/circles/makers/preview" {
			blocked.Add(1)
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
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "series_id": series.ID, "post_ids": postIDs})
	t.Log("collections browser fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("collections browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || blocked.Load() != 0 {
					t.Fatalf("fixture state changed or %d writes escaped browser interception", blocked.Load())
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "circles_and_attachments_unchanged": true, "link_boards_and_receipts_unchanged": true, "posts_revisions_and_series_unchanged": true, "settings_unchanged_except_fixture_style": true, "unintercepted_mutations": blocked.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
