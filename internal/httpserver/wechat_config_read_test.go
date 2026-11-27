package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/wechat"
)

type wechatConfigReadProbe struct {
	wechat.Store
	fullReads, configReads int
	failure                error
}

func (s *wechatConfigReadProbe) View(ctx context.Context) (wechat.State, error) {
	s.fullReads++
	return s.Store.View(ctx)
}

func (s *wechatConfigReadProbe) ViewConfig(ctx context.Context) (wechat.Config, error) {
	s.configReads++
	if s.failure != nil {
		return wechat.Config{}, s.failure
	}
	return wechat.ReadConfig(ctx, s.Store)
}

func TestWeChatConnectionCheckReadsFreshConfigWithoutHistory(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	cfg := wechat.Defaults()
	cfg.AppID, cfg.Secret = "wx0123456789abcdef", strings.Repeat("s", 32)
	secret := cfg.Secret
	cfg, err := s.wechat.SaveConfig(t.Context(), cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	store := &wechatConfigReadProbe{Store: s.wechat.Store}
	s.wechat.Store = store
	requests := 0
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Path != "/cgi-bin/stable_token" {
			t.Fatalf("unexpected remote request: %s", req.URL.Path)
		}
		var payload struct {
			AppID  string `json:"appid"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || payload.AppID != cfg.AppID || payload.Secret != secret {
			t.Fatal("saved credentials not used", err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"fixture-token","expires_in":7200}`)), Header: make(http.Header), Request: req}, nil
	})}
	const endpoint = "https://example.test/api/v1/admin/wechat/test"
	headers := authenticatedAPIHeaders(owner)
	r := serveContentAPI(handler, cookie, "POST", endpoint, `{}`, headers)
	if r.Code != 200 || store.fullReads != 0 || store.configReads != 1 || requests != 1 || strings.Contains(r.Body.String(), secret) || strings.Contains(r.Body.String(), "fixture-token") {
		t.Fatalf("connection check: status=%d full=%d config=%d remote=%d", r.Code, store.fullReads, store.configReads, requests)
	}
	for _, row := range []struct {
		err    error
		status int
	}{{errors.New("configuration unavailable"), 422}, {context.Canceled, 503}} {
		store.failure = row.err
		r = serveContentAPI(handler, cookie, "POST", endpoint, `{}`, headers)
		if r.Code != row.status || requests != 1 || store.fullReads != 0 {
			t.Fatalf("configuration failure bypassed: status=%d remote=%d full=%d", r.Code, requests, store.fullReads)
		}
	}
	store.failure = nil
	if _, err = s.wechat.SaveConfig(t.Context(), cfg, true); err != nil {
		t.Fatal(err)
	}
	r = serveContentAPI(handler, cookie, "POST", endpoint, `{}`, headers)
	if r.Code != 422 || requests != 1 || store.fullReads != 0 || store.configReads != 4 {
		t.Fatalf("cleared credentials were cached: status=%d full=%d config=%d remote=%d", r.Code, store.fullReads, store.configReads, requests)
	}
}
