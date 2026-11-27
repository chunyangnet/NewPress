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

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/verification"
)

// Opt-in real pages and reads, disposable accounts and stores. The browser
// intercepts every business write, including mail and verification exchanges.
func TestFormsBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_FORMS_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated feedback, invitations and verification browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, reader := createAndLoginTestUser(t, users, owner, "forms-reader", "forms-reader@example.test", "反馈读者", "subscriber")
	mailbox := &accessMailbox{}
	s.mailSender = mailbox
	setRegistration(t, s, "invite")
	_, invitation, err := users.IssueAccess(t.Context(), auth.AccessInvite, "invited@example.test", owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	boardPath := filepath.Join(storage, "feedback.json")
	boards, err := feedback.NewFileStore(boardPath)
	if err != nil {
		t.Fatal(err)
	}
	s.SetFeedbackStore(boards)
	settings := feedback.Defaults()
	settings.Name, settings.Description = "产品改进建议", "分享使用中的问题与建议，一起完善这个站点。"
	settings.Moderation = false
	actor := feedback.Actor{ID: owner.UserID, Admin: true}
	created, err := boards.Apply(t.Context(), "ideas", actor, feedback.Command{Action: "create", Key: "forms-create-ideas", Settings: &settings}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item, err := boards.Apply(t.Context(), "ideas", actor, feedback.Command{Action: "submit", Key: "forms-public-item", Version: created.Version, Title: "让阅读与讨论更方便", Body: "希望阅读时能方便地记录想法，并和作者交流。", Kind: "suggestion"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reply, err := boards.Apply(t.Context(), "ideas", feedback.Actor{ID: reader.UserID, Roles: []string{"subscriber"}}, feedback.Command{Action: "reply", Key: "forms-reader-reply", Version: item.Version, ItemID: item.ItemID, Body: "我也希望保留阅读时的想法。"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = boards.Apply(t.Context(), "ideas", feedback.Actor{ID: reader.UserID, Roles: []string{"subscriber"}}, feedback.Command{Action: "submit", Key: "forms-private-item", Version: reply.Version, Title: "仅本人可见的反馈", Body: "这段反馈内容不应出现在公开列表。", Kind: "issue", Private: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	verificationStore, err := verification.NewStore(filepath.Join(storage, "verification.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetVerification(verificationStore)
	verificationSettings := verificationStore.Snapshot()
	verificationSettings.Enabled = true
	for _, scene := range verification.Scenes {
		verificationSettings.Scenes[scene] = verification.Rule{Enabled: true}
	}
	if _, err = verificationStore.Save(verificationSettings); err != nil {
		t.Fatal(err)
	}
	readState := func() []byte {
		accounts, err := users.ListUsers(t.Context(), auth.UserQuery{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		invites, err := users.ListInvitations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		state := map[string]any{"accounts": accounts, "invitations": invites, "mailbox": fmt.Sprint(mailbox.messages), "site_settings": s.siteSettings.Snapshot()}
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
		state["theme_settings"] = values
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
		http.Redirect(w, r, "/feedback/ideas/", http.StatusSeeOther)
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
		if r.Method != "GET" && r.Method != "HEAD" {
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
	writeEvidence("fixture.json", map[string]any{"url": host.URL, "login": host.URL + "/fixture/login/owner", "owner_id": owner.UserID, "reader_id": reader.UserID, "item_id": item.ItemID, "reply_id": reply.ReplyID, "invitation_id": invitation.Hash})
	t.Log("forms browser fixture ready", host.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(40 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("forms browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err == nil {
				after := readState()
				if !reflect.DeepEqual(before, after) || blocked.Load() != 0 {
					t.Fatalf("fixture state changed or %d writes escaped browser interception", blocked.Load())
				}
				digest := sha256.Sum256(after)
				writeEvidence("read-only-confirmation.json", map[string]any{"accounts_unchanged": true, "feedback_items_replies_votes_and_receipts_unchanged": true, "invitations_unchanged": true, "mailbox_unchanged": true, "verification_and_site_settings_unchanged": true, "theme_settings_unchanged_except_fixture_style": true, "unintercepted_mutations": blocked.Load(), "state_sha256": hex.EncodeToString(digest[:])})
				return
			}
		}
	}
}
