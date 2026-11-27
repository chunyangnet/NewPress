package cdn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
)

func testProfile(provider string) Profile {
	return Profile{ID: "primary", Name: "Primary", Provider: provider, ResourceID: "zone123", Domains: []string{"www.example.com", "static.example.com"}, AccessKey: "test-access", Secret: "secret-sentinel", Token: "token-sentinel", AutoPurge: true}
}
func testService(t *testing.T) (*Service, *plugin.Manager, Settings) {
	t.Helper()
	m := plugin.NewManager(t.TempDir())
	if e := m.InstallBundled(plugin.CDN); e != nil {
		t.Fatal(e)
	}
	if e := m.SetEnabled(plugin.CDN, true); e != nil {
		t.Fatal(e)
	}
	s := New(m, "https://www.example.com")
	t.Cleanup(s.Close)
	c := Defaults()
	c.Profiles = []Profile{testProfile("cloudflare")}
	v, e := s.Save(c, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.Version = v.Version
	return s, m, c
}
func enqueue(t *testing.T, s *Service, c Settings, op string, targets ...string) Job {
	t.Helper()
	j, e := s.Enqueue(EnqueueRequest{ProfileID: "primary", ExpectedVersion: c.Version, Operation: op, Targets: targets, ConfirmBroad: true}, "tester", "manual")
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func awaitStatus(t *testing.T, s *Service, id, want string) Job {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		v, e := s.Status()
		if e != nil {
			t.Fatal(e)
		}
		for _, j := range v.Jobs {
			if j.ID == id && j.Status == want {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	v, _ := s.Status()
	t.Fatalf("did not reach %s: %+v", want, v.Jobs)
	return Job{}
}

func TestSettingsSecretsConflictAndIdentity(t *testing.T) {
	s, _, c := testService(t)
	state, e := s.Status()
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(state)
	if strings.Contains(string(b), "secret-sentinel") || strings.Contains(string(b), "token-sentinel") {
		t.Fatal("secret leak")
	}
	redacted := state.Settings.Settings
	redacted.Profiles[0].Name = "Renamed"
	v, e := s.Save(redacted, nil)
	if e != nil || !v.ProfileViews[0].SecretSaved {
		t.Fatal(e)
	}
	if _, e = s.Save(c, nil); !errors.Is(e, ErrConflict) {
		t.Fatal("stale configuration accepted", e)
	}
	redacted = v.Settings
	redacted.Profiles[0].AccessKey = "different-account"
	if _, e = s.Save(redacted, nil); e == nil {
		t.Fatal("old secret carried across accounts")
	}
	state, _ = s.Status()
	if state.Settings.Version != v.Version {
		t.Fatal("failed save changed version")
	}
	redacted = state.Settings.Settings
	v, e = s.Save(redacted, []string{"primary"})
	if e != nil || v.ProfileViews[0].TokenSaved {
		t.Fatal("token not cleared", e)
	}
}
func TestScopeValidation(t *testing.T) {
	p := testProfile("cloudflare")
	for _, raw := range []string{"http://www.example.com/a", "https://127.0.0.1/a", "https://www.example.com.evil.test/a", "https://user:pass@www.example.com/a", "https://www.example.com:444/a", "https://www.example.com/a#x", "https://www.example.com/a?token=secret", "https://www.example.com/*", "https://www.example.com/a?v=*", "https://www.example.com./a", "https://www.example.com/a?x=%zz"} {
		if _, e := targetURL(p, raw); e == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://www.example.com/admin", "https://www.example.com/%61pi/private", "https://www.example.com/a?x=1", "https://www.example.com/a/../admin"} {
		if _, e := normalizeTargets(p, "diagnose", []string{raw}); e == nil {
			t.Errorf("diagnostic scope %s", raw)
		}
	}
	if _, e := normalizeTargets(p, "purge_url", []string{"https://www.example.com/a?v=2"}); e != nil {
		t.Fatal(e)
	}
	if _, e := normalizeTargets(p, "warm", []string{"https://www.example.com/a"}); e == nil {
		t.Fatal("Cloudflare full-network warm advertised")
	}
	if _, e := normalizeTargets(p, "purge_prefix", []string{"https://www.example.com/path"}); e == nil {
		t.Fatal("non-directory prefix")
	}
}
func TestQueueDedupRecoveryAndVersion(t *testing.T) {
	s, m, c := testService(t)
	j := enqueue(t, s, c, "purge_url", "https://www.example.com/a")
	dup := enqueue(t, s, c, "purge_url", "https://www.example.com/a")
	if j.ID != dup.ID {
		t.Fatal("pending duplicate")
	}
	_, e := s.Enqueue(EnqueueRequest{ProfileID: "primary", ExpectedVersion: c.Version, Operation: "purge_all"}, "tester", "manual")
	if e == nil {
		t.Fatal("broad purge missing confirmation")
	}
	if e = s.JobAction(j.ID, "cancel", c.Version-1); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.with(true, 0, true, func(st *diskState) error { st.Jobs[0].Status = "running"; return nil }); e != nil {
		t.Fatal(e)
	}
	newer := enqueue(t, s, c, "purge_url", "https://www.example.com/a")
	if newer.ID == j.ID {
		t.Fatal("change during running purge was lost")
	}
	reopened := New(m, s.publicURL)
	defer reopened.Close()
	state, e := reopened.Status()
	if e != nil {
		t.Fatal(e)
	}
	if state.Counts["interrupted"] != 1 || state.Counts["queued"] != 1 {
		t.Fatalf("recovery: %+v", state.Counts)
	}
}
func TestWorkerAcceptanceRetryAndDisable(t *testing.T) {
	s, m, c := testService(t)
	started := make(chan struct{})
	s.execute = func(ctx context.Context, p Profile, j Job) (Receipt, error) {
		close(started)
		<-ctx.Done()
		return accepted("stale-receipt"), nil
	}
	j := enqueue(t, s, c, "purge_url", "https://www.example.com/a")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not started")
	}
	if e := m.SetEnabled(plugin.CDN, false); e != nil {
		t.Fatal(e)
	}
	awaitStatus(t, s, j.ID, "interrupted")
	state, _ := s.Status()
	if state.Enabled || state.Jobs[0].Receipt != nil {
		t.Fatal("stale receipt survived disable")
	}
}
func TestWorkerRetryAfterAndNoRetryForPermissions(t *testing.T) {
	for _, retry := range []bool{true, false} {
		t.Run(map[bool]string{true: "transient", false: "permission"}[retry], func(t *testing.T) {
			s, _, c := testService(t)
			s.execute = func(context.Context, Profile, Job) (Receipt, error) {
				return Receipt{}, &OperationError{"redacted", retry, 5 * time.Minute}
			}
			j := enqueue(t, s, c, "purge_url", "https://www.example.com/a")
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { s.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			want := "failed"
			if retry {
				want = "retrying"
			}
			result := awaitStatus(t, s, j.ID, want)
			if result.Attempts != 1 {
				t.Fatal("attempt accounting")
			}
			if retry && time.Until(result.Next) < 4*time.Minute {
				t.Fatal("Retry-After ignored")
			}
		})
	}
}
func TestAtomicSnapshotRejectsLinkedState(t *testing.T) {
	s, m, c := testService(t)
	_ = c
	var path string
	if e := m.WithData(plugin.CDN, false, 0, func(dir string, g uint64) error { path = filepath.Join(dir, "state.json"); return nil }); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(path)
	state, _ := s.Status()
	invalid := state.Settings.Settings
	invalid.Workers = 999
	if _, e := s.Save(invalid, nil); e == nil {
		t.Fatal("invalid save")
	}
	after, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid save changed disk")
	}
}
func TestCloudflareCandidateBoundsAndRanking(t *testing.T) {
	ranges := []netip.Prefix{netip.MustParsePrefix("104.16.0.0/13"), netip.MustParsePrefix("2606:4700::/32")}
	b := BenchmarkRequest{URL: "https://www.example.com/cdn-cgi/trace", SampleCount: 64, Rounds: 3, Concurrency: 4, TimeoutSeconds: 5, MinimumSuccess: 100}
	for _, ip := range []string{"127.0.0.1", "8.8.8.8", "::ffff:104.16.0.1", "2606:4700::1%bad"} {
		b.Candidates = []string{ip}
		if _, e := candidates(b, ranges); e == nil {
			t.Fatal("unapproved candidate", ip)
		}
	}
	b.Candidates = nil
	for _, ipv6 := range []bool{false, true} {
		b.IPv6 = ipv6
		ips, e := candidates(b, ranges)
		if e != nil || len(ips) != 64 {
			t.Fatal(e, len(ips))
		}
		for _, ip := range ips {
			if !containsIP(ranges, ip) || ip.Is6() != ipv6 {
				t.Fatal(ip)
			}
		}
	}
	nodes := []NodeResult{{IP: "104.16.0.1", Success: 2, Rounds: 3, TTFBP95: 10}, {IP: "104.16.0.2", Success: 3, Rounds: 3, TTFBP95: 120}, {IP: "104.16.0.3", Success: 3, Rounds: 3, TTFBP95: 80}}
	rank(nodes, 100)
	if nodes[0].IP != "104.16.0.3" || nodes[2].Eligible {
		t.Fatal(nodes)
	}
	client := pinnedClient("www.example.com", netip.MustParseAddr("104.16.0.1"), time.Second)
	tr := client.Transport.(*http.Transport)
	if tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.ServerName != "www.example.com" || tr.Proxy != nil {
		t.Fatal("unsafe probe transport")
	}
	if _, e := tr.DialContext(t.Context(), "tcp", "elsewhere.test:443"); e == nil {
		t.Fatal("host escape")
	}
}
func TestContentHooksPurgeOldAndNewPaths(t *testing.T) {
	s, _, _ := testService(t)
	repo := content.NewMemoryRepository(false)
	wrapped := WrapRepository(repo, s)
	in := content.PostInput{Title: "Test", Slug: "old-path", Status: content.StatusPublish, Type: content.TypePost, ContentSource: "body", AuthorName: "Editor", PublishedAt: time.Now().Add(-time.Minute)}
	p, e := wrapped.Create(t.Context(), in)
	if e != nil {
		t.Fatal(e)
	}
	in.Slug = "new-path"
	if _, e = wrapped.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, in); e != nil {
		t.Fatal(e)
	}
	state, _ := s.Status()
	foundOld, foundNew := false, false
	for _, j := range state.Jobs {
		for _, u := range j.Targets {
			foundOld = foundOld || strings.HasSuffix(u, "/posts/old-path")
			foundNew = foundNew || strings.HasSuffix(u, "/posts/new-path")
		}
	}
	if !foundOld || !foundNew {
		t.Fatal("old/new slug invalidations missing", state.Jobs)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestProviderProtocols(t *testing.T) {
	cases := []struct {
		provider, op, endpoint, reply string
		check                         func(*testing.T, *http.Request, []byte)
	}{
		{"cloudflare", "purge_prefix", "api.cloudflare.com", `{"success":true,"result":{"id":"purge-1"}}`, func(t *testing.T, r *http.Request, b []byte) {
			if r.Header.Get("Authorization") != "Bearer secret-sentinel" || string(b) != `{"prefixes":["www.example.com/assets/"]}` {
				t.Fatal("cloudflare prefix encoding", string(b))
			}
		}},
		{"tencent", "purge_directory", "cdn.tencentcloudapi.com", `{"Response":{"TaskId":"task-1","RequestId":"r"}}`, func(t *testing.T, r *http.Request, b []byte) {
			if r.Header.Get("X-TC-Action") != "PurgePathCache" || r.Header.Get("X-TC-Version") != "2018-06-06" || !strings.Contains(string(b), `"FlushType":"flush"`) || !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=test-access/2026-11-16/cdn/tc3_request") {
				t.Fatal("Tencent protocol")
			}
		}},
		{"edgeone", "purge_prefix", "teo.tencentcloudapi.com", `{"Response":{"JobId":"job-1","RequestId":"r"}}`, func(t *testing.T, r *http.Request, b []byte) {
			if r.Header.Get("X-TC-Action") != "CreatePurgeTask" || r.Header.Get("X-TC-Version") != "2022-09-01" || !strings.Contains(string(b), `"ZoneId":"zone123"`) {
				t.Fatal("EdgeOne protocol")
			}
		}},
		{"aliyun", "purge_directory", "cdn.aliyuncs.com", `{"RefreshTaskId":"task-1","RequestId":"r"}`, func(t *testing.T, r *http.Request, b []byte) {
			v, e := url.ParseQuery(string(b))
			if e != nil || v.Get("Action") != "RefreshObjectCaches" || v.Get("ObjectType") != "Directory" || v.Get("Force") != "true" || v.Get("SecurityToken") != "token-sentinel" || v.Get("Signature") == "" {
				t.Fatal("Alibaba form", e)
			}
		}},
		{"qiniu", "purge_directory", "fusion.qiniuapi.com", `{"code":200,"requestId":"r","invalidDirs":[]}`, func(t *testing.T, r *http.Request, b []byte) {
			if r.URL.Path != "/v2/tune/refresh" || string(b) != `{"dirs":["https://www.example.com/assets/"]}` || !strings.HasPrefix(r.Header.Get("Authorization"), "QBox test-access:") {
				t.Fatal("Qiniu protocol")
			}
		}},
		{"cloudfront", "purge_prefix", "cloudfront.amazonaws.com", `<Invalidation><Id>id-1</Id><Status>InProgress</Status></Invalidation>`, func(t *testing.T, r *http.Request, b []byte) {
			if !strings.Contains(string(b), `<Path>/assets/*</Path>`) || !strings.Contains(string(b), `<CallerReference>newpress-fixed-job</CallerReference>`) || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
				t.Fatal("CloudFront XML/signature", string(b))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			a := newProviderClient()
			a.now = func() time.Time { return time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC) }
			a.client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" || r.URL.Host != tc.endpoint {
					t.Fatal(r.URL)
				}
				b, _ := io.ReadAll(r.Body)
				tc.check(t, r, b)
				return response(200, tc.reply), nil
			})}
			got, e := a.Execute(t.Context(), testProfile(tc.provider), Job{ID: "fixed-job", Operation: tc.op, Targets: []string{"https://www.example.com/assets/"}})
			if e != nil || got.ID == "" {
				t.Fatal(got, e)
			}
		})
	}
}
func TestProviderFailuresAreNotFalseSuccess(t *testing.T) {
	for _, tc := range []struct{ provider, body string }{{"cloudflare", `{"success":false,"errors":[{"code":1000,"message":"secret-sentinel"}]}`}, {"cloudflare", `{"success":true}`}, {"tencent", `{"Response":{"Error":{"Code":"AuthFailure","Message":"secret-sentinel"}}}`}, {"aliyun", `{"Code":"InvalidAccessKeyId","Message":"secret-sentinel"}`}, {"qiniu", `{"code":200,"invalidUrls":["https://www.example.com/a?secret-sentinel"]}`}, {"cloudfront", `<Invalidation/>`}} {
		a := newProviderClient()
		a.client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(200, tc.body), nil })}
		_, e := a.Execute(t.Context(), testProfile(tc.provider), Job{ID: "id", Operation: "purge_url", Targets: []string{"https://www.example.com/a"}})
		if e == nil || strings.Contains(PublicError(e), "sentinel") {
			t.Fatalf("%s leak / success: %v", tc.provider, e)
		}
	}
}
