package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWeChatCanceledCollectionReleasesWorkspace(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	const base = "https://example.test/api/v1/admin/wechat/collect/preview"
	input := wechatJSON(t, map[string]any{"urls": []string{
		"https://mp.weixin.qq.com/s/firstx", "https://mp.weixin.qq.com/s/second",
		"https://mp.weixin.qq.com/s/thirdx", "https://mp.weixin.qq.com/s/fourth",
		"https://mp.weixin.qq.com/s/fifthx",
	}})
	started := make(chan struct{}, 3)
	var held atomic.Bool
	var calls atomic.Int32
	held.Store(true)
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if held.Load() {
			started <- struct{}{}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		body := "<html><h1 id=\"activity-name\">重新采集</h1><div id=\"js_content\"><p>正文保留</p></div></html>"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, base, strings.NewReader(input)).WithContext(ctx)
	request.AddCookie(cookie)
	for key, value := range authenticatedAPIHeaders(owner) {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	for range 3 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("preview requests did not start")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled preview did not release the workspace")
	}
	if response.Code != 503 || calls.Load() != 3 || strings.Contains(response.Body.String(), "\"receipts\"") {
		t.Fatal("canceled preview continued or returned usable receipts", response.Code, calls.Load(), response.Body.String())
	}
	held.Store(false)
	retry := serveContentAPI(handler, cookie, "POST", base, input, authenticatedAPIHeaders(owner))
	var preview wechatSelectionPreview
	if err := json.Unmarshal(retry.Body.Bytes(), &preview); err != nil || retry.Code != 200 || len(preview.Articles) != 5 || len(preview.Receipts) != 5 || calls.Load() != 8 {
		t.Fatal("new preview remained blocked after cancellation", retry.Code, calls.Load(), retry.Body.String(), err)
	}
}
