package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	htmlparser "golang.org/x/net/html"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

type resourcePreviewSnapshot struct {
	Preview  bool   `json:"preview"`
	Post     int64  `json:"post"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Variants []struct {
		ID        string                  `json:"id"`
		Body      string                  `json:"body"`
		MediaURL  string                  `json:"media_url"`
		Available bool                    `json:"available"`
		Groups    []resources.GroupStatus `json:"groups"`
		Downloads []resources.Download    `json:"downloads"`
		Questions []resources.Question    `json:"questions"`
	} `json:"variants"`
}

func readResourcePreviewSnapshot(t *testing.T, markup string) resourcePreviewSnapshot {
	t.Helper()
	tokens := htmlparser.NewTokenizer(strings.NewReader(markup))
	for kind := tokens.Next(); kind != htmlparser.ErrorToken; kind = tokens.Next() {
		if kind != htmlparser.StartTagToken {
			continue
		}
		token := tokens.Token()
		if token.Data != "aside" {
			continue
		}
		for _, attr := range token.Attr {
			if attr.Key == "data-npr-preview-state" {
				payload, err := base64.RawURLEncoding.DecodeString(attr.Val)
				if err != nil {
					t.Fatal(err)
				}
				var state resourcePreviewSnapshot
				if err := json.Unmarshal(payload, &state); err != nil {
					t.Fatal(err)
				}
				return state
			}
		}
	}
	t.Fatal("resource preview snapshot missing")
	return resourcePreviewSnapshot{}
}

func TestResourcePreviewUsesCurrentAutosaveAndPreservesLiveAccess(t *testing.T) {
	s, handler, accounts, repo, cookie, owner := newPersistentAdminTestServer(t)
	service, _, coin := commerceHTTPStore(t, s)
	_, otherCookie, _ := createAndLoginTestUser(t, accounts, owner, "preview-resource-editor", "preview-resource-editor@example.test", "另一位编辑", "editor")
	beforeResources, err := s.resources.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeCommerce, err := service.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeBalance, err := coin.Balance(t.Context(), owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	coinSettings, err := coin.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, status string }{{content.TypePost, content.StatusDraft}, {content.TypePage, content.StatusDraft}, {content.TypePost, content.StatusPublish}} {
		t.Run(tc.kind+"-"+tc.status, func(t *testing.T) {
			cfg := resourceFixtureConfig()
			post := createWidgetPost(t, repo, content.PostInput{Type: tc.kind, Status: tc.status, AuthorID: owner.UserID, Title: "原资源文章", Slug: "preview-resource-" + tc.kind + "-" + tc.status, ContentSource: "<p>[resource:bundle]</p>", Resources: &cfg})
			draft := resourceFixtureConfig()
			item := &draft.Items[0]
			item.Title = "本次草稿资源包"
			item.Selection = "first"
			ended := time.Now().Add(-time.Minute)
			item.EndsAt = &ended
			item.Variants[0].Body = "<p>DRAFT_SECRET_BODY</p>"
			item.Variants[0].Groups[0].Conditions[0].Amount = 1990
			item.Variants[0].Downloads[0].URL = "https://example.test/DRAFT_SECRET_DOWNLOAD"
			item.Variants[0].Downloads[0].Code = `</script><script>window.__resourcePreviewXSS=true</script>`
			item.Variants = append(item.Variants, resources.Variant{ID: "quiz", Name: "问答版本", Kind: "content", Body: "<p>DRAFT_QUIZ_BODY</p>", Groups: []resources.Group{{Conditions: []resources.Condition{{ID: "quiz", Kind: "quiz"}}}}, Questions: []resources.Question{{ID: "q", Title: "预览问题", Answer: "SECRET_QUIZ_ANSWER"}}})
			form := autosaveTestForm(owner.CSRFToken, post.Version, "当前草稿标题", post.Slug, "<p>草稿正文</p><p>[resource:bundle]</p>")
			form.Set("type", tc.kind)
			raw, err := json.Marshal(draft)
			if err != nil {
				t.Fatal(err)
			}
			form.Set("resources", string(raw))
			target := fmt.Sprintf("https://example.test/admin/content/%d/autosave/", post.ID)
			saved := serveContentForm(handler, cookie, target, "https://example.test", form)
			var autosave autosaveAPIResponse
			if saved.Code != http.StatusOK || json.Unmarshal(saved.Body.Bytes(), &autosave) != nil || autosave.PreviewURL == "" {
				t.Fatal("autosave", saved.Code, saved.Body.String())
			}
			preview := serveCookieRequest(handler, http.MethodGet, "https://example.test"+autosave.PreviewURL, cookie, nil)
			markup := preview.Body.String()
			if preview.Code != http.StatusOK || strings.Count(markup, `data-npr-preview="snapshot"`) != 1 || !strings.Contains(markup, "newpress-resources.js") || !strings.Contains(markup, "newpress-resources.css") || !strings.Contains(markup, "不会下单") {
				t.Fatal("preview did not render the snapshot resource", preview.Code)
			}
			if strings.Contains(markup, "正在核对解锁条件") || strings.Contains(markup, "SECRET_QUIZ_ANSWER") || strings.Contains(markup, `<script>window.__resourcePreviewXSS`) || strings.Contains(markup, `class="npr-membership" href=`) {
				t.Fatal("unsafe or live resource markup in preview")
			}
			state := readResourcePreviewSnapshot(t, markup)
			if !state.Preview || state.Post != post.ID || state.ID != item.ID || state.Title != item.Title || len(state.Variants) != 2 || state.Variants[0].Body != item.Variants[0].Body || state.Variants[0].Available || state.Variants[0].Groups[0].Conditions[0].Amount != 1990 {
				t.Fatal("preview used canonical terms or live eligibility", state)
			}
			if state.Variants[0].Downloads[0].Code != item.Variants[0].Downloads[0].Code || state.Variants[0].Downloads[0].URL != item.Variants[0].Downloads[0].URL || state.Variants[1].Questions[0].Answer != "" {
				t.Fatal("preview projection corrupted delivery metadata or included answers", state)
			}
			wantPoints := "使用 " + virtualcurrency.FormatAmount(100, coinSettings.Precision) + " " + coinSettings.Name
			if state.Variants[0].Groups[1].Conditions[0].Label != wantPoints {
				t.Fatal("preview points precision differs from live state", state.Variants[0].Groups[1])
			}
			if preview.Header().Get("Cache-Control") != "no-store" || preview.Header().Get("Referrer-Policy") != "no-referrer" || preview.Header().Get("X-Robots-Tag") != "noindex, nofollow, noarchive" {
				t.Fatal("private preview headers missing", preview.Header())
			}
			for _, forbidden := range []*http.Cookie{nil, otherCookie} {
				response := serveCookieRequest(handler, http.MethodGet, "https://example.test"+autosave.PreviewURL, forbidden, nil)
				if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "DRAFT_SECRET") || strings.Contains(response.Body.String(), "data-npr-preview-state") {
					t.Fatal("preview snapshot crossed identity boundary", response.Code)
				}
			}
			endpoint := fmt.Sprintf("https://example.test/api/v1/article-resources/%d/bundle", post.ID)
			live := serveContentAPI(handler, cookie, http.MethodGet, endpoint, "", nil)
			if tc.status == content.StatusPublish {
				if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"amount":990`) || strings.Contains(live.Body.String(), "SECRET_BODY") || strings.Contains(live.Body.String(), "DRAFT_SECRET") {
					t.Fatal("preview changed published resource state", live.Code, live.Body.String())
				}
			} else if live.Code != http.StatusNotFound {
				t.Fatal("draft preview made private resource public", live.Code)
			}
			query := serveContentAPI(handler, cookie, http.MethodGet, endpoint+"?preview=true", "", nil)
			if query.Code != http.StatusBadRequest {
				t.Fatal("preview query bypassed public resource boundary", query.Code)
			}
			// Removing every resource in the draft must remove the preview card,
			// while the canonical post and its existing resources stay untouched.
			form.Set("resources", `{"items":[]}`)
			removed := serveContentForm(handler, cookie, target, "https://example.test", form)
			if removed.Code != http.StatusOK || json.Unmarshal(removed.Body.Bytes(), &autosave) != nil {
				t.Fatal("remove draft resource", removed.Code)
			}
			cleared := serveCookieRequest(handler, http.MethodGet, "https://example.test"+autosave.PreviewURL, cookie, nil)
			if cleared.Code != http.StatusOK || strings.Contains(cleared.Body.String(), `data-npr-resource=`) {
				t.Fatal("preview resurrected a removed resource", cleared.Code)
			}
			canonical, err := repo.GetByID(t.Context(), post.ID)
			if err != nil || canonical.Version != post.Version || resources.FromMeta(canonical.Meta).Items[0].Variants[0].Groups[0].Conditions[0].Amount != 990 {
				t.Fatal("preview wrote canonical resources", err)
			}
		})
	}
	afterResources, err := s.resources.Backend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(beforeResources, afterResources) {
		t.Fatal("preview mutated resource history or purchase state", err)
	}
	afterCommerce, err := service.Backend.Read(t.Context())
	if err != nil || !reflect.DeepEqual(beforeCommerce, afterCommerce) {
		t.Fatal("preview created or changed orders", err)
	}
	afterBalance, err := coin.Balance(t.Context(), owner.UserID)
	if err != nil || beforeBalance != afterBalance {
		t.Fatal("preview changed wallet balance", beforeBalance, afterBalance, err)
	}
}

func TestResourceAppearancePreviewIsStaticAndWithholdsDeliveries(t *testing.T) {
	_, handler, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	cfg := resourceFixtureConfig()
	createWidgetPost(t, repo, content.PostInput{Status: content.StatusPublish, AuthorID: owner.UserID, Title: "资源外观预览", Slug: "resource-appearance-preview", ContentSource: "<p>[resource:bundle]</p>", PublishedAt: time.Now().Add(-time.Minute), Resources: &cfg})
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/appearance/preview/?page=article", cookie, nil)
	var result map[string]string
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatal("appearance preview", response.Code, response.Body.String())
	}
	markup := result["html"]
	for _, want := range []string{`data-npr-preview="appearance"`, "newpress-resources.css", "支付 9.90 CNY", "积分", "完整资料包"} {
		if !strings.Contains(markup, want) {
			t.Fatal("static resource preview missing", want)
		}
	}
	for _, secret := range []string{"SECRET_BODY", "SECRET_LINK", "SECRET_CODE", "SECRET_PASSWORD", "data-npr-preview-state", "正在核对解锁条件", "[resource:bundle]", `class="npr-membership" href=`} {
		if strings.Contains(markup, secret) {
			t.Fatal("appearance preview leaked delivery or left a live/loading card", secret)
		}
	}
}
