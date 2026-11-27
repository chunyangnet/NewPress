package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

func resourceFixtureConfig() resources.Config {
	c, e := resources.Normalize(resources.Config{Items: []resources.Resource{{ID: "bundle", Title: "社区内容运营全套资料包.zip", Description: "从内容规划到社区增长，完整的实用资料。", Size: "248.6 MB", Updated: "2026-11-21", OriginalPrice: "¥29.90", Badge: "专属资源", Features: []string{"专属资料", "持续更新"}, MemberCTA: true, DailyLimit: 2, Variants: []resources.Variant{{ID: "complete", Name: "完整资料包", Kind: "download", Body: "<p>SECRET_BODY</p>", Downloads: []resources.Download{{ID: "baidu", Name: "完整资料包", Provider: "baidu", URL: "https://pan.baidu.com/s/SECRET_LINK", Code: "SECRET_CODE", Password: "SECRET_PASSWORD"}}, Groups: []resources.Group{{Name: "单次购买", Conditions: []resources.Condition{{ID: "pay", Kind: "payment", Amount: 990, Currency: "CNY"}}}, {Name: "积分解锁", Conditions: []resources.Condition{{ID: "points", Kind: "points", Amount: 100, Currency: "main"}}}, {Name: "会员专享", Conditions: []resources.Condition{{ID: "member", Kind: "membership", Minimum: 2, Term: "annual"}}}}}}}}})
	if e != nil {
		panic(e)
	}
	return c
}
func TestResourcePrivacyPaymentReplayRefundAndDownloadLimits(t *testing.T) {
	s, h, accounts, repo, _, owner := newPersistentAdminTestServer(t)
	service, _, coin := commerceHTTPStore(t, s)
	_, cookie, reader := createAndLoginTestUser(t, accounts, owner, "resource-reader", "resource-reader@example.test", "资源读者", "subscriber")
	cfg := resourceFixtureConfig()
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "资源文章", Slug: "resource-article", Status: content.StatusPublish, ContentSource: "<p>公开内容</p>", Resources: &cfg})
	item := resources.FromMeta(p.Meta).Items[0]
	endpoint := fmt.Sprintf("https://example.test/api/v1/article-resources/%d/bundle", p.ID)
	for _, path := range []string{endpoint, "https://example.test/posts/" + p.Slug, "https://example.test/api/v1/posts"} {
		r := serveContentAPI(h, cookie, "GET", path, "", nil)
		if r.Code != 200 {
			t.Fatal(path, r.Code, r.Body.String())
		}
		for _, secret := range []string{"SECRET_BODY", "SECRET_LINK", "SECRET_CODE", "SECRET_PASSWORD"} {
			if strings.Contains(r.Body.String(), secret) {
				t.Fatal("leaked", path, secret)
			}
		}
	}
	req := resourceActionInput{Action: "points", Variant: "complete", Condition: "points", Group: 1, Revision: resources.Hash(item), Accepted: true}
	call := func(in resourceActionInput) int {
		raw, _ := json.Marshal(in)
		r := serveContentAPI(h, cookie, "POST", endpoint, string(raw), authenticatedAPIHeaders(reader))
		return r.Code
	}
	noCSRF, _ := json.Marshal(req)
	if r := serveContentAPI(h, cookie, "POST", endpoint, string(noCSRF), nil); r.Code != 403 {
		t.Fatal(r.Code)
	}
	old := req
	old.Revision = "stale"
	if call(old) != 409 {
		t.Fatal("stale price accepted")
	}
	settings, _ := coin.Settings(t.Context())
	if _, e := coin.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: reader.UserID, Delta: 1000, ExpectedVersion: settings.Version, RequestID: "resource-purchase-fund", Reason: "测试资源购买"}); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if status := call(req); status != 200 {
			t.Fatal("points", status)
		}
	}
	balance, _ := coin.Balance(t.Context(), reader.UserID)
	if balance != 900 {
		t.Fatal("double charge", balance)
	}
	state := serveContentAPI(h, cookie, "GET", endpoint, "", nil)
	if !strings.Contains(state.Body.String(), "SECRET_BODY") || strings.Contains(state.Body.String(), "SECRET_LINK") {
		t.Fatal(state.Body.String())
	}
	download := resourceActionInput{Action: "download", Variant: "complete", Link: "baidu", Request: "download-one", Revision: req.Revision}
	if call(download) != 200 || call(download) != 200 {
		t.Fatal("download retry")
	}
	download.Request = "download-two"
	if call(download) != 200 {
		t.Fatal("second download")
	}
	download.Request = "download-three"
	if call(download) != 429 {
		t.Fatal("quota bypass")
	}
	// A second reader buys through a verified provider callback, then loses access after refund.
	_, payCookie, payer := createAndLoginTestUser(t, accounts, owner, "resource-payer", "resource-payer@example.test", "付费读者", "subscriber")
	purchase := resourceActionInput{Action: "pay", Variant: "complete", Condition: "pay", Group: 0, Revision: req.Revision, Channel: "epay", Accepted: true}
	raw, _ := json.Marshal(purchase)
	response := serveContentAPI(h, payCookie, "POST", endpoint, string(raw), authenticatedAPIHeaders(payer))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var order struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &order)
	pending := serveContentAPI(h, payCookie, "GET", endpoint, "", nil)
	if strings.Contains(pending.Body.String(), "SECRET_BODY") {
		t.Fatal("pending unlocked")
	}
	if _, e := service.Notify(t.Context(), order.ID, http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}}, []byte(epayHTTPNotification(order.ID, "9.90", "fixture-merchant-secret"))); e != nil {
		t.Fatal(e)
	}
	paid := serveContentAPI(h, payCookie, "GET", endpoint, "", nil)
	if !strings.Contains(paid.Body.String(), "SECRET_BODY") {
		t.Fatal(paid.Body.String())
	}
	if e := service.Backend.Update(t.Context(), func(st *commerce.State) error {
		o := st.Orders[order.ID]
		o.Status = "refunded"
		st.Orders[order.ID] = o
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	refunded := serveContentAPI(h, payCookie, "GET", endpoint, "", nil)
	if strings.Contains(refunded.Body.String(), "SECRET_BODY") {
		t.Fatal("refund retained access")
	}
	if _, e := repo.ChangeStatus(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, content.StatusPrivate); e != nil {
		t.Fatal(e)
	}
	if r := serveContentAPI(h, cookie, "GET", endpoint, "", nil); r.Code != 404 {
		t.Fatal("withdrawn post", r.Code)
	}
}
func TestResourceVersionsQuizFormsAndEditorRoundTrip(t *testing.T) {
	_, h, accounts, repo, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, cookie, reader := createAndLoginTestUser(t, accounts, owner, "form-reader", "form-reader@example.test", "表单读者", "subscriber")
	cfg := resourceFixtureConfig()
	v := &cfg.Items[0].Variants[0]
	v.Kind = "content"
	v.Downloads = nil
	v.Groups = []resources.Group{{Name: "填写并答题", Conditions: []resources.Condition{{ID: "login", Kind: "login"}, {ID: "quiz", Kind: "quiz"}, {ID: "form", Kind: "form"}}}}
	v.Questions = []resources.Question{{ID: "question", Title: "请输入四", Answer: "答案四"}}
	v.Fields = []resources.Field{{ID: "email", Label: "邮箱", Type: "email", Required: true}}
	cfg.Items[0].Selection = "first"
	cfg.Items[0].Variants = append(cfg.Items[0].Variants, resources.Variant{ID: "alternative", Name: "公开版", Kind: "content", Body: "<p>PUBLIC_VERSION</p>", Groups: []resources.Group{{Conditions: []resources.Condition{{ID: "all", Kind: "public"}}}}})
	p := createWidgetPost(t, repo, content.PostInput{AuthorID: owner.UserID, Title: "隐藏版本", Slug: "hidden-versions", Status: content.StatusPublish, Resources: &cfg})
	item := resources.FromMeta(p.Meta).Items[0]
	endpoint := fmt.Sprintf("https://example.test/api/v1/article-resources/%d/bundle", p.ID)
	before := serveContentAPI(h, cookie, "GET", endpoint, "", nil)
	if strings.Contains(before.Body.String(), "答案四") || strings.Contains(before.Body.String(), "SECRET_BODY") || !strings.Contains(before.Body.String(), "PUBLIC_VERSION") {
		t.Fatal(before.Body.String())
	}
	body, _ := json.Marshal(resourceActionInput{Action: "submit", Variant: "complete", Revision: resources.Hash(item), Fields: map[string]string{"email": "reader@example.test"}, Answers: map[string]string{"question": "答案四"}})
	response := serveContentAPI(h, cookie, "POST", endpoint, string(body), authenticatedAPIHeaders(reader))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	after := serveContentAPI(h, cookie, "GET", endpoint, "", nil)
	if !strings.Contains(after.Body.String(), "SECRET_BODY") || strings.Contains(after.Body.String(), "PUBLIC_VERSION") || strings.Contains(after.Body.String(), "reader@example.test") {
		t.Fatal(after.Body.String())
	}
	admin := serveCookieRequest(h, "GET", fmt.Sprintf("https://example.test/admin/content/%d/edit/", p.ID), ownerCookie, nil)
	if admin.Code != 200 || !strings.Contains(admin.Body.String(), "data-resource-config") || !strings.Contains(admin.Body.String(), "SECRET_BODY") {
		t.Fatal("editor", admin.Code)
	}
	path := fmt.Sprintf("https://example.test/api/v1/admin/resource-submissions/%d", p.ID)
	if r := serveContentAPI(h, cookie, "GET", path, "", nil); r.Code != 403 {
		t.Fatal("submission exposed", r.Code)
	}
	data := serveContentAPI(h, ownerCookie, "GET", path, "", nil)
	if !strings.Contains(data.Body.String(), "reader@example.test") {
		t.Fatal(data.Body.String())
	}
	input := inputFromPost(p)
	input.Title = "修订标题"
	next, e := repo.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, input)
	if e != nil {
		t.Fatal(e)
	}
	if resources.FromMeta(next.Meta).Items[0].Variants[0].Body != v.Body {
		t.Fatal("configuration lost")
	}
	// Ended resources never expose private bodies, even with a successful answer receipt.
	ended := time.Now().Add(-time.Minute)
	input = inputFromPost(next)
	input.Resources.Items[0].EndsAt = &ended
	if _, e = repo.Update(t.Context(), next.ID, content.Mutation{ExpectedVersion: next.Version}, input); e != nil {
		t.Fatal(e)
	}
	after = serveContentAPI(h, cookie, "GET", endpoint, "", nil)
	if strings.Contains(after.Body.String(), "SECRET_BODY") {
		t.Fatal("expired resource exposed")
	}
}
