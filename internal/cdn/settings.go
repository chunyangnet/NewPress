// Package cdn provides the reviewed host implementation of the CDN plugin.
package cdn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"newpress.local/newpress/internal/safehttp"
)

var ErrConflict = errors.New("配置或任务已变化，请刷新后重试")
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
var resourcePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var hostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9:_./-]{1,128}$`)

type Profile struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Provider   string   `json:"provider"`
	ResourceID string   `json:"resource_id"`
	Domains    []string `json:"domains"`
	AccessKey  string   `json:"access_key"`
	Secret     string   `json:"secret,omitempty"`
	Token      string   `json:"token,omitempty"`
	AutoPurge  bool     `json:"auto_purge"`
}
type Settings struct {
	Version         int64     `json:"version"`
	Profiles        []Profile `json:"profiles"`
	Paused          bool      `json:"paused"`
	Workers         int       `json:"workers"`
	MaxAttempts     int       `json:"max_attempts"`
	IntervalSeconds int       `json:"interval_seconds"`
	AutoContent     bool      `json:"auto_content"`
	AutoComments    bool      `json:"auto_comments"`
	AutoStorage     bool      `json:"auto_storage"`
	CoreAssets      bool      `json:"core_assets"`
	AssetBase       string    `json:"asset_base"`
	StaticTTL       int       `json:"static_ttl"`
}
type ProfileView struct {
	Profile
	SecretSaved bool `json:"secret_saved"`
	TokenSaved  bool `json:"token_saved"`
}
type SettingsView struct {
	Settings
	ProfileViews []ProfileView `json:"profile_views"`
}
type Capability struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Operations []string `json:"operations"`
	Note       string   `json:"note"`
}

func Capabilities() []Capability {
	return []Capability{
		{"cloudflare", "Cloudflare", []string{"purge_url", "purge_prefix", "purge_host", "purge_tag", "purge_all", "test", "diagnose", "benchmark"}, "API Token + Zone ID；按套餐决定标签、前缀等权限。无全网预热 API。"},
		{"tencent", "腾讯云 CDN", []string{"purge_url", "purge_directory", "warm", "test", "diagnose"}, "SecretId / SecretKey；支持临时 Token。目录刷新采用 flush。"},
		{"edgeone", "腾讯云 EdgeOne", []string{"purge_url", "purge_prefix", "purge_host", "purge_all", "warm", "test", "diagnose"}, "SecretId / SecretKey + Zone ID；与腾讯 CDN 使用独立接口。"},
		{"aliyun", "阿里云 CDN", []string{"purge_url", "purge_directory", "warm", "test", "diagnose"}, "AccessKey ID / Secret；支持 STS Token。此连接使用 CDN 产品接口。"},
		{"qiniu", "七牛云 Fusion", []string{"purge_url", "purge_directory", "warm", "test", "diagnose"}, "AccessKey / SecretKey；逐项检查无效 URL 和目录。"},
		{"cloudfront", "Amazon CloudFront", []string{"purge_url", "purge_prefix", "purge_all", "test", "diagnose"}, "AccessKey / SecretKey + Distribution ID；全站失效作用于整个分配。"},
	}
}
func supports(provider, op string) bool {
	for _, c := range Capabilities() {
		if c.ID == provider {
			return slices.Contains(c.Operations, op)
		}
	}
	return false
}
func Defaults() Settings {
	return Settings{Version: 1, Profiles: []Profile{}, Workers: 2, MaxAttempts: 4, IntervalSeconds: 3, AutoContent: true, AutoComments: true, AutoStorage: true, StaticTTL: 3600}
}
func view(c Settings) SettingsView {
	v := SettingsView{Settings: c, ProfileViews: []ProfileView{}}
	v.Profiles = slices.Clone(c.Profiles)
	for i, p := range c.Profiles {
		v.Profiles[i].Secret, v.Profiles[i].Token = "", ""
		v.Profiles[i].Domains = slices.Clone(p.Domains)
		v.ProfileViews = append(v.ProfileViews, ProfileView{v.Profiles[i], p.Secret != "", p.Token != ""})
	}
	return v
}
func validHost(h string) bool {
	if !hostPattern.MatchString(h) || !strings.Contains(h, ".") || strings.Contains(h, "..") {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false
	}
	for _, s := range strings.Split(h, ".") {
		if len(s) > 63 || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
			return false
		}
	}
	return true
}
func normalize(c Settings) (Settings, error) {
	if c.Version < 1 || len(c.Profiles) > 8 || c.Workers < 1 || c.Workers > 4 || c.MaxAttempts < 1 || c.MaxAttempts > 6 || c.IntervalSeconds < 2 || c.IntervalSeconds > 300 || c.StaticTTL < 0 || c.StaticTTL > 86400 {
		return c, errors.New("最多 8 个连接；并发 1–4、尝试 1–6 次、提交间隔 2–300 秒、静态缓存 0–86400 秒")
	}
	seen := map[string]bool{}
	for i, p := range c.Profiles {
		p.Name = strings.TrimSpace(p.Name)
		p.AccessKey = strings.TrimSpace(p.AccessKey)
		p.ResourceID = strings.TrimSpace(p.ResourceID)
		if !idPattern.MatchString(p.ID) || seen[p.ID] || p.Name == "" || len(p.Name) > 160 || !supports(p.Provider, "test") || len(p.Domains) < 1 || len(p.Domains) > 20 {
			return c, fmt.Errorf("第 %d 个连接的名称、标识、服务商或域名列表不正确", i+1)
		}
		seen[p.ID] = true
		if len(p.Secret) > 8192 || len(p.Token) > 8192 || len(p.AccessKey) > 256 || strings.ContainsAny(p.AccessKey+p.Secret+p.Token, "\r\n\x00") {
			return c, errors.New("凭据格式不正确")
		}
		if p.Secret == "" || p.Provider != "cloudflare" && p.AccessKey == "" {
			return c, errors.New("请填写连接凭据；Cloudflare 的 API Token 填入 Secret / Token 字段")
		}
		if (p.Provider == "cloudflare" || p.Provider == "edgeone" || p.Provider == "cloudfront") && !resourcePattern.MatchString(p.ResourceID) {
			return c, errors.New("请填写有效的 Zone / Distribution ID，不能使用通配符")
		}
		p.Domains = slices.Clone(p.Domains)
		for n, d := range p.Domains {
			d = strings.ToLower(strings.TrimSpace(d))
			if !validHost(d) {
				return c, errors.New("域名仅填写完整主机名，国际化域名使用 punycode；不包含协议、端口或通配符")
			}
			p.Domains[n] = d
		}
		slices.Sort(p.Domains)
		p.Domains = slices.Compact(p.Domains)
		c.Profiles[i] = p
	}
	c.AssetBase = strings.TrimRight(strings.TrimSpace(c.AssetBase), "/")
	if c.AssetBase != "" {
		u, e := safehttp.ValidateURL(c.AssetBase)
		if e != nil || u.RawQuery != "" || u.RawPath != "" || u.Path != "" || !validHost(u.Hostname()) {
			return c, errors.New("静态资源 CDN 填写 HTTPS 根地址，不含路径、查询或 IP")
		}
		found := false
		for _, p := range c.Profiles {
			found = found || slices.Contains(p.Domains, strings.ToLower(u.Hostname()))
		}
		if !found {
			return c, errors.New("静态资源域名必须属于一个已配置连接")
		}
		c.AssetBase = "https://" + strings.ToLower(u.Host)
	}
	return c, nil
}
func identity(p Profile) string {
	p.Name = ""
	p.AutoPurge = false
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func profile(c Settings, id string) (Profile, bool) {
	for _, p := range c.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}
func targetURL(p Profile, raw string) (*url.URL, error) {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00\\") {
		return nil, errors.New("URL 太长或包含不允许的字符")
	}
	u, e := safehttp.ValidateURL(raw)
	if e != nil {
		return nil, e
	}
	if !slices.Contains(p.Domains, strings.ToLower(u.Hostname())) {
		return nil, errors.New("URL 必须属于所选连接的精确域名列表")
	}
	if strings.Contains(u.Path, "*") || strings.Contains(u.Path, "..") {
		return nil, errors.New("URL 路径不能包含通配符或 ..")
	}
	if strings.Contains(raw, "*") {
		return nil, errors.New("URL 不能包含通配符")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("URL 查询参数不正确")
	}
	for key := range query {
		k := strings.ToLower(key)
		if strings.HasPrefix(k, "x-amz-") || strings.HasPrefix(k, "x-oss-") || slices.Contains([]string{"token", "access_token", "signature", "authorization", "key-pair-id", "policy", "auth_key"}, k) {
			return nil, errors.New("请使用未签名的公开 URL，不要提交带访问令牌或签名的链接")
		}
	}
	return u, nil
}
func normalizeTargets(p Profile, op string, targets []string) ([]string, error) {
	if !supports(p.Provider, op) {
		return nil, errors.New("所选服务商不支持此操作")
	}
	if op == "test" || op == "purge_all" {
		if len(targets) > 0 {
			return nil, errors.New("此操作不接受 URL")
		}
		return []string{}, nil
	}
	if len(targets) < 1 || len(targets) > 100 {
		return nil, errors.New("每批提交 1–100 个目标")
	}
	if op == "diagnose" && len(targets) != 1 {
		return nil, errors.New("诊断每次仅接受一个 URL")
	}
	result := []string{}
	for _, raw := range targets {
		raw = strings.TrimSpace(raw)
		switch op {
		case "purge_tag":
			if !tagPattern.MatchString(raw) {
				return nil, errors.New("标签仅支持字母、数字及 : _ . / -")
			}
		case "purge_host":
			raw = strings.ToLower(raw)
			if !slices.Contains(p.Domains, raw) {
				return nil, errors.New("只能刷新已配置的精确域名")
			}
		default:
			u, e := targetURL(p, raw)
			if e != nil {
				return nil, e
			}
			if (op == "warm" || op == "diagnose") && (!publicPath(u.Path) || u.RawQuery != "") {
				return nil, errors.New("预热和诊断仅允许无查询参数的公开资源，不访问后台、接口、账号或预览")
			}
			if op == "purge_directory" || op == "purge_prefix" {
				if u.RawQuery != "" || !strings.HasSuffix(u.Path, "/") {
					return nil, errors.New("目录和前缀必须以 / 结尾且不带查询参数")
				}
			}
			raw = u.String()
		}
		if !slices.Contains(result, raw) {
			result = append(result, raw)
		}
	}
	return result, nil
}
func publicPath(p string) bool {
	if strings.ContainsAny(p, "\\%") || strings.Contains(p, "..") || strings.Contains(p, "//") {
		return false
	}
	for _, prefix := range []string{"/admin", "/api", "/account", "/preview", "/login", "/logout", "/register", "/billing", "/checkout", "/payment", "/oauth", "/auth", "/."} {
		if strings.HasPrefix(strings.ToLower(p), prefix) {
			return false
		}
	}
	return true
}
