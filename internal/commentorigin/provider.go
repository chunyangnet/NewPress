package commentorigin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"

	"newpress.local/newpress/internal/safehttp"
)

type Location struct {
	Country     string `json:"country"`
	CountryCode string `json:"country_code,omitempty"`
	Region      string `json:"region"`
	City        string `json:"city"`
	Precision   string `json:"precision"`
	Provider    string `json:"provider"`
}
type Lookup func(context.Context, Settings, string) (Location, error)

func NewLookup() Lookup {
	client := safehttp.Client(5 * time.Second)
	return func(ctx context.Context, s Settings, ip string) (Location, error) { return lookup(ctx, client, s, ip) }
}

func lookup(ctx context.Context, client *http.Client, s Settings, ip string) (Location, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil || !safehttp.PublicIP(a) {
		return Location{}, errors.New("非公网地址，不查询归属地")
	}
	ip = a.Unmap().String()
	raw := ""
	switch s.Provider {
	case "ipwhois":
		raw = "https://ipwho.is/" + url.PathEscape(ip) + "?fields=ip,success,country,country_code,region,city"
	case "amap":
		if !a.Unmap().Is4() {
			return Location{}, errors.New("高德普通 IP 接口不支持 IPv6")
		}
		raw = "https://restapi.amap.com/v3/ip?" + url.Values{"ip": {ip}, "key": {s.Key}, "output": {"json"}}.Encode()
	case "baidu":
		raw = "https://api.map.baidu.com/location/ip?" + url.Values{"ip": {ip}, "ak": {s.Key}, "coor": {"bd09ll"}}.Encode()
	case "custom":
		raw = strings.NewReplacer("{ip}", url.QueryEscape(ip), "{key}", url.QueryEscape(s.Key)).Replace(s.Endpoint)
	default:
		return Location{}, errors.New("未知 IP 服务")
	}
	if _, err = safehttp.ValidateURL(raw); err != nil {
		return Location{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return Location{}, errors.New("IP 服务请求无效")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "NewPress-CommentOrigin/1")
	resp, err := client.Do(req)
	if err != nil {
		return Location{}, errors.New("IP 服务连接失败或超时")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Location{}, fmt.Errorf("IP 服务返回 HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return Location{}, errors.New("IP 服务响应过大或读取失败")
	}
	var data map[string]any
	if json.Unmarshal(b, &data) != nil {
		return Location{}, errors.New("IP 服务未返回有效 JSON")
	}
	if value := fieldText(data, "ip"); value != "" {
		returned, e := netip.ParseAddr(value)
		if e != nil || returned.Unmap() != a.Unmap() {
			return Location{}, errors.New("IP 服务返回了不同地址的结果")
		}
	}
	location := Location{Provider: s.Provider}
	switch s.Provider {
	case "ipwhois":
		if data["success"] != true {
			return Location{}, errors.New("免费 IP 服务未返回结果或额度不足")
		}
		location.Country = fieldText(data, "country")
		location.CountryCode = fieldText(data, "country_code")
		location.Region = fieldText(data, "region")
		location.City = fieldText(data, "city")
	case "amap":
		if fieldText(data, "status") != "1" {
			return Location{}, errors.New("高德查询失败，请检查 Key、权限和额度")
		}
		location.Region = fieldText(data, "province")
		location.City = fieldText(data, "city")
		if location.Region != "" || location.City != "" {
			location.Country = "中国"
			location.CountryCode = "CN"
		}
	case "baidu":
		if fieldText(data, "status") != "0" {
			return Location{}, errors.New("百度查询失败，请检查 AK、IP 白名单、IPv6 权限和额度")
		}
		location.Region = fieldText(data, "content.address_detail.province")
		location.City = fieldText(data, "content.address_detail.city")
		// Baidu's documented address starts with a country code (CN|...).
		// Missing or unrecognised codes stay unknown; never infer from city names.
		if address := strings.Split(fieldText(data, "address"), "|"); len(address) > 1 {
			location.CountryCode = address[0]
		}
	case "custom":
		if fieldText(data, s.SuccessPath) != s.SuccessValue {
			return Location{}, errors.New("自定义 IP 服务未返回成功状态")
		}
		location.Country = fieldText(data, s.CountryPath)
		location.CountryCode = fieldText(data, s.CountryCodePath)
		location.Region = fieldText(data, s.RegionPath)
		location.City = fieldText(data, s.CityPath)
	}
	location.Country = cleanLabel(location.Country)
	location.CountryCode = normalizeCountryCode(location.CountryCode)
	location.Region = cleanLabel(location.Region)
	location.City = cleanLabel(location.City)
	switch {
	case location.City != "":
		location.Precision = "city"
	case location.Region != "":
		location.Precision = "region"
	case location.Country != "":
		location.Precision = "country"
	default:
		return Location{}, errors.New("服务没有提供可显示的归属地")
	}
	return location, nil
}

func fieldText(data map[string]any, path string) string {
	if path == "" {
		return ""
	}
	var value any = data
	for _, part := range strings.Split(path, ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = m[part]
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		return fmt.Sprintf("%g", v)
	}
	return ""
}
func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	if _, err := netip.ParseAddr(s); err == nil {
		return ""
	}
	if len([]rune(s)) > 80 || strings.IndexFunc(s, unicode.IsControl) >= 0 || strings.ContainsAny(s, "<>\u202e\u202d\u2066\u2067\u2068\u2069") {
		return ""
	}
	switch strings.ToLower(s) {
	case "unknown", "none", "null", "n/a", "-", "局域网", "未知":
		return ""
	}
	return s
}

func (l Location) Label() string {
	if l.City != "" {
		if l.Region != "" && l.Region != l.City {
			return l.Region + " · " + l.City
		}
		return l.City
	}
	if l.Region != "" {
		return l.Region
	}
	return l.Country
}
