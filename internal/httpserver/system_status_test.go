package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/systeminfo"
)

func TestSystemStatusPageAPIAndPermissions(t *testing.T) {
	s, handler, users, _, cookie, owner := newPersistentAdminTestServer(t)
	s.config.DataDir = t.TempDir()
	s.config.DatabaseURL = "postgres://status-user:status-database-secret@private-db.test/site"
	s.config.AdminPassHash = "status-password-hash-secret"
	s.config.SMTP.Password = "status-smtp-secret"
	_, reader, _ := createAndLoginTestUser(t, users, owner, "status-reader", "status-reader@example.test", "状态读者", "author")
	api := "https://example.test/api/v1/admin/system-status"
	for _, actor := range []struct {
		cookie *http.Cookie
		want   int
	}{{nil, 401}, {reader, 403}, {cookie, 200}} {
		r := serveCookieRequest(handler, http.MethodGet, api, actor.cookie, nil)
		if r.Code != actor.want {
			t.Fatalf("system API = %d, want %d: %s", r.Code, actor.want, r.Body.String())
		}
	}
	for _, path := range []string{"/admin/system/", "/admin/"} {
		r := serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, reader, nil)
		if strings.Contains(r.Body.String(), `href="/admin/system/"`) || strings.Contains(r.Body.String(), "服务器信息") {
			t.Fatal("status data or navigation leaked to author")
		}
		if path == "/admin/system/" && r.Code != 403 {
			t.Fatalf("author page = %d", r.Code)
		}
	}
	r := serveCookieRequest(handler, http.MethodGet, api, cookie, nil)
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("status JSON must not be cached by clients")
	}
	var report systemStatusView
	if err := json.Unmarshal(r.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Metrics) != 4 || len(report.Groups) != 5 || len(report.Checks) != 10 || report.Total != 9 || report.Healthy != 9 || report.Status != "pass" {
		t.Fatalf("incomplete report: %+v", report)
	}
	if report.Process.PID <= 0 || report.Process.HeapAllocBytes == 0 || report.Resources.CollectedAt.IsZero() || report.CollectedAt.IsZero() {
		t.Fatal("missing real measurements")
	}
	if s.repositoryKind != "内存存储" || report.Groups[3].Rows[0].Value != "内存存储" {
		t.Fatal("repository decorators must not hide the actual storage type")
	}
	for _, secret := range []string{"status-database-secret", "private-db.test", "status-password-hash-secret", "status-smtp-secret"} {
		if strings.Contains(r.Body.String(), secret) {
			t.Fatalf("status response exposed %s", secret)
		}
	}
	for _, suffix := range []string{"?token=private", "?refresh=1&refresh=2", "?%zz"} {
		if r := serveCookieRequest(handler, http.MethodGet, api+suffix, cookie, nil); r.Code != 400 {
			t.Fatalf("query %q = %d", suffix, r.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if r := serveCookieRequest(handler, method, api, cookie, nil); r.Code != 405 {
			t.Fatalf("method %s = %d", method, r.Code)
		}
	}
	r = serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/system/", cookie, nil)
	if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page = %d", r.Code)
	}
	for _, marker := range []string{"服务器信息", "运行时详情", "服务健康检查", "data-system-status", "chunnel-system.css", "chunnel-system.js", "</html>"} {
		if !strings.Contains(r.Body.String(), marker) {
			t.Fatalf("page missing %s", marker)
		}
	}
	activeLink := false
	tokens := html.NewTokenizer(strings.NewReader(r.Body.String()))
	for tokenType := tokens.Next(); tokenType != html.ErrorToken; tokenType = tokens.Next() {
		if tokenType != html.StartTagToken {
			continue
		}
		token := tokens.Token()
		if token.Data != "a" {
			continue
		}
		var href, current string
		for _, attr := range token.Attr {
			switch attr.Key {
			case "href":
				href = attr.Val
			case "aria-current":
				current = attr.Val
			}
		}
		activeLink = activeLink || href == "/admin/system/" && current == "page"
	}
	if !activeLink {
		t.Fatal("status navigation must be active")
	}
	if strings.Contains(r.Body.String(), "ZgotmplZ") {
		t.Fatal("unsafe template value")
	}
}

type unavailableStatusRepository struct{ content.Repository }

func (unavailableStatusRepository) Ready(context.Context) error {
	return errors.New("postgres://private-user:private-password@host")
}

func TestSystemStatusReadinessFailureIsVisibleAndRedacted(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.repository = unavailableStatusRepository{s.repository}
	s.config.CommunityEnabled = true
	s.circles = nil
	report := s.systemStatusReport(t.Context())
	if report.Status != "error" || report.Healthy != 8 || report.Total != 10 {
		t.Fatalf("false health result: %+v", report)
	}
	for _, index := range []int{0, 9} {
		if report.Checks[index].Status != "error" || report.Checks[index].StatusLabel != "不可用" {
			t.Fatal("failed service was not identified")
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-password") || strings.Contains(string(encoded), "private-user") {
		t.Fatal("probe errors leaked credentials")
	}
	// Within the two-second cache lifetime, the exact sample is reused.
	if next := s.systemStatusReport(t.Context()); !next.CollectedAt.Equal(report.CollectedAt) || !next.Resources.CollectedAt.Equal(report.Resources.CollectedAt) {
		t.Fatal("status reads bypassed bounded cache")
	}
}

func TestSystemStatusMetricScopesAndUnavailableValues(t *testing.T) {
	unknown := statusMetrics(systeminfo.Snapshot{}, systemProcessSnapshot{})
	for _, metric := range unknown {
		if metric.Available || metric.Value != "—" {
			t.Fatalf("unknown measurement became zero: %+v", metric)
		}
	}
	cpu, rss := 0.0, uint64(32<<20)
	metrics := statusMetrics(systeminfo.Snapshot{
		CPUPercent: &cpu, CPUSupported: true, LogicalCPUs: 8, CPUSampleSeconds: 15,
		Memory:          &systeminfo.Memory{TotalBytes: 8 << 30, AvailableBytes: 2 << 30},
		Disk:            &systeminfo.Disk{TotalBytes: 100 << 30, FreeBytes: 10 << 30, AvailableBytes: 8 << 30},
		ProcessRSSBytes: &rss,
	}, systemProcessSnapshot{HeapAllocBytes: 4 << 20})
	if metrics[0].Value != "0.0" || !metrics[0].Available {
		t.Fatal("measured zero CPU must remain a real value")
	}
	if metrics[1].Percent != 75 || metrics[2].Percent != 90 || metrics[2].Tone != "warning" {
		t.Fatal("resource calculation is incorrect")
	}
	if metrics[3].Value != "32.0" || metrics[3].Unit != "MiB" || metrics[3].Detail != "Go 堆已分配 4.0 MiB" {
		t.Fatal("RSS and heap scopes were conflated")
	}
	if !strings.Contains(metrics[2].Detail, "8.0 GiB") {
		t.Fatal("disk availability must honor the current user's allowance")
	}
	if statusBytes(0) != "0 B" || statusBytes(1<<40) != "1.0 TiB" || statusDuration(90061) != "1 天 1 小时" || statusTimeout(0) != "未设置" || statusTimeout(2*time.Second) != "2s" {
		t.Fatal("incorrect metric formatting")
	}
}
