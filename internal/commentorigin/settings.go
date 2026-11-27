// Package commentorigin is NewPress's official, statically registered comment
// origin extension. Its private storage is owned by the plugin lifecycle.
package commentorigin

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"newpress.local/newpress/internal/safehttp"
)

type Rule struct {
	Role     string `json:"role"`
	Location bool   `json:"location"`
	Device   bool   `json:"device"`
}
type Settings struct {
	Version         int64  `json:"version"`
	Provider        string `json:"provider"`
	Key             string `json:"key,omitempty"`
	Endpoint        string `json:"endpoint"`
	CountryPath     string `json:"country_path"`
	CountryCodePath string `json:"country_code_path"`
	RegionPath      string `json:"region_path"`
	CityPath        string `json:"city_path"`
	SuccessPath     string `json:"success_path"`
	SuccessValue    string `json:"success_value"`
	Location        bool   `json:"location"`
	FlagMode        string `json:"flag_mode"`
	Device          bool   `json:"device"`
	Browser         bool   `json:"browser"`
	Fallback        bool   `json:"fallback"`
	RetentionDays   int    `json:"retention_days"`
	CacheDays       int    `json:"cache_days"`
	DailyLimit      int    `json:"daily_limit"`
	Rules           []Rule `json:"rules"`
}

func Defaults() Settings {
	return Settings{Version: 1, Provider: "ipwhois", Location: true, FlagMode: "foreign", Device: true, Browser: true, RetentionDays: 30, CacheDays: 7, DailyLimit: 900, CountryPath: "country", CountryCodePath: "country_code", RegionPath: "region", CityPath: "city", SuccessPath: "success", SuccessValue: "true", Rules: []Rule{{"guest", true, true}, {"administrator", false, false}, {"editor", true, true}, {"author", true, true}, {"subscriber", true, true}}}
}

func validate(s Settings) error {
	if s.FlagMode != "off" && s.FlagMode != "all" && s.FlagMode != "foreign" {
		return errors.New("国旗显示请选择关闭、全部显示或仅国外")
	}
	if s.Provider != "ipwhois" && s.Provider != "amap" && s.Provider != "baidu" && s.Provider != "custom" {
		return errors.New("请选择支持的 IP 服务")
	}
	if (s.Provider == "amap" || s.Provider == "baidu") && s.Key == "" {
		return errors.New("该服务需要 API Key／AK")
	}
	if len(s.Key) > 512 || strings.IndexFunc(s.Key, unicode.IsControl) >= 0 {
		return errors.New("API 密钥格式无效")
	}
	if s.RetentionDays < 1 || s.RetentionDays > 365 || s.CacheDays < 1 || s.CacheDays > 30 || s.DailyLimit < 1 || s.DailyLimit > 900 {
		return errors.New("原始记录保留 1–365 天，缓存 1–30 天，每日查询 1–900 次")
	}
	if len(s.Rules) > 100 {
		return errors.New("角色规则过多")
	}
	seen := map[string]bool{}
	re := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	for _, r := range s.Rules {
		if !re.MatchString(r.Role) || seen[r.Role] {
			return errors.New("角色规则无效或重复")
		}
		seen[r.Role] = true
	}
	if s.Provider == "custom" {
		if len(s.Endpoint) > 2048 || strings.Count(s.Endpoint, "{ip}") != 1 || strings.Count(s.Endpoint, "{key}") > 1 {
			return errors.New("自定义 HTTPS 地址必须包含一个 {ip}，可包含一个 {key}")
		}
		test := strings.NewReplacer("{ip}", "8.8.8.8", "{key}", "test").Replace(s.Endpoint)
		u, err := safehttp.ValidateURL(test)
		if err != nil {
			return err
		}
		original, _ := url.Parse(strings.ReplaceAll(strings.ReplaceAll(s.Endpoint, "{ip}", "IPPLACEHOLDER"), "{key}", "KEYPLACEHOLDER"))
		if strings.Contains(original.Host, "PLACEHOLDER") || strings.ContainsAny(u.Path, "{}") || strings.ContainsAny(u.RawQuery, "{}") {
			return errors.New("占位符只能出现在路径或查询参数中")
		}
		field := regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+){0,7}$`)
		for _, v := range []string{s.CountryPath, s.CountryCodePath, s.RegionPath, s.CityPath, s.SuccessPath} {
			if len(v) > 160 || v != "" && !field.MatchString(v) {
				return errors.New("JSON 字段使用点分隔路径，例如 data.city")
			}
		}
		if s.CityPath == "" || s.SuccessPath == "" || s.SuccessValue == "" || len(s.SuccessValue) > 60 {
			return errors.New("请填写城市字段、成功字段与成功值")
		}
	}
	return nil
}

// Multiple roles use the most restrictive explicit rule. A newly added group
// has no display permission until the administrator configures it.
func (s Settings) Allowed(roles []string) (bool, bool) {
	if len(roles) == 0 {
		roles = []string{"guest"}
	}
	location, device := s.Location, s.Device
	for _, role := range roles {
		found := false
		for _, r := range s.Rules {
			if r.Role == role {
				location = location && r.Location
				device = device && r.Device
				found = true
				break
			}
		}
		if !found {
			return false, false
		}
	}
	return location, device
}
