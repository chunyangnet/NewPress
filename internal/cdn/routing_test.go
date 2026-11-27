package cdn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func testRoute() Route {
	return Route{ID: "site", Name: "测试接入", CFProfileID: "primary", DNSProfileID: "dns", Hostname: "app.customer.test", FallbackOrigin: "origin.example.com", Target: "customers.example.com", PreferredTarget: "preferred.example.net", PreferredLines: []string{"cn"}, TTL: 600, HealthPath: "/health.txt", Interval: 300, FailureThreshold: 3, RecoveryThreshold: 3, Cooldown: 900}
}
func testDNS() DNSProfile {
	return DNSProfile{ID: "dns", Name: "测试 DNS", Provider: "aliyun", Zone: "customer.test", AccessKey: "access", Secret: "dns-secret-sentinel", Token: "dns-token-sentinel"}
}
func readyReport(r Route) RouteReport {
	h := SaaSHostname{ID: "host123", Hostname: r.Hostname, Status: "active"}
	h.SSL.Status = "active"
	return RouteReport{RouteID: r.ID, Checked: time.Now(), Hostname: h, Fallback: FallbackOrigin{Origin: r.FallbackOrigin, Status: "active"}, DNS: DNSZone{Name: "customer.test", MinTTL: 600, DefaultLine: "default", Lines: []DNSLine{{"default", "默认"}, {"cn", "中国大陆"}}, Nameservers: []string{"ns1.dns.test"}}, Delegated: true, Normal: RouteHealth{Healthy: true, Checked: time.Now()}, Preferred: RouteHealth{Healthy: true, Checked: time.Now()}}
}
func routingService(t *testing.T) (*Service, *plugin.Manager, Route, DNSProfile, Profile) {
	t.Helper()
	s, m, c := testService(t)
	r, d := testRoute(), testDNS()
	v, e := s.SaveRouting(RoutingConfig{Version: 1, DNS: []DNSProfile{d}, Routes: []Route{r}}, nil, c.Version)
	if e != nil {
		t.Fatal(e)
	}
	if v.Config.Version != 2 {
		t.Fatal(v)
	}
	return s, m, r, d, c.Profiles[0]
}
func TestRoutingConfigurationPrivacyAndConstraints(t *testing.T) {
	s, _, r, _, cf := routingService(t)
	v, e := s.RoutingStatus()
	if e != nil {
		t.Fatal(e)
	}
	b := string(jsonBody(v))
	if strings.Contains(b, "dns-secret-sentinel") || strings.Contains(b, "dns-token-sentinel") || !v.Config.DNS[0].SecretSaved {
		t.Fatal("secret leak", b)
	}
	c := cloneRouting(v).Config
	c.DNS[0].Name = "renamed"
	v, e = s.SaveRouting(c, nil, 2)
	if e != nil || !v.Config.DNS[0].TokenSaved {
		t.Fatal(e, v)
	}
	if _, e = s.SaveRouting(c, nil, 2); !errors.Is(e, ErrConflict) {
		t.Fatal("stale routing version accepted", e)
	}
	c = cloneRouting(v).Config
	c.DNS[0].Zone = "other.test"
	if _, e = s.SaveRouting(c, nil, 2); e == nil {
		t.Fatal("secret inherited across zones")
	}
	c = cloneRouting(v).Config
	c.DNS[0].AccessKey = "new-account"
	if _, e = s.SaveRouting(c, nil, 2); e == nil {
		t.Fatal("secret inherited across accounts")
	}
	c = cloneRouting(v).Config
	v, e = s.SaveRouting(c, []string{"dns"}, 2)
	if e != nil || v.Config.DNS[0].TokenSaved {
		t.Fatal("clear STS", e)
	}
	for _, change := range []func(*Route){func(r *Route) { r.Hostname = "evil.test" }, func(r *Route) { r.HealthPath = "//evil.test/x" }, func(r *Route) { r.HealthPath = "/admin/users" }, func(r *Route) { r.HealthPath = "/health?token=x" }, func(r *Route) { r.AutoRecover = true }, func(r *Route) { r.PreferredLines = []string{"default"} }, func(r *Route) { r.PreferredTarget = r.Hostname }} {
		bad := testRoute()
		change(&bad)
		if _, e = normalizeRouting(RoutingConfig{Version: 1, DNS: []DNSProfile{testDNS()}, Routes: []Route{bad}}); e == nil {
			t.Fatal("invalid route accepted", bad)
		}
	}
	base := routeIdentity(r, testDNS(), cf)
	cf.Secret = "rotated"
	if base == routeIdentity(r, testDNS(), cf) {
		t.Fatal("credential rotation did not invalidate plans")
	}
}
func TestRoutingValidationRecordsScopedAndMultipleTXT(t *testing.T) {
	r := testRoute()
	h := SaaSHostname{}
	h.Ownership.Name = "_cf-custom-hostname." + r.Hostname
	h.Ownership.Type = "txt"
	h.Ownership.Value = "ownership"
	h.SSL.Validation = []ValidationRecord{{TXTName: "_acme-challenge." + r.Hostname, TXTValue: "ecdsa"}, {TXTName: "_acme-challenge." + r.Hostname, TXTValue: "rsa"}}
	v, e := validationRecords(r, h, "default")
	if e != nil || len(v) != 3 {
		t.Fatal(v, e)
	}
	h.SSL.Validation[0].TXTName = "_acme-challenge.victim.test"
	if _, e = validationRecords(r, h, "default"); e == nil {
		t.Fatal("out-of-scope remote token accepted")
	}
}
func TestRoutingPlansGuardExistingRecordsAndReadiness(t *testing.T) {
	r, d, cf := testRoute(), testDNS(), testProfile("cloudflare")
	report := readyReport(r)
	st := routingDefaults()
	task := RoutingTask{Action: "preview_route", Actor: "tester"}
	p, e := buildRoutingPlan(task, r, d, cf, report, st)
	if e != nil || len(p.Changes) != 2 {
		t.Fatal(p, e)
	}
	before := DNSRecord{ID: "old", Name: r.Hostname, Type: "CNAME", Value: "old.example.net", Line: "cn", TTL: 600, Status: "ENABLE"}
	report.Records = []DNSRecord{before}
	if _, e = buildRoutingPlan(task, r, d, cf, report, st); e == nil {
		t.Fatal("unmanaged CNAME overwritten")
	}
	r.ManageExisting = true
	p, e = buildRoutingPlan(task, r, d, cf, report, st)
	if e != nil || p.Changes[1].Before == nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*RouteReport){func(v *RouteReport) { v.Hostname.SSL.Status = "pending_validation" }, func(v *RouteReport) { v.Delegated = false }, func(v *RouteReport) { v.Normal.Healthy = false }, func(v *RouteReport) { v.Preferred.Healthy = false }, func(v *RouteReport) { v.Fallback.Origin = "other.example.com" }, func(v *RouteReport) { v.DNS.Lines = []DNSLine{{"default", "default"}} }, func(v *RouteReport) {
		v.Records = []DNSRecord{{ID: "a", Name: r.Hostname, Type: "A", Value: "1.1.1.1", Line: "cn", Status: "ENABLE"}}
	}} {
		v := readyReport(r)
		mutate(&v)
		if _, e = buildRoutingPlan(task, r, d, cf, v, st); e == nil {
			t.Fatal("unsafe cutover accepted", v)
		}
	}
	report = readyReport(r)
	report.Hostname.SSL.Validation = []ValidationRecord{{TXTName: "_acme-challenge." + r.Hostname, TXTValue: "new-token"}}
	report.Validation, _ = validationRecords(r, report.Hostname, "default")
	report.Records = []DNSRecord{{ID: "old-txt", Name: "_acme-challenge." + r.Hostname, Type: "TXT", Value: "other-certificate-token", Line: "default", TTL: 600, Status: "ENABLE"}}
	task.Action = "preview_validation"
	p, e = buildRoutingPlan(task, r, d, cf, report, st)
	if e != nil || len(p.Changes) != 1 || p.Changes[0].Kind != "create" {
		t.Fatal("existing TXT overwritten", p, e)
	}
}

type dnsFixture struct {
	mu       sync.Mutex
	records  []DNSRecord
	writes   int
	next     int
	failAt   int
	requests []url.Values
}

func (f *dnsFixture) transport(t *testing.T) transportFunc {
	return func(req *http.Request) (*http.Response, error) {
		t.Helper()
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(req.Body)
		v, e := url.ParseQuery(string(body))
		if e != nil {
			t.Fatal(e)
		}
		f.requests = append(f.requests, v)
		if req.URL.Host != "alidns.aliyuncs.com" || v.Get("Version") != "2015-01-09" || v.Get("Signature") == "" || v.Get("SecurityToken") != "dns-token-sentinel" {
			t.Fatal("invalid AliDNS request", req.URL, v.Get("Action"))
		}
		action := v.Get("Action")
		result := map[string]any{"RequestId": "req"}
		if action == "DescribeDomainRecords" {
			if v.Get("SearchMode") != "COMBINATION" {
				t.Fatal("AliDNS RR exact matching requires COMBINATION")
			}
			name := fullName(v.Get("RRKeyWord"), v.Get("DomainName"))
			var rows []map[string]any
			for _, r := range f.records {
				if r.Name == name {
					rows = append(rows, map[string]any{"RecordId": r.ID, "RR": relativeName(r.Name, "customer.test"), "DomainName": "customer.test", "Type": r.Type, "Value": r.Value, "Line": r.Line, "TTL": r.TTL, "Status": r.Status, "Weight": r.Weight, "Locked": r.Locked})
				}
			}
			result["TotalCount"] = len(rows)
			result["DomainRecords"] = map[string]any{"Record": rows}
		} else {
			f.writes++
			if f.failAt > 0 && f.writes == f.failAt {
				return nil, errors.New("simulated lost response")
			}
			ttl, _ := strconv.Atoi(v.Get("TTL"))
			id := v.Get("RecordId")
			switch action {
			case "AddDomainRecord":
				f.next++
				id = strconv.Itoa(1000 + f.next)
				f.records = append(f.records, DNSRecord{ID: id, Name: fullName(v.Get("RR"), "customer.test"), Type: v.Get("Type"), Value: v.Get("Value"), Line: v.Get("Line"), TTL: ttl, Status: "ENABLE"})
			case "UpdateDomainRecord":
				for i, r := range f.records {
					if r.ID == id {
						r.Type = v.Get("Type")
						r.Value = v.Get("Value")
						r.Line = v.Get("Line")
						r.TTL = ttl
						f.records[i] = r
					}
				}
			case "DeleteDomainRecord":
				f.records = slices.DeleteFunc(f.records, func(r DNSRecord) bool { return r.ID == id })
			default:
				t.Fatalf("unexpected DNS action %q", action)
			}
			result["RecordId"] = id
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(jsonBody(result)))), Request: req}, nil
	}
}
func (f *dnsFixture) snapshot() []DNSRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records)
}
func savePlan(t *testing.T, s *Service, p RoutingPlan) uint64 {
	t.Helper()
	var g uint64
	if e := s.with(true, 0, true, func(st *diskState) error { g = s.generation; return putPlan(&st.Routing, p) }); e != nil {
		t.Fatal(e)
	}
	return g
}
func TestRoutingApplyRollbackAndOneShot(t *testing.T) {
	s, _, r, d, cf := routingService(t)
	r.ManageExisting = true
	fixture := &dnsFixture{records: []DNSRecord{{ID: "old", Name: r.Hostname, Type: "CNAME", Value: "old.example.net", Line: "cn", TTL: 600, Status: "ENABLE"}}}
	s.api.client = &http.Client{Transport: fixture.transport(t)}
	report := readyReport(r)
	report.Records = fixture.snapshot()
	p, e := buildRoutingPlan(RoutingTask{Action: "preview_route"}, r, d, cf, report, routingDefaults())
	if e != nil {
		t.Fatal(e)
	}
	g := savePlan(t, s, p)
	if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, p); e != nil {
		t.Fatal(e)
	}
	v, _ := s.RoutingStatus()
	p = v.Plans[0]
	if p.Status != "applied" || len(fixture.snapshot()) != 2 {
		t.Fatal(p, fixture.snapshot())
	}
	// Fingerprints are never returned by the status API.
	if p.Identity != "" {
		t.Fatal("fingerprint exposed")
	}
	var internal RoutingState
	_ = s.with(false, g, false, func(st *diskState) error { internal = cloneRouting(st.Routing); return nil })
	p = internal.Plans[0]
	if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, p); !errors.Is(e, ErrConflict) {
		t.Fatal("executed plan replayed", e)
	}
	report.Records = fixture.snapshot()
	rollback, e := buildRoutingPlan(RoutingTask{Action: "preview_rollback", PlanID: p.ID}, r, d, cf, report, internal)
	if e != nil {
		t.Fatal(e)
	}
	savePlan(t, s, rollback)
	if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, rollback); e != nil {
		t.Fatal(e)
	}
	got := fixture.snapshot()
	if len(got) != 1 || got[0].ID != "old" || got[0].Value != "old.example.net" {
		t.Fatal("rollback failed", got)
	}
}
func TestRoutingStaleDNSAndUnknownResultStopWrites(t *testing.T) {
	for _, mode := range []string{"stale", "lost-second-response"} {
		t.Run(mode, func(t *testing.T) {
			s, _, r, d, cf := routingService(t)
			fixture := &dnsFixture{}
			s.api.client = &http.Client{Transport: fixture.transport(t)}
			report := readyReport(r)
			p, e := buildRoutingPlan(RoutingTask{Action: "preview_route"}, r, d, cf, report, routingDefaults())
			if e != nil {
				t.Fatal(e)
			}
			g := savePlan(t, s, p)
			if mode == "stale" {
				fixture.records = []DNSRecord{{ID: "external", Name: r.Hostname, Type: "A", Value: "1.1.1.1", Line: "default", TTL: 60, Status: "ENABLE"}}
			} else {
				fixture.failAt = 2
			}
			if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, p); e == nil {
				t.Fatal("failure accepted")
			}
			v, _ := s.RoutingStatus()
			if mode == "stale" && fixture.writes != 0 {
				t.Fatal("stale DNS changed")
			}
			if mode != "stale" && (fixture.writes != 2 || v.Plans[0].Status != "unknown" || v.Plans[0].Changes[0].State != "done") {
				t.Fatal(fixture.writes, v.Plans)
			}
		})
	}
}
func TestRoutingAutomaticFailoverThresholdAndOwnership(t *testing.T) {
	s, _, r, d, cf := routingService(t)
	r.AutoFailover = true
	r.AutoRecover = true
	r.Monitor = true
	fixture := &dnsFixture{records: []DNSRecord{{ID: "1", Name: r.Hostname, Type: "CNAME", Value: r.Target, Line: "default", TTL: 600, Status: "ENABLE"}, {ID: "2", Name: r.Hostname, Type: "CNAME", Value: r.PreferredTarget, Line: "cn", TTL: 600, Status: "ENABLE"}}}
	s.api.client = &http.Client{Transport: fixture.transport(t)}
	var g uint64
	_ = s.with(true, 0, true, func(st *diskState) error {
		g = s.generation
		for _, record := range fixture.snapshot() {
			st.Routing.Owned = append(st.Routing.Owned, ownedRecord{dnsIdentity(d), r.ID, record})
		}
		return nil
	})
	report := readyReport(r)
	report.Mode = "preferred"
	report.Preferred.Healthy = false
	report.Records = fixture.snapshot()
	for n := 1; n <= 3; n++ {
		var snap RoutingState
		_ = s.with(true, g, false, func(st *diskState) error { snap = cloneRouting(st.Routing); return nil })
		if e := s.monitorRoute(t.Context(), g, RoutingTask{Actor: "monitor"}, r, d, cf, report, snap); e != nil {
			t.Fatal(e)
		}
		if n < 3 && fixture.writes != 0 {
			t.Fatal("threshold bypassed")
		}
	}
	if fixture.writes != 1 || fixture.snapshot()[1].Value != r.Target {
		t.Fatal("failover missing", fixture.snapshot())
	}
	// Recovery cannot flap back during the cooldown, even after enough successes.
	report.Mode = "fallback"
	report.Preferred.Healthy = true
	report.Records = fixture.snapshot()
	for range 4 {
		var snap RoutingState
		_ = s.with(true, g, false, func(st *diskState) error { snap = cloneRouting(st.Routing); return nil })
		if e := s.monitorRoute(t.Context(), g, RoutingTask{}, r, d, cf, report, snap); e != nil {
			t.Fatal(e)
		}
	}
	if fixture.writes != 1 {
		t.Fatal("cooldown bypassed")
	}
}
func TestRoutingWorkerPersistenceAndDisable(t *testing.T) {
	s, m, r, _, _ := routingService(t)
	started := make(chan struct{})
	s.routeInspect = func(ctx context.Context, _ Profile, _ DNSProfile, _ Route) (RouteReport, error) {
		close(started)
		<-ctx.Done()
		return RouteReport{}, ctx.Err()
	}
	_, e := s.EnqueueRouting(RoutingRequest{Action: "inspect", RouteID: r.ID, ExpectedVersion: 2, CDNVersion: 2}, "tester")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := s.RoutingStatus()
	if _, e = s.SaveRouting(v.Config, nil, 2); e == nil {
		t.Fatal("saved during queued task")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker not started")
	}
	if e = m.SetEnabled(plugin.CDN, false); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, _ = s.RoutingStatus()
		if len(v.Tasks) > 0 && v.Tasks[0].Status == "interrupted" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("disable did not interrupt", v.Tasks)
}
func TestRoutingProviderResponsesAndPagination(t *testing.T) {
	p := testDNS()
	p.Provider = "dnspod"
	api := newProviderClient()
	var action string
	api.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "dnspod.tencentcloudapi.com" || req.Header.Get("X-TC-Version") != "2021-03-23" || !strings.Contains(req.Header.Get("Authorization"), "/dnspod/tc3_request") {
			t.Fatal("DNSPod signing", req.Header)
		}
		action = req.Header.Get("X-TC-Action")
		var params map[string]any
		_ = json.NewDecoder(req.Body).Decode(&params)
		result := map[string]any{"RequestId": "req"}
		switch action {
		case "DescribeDomain":
			result["DomainInfo"] = map[string]any{"Domain": p.Zone, "Grade": "DP_FREE", "Status": "ENABLE", "TTL": 600, "DnspodNsList": []string{"ns1.dnspod.test"}}
		case "DescribeRecordLineList":
			if params["DomainGrade"] != "DP_FREE" {
				t.Fatal(params)
			}
			result["LineList"] = []any{map[string]any{"Name": "默认", "LineId": "0"}, map[string]any{"Name": "境内", "LineId": "10=0"}}
		case "DescribeRecordList":
			if params["SubDomain"] != "app" || params["ErrorOnEmpty"] != "no" {
				t.Fatal(params)
			}
			result["RecordCountInfo"] = map[string]any{"TotalCount": 1}
			result["RecordList"] = []any{map[string]any{"RecordId": 5, "Name": "app", "Type": "CNAME", "Value": "target.example.com.", "LineId": "0", "TTL": 600, "Status": "ENABLE"}}
		case "ModifyRecord":
			if params["RecordId"] != float64(5) || params["RecordLineId"] != "0" {
				t.Fatal(params)
			}
			result["RecordId"] = 5
		default:
			t.Fatal(action)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(jsonBody(map[string]any{"Response": result}))))}, nil
	})}
	z, e := api.dnsZone(t.Context(), p)
	if e != nil || len(z.Lines) != 2 {
		t.Fatal(z, e)
	}
	records, e := api.dnsRecords(t.Context(), p, "app.customer.test")
	if e != nil || records[0].Value != "target.example.com" {
		t.Fatal(records, e)
	}
	before := records[0]
	after := before
	after.Value = "next.example.com"
	if _, e = api.writeDNS(t.Context(), p, DNSChange{Kind: "update", Before: &before, After: &after}); e != nil {
		t.Fatal(e)
	}
	for _, response := range []string{`{"RequestId":"r","DomainRecords":{"Record":[]}}`, `{"RequestId":"r","TotalCount":1001,"DomainRecords":{"Record":[]}}`, `{"RequestId":"r","TotalCount":1,"DomainRecords":{"Record":[]}}`} {
		api.client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
		})}
		if _, e = api.dnsRecords(t.Context(), testDNS(), "app.customer.test"); e == nil {
			t.Fatal("incomplete list accepted", response)
		}
	}
}
func TestRoutingCloudflareSetupProtocol(t *testing.T) {
	s, _, r, d, cf := routingService(t)
	report := readyReport(r)
	report.Hostname = SaaSHostname{}
	report.Fallback = FallbackOrigin{}
	p, e := buildRoutingPlan(RoutingTask{Action: "preview_setup"}, r, d, cf, report, routingDefaults())
	if e != nil {
		t.Fatal(e)
	}
	g := savePlan(t, s, p)
	var methods []string
	s.api.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		methods = append(methods, req.Method+" "+req.URL.Path)
		if req.Header.Get("Authorization") != "Bearer "+cf.Secret {
			t.Fatal("CF token missing")
		}
		var result any
		if req.Method == "PUT" {
			result = FallbackOrigin{Origin: r.FallbackOrigin, Status: "pending_deployment"}
		} else {
			var body struct {
				Hostname string
				SSL      struct {
					Method   string
					Type     string
					Settings map[string]string
				}
			}
			if e := json.NewDecoder(req.Body).Decode(&body); e != nil || body.Hostname != r.Hostname || body.SSL.Method != "txt" || body.SSL.Type != "dv" || body.SSL.Settings["min_tls_version"] != "1.2" {
				t.Fatal(body, e)
			}
			result = SaaSHostname{ID: "new-host", Hostname: r.Hostname, Status: "pending"}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"success":true,"result":%s}`, jsonBody(result))))}, nil
	})}
	if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, p); e != nil {
		t.Fatal(e)
	}
	if len(methods) != 2 || !strings.HasPrefix(methods[0], "PUT ") || !strings.HasPrefix(methods[1], "POST ") {
		t.Fatal(methods)
	}
}

func TestRoutingRecoveryAndChangedValidationNeverReplay(t *testing.T) {
	st := routingDefaults()
	st.Config.DNS = []DNSProfile{testDNS()}
	st.Config.Routes = []Route{testRoute()}
	st.Tasks = []RoutingTask{{ID: "task", Status: "running"}}
	st.Plans = []RoutingPlan{{ID: "plan", Status: "applying", Changes: []DNSChange{{Kind: "create", State: "attempting"}}}}
	if e := loadRouting(&st); e != nil {
		t.Fatal(e)
	}
	if st.Tasks[0].Status != "interrupted" || st.Plans[0].Status != "unknown" || routingBusy(st) {
		t.Fatal("interrupted work can replay", st)
	}
	s, _, r, d, cf := routingService(t)
	fixture := &dnsFixture{}
	s.api.client = &http.Client{Transport: fixture.transport(t)}
	report := readyReport(r)
	report.Validation = []DNSRecord{{Name: "_acme-challenge." + r.Hostname, Type: "TXT", Value: "old", Line: "default", TTL: 600, Status: "ENABLE"}}
	p, e := buildRoutingPlan(RoutingTask{Action: "preview_validation"}, r, d, cf, report, routingDefaults())
	if e != nil {
		t.Fatal(e)
	}
	g := savePlan(t, s, p)
	report.Validation[0].Value = "rotated"
	if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, p); e == nil || fixture.writes != 0 {
		t.Fatal("obsolete DCV token written", e)
	}
	p.Expires = time.Now().Add(-time.Minute)
	if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, p); !errors.Is(e, ErrConflict) {
		t.Fatal("expired plan accepted")
	}
}

func TestRoutingEventualReadDoesNotResubmitWrites(t *testing.T) {
	s, _, r, d, cf := routingService(t)
	fixture := &dnsFixture{}
	base := fixture.transport(t)
	staleRead := true
	s.api.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(strings.NewReader(string(body)))
		v, _ := url.ParseQuery(string(body))
		if v.Get("Action") == "DescribeDomainRecords" && fixture.writes == 1 && staleRead {
			staleRead = false
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"RequestId":"r","TotalCount":0,"DomainRecords":{"Record":[]}}`))}, nil
		}
		return base(req)
	})}
	report := readyReport(r)
	p, e := buildRoutingPlan(RoutingTask{Action: "preview_route"}, r, d, cf, report, routingDefaults())
	if e != nil {
		t.Fatal(e)
	}
	g := savePlan(t, s, p)
	if e = s.applyRoutingPlan(t.Context(), g, RoutingTask{}, r, d, cf, report, p); e != nil {
		t.Fatal(e)
	}
	if fixture.writes != 2 || len(fixture.snapshot()) != 2 || staleRead {
		t.Fatal("indexing delay resubmitted writes", fixture.writes)
	}
}

func TestRoutingAliStatusAndEmptyFallback(t *testing.T) {
	if canonicalRecord(DNSRecord{Status: "Enable"}).Status != "ENABLE" {
		t.Fatal("AliDNS mixed-case status unsupported")
	}
	api := newProviderClient()
	api.client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":null}`))}, nil
	})}
	var f FallbackOrigin
	if e := api.cfJSON(t.Context(), testProfile("cloudflare"), "GET", "/custom_hostnames/fallback_origin", nil, &f); e != nil || f.Origin != "" {
		t.Fatal(e, f)
	}
	var other map[string]any
	if e := api.cfJSON(t.Context(), testProfile("cloudflare"), "GET", "", nil, &other); e == nil {
		t.Fatal("unexpected null zone response accepted")
	}
}

func TestCloudflareConnectionAcceptsOnlyVerifiedSaaSDomains(t *testing.T) {
	for _, status := range []string{"active", "pending"} {
		t.Run(status, func(t *testing.T) {
			p := testProfile("cloudflare")
			p.Domains = []string{"app.customer.test"}
			api := newProviderClient()
			calls := 0
			api.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				result := `{"id":"zone123","name":"example.com"}`
				if calls == 2 {
					if req.URL.Query().Get("hostname") != "app.customer.test" {
						t.Fatal(req.URL)
					}
					result = fmt.Sprintf(`[{"id":"host123","hostname":"app.customer.test","status":%q,"ssl":{"status":"active"}}]`, status)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":true,"result":` + result + `}`))}, nil
			})}
			_, e := api.Execute(t.Context(), p, Job{Operation: "test"})
			if calls != 2 || (e == nil) != (status == "active") {
				t.Fatal("incorrect SaaS scope acceptance", calls, e)
			}
		})
	}
}
