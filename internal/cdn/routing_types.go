package cdn

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Routing is stored with the CDN state but has its own optimistic version.
// Exported views never contain provider secrets or credential fingerprints.
type DNSProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Zone        string `json:"zone"`
	AccessKey   string `json:"access_key"`
	Secret      string `json:"secret,omitempty"`
	Token       string `json:"token,omitempty"`
	SecretSaved bool   `json:"secret_saved,omitempty"`
	TokenSaved  bool   `json:"token_saved,omitempty"`
}
type Route struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	CFProfileID       string   `json:"cf_profile_id"`
	DNSProfileID      string   `json:"dns_profile_id"`
	Hostname          string   `json:"hostname"`
	FallbackOrigin    string   `json:"fallback_origin"`
	Target            string   `json:"target"`
	PreferredTarget   string   `json:"preferred_target"`
	PreferredLines    []string `json:"preferred_lines"`
	TTL               int      `json:"ttl"`
	ManageExisting    bool     `json:"manage_existing"`
	HealthPath        string   `json:"health_path"`
	ExpectedBody      string   `json:"expected_body"`
	Monitor           bool     `json:"monitor"`
	Interval          int      `json:"interval"`
	FailureThreshold  int      `json:"failure_threshold"`
	RecoveryThreshold int      `json:"recovery_threshold"`
	Cooldown          int      `json:"cooldown"`
	AutoFailover      bool     `json:"auto_failover"`
	AutoRecover       bool     `json:"auto_recover"`
}
type RoutingConfig struct {
	Version int64        `json:"version"`
	DNS     []DNSProfile `json:"dns"`
	Routes  []Route      `json:"routes"`
}
type DNSLine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type DNSRecord struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"` // Fully qualified; @ is only used at provider boundaries.
	Type   string `json:"type"`
	Value  string `json:"value"`
	Line   string `json:"line"`
	TTL    int    `json:"ttl"`
	Status string `json:"status"`
	Weight int    `json:"weight,omitempty"`
	Locked bool   `json:"locked,omitempty"`
}
type DNSZone struct {
	Name        string    `json:"name"`
	Lines       []DNSLine `json:"lines"`
	Nameservers []string  `json:"nameservers"`
	MinTTL      int       `json:"min_ttl"`
	DefaultLine string    `json:"default_line"`
}
type ValidationRecord struct {
	TXTName     string `json:"txt_name,omitempty"`
	TXTValue    string `json:"txt_value,omitempty"`
	CNAME       string `json:"cname,omitempty"`
	CNAMETarget string `json:"cname_target,omitempty"`
	HTTPURL     string `json:"http_url,omitempty"`
	HTTPBody    string `json:"http_body,omitempty"`
}
type SaaSHostname struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Status       string `json:"status"`
	CustomOrigin string `json:"custom_origin_server,omitempty"`
	Ownership    struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"ownership_verification"`
	SSL struct {
		Status     string             `json:"status"`
		Method     string             `json:"method"`
		ExpiresOn  string             `json:"expires_on,omitempty"`
		Validation []ValidationRecord `json:"validation_records"`
		Delegation []ValidationRecord `json:"dcv_delegation_records"`
	} `json:"ssl"`
}
type FallbackOrigin struct {
	Origin string `json:"origin"`
	Status string `json:"status"`
}
type RouteHealth struct {
	Healthy    bool      `json:"healthy"`
	Checked    time.Time `json:"checked"`
	Samples    int       `json:"samples"`
	Success    int       `json:"success"`
	LatencyMS  int64     `json:"latency_ms"`
	Colos      []string  `json:"colos"`
	TLSExpires time.Time `json:"tls_expires"`
	Error      string    `json:"error,omitempty"`
}
type RouteReport struct {
	RouteID    string         `json:"route_id"`
	Checked    time.Time      `json:"checked"`
	ZoneName   string         `json:"zone_name"`
	Fallback   FallbackOrigin `json:"fallback"`
	Hostname   SaaSHostname   `json:"hostname"`
	DNS        DNSZone        `json:"dns"`
	ActualNS   []string       `json:"actual_ns"`
	Delegated  bool           `json:"delegated"`
	Records    []DNSRecord    `json:"records"`
	Validation []DNSRecord    `json:"validation"`
	Preferred  RouteHealth    `json:"preferred"`
	Normal     RouteHealth    `json:"normal"`
	Warnings   []string       `json:"warnings"`
	Mode       string         `json:"mode"`
}
type DNSChange struct {
	Kind   string     `json:"kind"`
	Before *DNSRecord `json:"before,omitempty"`
	After  *DNSRecord `json:"after,omitempty"`
	State  string     `json:"state"` // pending / attempting / done / unknown
}
type RoutingPlan struct {
	ID             string                 `json:"id"`
	RouteID        string                 `json:"route_id"`
	Kind           string                 `json:"kind"`
	Identity       string                 `json:"identity,omitempty"`
	Created        time.Time              `json:"created"`
	Expires        time.Time              `json:"expires"`
	Status         string                 `json:"status"`
	Actor          string                 `json:"actor"`
	Changes        []DNSChange            `json:"changes"`
	Snapshots      map[string][]DNSRecord `json:"snapshots"`
	FallbackBefore string                 `json:"fallback_before,omitempty"`
	FallbackAfter  string                 `json:"fallback_after,omitempty"`
	FallbackState  string                 `json:"fallback_state,omitempty"`
	CreateHostname bool                   `json:"create_hostname"`
	HostnameState  string                 `json:"hostname_state,omitempty"`
	HostnameID     string                 `json:"hostname_id,omitempty"`
	RollbackOf     string                 `json:"rollback_of,omitempty"`
	Error          string                 `json:"error,omitempty"`
}
type RoutingTask struct {
	ID       string    `json:"id"`
	RouteID  string    `json:"route_id"`
	Action   string    `json:"action"`
	PlanID   string    `json:"plan_id,omitempty"`
	Identity string    `json:"identity,omitempty"`
	Actor    string    `json:"actor"`
	Status   string    `json:"status"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Error    string    `json:"error,omitempty"`
}
type routeMonitor struct {
	Identity   string    `json:"identity"`
	Next       time.Time `json:"next"`
	LastSwitch time.Time `json:"last_switch"`
	Failures   int       `json:"failures"`
	Successes  int       `json:"successes"`
}
type ownedRecord struct {
	DNSIdentity string    `json:"dns_identity"`
	RouteID     string    `json:"route_id"`
	Record      DNSRecord `json:"record"`
}
type RoutingState struct {
	Config   RoutingConfig           `json:"config"`
	Reports  map[string]RouteReport  `json:"reports"`
	Plans    []RoutingPlan           `json:"plans"`
	Tasks    []RoutingTask           `json:"tasks"`
	Owned    []ownedRecord           `json:"owned,omitempty"`
	Monitors map[string]routeMonitor `json:"monitors"`
}

func routingDefaults() RoutingState {
	return RoutingState{Config: RoutingConfig{Version: 1, DNS: []DNSProfile{}, Routes: []Route{}}, Reports: map[string]RouteReport{}, Plans: []RoutingPlan{}, Tasks: []RoutingTask{}, Monitors: map[string]routeMonitor{}}
}
func cleanHost(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}
func inZone(host, zone string) bool { return host == zone || strings.HasSuffix(host, "."+zone) }
func relativeName(host, zone string) string {
	if host == zone {
		return "@"
	}
	return strings.TrimSuffix(host, "."+zone)
}
func fullName(rr, zone string) string {
	if rr == "@" || rr == "" {
		return zone
	}
	return cleanHost(rr) + "." + zone
}
func defaultDNSLine(p DNSProfile) string {
	if p.Provider == "aliyun" {
		return "default"
	}
	return "0"
}

var dnsLinePattern = regexp.MustCompile(`^[A-Za-z0-9_=.\-]{1,128}$`)

func normalizeRouting(c RoutingConfig) (RoutingConfig, error) {
	if c.DNS == nil {
		c.DNS = []DNSProfile{}
	}
	if c.Routes == nil {
		c.Routes = []Route{}
	}
	if c.Version < 1 || len(c.DNS) > 8 || len(c.Routes) > 20 {
		return c, errors.New("最多 8 个 DNS 连接和 20 个接入域名")
	}
	seen := map[string]bool{}
	for i, p := range c.DNS {
		p.Name = strings.TrimSpace(p.Name)
		p.Zone = cleanHost(p.Zone)
		p.AccessKey = strings.TrimSpace(p.AccessKey)
		if !idPattern.MatchString(p.ID) || seen[p.ID] || len(p.Name) < 1 || len(p.Name) > 160 || !slices.Contains([]string{"aliyun", "dnspod"}, p.Provider) || !validHost(p.Zone) || p.AccessKey == "" || p.Secret == "" || len(p.AccessKey) > 256 || len(p.Secret) > 8192 || len(p.Token) > 8192 || strings.ContainsAny(p.AccessKey+p.Secret+p.Token, "\r\n\x00") {
			return c, errors.New("DNS 连接的标识、域名、服务商或凭据不正确")
		}
		p.SecretSaved = false
		p.TokenSaved = false
		c.DNS[i] = p
		seen[p.ID] = true
	}
	seen = map[string]bool{}
	hosts := map[string]bool{}
	for i, r := range c.Routes {
		r.PreferredLines = slices.Clone(r.PreferredLines)
		if r.PreferredLines == nil {
			r.PreferredLines = []string{}
		}
		r.Name = strings.TrimSpace(r.Name)
		r.Hostname = cleanHost(r.Hostname)
		r.FallbackOrigin = cleanHost(r.FallbackOrigin)
		r.Target = cleanHost(r.Target)
		r.PreferredTarget = cleanHost(r.PreferredTarget)
		p, ok := dnsProfile(c, r.DNSProfileID)
		if !ok || !idPattern.MatchString(r.ID) || seen[r.ID] || hosts[r.Hostname] || !idPattern.MatchString(r.CFProfileID) || r.Name == "" || len(r.Name) > 160 || !validHost(r.Hostname) || !inZone(r.Hostname, p.Zone) || !validHost(r.FallbackOrigin) || !validHost(r.Target) || !validHost(r.PreferredTarget) || r.Hostname == r.Target || r.Hostname == r.PreferredTarget || r.Hostname == r.FallbackOrigin || r.Target == r.PreferredTarget || r.FallbackOrigin == r.PreferredTarget {
			return c, errors.New("接入域名必须属于 DNS 连接的区域，域名不能重复或形成直接回环")
		}
		if len(r.PreferredLines) > 8 || r.TTL < 60 || r.TTL > 86400 || r.Interval < 60 || r.Interval > 86400 || r.FailureThreshold < 2 || r.FailureThreshold > 10 || r.RecoveryThreshold < 2 || r.RecoveryThreshold > 10 || r.Cooldown < 300 || r.Cooldown > 86400 || len(r.ExpectedBody) > 256 {
			return c, errors.New("优选线路最多 8 条；TTL 60–86400 秒，检测间隔 60–86400 秒，连续阈值 2–10，切换冷却 300–86400 秒")
		}
		for _, line := range r.PreferredLines {
			if !dnsLinePattern.MatchString(line) || line == defaultDNSLine(p) {
				return c, errors.New("请选择服务商实际支持的地区或运营商线路；默认线路保留为常规接入")
			}
		}
		slices.Sort(r.PreferredLines)
		r.PreferredLines = slices.Compact(r.PreferredLines)
		u, e := url.ParseRequestURI(r.HealthPath)
		if e != nil || !strings.HasPrefix(r.HealthPath, "/") || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || !publicPath(u.Path) || len(r.HealthPath) > 1024 || strings.ContainsAny(r.HealthPath, "\r\n\x00") {
			return c, errors.New("健康检查使用无查询参数的本站公开路径，例如 /health.txt")
		}
		if (r.AutoFailover || r.AutoRecover) && !r.Monitor || r.AutoRecover && !r.AutoFailover {
			return c, errors.New("自动回退需要开启监测；自动恢复需要开启自动回退")
		}
		seen[r.ID] = true
		hosts[r.Hostname] = true
		c.Routes[i] = r
	}
	return c, nil
}
func dnsProfile(c RoutingConfig, id string) (DNSProfile, bool) {
	for _, p := range c.DNS {
		if p.ID == id {
			return p, true
		}
	}
	return DNSProfile{}, false
}
func routingRoute(c RoutingConfig, id string) (Route, bool) {
	for _, r := range c.Routes {
		if r.ID == id {
			return r, true
		}
	}
	return Route{}, false
}
func dnsIdentity(p DNSProfile) string {
	p.Name = ""
	p.SecretSaved = false
	p.TokenSaved = false
	return digest(jsonBody(p))
}
func routeIdentity(r Route, p DNSProfile, cf Profile) string {
	return digest(jsonBody([]any{r, dnsIdentity(p), identity(cf)}))
}
func routingView(st RoutingState) RoutingState {
	st.Config.DNS = slices.Clone(st.Config.DNS)
	for i, p := range st.Config.DNS {
		p.SecretSaved = p.Secret != ""
		p.TokenSaved = p.Token != ""
		p.Secret = ""
		p.Token = ""
		st.Config.DNS[i] = p
	}
	st.Tasks = slices.Clone(st.Tasks)
	for i := range st.Tasks {
		st.Tasks[i].Identity = ""
	}
	st.Plans = slices.Clone(st.Plans)
	for i := range st.Plans {
		st.Plans[i].Identity = ""
	}
	st.Owned = nil
	st.Monitors = nil
	return st
}
