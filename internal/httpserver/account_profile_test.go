package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/mediausage"
	"strconv"
	"strings"
	"testing"
	"time"
)

func avatarRequest(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf string, version int64, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(payload)
	_ = form.WriteField("version", strconv.FormatInt(version, 10))
	_ = form.Close()
	r := httptest.NewRequest("POST", "https://example.test/api/v1/social/profile/avatar", &body)
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestAccountProfileAvatarNarrowAccessAndReferences(t *testing.T) {
	s, h, m, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	u, cookie, p := createAndLoginTestUser(t, m, owner, "profile-reader", "private-profile@example.test", "读者", "subscriber")
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": p.CSRFToken}
	base := "https://example.test/api/v1/social"
	r := serveContentAPI(h, cookie, "POST", base+"/profile", fmt.Sprintf(`{"version":%d,"display_name":"读者","locale":"zh-CN","bio":"记录自己的阅读\n<script>safe text</script>"}`, u.Version), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	u, _ = m.GetUser(t.Context(), u.ID)
	var imageBytes bytes.Buffer
	_ = png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 32, 20)))
	bad := avatarRequest(t, h, cookie, "", u.Version, imageBytes.Bytes())
	if bad.Code != 403 {
		t.Fatal(bad.Code)
	}
	r = avatarRequest(t, h, cookie, p.CSRFToken, u.Version, imageBytes.Bytes())
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	fresh, _ := m.GetUser(t.Context(), u.ID)
	if fresh.AvatarMediaID <= 0 || fresh.Email != u.Email || !fresh.HasRole("subscriber") {
		t.Fatal("identity changed")
	}
	if got := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/media", "", nil); got.Code != 403 {
		t.Fatalf("subscriber media access=%d", got.Code)
	}
	item, err := s.media.Get(fresh.AvatarMediaID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.mediaUsage.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, mediausage.ErrInUse) {
		t.Fatal("avatar deletion was not guarded", err)
	}
	refs := serveContentAPI(h, ownerCookie, "GET", fmt.Sprintf("https://example.test/api/v1/admin/media/%d/references", item.ID), "", nil)
	if refs.Code != 200 || !strings.Contains(refs.Body.String(), `"source":"profile"`) {
		t.Fatal(refs.Code, refs.Body.String())
	}
	page := serveContentAPI(h, &http.Cookie{}, "GET", fmt.Sprintf("https://example.test/authors/%d/", u.ID), "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), item.PublicURL) || strings.Contains(page.Body.String(), u.Email) || strings.Contains(page.Body.String(), "<script>safe text</script>") {
		t.Fatal("public profile failed isolation or escaping")
	}
	before := s.media.Snapshot().Count
	if got := avatarRequest(t, h, cookie, p.CSRFToken, u.Version, imageBytes.Bytes()); got.Code != 409 || s.media.Snapshot().Count != before {
		t.Fatal("stale avatar upload persisted")
	}
	assertAPIError(t, serveContentAPI(h, cookie, "POST", base+"/profile", `{"version":1,"display_name":"x","locale":"zh-CN","avatar_media_id":99}`, headers), 400, "request.invalid_json")
	// A shared avatar remains protected after one account removes it.
	peer, peerCookie, peerP := createAndLoginTestUser(t, m, owner, "profile-peer", "peer-profile@example.test", "同图读者", "subscriber")
	if got := avatarRequest(t, h, peerCookie, peerP.CSRFToken, peer.Version, imageBytes.Bytes()); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	r = serveContentAPI(h, cookie, "POST", base+"/profile", fmt.Sprintf(`{"version":%d,"display_name":"读者","locale":"zh-CN","remove_avatar":true}`, fresh.Version), headers)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if _, err := s.media.Get(item.ID); err != nil {
		t.Fatal("shared avatar removed", err)
	}
}

func TestOwnContentAndSignedCommentOwnership(t *testing.T) {
	s, h, m, repo, ownerCookie, owner := newPersistentAdminTestServer(t)
	u, cookie, p := createAndLoginTestUser(t, m, owner, "own-reader", "own-private@example.test", "相同昵称", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{Title: "公开文章", Slug: "own-comment-post", AuthorID: owner.UserID, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Create(t.Context(), content.PostInput{Title: "我的草稿", Slug: "own-draft", AuthorID: u.ID, Status: content.StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Create(t.Context(), content.PostInput{Title: "其他账号私密标题", Slug: "other-secret", AuthorID: owner.UserID, Status: content.StatusPrivate})
	if err != nil {
		t.Fatal(err)
	}
	base := "https://example.test/api/v1/social/own-content?kind="
	page := serveContentAPI(h, cookie, "GET", "https://example.test/posts/"+post.Slug, "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "data-comment-account-name") || strings.Contains(page.Body.String(), u.Email) {
		t.Fatal("signed-in composer exposed private identity")
	}
	got := serveContentAPI(h, cookie, "GET", base+"posts", "", nil)
	if got.Code != 200 || !strings.Contains(got.Body.String(), "我的草稿") || strings.Contains(got.Body.String(), "其他账号私密标题") || strings.Contains(got.Body.String(), `/admin/content/`) {
		t.Fatal(got.Code, got.Body.String())
	}
	body := fmt.Sprintf(`{"form_token":%q,"parent_id":0,"author_name":"伪造署名","email":"forged@example.test","content":"这条评论应当归属当前登录账号。","website":""}`, s.commentGuard.formToken(post.ID, time.Now()))
	got = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/posts/"+post.Slug+"/comments", body, map[string]string{"Origin": "https://example.test", "Content-Type": "application/json"})
	if got.Code != 201 && got.Code != 202 {
		t.Fatal(got.Code, got.Body.String())
	}
	owned, err := s.comments.ListAdmin(t.Context(), comment.Query{UserID: u.ID, OwnOnly: true})
	if err != nil || len(owned.Items) != 1 || owned.Items[0].AuthorName != p.Label() || owned.Items[0].AuthorEmail != u.Email {
		t.Fatal(owned, err)
	}
	for _, status := range []string{comment.StatusApproved, comment.StatusSpam, comment.StatusTrash} {
		_, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: owner.UserID, AuthorName: p.Label(), AuthorEmail: u.Email, ContentSource: "别人的同名评论", Status: status})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: u.ID, AuthorName: p.Label(), AuthorEmail: u.Email, ContentSource: "自己回收站的评论", Status: comment.StatusTrash})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Title: "修改后的私密文章名", Slug: post.Slug, AuthorID: owner.UserID, Status: content.StatusPrivate})
	if err != nil {
		t.Fatal(err)
	}
	got = serveContentAPI(h, cookie, "GET", base+"comments", "", nil)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"total":1`) || !strings.Contains(got.Body.String(), "原文章当前不可用") {
		t.Fatal(got.Code, got.Body.String())
	}
	for _, secret := range []string{"别人的同名评论", "自己回收站的评论", u.Email, "修改后的私密文章名", post.Slug} {
		if strings.Contains(got.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	other := serveContentAPI(h, ownerCookie, "GET", base+"comments", "", nil)
	if strings.Contains(other.Body.String(), "这条评论应当") {
		t.Fatal("comment ownership crossed accounts")
	}
	if got := serveContentAPI(h, cookie, "GET", base+"comments&status=trash", "", nil); got.Code != 422 {
		t.Fatal(got.Code)
	}
	if got := serveContentAPI(h, cookie, "GET", base+"comments&user_id=1", "", nil); got.Code != 400 {
		t.Fatal(got.Code)
	}
}

func TestGuestImportPreviewVisibilityAndReplay(t *testing.T) {
	s, h, m, repo, _, owner := newPersistentAdminTestServer(t)
	u, cookie, p := createAndLoginTestUser(t, m, owner, "import-reader", "import@example.test", "导入读者", "subscriber")
	pub, err := repo.Create(t.Context(), content.PostInput{Title: "导入文章", Slug: "import-post", AuthorID: owner.UserID, Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	private, err := repo.Create(t.Context(), content.PostInput{Title: "不可导入私密", Slug: "import-private", AuthorID: owner.UserID, Status: content.StatusPrivate})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.engagements.Set(t.Context(), engagement.Input{UserID: u.ID, TargetID: pub.ID, Kind: engagement.Bookmark, Collection: "原收藏夹", Active: true}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"account_id": u.ID, "preview": true, "bookmarks": []string{"/posts/" + pub.Slug, "/posts/" + private.Slug, "https://evil.test/posts/x"}, "likes": []int64{pub.ID, private.ID}, "following": []int64{owner.UserID, u.ID}}
	headers := map[string]string{"Origin": "https://example.test", "Content-Type": "application/json", "X-CSRF-Token": p.CSRFToken}
	post := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		return serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/social/import", string(body), headers)
	}
	got := post()
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"added":2`) || !strings.Contains(got.Body.String(), `"skipped":4`) {
		t.Fatal(got.Code, got.Body.String())
	}
	likes, _ := s.engagements.Entries(t.Context(), u.ID, engagement.Like)
	if len(likes) != 0 {
		t.Fatal("preview saved")
	}
	input["preview"] = false
	got = post()
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	got = post()
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"added":0`) || !strings.Contains(got.Body.String(), `"existing":3`) {
		t.Fatal(got.Code, got.Body.String())
	}
	entries, _ := s.engagements.Entries(t.Context(), u.ID, engagement.Bookmark)
	if len(entries) != 1 || entries[0].Collection != "原收藏夹" {
		t.Fatal(entries)
	}
	input["account_id"] = owner.UserID
	if got := post(); got.Code != 409 {
		t.Fatal("changed account accepted", got.Code)
	}
	input["account_id"] = u.ID
	headers["X-CSRF-Token"] = ""
	if got := post(); got.Code != 403 {
		t.Fatal("CSRF accepted", got.Code)
	}
}
