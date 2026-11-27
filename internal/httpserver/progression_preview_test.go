package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/progression"
)

func TestProgressionPreviewHTTPIsReadOnlyAndStrict(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	store, cfg, _ := progressionHTTPStore(t, s)
	_, readerCookie, reader := createAndLoginTestUser(t, manager, owner, "preview-reader", "preview-reader@example.test", "预览读者", "subscriber")
	body := currencyJSON(t, map[string]any{"settings": cfg})
	headers := authenticatedAPIHeaders(owner)

	if r := serveContentAPI(handler, nil, "POST", "https://example.test/api/v1/admin/progression/preview", body, headers); r.Code != http.StatusUnauthorized {
		t.Fatal("anonymous", r.Code, r.Body.String())
	}
	if r := serveContentAPI(handler, readerCookie, "POST", "https://example.test/api/v1/admin/progression/preview", body, authenticatedAPIHeaders(reader)); r.Code != http.StatusForbidden {
		t.Fatal("reader", r.Code, r.Body.String())
	}
	withoutCSRF := map[string]string{}
	for key, value := range headers {
		if key != "X-CSRF-Token" {
			withoutCSRF[key] = value
		}
	}
	if r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/preview", body, withoutCSRF); r.Code != http.StatusForbidden {
		t.Fatal("csrf", r.Code, r.Body.String())
	}
	attacker := map[string]string{}
	for key, value := range headers {
		attacker[key] = value
	}
	attacker["Origin"] = "https://attacker.test"
	if r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/preview", body, attacker); r.Code != http.StatusForbidden {
		t.Fatal("origin", r.Code, r.Body.String())
	}
	if r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/preview?user_id=1", body, headers); r.Code != http.StatusBadRequest {
		t.Fatal("query", r.Code, r.Body.String())
	}
	extra := strings.TrimSuffix(body, "}") + `,"unexpected":true}`
	if r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/preview", extra, headers); r.Code != http.StatusBadRequest {
		t.Fatal("extra field", r.Code, r.Body.String())
	}
	oversize := strings.TrimSuffix(body, "}") + `,"padding":"` + strings.Repeat("a", 132_000) + `"}`
	if r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/preview", oversize, headers); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatal("oversize", r.Code)
	}

	r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/preview", body, headers)
	if r.Code != http.StatusOK || r.Header().Get("Cache-Control") != "no-store" || !strings.Contains(r.Header().Get("Vary"), "Cookie") {
		t.Fatal(r.Code, r.Header(), r.Body.String())
	}
	var preview progression.SettingsPreview
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || len(preview.Metrics) != 8 || len(preview.Plans) != len(cfg.Membership.Plans) {
		t.Fatal(preview)
	}
	after, err := store.Settings(t.Context())
	if err != nil || after.Version != cfg.Version {
		t.Fatal("preview mutated settings", after.Version, cfg.Version, err)
	}
	for _, private := range []string{"user_id", "username", "display_name", "payment_request", "fingerprint"} {
		if strings.Contains(r.Body.String(), private) {
			t.Fatal("private field exposed", private)
		}
	}

	invalid := cfg
	invalid.Experience.Maximum = 1
	r = serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/progression/preview", currencyJSON(t, map[string]any{"settings": invalid}), headers)
	if r.Code != http.StatusOK {
		t.Fatal("invalid draft should be explained", r.Code, r.Body.String())
	}
	if err = json.Unmarshal(r.Body.Bytes(), &preview); err != nil || preview.Valid || len(preview.Issues) == 0 {
		t.Fatal("invalid preview", preview, err)
	}
}
