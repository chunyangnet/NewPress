package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/messaging"
	"newpress.local/newpress/internal/notification"
)

// Opt-in browser QA uses memory stores and temporary theme settings. The
// workspace theme is read for live previews; no running site's data is opened.
func TestFeatureControlsBrowserFixture(t *testing.T) {
	artifacts := os.Getenv("NEWPRESS_FEATURE_BROWSER_FIXTURE")
	if artifacts == "" {
		t.Skip("set NEWPRESS_FEATURE_BROWSER_FIXTURE to an output directory for browser QA")
	}
	artifacts, err := filepath.Abs(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, repository, cookie, owner := newPersistentAdminTestServer(t)
	homeQA := os.Getenv("NEWPRESS_HOME_BROWSER") == "1"
	interactionQA := os.Getenv("NEWPRESS_INTERACTION_BROWSER") == "1"
	experienceQA := os.Getenv("NEWPRESS_EXPERIENCE_BROWSER") == "1"
	walletQA := os.Getenv("NEWPRESS_WALLET_BROWSER") == "1"
	var walletRecipientID int64
	var walletLedgerFiles []string
	var peerID int64
	if interactionQA {
		peer, _, _ := createAndLoginTestUser(t, users, owner, "browser-peer", "browser-peer@example.test", "私信验收读者", "subscriber")
		peerID = peer.ID
		for i := 1; i <= 55; i++ {
			if _, err := s.messages.Send(t.Context(), messaging.Input{SenderID: peerID, RecipientID: owner.UserID, Content: fmt.Sprintf("私信验收记录 %02d", i), Nonce: fmt.Sprintf("browser-message-fixture-%04d", i)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if homeQA {
		if !walletQA {
			progressionHTTPStore(t, s)
		}
		values := s.themes.SettingsSnapshot().Values
		for _, key := range []string{"welcome", "checkin", "carousel", "search_banner"} {
			values["home_"+key+"_visibility"] = "both"
		}
		values["home_search_background"] = "video"
		values["home_search_motion"] = true
		values["home_search_video"] = "/fixture/home-background.webm"
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
			t.Fatalf("configure homepage fixture: %v %v", fields, err)
		}
	}
	if walletQA {
		receiver, _, _ := createAndLoginTestUser(t, users, owner, "browser-wallet-peer", "browser-wallet-peer@example.test", "钱包验收读者", "subscriber")
		walletRecipientID = receiver.ID
		walletLedgerFiles = walletBrowserStores(t, s, owner.UserID)
	}
	for _, entry := range []struct {
		slug, name, description string
		enabled                 bool
	}{
		{"ideas", "产品建议与反馈", "收集使用中的想法，和读者一起讨论下一步改进。", true},
		{"archive", "旧版体验反馈", "保留历史反馈，暂时停止开放。", false},
	} {
		settings := feedback.Defaults()
		settings.Name, settings.Description, settings.Enabled = entry.name, entry.description, entry.enabled
		if _, err := s.feedback.Apply(t.Context(), entry.slug, feedbackActor(owner), feedback.Command{Action: "create", Key: "fixture-create-" + entry.slug, Settings: &settings}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct{ slug, name, description string }{
		{"friends", "朋友们的独立站点", "记录常常拜访的博客、工具与创作空间。"},
		{"resources", "设计与开发资源", "为创作者整理的一份长期更新的阅读清单。"},
	} {
		settings := linkpage.Defaults()
		settings.Name, settings.Description = entry.name, entry.description
		if _, err := s.linkPages.Apply(t.Context(), entry.slug, linkpage.Actor{ID: owner.UserID, Admin: true}, linkpage.Command{Action: "create", Key: "fixture-create-" + entry.slug, ExpectedUserID: owner.UserID, Settings: &settings}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	post := createOwnedTestPost(t, repository, owner.UserID, owner.DisplayName, "browser-story", "读者反馈中的下一步：重新整理内容与社区入口")
	var closedPost content.Post
	var parentComment comment.Comment
	if experienceQA {
		post, err = repository.ChangeStatus(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: owner.DisplayName}, content.StatusPublish)
		if err != nil {
			t.Fatal(err)
		}
		closedPost, err = repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish,
			Title: "关闭评论后的输入恢复", Slug: "browser-closed", AuthorID: owner.UserID, AuthorName: owner.DisplayName,
			ContentSource: "<p>保留草稿和已有回执查询。</p>", CommentStatus: content.CommentClosed, PublishedAt: time.Now().Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		parentComment, err = s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, AuthorName: "评论验收读者", AuthorEmail: "comment-reader@example.test", Status: comment.StatusApproved, ContentSource: "已有评论，供回复目标验证。"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var homePosts []content.Post
	if homeQA {
		for i := 0; i < s.siteSettings.Snapshot().Settings.Reading.HomePosts+3; i++ {
			item, err := repository.Create(t.Context(), content.PostInput{
				Type: content.TypePost, Status: content.StatusPublish, Title: fmt.Sprintf("首页交互验收文章 %02d", i),
				Slug: fmt.Sprintf("home-browser-%d", i), AuthorID: owner.UserID, AuthorName: owner.DisplayName,
				Category: "reading", ContentSource: "<p>首页列表与分页的公开内容。</p>", PublishedAt: time.Now().Add(-time.Duration(i+1) * time.Hour),
			})
			if err != nil {
				t.Fatal(err)
			}
			homePosts = append(homePosts, item)
		}
	}
	readState := func() []byte {
		account, err := users.GetUser(t.Context(), owner.UserID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := repository.GetByID(t.Context(), post.ID)
		if err != nil {
			t.Fatal(err)
		}
		revisions, err := repository.ListRevisions(t.Context(), post.ID, 20)
		if err != nil {
			t.Fatal(err)
		}
		values := s.themes.SettingsSnapshot().Values
		delete(values, "visual_style") // Explicit fixture controls change only this preference.
		state := map[string]any{"account": account, "post": current, "revisions": revisions, "theme": values}
		if walletQA {
			for _, path := range walletLedgerFiles {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				state["wallet_"+filepath.Base(path)] = json.RawMessage(data)
			}
			receiver, err := users.GetUser(t.Context(), walletRecipientID)
			if err != nil {
				t.Fatal(err)
			}
			state["wallet_recipient"] = receiver
		}
		if experienceQA {
			comments, err := s.comments.ListAdmin(t.Context(), comment.Query{Limit: 200})
			if err != nil {
				t.Fatal(err)
			}
			posts, err := repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 200})
			if err != nil {
				t.Fatal(err)
			}
			state["all_comments"], state["all_posts"] = comments, posts
		}
		if interactionQA {
			peer, err := users.GetUser(t.Context(), peerID)
			if err != nil {
				t.Fatal(err)
			}
			state["peer"] = peer
			var messages []messaging.Message
			for before := int64(0); ; {
				page, err := s.messages.Messages(t.Context(), owner.UserID, peerID, messaging.MessageQuery{Before: before})
				if err != nil {
					t.Fatal(err)
				}
				messages = append(messages, page.Items...)
				if !page.HasMore {
					break
				}
				before = page.Items[len(page.Items)-1].ID
			}
			state["messages"] = messages
			var notices []messaging.Notice
			for before := int64(0); ; {
				page, err := s.messages.Notices(t.Context(), owner.UserID, messaging.NoticeQuery{Before: before})
				if err != nil {
					t.Fatal(err)
				}
				notices = append(notices, page.Items...)
				if !page.HasMore {
					break
				}
				before = page.Items[len(page.Items)-1].ID
			}
			state["message_notices"] = notices
			prefs, err := s.notifications.Store.Preferences(t.Context(), owner.UserID)
			if err != nil {
				t.Fatal(err)
			}
			state["notification_preferences"] = prefs
			activities, err := s.notifications.Store.Notices(t.Context(), owner.UserID, notification.Query{})
			if err != nil {
				t.Fatal(err)
			}
			state["activity_notices"] = activities
		}
		if homeQA {
			var currentPosts []content.Post
			for _, item := range homePosts {
				current, err := repository.GetByID(t.Context(), item.ID)
				if err != nil {
					t.Fatal(err)
				}
				currentPosts = append(currentPosts, current)
			}
			state["home_posts"] = currentPosts
			progression, err := s.progression.Store.Snapshot(t.Context(), owner.UserID, 0)
			if err != nil {
				t.Fatal(err)
			}
			state["progression"] = progression
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := readState()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		// Reuse the isolated session so QA does not update the account's login timestamp.
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/features/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /fixture/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := s.themes.Load(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
			http.Error(w, "cannot save fixture style", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", handler)
	host := httptest.NewServer(mux)
	defer host.Close()
	s.config.PublicURL = host.URL
	secondPage, err := repository.ListPublishedPage(t.Context(), s.siteSettings.Snapshot().Settings.Reading.HomePosts, s.siteSettings.Snapshot().Settings.Reading.HomePosts)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"url": host.URL, "login": host.URL + "/fixture/login", "owner_id": owner.UserID, "home": homeQA, "peer_id": peerID, "interactions": interactionQA,
		"experience": experienceQA, "post_id": post.ID, "closed_post_id": closedPost.ID, "parent_comment_id": parentComment.ID, "home_second_page_count": len(secondPage.Items),
		"wallet": walletQA, "wallet_recipient_id": walletRecipientID, "wallet_recipient": "browser-wallet-peer"})
	if err := os.WriteFile(filepath.Join(artifacts, "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated feature browser fixture ready", host.URL)
	deadline := time.NewTimer(75 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(artifacts, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(after, before) {
					var old, current map[string]json.RawMessage
					_ = json.Unmarshal(before, &old)
					_ = json.Unmarshal(after, &current)
					for key, value := range old {
						if !reflect.DeepEqual(value, current[key]) {
							t.Errorf("browser checks changed %s", key)
						}
					}
					t.FailNow()
				}
				data, _ := json.Marshal(map[string]bool{"account_unchanged": true, "content_unchanged": true, "revisions_unchanged": true, "progression_checked": homeQA || walletQA, "wallet_all_accounts_ledgers_receipts_orders_unchanged": walletQA, "interactions_checked": interactionQA, "comments_and_community_checked": experienceQA, "settings_unchanged_except_fixture_style": true})
				if err := os.WriteFile(filepath.Join(artifacts, "read-only-confirmation.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
}
