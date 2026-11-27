package httpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/extensionupdate"
)

func TestExtensionJobAPIReportsFinalPersistenceFailure(t *testing.T) {
	for _, kind := range []string{"plugin", "theme"} {
		t.Run(kind, func(t *testing.T) {
			s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
			market := &httpExtensionMarket{check: func(context.Context) error {
				close(started)
				<-release
				return nil
			}}
			statePath := filepath.Join(t.TempDir(), "extension-updates.json")
			service, err := extensionupdate.New(statePath, market, s.plugins, s.themes)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				releaseWorker()
				service.Close()
			})
			s.SetExtensionUpdates(service)
			baseURL := "https://example.test/api/v1/admin/extensions/" + kind
			body, err := json.Marshal(map[string]any{"channel": "stable", "expected_user_id": owner.UserID})
			if err != nil {
				t.Fatal(err)
			}
			response := serveAdminCommentAPI(handler, cookie, owner.CSRFToken, "POST", baseURL+"/check", string(body), "", "https://example.test")
			var accepted extensionupdate.Job
			if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &accepted) != nil || accepted.ID == "" {
				t.Fatal("check request was not accepted", response.Code, response.Body.String())
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("accepted task did not start")
			}
			if err := os.Rename(statePath, statePath+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(statePath, 0700); err != nil {
				t.Fatal(err)
			}
			releaseWorker()
			deadline := time.Now().Add(3 * time.Second)
			for service.Busy() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if service.Busy() {
				t.Fatal("completed task stayed busy")
			}
			response = serveCookieRequest(handler, "GET", baseURL+"/job", cookie, nil)
			var reported extensionupdate.Job
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &reported) != nil {
				t.Fatal("job polling failed", response.Code, response.Body.String())
			}
			if reported.ID != accepted.ID || reported.Kind != kind || reported.Phase != "failed" || !strings.Contains(reported.Message, "结果无法保存") || !strings.Contains(reported.Message, "核对当前版本") {
				t.Fatal("API hid the final state persistence failure", reported)
			}
			if strings.Contains(reported.Message, statePath) || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("storage diagnostic leaked a path or became cacheable", response.Header(), reported.Message)
			}
		})
	}
}
