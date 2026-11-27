package httpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/aieditor"
)

func aiTestImage(t *testing.T) string {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, 960, 640))
	for y := 0; y < 640; y++ {
		for x := 0; x < 960; x++ {
			pixel := color.NRGBA{R: byte(230 - y/14), G: byte(221 - y/12), B: byte(199 - y/10), A: 255}
			if (x-720)*(x-720)+(y-165)*(y-165) < 75*75 {
				pixel = color.NRGBA{R: 211, G: 139, B: 90, A: 255}
			}
			if y > 380+(x-240)*(x-240)/4500 {
				pixel = color.NRGBA{R: 125, G: 148, B: 130, A: 255}
			}
			if y > 430+(x-790)*(x-790)/3300 {
				pixel = color.NRGBA{R: 67, G: 97, B: 88, A: 255}
			}
			picture.SetNRGBA(x, y, pixel)
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, picture); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestAIImagesPermissionsMediaPersistenceAndReuse(t *testing.T) {
	s, h, manager, _, owner, principal := newPersistentAdminTestServer(t)
	_, author, authorPrincipal := createAndLoginTestUser(t, manager, principal, "image-author", "image-author@example.test", "作者", "author")
	_, editor, editorPrincipal := createAndLoginTestUser(t, manager, principal, "image-editor", "image-editor@example.test", "编辑", "editor")
	input := s.aiSettings.Snapshot()
	input.Providers = []aieditor.Provider{{ID: "images", Protocol: aieditor.ProtocolOpenAIImages, Name: "GPT", BaseURL: "https://images.example.test", APIKey: "saved-image-secret", Models: []string{"gpt-image-1"}}}
	if _, err := s.aiSettings.Save(input); err != nil {
		t.Fatal(err)
	}
	encoded, calls := aiTestImage(t), 0
	s.aiImages = &aieditor.ImageClient{HTTP: &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer saved-image-secret" {
			t.Error("saved key missing")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"` + encoded + `"}]}`))}, nil
	})}}
	body := `{"provider_id":"images","model":"gpt-image-1","prompt":"书店暖光","aspect_ratio":"3:2","title":"文章配图"}`
	for _, tc := range []struct {
		cookie        *http.Cookie
		token, origin string
		status        int
	}{
		{nil, "", "https://example.test", 401}, {author, authorPrincipal.CSRFToken, "https://example.test", 403}, {owner, "", "https://example.test", 403}, {owner, principal.CSRFToken, "https://other.test", 403},
	} {
		r := serveContentAPI(h, tc.cookie, "POST", "https://example.test/api/v1/admin/ai/images", body, map[string]string{"Content-Type": "application/json", "X-CSRF-Token": tc.token, "Origin": tc.origin})
		if r.Code != tc.status || calls != 0 {
			t.Fatal("unauthorized provider access", r.Code, calls)
		}
	}
	headers := map[string]string{"Content-Type": "application/json", "X-CSRF-Token": editorPrincipal.CSRFToken, "Origin": "https://example.test"}
	r := serveContentAPI(h, editor, "POST", "https://example.test/api/v1/admin/ai/images", body, headers)
	if r.Code != 200 || calls != 1 || !strings.Contains(r.Header().Get("Cache-Control"), "no-store") || strings.Contains(r.Body.String(), "saved-image-secret") || strings.Contains(r.Body.String(), encoded) {
		t.Fatal("invalid stream", r.Code, r.Body.String())
	}
	done := aiDone(t, r.Body.String())
	data, _ := json.Marshal(done["items"])
	var items []adminMediaAPIItem
	if err := json.Unmarshal(data, &items); err != nil || len(items) != 1 {
		t.Fatal(err, string(data))
	}
	item, err := s.media.Get(items[0].ID)
	if err != nil || item.UploadedBy != "image-editor" || item.Width != 960 || item.Height != 640 || !strings.HasPrefix(item.PublicURL, "/media/") {
		t.Fatal(item, err)
	}
	if page := serveCookieRequest(h, "GET", "https://example.test"+item.PublicURL, nil, nil); page.Code != 200 {
		t.Fatal("image not publicly usable", page.Code)
	}
	// Identical provider output reuses active media instead of creating a duplicate.
	baseline := s.media.Snapshot().Count
	r = serveContentAPI(h, editor, "POST", "https://example.test/api/v1/admin/ai/images", body, headers)
	if !strings.Contains(r.Body.String(), "event: done") || s.media.Snapshot().Count != baseline {
		t.Fatal("duplicate image not reused", r.Body.String())
	}
	if !s.aiState.acquire(editorPrincipal.UserID) {
		t.Fatal("image slot not released")
	}
	r = serveContentAPI(h, editor, "POST", "https://example.test/api/v1/admin/ai/images", body, headers)
	s.aiState.release(editorPrincipal.UserID)
	if r.Code != 429 || calls != 2 {
		t.Fatal("parallel generation not blocked", r.Code, calls)
	}
	for _, method := range []string{"GET", "DELETE", "PUT"} {
		if r := serveContentAPI(h, editor, method, "https://example.test/api/v1/admin/ai/images", "", headers); r.Code != 405 {
			t.Fatal(method, r.Code)
		}
	}
	for _, tc := range []struct {
		cookie  *http.Cookie
		allowed bool
	}{{owner, true}, {editor, true}, {author, false}} {
		page := serveCookieRequest(h, "GET", "https://example.test/admin/content/new/", tc.cookie, nil)
		if page.Code != 200 || strings.Contains(page.Body.String(), "data-ai-image-dialog") != tc.allowed {
			t.Fatal("image UI permission", page.Code, tc.allowed)
		}
	}
}

func TestAIImagesProviderFailuresDoNotCreateMedia(t *testing.T) {
	s, h, _, _, owner, p := newPersistentAdminTestServer(t)
	before := s.media.Snapshot().Count
	s.aiImages = &aieditor.ImageClient{HTTP: &http.Client{Transport: aiRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"error":"echo-secret"}`))}, nil
	})}}
	body := `{"protocol":"gemini_images","base_url":"https://images.example.test","api_key":"echo-secret","model":"nano-image","prompt":"书店"}`
	r := serveContentAPI(h, owner, "POST", "https://example.test/api/v1/admin/ai/images", body, map[string]string{"Content-Type": "application/json", "X-CSRF-Token": p.CSRFToken, "Origin": "https://example.test"})
	if !strings.Contains(r.Body.String(), "event: error") || strings.Contains(r.Body.String(), "echo-secret") || s.media.Snapshot().Count != before {
		t.Fatal("failure leaked or wrote media", r.Body.String())
	}
}
