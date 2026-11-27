package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/virtualcurrency"
)

func recognitionHTTPStore(t *testing.T, s *Server) (*recognition.Service, *virtualcurrency.FileStore, virtualcurrency.Settings) {
	t.Helper()
	coin, cfg := currencyHTTPStore(t, s, false)
	backend, err := recognition.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	service := &recognition.Service{Backend: backend, Users: s.auth, Content: s.repository, Currencies: s.currencyCatalog}
	s.SetRecognitionService(service)
	settings := recognition.InitialState().Settings
	settings.TipsEnabled = true
	settings.VerificationEnabled = true
	settings.Currencies[0].Presets = []string{"1.25", "5", "10"}
	if err = service.SaveSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	return service, coin, cfg
}
func TestRecognitionHTTPPermissionsPrivacyAndCommentProjection(t *testing.T) {
	s, h, manager, repo, cookie, owner := newPersistentAdminTestServer(t)
	service, coin, cfg := recognitionHTTPStore(t, s)
	u, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "identity-reader", "identity-reader@example.test", "认证读者", "subscriber")
	for _, path := range []string{"/api/v1/social/recognition", "/api/v1/admin/recognition"} {
		r := serveContentAPI(h, nil, "GET", "https://example.test"+path, "", nil)
		if r.Code != 401 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/admin/recognition", "/admin/recognition/"} {
		r := serveContentAPI(h, readerCookie, "GET", "https://example.test"+path, "", nil)
		if r.Code != 403 {
			t.Fatal(path, r.Code)
		}
	}
	apply := recognition.IdentityInput{UserID: u.ID, Version: 1, Action: "apply", Key: "creator", Label: "摄影作者", Evidence: "private-application-evidence"}
	body := currencyJSON(t, apply)
	r := serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/social/recognition", body, authenticatedAPIHeaders(reader))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	apply.Action = "grant"
	apply.Version = 2
	apply.Reason = "自行授予"
	r = serveContentAPI(h, readerCookie, "POST", "https://example.test/api/v1/social/recognition", currencyJSON(t, apply), authenticatedAPIHeaders(reader))
	if r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	apply.Action = "approve"
	r = serveContentAPI(h, cookie, "POST", "https://example.test/api/v1/admin/recognition/account", currencyJSON(t, apply), authenticatedAPIHeaders(owner))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "支持认真创作", Slug: "recognition-test", AuthorID: owner.UserID, AuthorName: owner.Label(), ContentSource: "<p>认真写作，友善交流。</p>", CommentStatus: content.CommentOpen})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: u.ID, Delta: 10000, ExpectedVersion: cfg.Version, RequestID: "recognition-http-credit", Reason: "测试余额"})
	if err != nil {
		t.Fatal(err)
	}
	tip := recognition.TipInput{PostID: post.ID, RequestID: "recognition-http-tip", Currency: "main", Amount: "1.25", Hidden: true, SettingsVersion: 2, CurrencyVersion: cfg.Version}
	path := fmt.Sprintf("https://example.test/api/v1/social/tips/%d", post.ID)
	for _, origin := range []string{"", "https://evil.test"} {
		headers := authenticatedAPIHeaders(reader)
		headers["Origin"] = origin
		r = serveContentAPI(h, readerCookie, "POST", path, currencyJSON(t, tip), headers)
		if r.Code != 403 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for range 2 {
		r = serveContentAPI(h, readerCookie, "POST", path, currencyJSON(t, tip), authenticatedAPIHeaders(reader))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	balance, _ := coin.Balance(t.Context(), u.ID)
	if balance != 9875 {
		t.Fatal("duplicate charge", balance)
	}
	c, err := s.comments.Create(t.Context(), comment.CreateInput{PostID: post.ID, UserID: u.ID, AuthorName: u.DisplayName, AuthorEmail: u.Email, ContentSource: "谢谢分享，已支持。", Status: comment.StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	view := s.publicCommentWithOrigin(httptest.NewRequest("GET", "https://example.test/", nil), c)
	raw, _ := json.Marshal(view)
	if len(view.Badges) != 1 || view.Support.Label != "已打赏" || strings.Contains(string(raw), "private-application") || strings.Contains(string(raw), "1.25") {
		t.Fatal(string(raw))
	}
	guest := c
	guest.UserID = 0
	guest.ID++
	if v := s.publicCommentWithOrigin(httptest.NewRequest("GET", "https://example.test/", nil), guest); len(v.Badges) > 0 || v.Support.Supported {
		t.Fatal("guest nickname impersonated authenticated identity", v)
	}
	for _, route := range []string{"/posts/recognition-test", "/admin/recognition/", "/account/"} {
		r = serveContentAPI(h, cookie, "GET", "https://example.test"+route, "", nil)
		if r.Code != 200 {
			t.Fatal(route, r.Code, r.Body.String())
		}
		if strings.Contains(r.Body.String(), "private-application-evidence") {
			t.Fatal("private evidence rendered", route)
		}
	}
	st, _ := service.Backend.Read(t.Context())
	st.Settings.HideAmounts = true
	if err = service.SaveSettings(t.Context(), st.Settings); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(h, readerCookie, "GET", "https://example.test/api/v1/social/recognition", "", nil)
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(r.Header())
	}
	_, err = manager.UpdateUser(t.Context(), owner, u.ID, u.Version, auth.UserInput{Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, Locale: u.Locale, Status: auth.UserStatusSuspended, RoleSlugs: []string{"subscriber"}})
	if err != nil {
		t.Fatal(err)
	}
	items := []comment.Comment{{ID: 1, UserID: u.ID}, {ID: 2, UserID: u.ID}}
	badges, supports := s.commentRecognition(t.Context(), post.ID, items)
	for _, item := range items {
		if len(badges[item.ID]) > 0 || supports[item.ID].Supported {
			t.Fatal("suspended account retained public identity", badges, supports)
		}
	}
	d := s.baseView("文章", "", "/posts/recognition-test")
	d.Post = post
	d.Post.AuthorID = u.ID
	s.prepareRecognition(t.Context(), &d)
	if len(d.Recognition.AuthorBadges) > 0 || d.Recognition.TipsEnabled {
		t.Fatal("suspended author retained badges or tipping", d.Recognition)
	}
}
func TestRecognitionTipsUseSingleSwitch(t *testing.T) {
	s, h, manager, repo, _, owner := newPersistentAdminTestServer(t)
	service, _, cfg := recognitionHTTPStore(t, s)
	_, cookie, reader := createAndLoginTestUser(t, manager, owner, "tip-switch-reader", "tip-switch@example.test", "读者", "subscriber")
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "统一打赏开关", Slug: "tip-switch", AuthorID: owner.UserID, AuthorName: owner.Label(), ContentSource: "<p>正文</p>"})
	if err != nil {
		t.Fatal(err)
	}
	// Persisted legacy theme settings must load without becoming a second gate.
	values := s.themes.SettingsSnapshot().Values
	values["article_tips"] = false
	values["comments_module"] = false
	if _, _, err = s.themes.SaveSettings(values); err != nil {
		t.Fatal(err)
	}
	if err = s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	for _, group := range s.themes.SettingsSnapshot().Groups {
		for _, field := range group.Fields {
			if field.Key == "article_tips" {
				t.Fatal("legacy tip switch is still exposed")
			}
		}
	}
	path := fmt.Sprintf("https://example.test/api/v1/social/tips/%d", post.ID)
	for _, enabled := range []bool{true, false, true} {
		st, err := service.Backend.Read(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		st.Settings.TipsEnabled = enabled
		if err = service.SaveSettings(t.Context(), st.Settings); err != nil {
			t.Fatal(err)
		}
		r := serveContentAPI(h, nil, "GET", "https://example.test/posts/tip-switch", "", nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), "data-article-tips") != enabled {
			t.Fatalf("article entry does not follow switch %v: status %d", enabled, r.Code)
		}
		if !strings.Contains(r.Body.String(), `id="comments"`) || strings.Contains(s.themes.SettingsBodyClasses(), "theme-comments-off") {
			t.Fatal("legacy theme switch still suppresses comments")
		}
		r = serveContentAPI(h, cookie, "GET", path, "", nil)
		var response struct {
			Enabled bool `json:"enabled"`
		}
		if err = json.Unmarshal(r.Body.Bytes(), &response); err != nil || r.Code != 200 || response.Enabled != enabled {
			t.Fatalf("tip API does not follow switch %v: %d %s", enabled, r.Code, r.Body.String())
		}
		if !enabled {
			tip := recognition.TipInput{PostID: post.ID, RequestID: "disabled-tip-switch", Currency: "main", Amount: "1.25", SettingsVersion: st.Settings.Version + 1, CurrencyVersion: cfg.Version}
			r = serveContentAPI(h, cookie, "POST", path, currencyJSON(t, tip), authenticatedAPIHeaders(reader))
			if r.Code != 409 || !strings.Contains(r.Body.String(), "recognition.disabled") {
				t.Fatal("disabled tips accepted", r.Code, r.Body.String())
			}
		}
	}
}

func TestRecognitionLegacyPrivacyUnifiedOnSave(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	service, _, _ := recognitionHTTPStore(t, s)
	values := s.themes.SettingsSnapshot().Values
	values["tip_hide_amount"] = true
	if _, _, err := s.themes.SaveSettings(values); err != nil {
		t.Fatal(err)
	}
	if err := service.Backend.Update(t.Context(), func(st *recognition.State) error {
		st.PrivacyUnified = false // Existing installation, before the first unified save.
		st.Settings.HideAmounts = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/recognition", "", nil)
	var response struct {
		Settings recognition.Settings `json:"settings"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil || r.Code != 200 || !response.Settings.HideAmounts {
		t.Fatal("legacy privacy was not carried into the admin form", r.Body.String())
	}
	st, _ := service.Backend.Read(t.Context())
	if !s.recognitionHideAmounts(st) {
		t.Fatal("legacy amounts became public before an explicit save")
	}
	response.Settings.HideAmounts = false
	r = serveContentAPI(h, cookie, "PUT", "https://example.test/api/v1/admin/recognition", currencyJSON(t, response.Settings), authenticatedAPIHeaders(owner))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	st, _ = service.Backend.Read(t.Context())
	if !st.PrivacyUnified || s.recognitionHideAmounts(st) {
		t.Fatal("legacy theme still overrides the saved global preference")
	}
}

func TestRecognitionCopyrightEscapesAndThemePrivacy(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	recognitionHTTPStore(t, s)
	d := s.baseView("文章", "摘要", "/posts/sample")
	d.Now = time.Now()
	d.Post = content.Post{ID: 1, AuthorID: 1, Type: content.TypePost, Title: "测试", AuthorName: "<script>alert(1)</script>"}
	d.ThemeValues["article_copyright_text"] = "作者 {author}，站点 {site}，文章 {title}，链接 {url}，年份 {year}"
	s.prepareRecognition(t.Context(), &d)
	if !strings.Contains(d.Recognition.CopyrightText, d.Post.AuthorName) || strings.Contains(d.Recognition.CopyrightText, "{author}") {
		t.Fatal(d.Recognition)
	}
	// Template rendering is exercised separately; the projection remains plain text.
	if d.Recognition.CopyrightTitle == "" {
		t.Fatal("missing copyright")
	}
	w := httptest.NewRecorder()
	s.render(w, 200, "single", d)
	if w.Code != 200 || strings.Contains(w.Body.String(), d.Post.AuthorName) || !strings.Contains(w.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("copyright was not escaped in rendered HTML", w.Code)
	}
}

func TestRecognitionSettingsProtocolAndThemeSwitches(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	service, _, _ := recognitionHTTPStore(t, s)
	st, _ := service.Backend.Read(t.Context())
	valid := currencyJSON(t, st.Settings)
	for _, body := range []string{
		strings.Replace(valid, `"tips_enabled":true,`, "", 1),
		strings.Replace(valid, `"tips_enabled":true`, `"tips_enabled":null`, 1),
		strings.Replace(valid, `"tips_enabled":true`, `"Tips_Enabled":true`, 1),
		strings.Replace(valid, `"tips_enabled":true`, `"tips_enabled":true,"tips_enabled":false`, 1),
	} {
		r := serveContentAPI(h, cookie, "PUT", "https://example.test/api/v1/admin/recognition", body, authenticatedAPIHeaders(owner))
		if r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	r := serveContentAPI(h, cookie, "PUT", "https://example.test/api/v1/admin/recognition", valid, authenticatedAPIHeaders(owner))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	r = serveContentAPI(h, cookie, "PUT", "https://example.test/api/v1/admin/recognition", valid, authenticatedAPIHeaders(owner))
	if r.Code != 409 {
		t.Fatal("stale settings were accepted", r.Code)
	}
	_, err := service.UpdateIdentity(t.Context(), owner.UserID, true, recognition.IdentityInput{UserID: owner.UserID, Version: 1, Action: "grant", Key: "original", Reason: "社区贡献"})
	if err != nil {
		t.Fatal(err)
	}
	items := []comment.Comment{{ID: 1, UserID: owner.UserID}}
	badges, _ := s.commentRecognition(t.Context(), 1, items)
	if len(badges[1]) != 1 {
		t.Fatal("medal missing", badges)
	}
	values := s.themes.SettingsSnapshot().Values
	values["comment_medals"] = false
	if _, _, err = s.themes.SaveSettings(values); err != nil {
		t.Fatal(err)
	}
	badges, _ = s.commentRecognition(t.Context(), 1, items)
	if len(badges[1]) != 1 {
		t.Fatal("legacy theme switch hid an enabled medal", badges)
	}
}

func TestRecognitionIndependentThemeAccountFallback(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	service, _, _ := recognitionHTTPStore(t, s)
	_, err := service.UpdateIdentity(t.Context(), owner.UserID, true, recognition.IdentityInput{UserID: owner.UserID, Version: 1, Action: "grant", Key: "original", Reason: "原创贡献"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", "starter"), filepath.Join(root, "starter")); err != nil {
		t.Fatal(err)
	}
	s.themes = theme.NewManager(root, "starter", theme.WithSettingsDir(t.TempDir()))
	if err = s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	r := serveContentAPI(h, cookie, "GET", "https://example.test/account/?view=identity", "", nil)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, marker := range []string{"data-identity-panel", "原创之星", "/css/newpress-recognition.css", "/js/newpress-client.js", "/js/newpress-recognition.js"} {
		if !strings.Contains(r.Body.String(), marker) {
			t.Fatal("independent theme account fallback missing", marker)
		}
	}
}

func TestRecognitionMedalDatesExcludePrivateAuditAndExpiredAwards(t *testing.T) {
	st := recognition.InitialState()
	now := time.Now().UTC().Truncate(time.Second)
	st.Identities["2"] = recognition.Identity{Version: 1, Equipped: []string{"original"},
		Grants:      []recognition.Grant{{Key: "original", GrantedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ActorID: 17}},
		Application: &recognition.Application{Evidence: "private-evidence"}, History: []recognition.Event{{Reason: "private-audit", ActorID: 17}}}
	badges := publicIdentityBadges(st, 2)
	if len(badges) != 1 || badges[0].GrantedAt != now.Add(-time.Hour).Format(time.RFC3339) || badges[0].ExpiresAt != now.Add(time.Hour).Format(time.RFC3339) {
		t.Fatal(badges)
	}
	raw, _ := json.Marshal(badges)
	for _, secret := range []string{"private-evidence", "private-audit", "actor_id", "history", "application"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private award data exposed", string(raw))
		}
	}
	identity := st.Identities["2"]
	identity.Grants[0].ExpiresAt = now.Add(-time.Second)
	st.Identities["2"] = identity
	if badges = publicIdentityBadges(st, 2); len(badges) != 0 {
		t.Fatal("expired medal remains public", badges)
	}
	identity.Grants[0].ExpiresAt = time.Time{}
	st.Identities["2"] = identity
	if badges = publicIdentityBadges(st, 2); len(badges) != 1 || badges[0].ExpiresAt != "" {
		t.Fatal("permanent medal has an expiry", badges)
	}
}

func TestRecognitionLevelsUseRankAndRespectPublicVisibility(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg, _ := progressionHTTPStore(t, s)
	cfg.Levels.Items[2].Name = "名称里没有数字"
	cfg, err := store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Adjust(t.Context(), progression.Adjustment{ActorID: owner.UserID, UserID: owner.UserID, Delta: 750, ExpectedVersion: cfg.Version, RequestID: "recognition-level-rank-test", Reason: "等级测试"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ManageMembership(t.Context(), progression.MembershipInput{ActorID: owner.UserID, UserID: owner.UserID, ExpectedVersion: cfg.Version, MemberVersion: 0, Action: "grant", Plan: "member", Days: 30, RequestID: "recognition-membership-rank-test", Reason: "会员测试"}); err != nil {
		t.Fatal(err)
	}
	// Levels also work when the independent recognition module is unavailable.
	s.recognition = nil
	items := []comment.Comment{{ID: 1, UserID: owner.UserID}}
	badges, _ := s.commentRecognition(t.Context(), 1, items)
	if len(badges[1]) != 2 || badges[1][0].Kind != "level" || badges[1][0].Level != 3 || badges[1][1].Kind != "membership" || badges[1][1].Level != 0 {
		t.Fatal(badges)
	}
	r := serveContentAPI(h, cookie, "GET", "https://example.test/account/", "", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `aria-label="等级 3 · 名称里没有数字"`) || !strings.Contains(r.Body.String(), `identity-level-number">3</span>`) {
		t.Fatal("account lost level artwork", r.Code)
	}
	cfg.Levels.PublicBadges = false
	cfg.Membership.PublicBadges = false
	if _, err = store.UpdateSettings(t.Context(), cfg.Version, cfg); err != nil {
		t.Fatal(err)
	}
	badges, _ = s.commentRecognition(t.Context(), 1, items)
	if len(badges[1]) != 0 {
		t.Fatal("private levels or membership exposed", badges)
	}
	if own := s.progressionBadges(t.Context(), owner.UserID, false); len(own) != 2 || own[0].Level != 3 {
		t.Fatal("own level was hidden", own)
	}
}
