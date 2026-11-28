package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/cdn"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
)

// This service has no worker. Browser mutations and external traffic must be
// intercepted; fixture initialization writes only to t.TempDir stores.
func TestCDNMigrationBrowserFixture(t *testing.T) {
	directory := os.Getenv("NEWPRESS_CDN_BROWSER_FIXTURE")
	if directory == "" {
		t.Skip("isolated CDN migration browser fixture")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, handler, cookie, owner := newCDNHTTP(t)
	if _, err = s.cdn.Save(cdnHTTPSettings(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.cdn.SaveRouting(cdnRoutingHTTPConfig(), nil, 2); err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.SetEnabled(plugin.CDN, true); err != nil {
		t.Fatal(err)
	}
	state := func() []byte {
		status, e := s.cdn.Status()
		if e != nil {
			t.Fatal(e)
		}
		routing, e := s.cdn.RoutingStatus()
		if e != nil {
			t.Fatal(e)
		}
		posts, e := s.repository.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		var privateHash string
		if e = s.plugins.WithData(plugin.CDN, false, 0, func(path string, _ uint64) error {
			data, err := os.ReadFile(filepath.Join(path, "state.json"))
			if err != nil {
				return err
			}
			hash := sha256.Sum256(data)
			privateHash = hex.EncodeToString(hash[:])
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		data, e := json.Marshal(map[string]any{"cdn": status, "routing": routing, "private_state_sha256": privateHash, "posts": posts, "media": s.media.Snapshot(), "settings": s.siteSettings.Snapshot()})
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	write := func(name string, value any) {
		data, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(directory, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	capture := func(path string) json.RawMessage {
		response := serveCookieRequest(handler, "GET", "https://example.test"+path, cookie, nil)
		if response.Code != 200 {
			t.Fatal(path, response.Code, response.Body.String())
		}
		return append(json.RawMessage{}, response.Body.Bytes()...)
	}
	snapshot := capture("/api/v1/admin/plugins/cdn/status")
	routing := capture("/api/v1/admin/plugins/cdn/routing")
	now := time.Now().UTC()
	request := cdn.BenchmarkRequest{URL: "https://example.test/cdn-cgi/trace", Candidates: []string{}, SampleCount: 1, Rounds: 1, Concurrency: 1, TimeoutSeconds: 2, MinimumSuccess: 50}
	job := cdn.Job{ID: "11111111111111111111111111111111", ProfileID: "cloudflare", Operation: "benchmark", Targets: []string{}, Status: "succeeded", Source: "manual", Actor: owner.Username, Attempts: 1, Created: now, Updated: now, BenchmarkRequest: &request, Benchmark: &cdn.Benchmark{Started: now, Finished: now, URL: request.URL, Scope: "isolated Go projection", IPListFetched: now, Bytes: 8192, Results: []cdn.NodeResult{{IP: "104.16.0.1", Family: "IPv4", Colo: "TEST", Success: 1, Rounds: 1, SuccessRate: 100, TCPMS: 3, TLSMS: 5, TTFBP50: 10, TTFBP95: 12, Bytes: 8192, StatusCode: 200, Eligible: true}}}}
	plan := cdn.RoutingPlan{ID: "22222222222222222222222222222222", RouteID: "site", Kind: "route", Created: now, Expires: now.Add(30 * time.Minute), Status: "ready", Actor: owner.Username, Changes: []cdn.DNSChange{{Kind: "create", After: &cdn.DNSRecord{Name: "app.customer.test", Type: "CNAME", Value: "customers.example.test", Line: "default", TTL: 600, Status: "ENABLE"}, State: "pending"}}, Snapshots: map[string][]cdn.DNSRecord{}}
	task := cdn.RoutingTask{ID: "33333333333333333333333333333333", RouteID: "site", Action: "inspect", Actor: owner.Username, Status: "succeeded", Created: now, Updated: now}
	report := cdn.RouteReport{RouteID: "site", Checked: now, ZoneName: "customer.test", Mode: "unmanaged", Fallback: cdn.FallbackOrigin{Origin: "origin.example.test", Status: "active"}, DNS: cdn.DNSZone{Name: "customer.test", Lines: []cdn.DNSLine{{ID: "default", Name: "默认"}, {ID: "telecom", Name: "电信"}}, Nameservers: []string{"ns1.example.test"}, MinTTL: 600, DefaultLine: "default"}, ActualNS: []string{"ns1.example.test"}, Delegated: true, Warnings: []string{"隔离检查结果，仅用于浏览器回归"}}
	report.Hostname.ID = "fixture-host"
	report.Hostname.Hostname = "app.customer.test"
	report.Hostname.Status = "active"
	report.Hostname.SSL.Status = "active"
	report.Normal = cdn.RouteHealth{Healthy: true, Checked: now, Samples: 1, Success: 1, LatencyMS: 10, Colos: []string{"TEST"}}
	before := state()
	var escaped atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/admin/plugins/cdn/", 303)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			escaped.Add(1)
			http.Error(w, "unintercepted mutation", 405)
			return
		}
		handler.ServeHTTP(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	s.config.PublicURL = server.URL
	write("fixture.json", map[string]any{"url": server.URL, "login": server.URL + "/fixture/login", "owner_id": owner.UserID, "snapshot": snapshot, "routing": routing, "job": job, "plan": plan, "task": task, "report": report, "notice": "Synthetic Go jobs/plans/reports. CDN and DNS providers are never called; the service has no running worker."})
	t.Log("CDN migration fixture ready", server.URL)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(45 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-t.Context().Done():
			return
		case <-deadline.C:
			t.Fatal("CDN fixture timeout")
		case <-ticker.C:
			if _, e := os.Stat(filepath.Join(directory, "stop")); e == nil {
				after := state()
				if !bytes.Equal(before, after) || escaped.Load() != 0 {
					write("state-before.json", json.RawMessage(before))
					write("state-after.json", json.RawMessage(after))
					t.Fatalf("CDN fixture changed; unintercepted writes %d", escaped.Load())
				}
				hash := sha256.Sum256(after)
				write("read-only-confirmation.json", map[string]any{"state_unchanged": true, "unintercepted_mutations": escaped.Load(), "state_sha256": hex.EncodeToString(hash[:]), "worker_started": false})
				return
			}
		}
	}
}
