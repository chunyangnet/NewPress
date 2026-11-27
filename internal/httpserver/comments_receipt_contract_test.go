package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

// Real response bodies must satisfy the browser's confirmation checks. A page
// opened under guest policy must also contain the initially hidden login gate
// needed when the administrator changes that policy before submission.
func TestCommentReceiptAndLateLoginPolicyContract(t *testing.T) {
	s, handler, users, repo, _, owner := newPersistentAdminTestServer(t)
	_, member, _ := createAndLoginTestUser(t, users, owner, "receipt-reader", "reader@example.test", "账号读者", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{
		Title: "评论回执", Slug: "comment-receipt-contract", AuthorID: owner.UserID,
		Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour), CommentStatus: content.CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := func(login, moderation bool) {
		t.Helper()
		settings := s.siteSettings.Snapshot()
		settings.Settings.Discussion.RequireLogin = login
		settings.Settings.Discussion.RequireModeration = moderation
		settings.Settings.Discussion.RateLimitPerTenMinutes = 20
		if _, fields, err := s.siteSettings.Save(settings.Revision, settings.Settings); err != nil || len(fields) > 0 {
			t.Fatal(fields, err)
		}
	}
	policy(false, true)
	article := "https://example.test/posts/" + post.Slug
	endpoint := "https://example.test/api/v1/posts/" + post.Slug + "/comments"
	initial := serveContentAPI(handler, nil, http.MethodGet, article, "", nil)
	if initial.Code != http.StatusOK || !strings.Contains(initial.Body.String(), fmt.Sprintf(`data-comment-post-id="%d"`, post.ID)) ||
		!regexp.MustCompile(`data-comment-login-gate\s+hidden`).MatchString(initial.Body.String()) ||
		!strings.Contains(initial.Body.String(), `data-comment-login-required="false"`) {
		t.Fatal("guest page needs its post identity and a hidden recovery gate", initial.Code)
	}
	token := s.commentGuard.formToken(post.ID, time.Now())
	write := func(session *http.Cookie, parent int64, website string, want int) publicCommentCreateAPIResponse {
		t.Helper()
		payload := publicCommentCreateAPIRequest{FormToken: token, ParentID: parent, AuthorName: "访客读者", Email: "guest@example.test", Content: "保留这次认真表达", Website: website}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response := serveContentAPI(handler, session, http.MethodPost, endpoint, string(body), map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"})
		if response.Code != want {
			t.Fatalf("submit: %d, want %d: %s", response.Code, want, response.Body.String())
		}
		if want == http.StatusUnauthorized {
			if !strings.Contains(response.Body.String(), `comments.login_required`) {
				t.Fatal(response.Body.String())
			}
			return publicCommentCreateAPIResponse{}
		}
		var result publicCommentCreateAPIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != comment.StatusApproved && result.Status != comment.StatusPending {
			t.Fatal("unsupported success state", result)
		}
		if website != "" {
			if result.Status != comment.StatusPending || result.Comment != nil {
				t.Fatal("honeypot acknowledgement changed", result)
			}
			return result
		}
		if result.Comment == nil || result.Comment.ID <= 0 || result.Comment.PostID != post.ID || result.Comment.ParentID != parent || strings.TrimSpace(result.Comment.AuthorName) == "" {
			t.Fatal("incomplete or mismatched receipt", result)
		}
		return result
	}
	write(nil, 0, "", http.StatusAccepted)
	write(nil, 0, "https://bot.example.test", http.StatusAccepted)
	policy(false, false)
	published := write(nil, 0, "", http.StatusCreated)
	write(nil, published.Comment.ID, "", http.StatusCreated)
	before, err := s.comments.ListAdmin(t.Context(), comment.Query{PostID: post.ID})
	if err != nil || len(before.Items) != 3 {
		t.Fatal("normal writes or honeypot handling changed", before, err)
	}
	policy(true, true)
	write(nil, 0, "", http.StatusUnauthorized)
	after, err := s.comments.ListAdmin(t.Context(), comment.Query{PostID: post.ID})
	if err != nil || len(after.Items) != len(before.Items) {
		t.Fatal("rejected guest write persisted", after, err)
	}
	locked := serveContentAPI(handler, nil, http.MethodGet, article, "", nil)
	if locked.Code != http.StatusOK || !strings.Contains(locked.Body.String(), `data-comment-locked="true"`) || regexp.MustCompile(`data-comment-login-gate\s+hidden`).MatchString(locked.Body.String()) {
		t.Fatal("new policy must lock freshly rendered pages", locked.Code)
	}
	confirmed := write(member, published.Comment.ID, "", http.StatusAccepted)
	if confirmed.Comment.AuthorName != "账号读者" {
		t.Fatal("account identity was not retained", confirmed.Comment)
	}
}
