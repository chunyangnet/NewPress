package httpserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStatusThemeFallback(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.themes = boundaryTheme(t, "status-fallback", "body{}", "home")
	for _, code := range []int{403, 404, 500, 503} {
		w := httptest.NewRecorder()
		s.renderError(w, code, "安全提示")
		if w.Code != code {
			t.Fatalf("fallback %d: %d", code, w.Code)
		}
		if strings.Contains(w.Body.String(), "INDEPENDENT_404") != (code < 500) {
			t.Fatalf("wrong fallback for %d", code)
		}
	}
}

func TestPublicStatusPages(t *testing.T) {
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	for _, code := range []int{400, 401, 403, 404, 405, 409, 410, 422, 429, 500, 502, 503, 504} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			w := httptest.NewRecorder()
			s.renderError(w, code, "状态说明 <script>alert(1)</script>")
			if w.Code != code || !strings.Contains(w.Body.String(), fmt.Sprintf(`data-status-code="%d"`, code)) {
				t.Fatalf("status %d: %d %.200s", code, w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
				t.Fatal("missing error headers")
			}
			if strings.Contains(w.Body.String(), "<script>alert(1)</script>") {
				t.Fatal("unescaped message")
			}
			if !strings.Contains(w.Body.String(), "chunnel-status.css") {
				t.Fatal("missing status stylesheet")
			}
		})
	}
	for _, tc := range []struct {
		path string
		code int
		text string
	}{
		{"/missing-status-page", 404, "页面没有找到"},
		{"/questions/", 503, "问答服务暂时不可用"},
		{"/community/sample/", 404, "圈子服务未启用"},
		{"/shop/", 503, "商城服务暂时不可用"},
		{"/billing/", 503, "支付与订单服务暂时不可用"},
	} {
		w := serveCookieRequest(h, "GET", "https://example.test"+tc.path, nil, nil)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.text) {
			t.Errorf("%s: %d expected %d / %s", tc.path, w.Code, tc.code, tc.text)
		}
	}
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Modules = map[string]bool{"questions": false}
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
		t.Fatalf("settings: %v %v", fields, err)
	}
	w := serveCookieRequest(h, "GET", "https://example.test/questions/", nil, nil)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "问答服务未启用") {
		t.Fatal("disabled page missing")
	}
	w = serveCookieRequest(h, "POST", "https://example.test/api/v1/questions/", nil, nil)
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"module.disabled"`) || strings.Contains(w.Body.String(), "<!doctype") {
		t.Fatal("API contract changed")
	}
	snapshot = s.siteSettings.Snapshot()
	snapshot.Settings.Operations.MaintenanceEnabled = true
	snapshot.Settings.Operations.MaintenanceMessage = "正在整理新的内容，请稍后访问。"
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
		t.Fatalf("maintenance: %v %v", fields, err)
	}
	w = serveCookieRequest(h, "GET", "https://example.test/", nil, nil)
	if w.Code != 503 || w.Header().Get("Retry-After") != "300" || !strings.Contains(w.Body.String(), `data-status-kind="maintenance"`) || !strings.Contains(w.Body.String(), "正在整理新的内容") {
		t.Fatal("maintenance surface missing")
	}
}

// Opt-in, loopback-only fixture with temporary settings; never mutates the live site.
func TestStatusPagesBrowserFixture(t *testing.T) {
	addr := os.Getenv("NEWPRESS_STATUS_BROWSER_ADDR")
	if addr == "" {
		t.Skip("isolated browser fixture")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatal("loopback required")
	}
	s, h, _, _, _, _ := newPersistentAdminTestServer(t)
	s.config.PublicURL = "http://" + addr
	snapshot := s.siteSettings.Snapshot()
	snapshot.Settings.Modules = map[string]bool{"questions": false}
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) > 0 {
		t.Fatalf("settings: %v %v", fields, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/status/{code}", func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(r.PathValue("code"))
		if err != nil || code < 400 || code > 599 {
			http.NotFound(w, r)
			return
		}
		s.renderError(w, code, "当前页面暂时无法打开，你可以从其他入口继续浏览。")
	})
	mux.HandleFunc("GET /fixture/maintenance", func(w http.ResponseWriter, r *http.Request) {
		data := s.baseViewFor(w, "站点维护中", "我们正在更新站点内容与服务，请稍后访问。", "")
		data.BodyClass = "is-maintenance"
		data.Status = statusPresentation(503, data.Description)
		data.Status.Kind, data.Status.Title = "maintenance", "站点维护中"
		s.render(w, 503, "maintenance", data)
	})
	mux.HandleFunc("GET /fixture/long-service", func(w http.ResponseWriter, r *http.Request) {
		s.renderServiceStatus(w, 404, "社区内容创作与资源分享", "disabled")
	})
	mux.HandleFunc("POST /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.Error(w, "invalid style", 400)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
			http.Error(w, fmt.Sprint(fields, err), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", h)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer listener.Close()
	go func() { <-time.After(30 * time.Minute); _ = server.Shutdown(context.Background()) }()
	t.Log("status pages fixture ready", addr)
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
