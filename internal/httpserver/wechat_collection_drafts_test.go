package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/wechat"
)

func TestWeChatCollectionRecoveryPolicyAndAssets(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	base := "https://example.test/api/v1/admin/wechat"
	read := func() wechat.Config {
		t.Helper()
		r := serveContentAPI(handler, cookie, http.MethodGet, base, "", nil)
		var response struct {
			Config wechat.Config `json:"config"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &response); err != nil || r.Code != 200 || response.Config.Secret != "" {
			t.Fatal(r.Code, r.Body.String(), err)
		}
		return response.Config
	}
	if read().CollectDrafts {
		t.Fatal("default must be opt-out")
	}
	for _, enabled := range []bool{true, false} {
		config := read()
		config.CollectDrafts = enabled
		body := wechatJSON(t, map[string]any{"config": config})
		response := serveContentAPI(handler, cookie, http.MethodPost, base+"/config", body, authenticatedAPIHeaders(owner))
		if response.Code != 200 || read().CollectDrafts != enabled {
			t.Fatal(response.Code, response.Body.String())
		}
		stored, err := wechat.ReadConfig(t.Context(), s.wechat.Store)
		if err != nil || stored.CollectDrafts != enabled {
			t.Fatal(stored, err)
		}
	}
	page := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/wechat/", cookie, nil)
	for _, value := range []string{"data-wechat-collection-draft", "data-wechat-draft-restore", `name="collect_drafts"`, "js/chunnel-wechat-drafts.js"} {
		if page.Code != 200 || !strings.Contains(page.Body.String(), value) {
			t.Fatal("recovery UI missing", value, page.Code)
		}
	}
	home := serveCookieRequest(handler, http.MethodGet, "https://example.test/", nil, nil)
	if strings.Contains(home.Body.String(), "chunnel-wechat-drafts") {
		t.Fatal("collection storage module loaded on homepage")
	}
	response := serveContentAPI(handler, nil, http.MethodPost, base+"/config", `{"config":{"version":1,"collect_drafts":true}}`, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("anonymous policy write", response.Code)
	}
}
