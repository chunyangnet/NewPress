package httpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/wechat"
)

type tokenWaitConfigStore struct {
	wechat.Store
	read chan struct{}
}

func (s *tokenWaitConfigStore) ViewConfig(ctx context.Context) (wechat.Config, error) {
	cfg, err := wechat.ReadConfig(ctx, s.Store)
	select {
	case s.read <- struct{}{}:
	default:
	}
	return cfg, err
}

func TestWeChatConnectionCancellationWhileTokenRefreshContinues(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	cfg, err := wechat.ReadConfig(t.Context(), s.wechat.Store)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AppID, cfg.Secret = "wx0123456789abcdef", strings.Repeat("s", 32)
	if _, err = s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	store := &tokenWaitConfigStore{Store: s.wechat.Store, read: make(chan struct{}, 1)}
	s.wechat.Store = store
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var requests atomic.Int32
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"private-shared-token","expires_in":7200}`)), Request: req}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}
	leader := make(chan error, 1)
	go func() { _, err := s.wechat.Client.Token(t.Context(), cfg); leader <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("token refresh did not start")
	}
	const endpoint = "https://example.test/api/v1/admin/wechat/test"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequest("POST", endpoint, strings.NewReader(`{}`)).WithContext(ctx)
	req.AddCookie(cookie)
	headers := authenticatedAPIHeaders(owner)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	waiting := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		waiting <- result
	}()
	select {
	case <-store.read:
	case <-time.After(2 * time.Second):
		t.Fatal("connection request did not reach saved configuration")
	}
	cancel()
	returned := false
	select {
	case result := <-waiting:
		returned = true
		if result.Code != 503 || strings.Contains(result.Body.String(), "private-shared-token") || strings.Contains(result.Body.String(), cfg.Secret) {
			t.Error("canceled connection reported success or leaked credentials", result.Code, result.Body.String())
		}
	case <-time.After(time.Second):
		t.Error("canceled connection remained blocked by the active token refresh")
	}
	select {
	case err := <-leader:
		t.Fatal("connection cancellation interrupted another refresh", err)
	default:
	}
	unblock()
	if err = <-leader; err != nil {
		t.Fatal(err)
	}
	if !returned {
		<-waiting
	}
	result := serveContentAPI(handler, cookie, "POST", endpoint, `{}`, headers)
	if result.Code != 200 || requests.Load() != 1 || strings.Contains(result.Body.String(), "private-shared-token") || strings.Contains(result.Body.String(), cfg.Secret) {
		t.Fatal("subsequent connection lost the shared credential", result.Code, requests.Load(), result.Body.String())
	}
}
