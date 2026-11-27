package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
)

const deviceMockupAPIPrefix = "/api/v1/admin/tools/device-mockup/"

var deviceMockupClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		ResponseHeaderTimeout: 90 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   8,
		MaxConnsPerHost:       16,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (s *Server) registerDeviceMockupRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/tools/device-mockup/{$}", s.adminDeviceMockup)
	mux.HandleFunc("GET "+deviceMockupAPIPrefix+"health", s.deviceMockupAPI)
	for _, endpoint := range []string{"live", "live/control", "render"} {
		mux.HandleFunc("POST "+deviceMockupAPIPrefix+endpoint, s.deviceMockupAPI)
	}
}

func (s *Server) adminDeviceMockup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	principal, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if readOnly {
		s.renderError(w, http.StatusForbidden, "请先配置管理员账号并登录后使用宣传截图。")
		return
	}
	data := s.baseViewFor(w, "宣传截图", "为网站宣传制作四端设备组合图，导出高清 PNG。", "/admin/tools/device-mockup/")
	data.BodyClass, data.ActiveNav, data.IsAdmin = "is-admin is-admin-device-mockup", "account", true
	data.Admin = s.adminView("tools", principal, false)
	data.Admin.DetailTitle = "宣传截图"
	s.render(w, http.StatusOK, "admin-device-mockup", data)
}

func withDeviceMockupAssets(output []byte) []byte {
	assets := `<link rel="stylesheet" href="` + publicCoreAsset("css/newpress-device-mockup.css") + `">`
	assets += `<script defer src="` + publicCoreAsset("js/newpress-device-mockup.js") + `"></script>`
	return bytes.Replace(output, []byte("</head>"), []byte(assets+"</head>"), 1)
}

func (s *Server) deviceMockupWorker() string {
	if s.config.DeviceMockupURL != "" {
		return strings.TrimRight(s.config.DeviceMockupURL, "/")
	}
	return "http://127.0.0.1:4173"
}

func (s *Server) deviceMockupReady(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, s.deviceMockupWorker()+"/api/health", nil)
	if err != nil {
		return false
	}
	response, err := deviceMockupClient.Do(r)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var status struct {
		Ready   bool
		Version int
		Service string
	}
	return response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status) == nil && status.Ready && status.Service == "device-mockup" && status.Version >= 6
}

func (s *Server) deviceMockupLocalRequest(r *http.Request) bool {
	client, ok := s.clientIPs.address(r)
	if !ok || !client.IsLoopback() {
		return false
	}
	public, err := url.Parse(s.config.PublicURL)
	if err != nil {
		return false
	}
	host := public.Hostname()
	if host == "localhost" {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func (s *Server) deviceMockupAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex")
	endpoint := strings.TrimPrefix(r.URL.Path, deviceMockupAPIPrefix)
	// Authenticate before probing the worker or allocating browser resources.
	if endpoint == "health" {
		_, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
		if !ok {
			return
		}
		if readOnly {
			s.writeAPIError(w, r, http.StatusForbidden, "auth.authentication_required", "请登录管理员账号后使用宣传截图。", nil)
			return
		}
	} else if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-transform")
	if r.URL.RawQuery != "" {
		writeJSON(w, 400, map[string]string{"error": "此接口不接受查询参数。"})
		return
	}
	if endpoint == "health" {
		writeJSON(w, 200, map[string]any{"ready": s.deviceMockupReady(r.Context()), "service": "device-mockup"})
		return
	}
	if !s.sameOriginPublicWrite(r) {
		writeJSON(w, 403, map[string]string{"error": "请从本站后台宣传截图页面发起预览。"})
		return
	}
	limit := 12
	if endpoint == "live/control" {
		limit = 120
	}
	now := time.Now()
	window := now.Truncate(time.Minute)
	key := "device-mockup:" + endpoint + ":" + s.commentGuard.sourceDigest(s.clientIPs.string(r)) + ":" + strconv.FormatInt(window.Unix(), 10)
	if allowed, _ := s.accessRate.allow(key, limit, now); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(window.Add(time.Minute).Sub(now).Seconds())+1))
		writeJSON(w, 429, map[string]string{"error": "操作过于频繁，请稍后再试。"})
		return
	}
	if contentType := strings.Split(r.Header.Get("Content-Type"), ";")[0]; !strings.EqualFold(strings.TrimSpace(contentType), "application/json") {
		writeJSON(w, 415, map[string]string{"error": "预览请求必须使用 JSON 数据。"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": "请求内容过大。"})
		return
	}
	if !json.Valid(body) {
		writeJSON(w, 400, map[string]string{"error": "请求数据无效。"})
		return
	}
	// Verify the worker understands the public-network restriction before sending a URL.
	if endpoint != "live/control" && !s.deviceMockupReady(r.Context()) {
		writeJSON(w, 503, map[string]string{"error": "截图服务暂未连接，请稍后再试。"})
		return
	}
	ctx := r.Context()
	if endpoint != "live" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(90 * time.Second))
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, s.deviceMockupWorker()+"/api/"+endpoint, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "截图服务暂不可用。"})
		return
	}
	// Only forward the JSON payload. Site cookies, credentials and caller headers stay here.
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Origin", s.deviceMockupWorker())
	if !s.deviceMockupLocalRequest(r) {
		upstream.Header.Set("X-NewPress-Mockup-Scope", "public")
	}
	response, err := deviceMockupClient.Do(upstream)
	if err != nil {
		if r.Context().Err() == nil {
			writeJSON(w, 503, map[string]string{"error": "截图服务连接中断，请重新连接。"})
		}
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(response.StatusCode)
	if endpoint == "live" {
		_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_ = controller.Flush()
	}
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if endpoint == "live" {
				if controller.Flush() != nil {
					return
				}
			}
		}
		if readErr != nil {
			return
		}
	}
}
