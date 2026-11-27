package objectstorage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"newpress.local/newpress/internal/safehttp"
)

type Settings struct {
	Version          int64                  `json:"version"`
	ActiveProfile    string                 `json:"active_profile"`
	Mode             string                 `json:"mode"`
	AutoSync         bool                   `json:"auto_sync"`
	VerifyDownload   bool                   `json:"verify_download"`
	Paused           bool                   `json:"paused"`
	Workers          int                    `json:"workers"`
	ScanSeconds      int                    `json:"scan_seconds"`
	MaxAttempts      int                    `json:"max_attempts"`
	SignedURLSeconds int                    `json:"signed_url_seconds"`
	Profiles         []Profile              `json:"profiles"`
	UploadRoutes     map[string]UploadRoute `json:"upload_routes,omitempty"`
}

type ProfileView struct {
	Profile
	SecretSaved   bool `json:"secret_saved"`
	TokenSaved    bool `json:"token_saved"`
	PasswordSaved bool `json:"password_saved"`
}

type SettingsView struct {
	Settings
	ProfileViews []ProfileView `json:"profile_views"`
}

func Defaults() Settings {
	return Settings{Version: 1, Mode: "mirror", AutoSync: true, VerifyDownload: true, Workers: 2, ScanSeconds: 60, MaxAttempts: 5, SignedURLSeconds: 300, Profiles: []Profile{}}
}

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
var regionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var cosBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-[0-9]+$`)

func normalizeSettings(c Settings) (Settings, error) {
	if c.Version < 1 || len(c.Profiles) > 8 || c.Workers < 1 || c.Workers > 4 || c.ScanSeconds < 30 || c.ScanSeconds > 3600 || c.MaxAttempts < 1 || c.MaxAttempts > 10 || c.SignedURLSeconds < 60 || c.SignedURLSeconds > 3600 {
		return c, errors.New("配置范围不正确：最多 8 个存储，1–4 个并发，扫描 30–3600 秒，重试 1–10 次，链接有效期 60–3600 秒")
	}
	if c.Mode != "mirror" && c.Mode != "redirect" {
		return c, errors.New("访问方式必须是本地读取或云端加速")
	}
	seen := map[string]bool{}
	for i, p := range c.Profiles {
		var err error
		p, err = normalizeProfile(p)
		if err != nil {
			return c, fmt.Errorf("存储配置 %d：%w", i+1, err)
		}
		if seen[p.ID] {
			return c, errors.New("存储标识不能重复")
		}
		seen[p.ID] = true
		c.Profiles[i] = p
	}
	if c.ActiveProfile != "" && !seen[c.ActiveProfile] {
		return c, errors.New("当前存储配置不存在")
	}
	return c, validateRoutes(c)
}

func normalizeProfile(p Profile) (Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Endpoint = strings.TrimRight(strings.TrimSpace(p.Endpoint), "/")
	p.PublicURL = strings.TrimRight(strings.TrimSpace(p.PublicURL), "/")
	p.Region = strings.TrimSpace(p.Region)
	p.Bucket = strings.TrimSpace(p.Bucket)
	p.Prefix = strings.Trim(strings.TrimSpace(p.Prefix), "/")
	if !identifierPattern.MatchString(p.ID) || p.Name == "" || utf8.RuneCountInString(p.Name) > 80 || strings.ContainsAny(p.Name, "\r\n\x00") {
		return p, errors.New("请填写有效的存储标识和名称")
	}
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 120
	}
	if p.TimeoutSeconds < 10 || p.TimeoutSeconds > 600 {
		return p, errors.New("请求超时必须为 10–600 秒")
	}
	if p.Prefix != "" && (!cleanKey(p.Prefix) || len(p.Prefix) > 200) {
		return p, errors.New("目录前缀必须是相对路径，不允许 .、.. 或空段")
	}
	webdav := p.Provider == "cloudreve" || p.Provider == "webdav"
	if !webdav {
		if p.Username != "" || p.Password != "" {
			return p, errors.New("对象存储配置不使用 WebDAV 用户名或密码")
		}
		if !bucketPattern.MatchString(p.Bucket) || strings.Contains(p.Bucket, "..") {
			return p, errors.New("请填写有效的存储桶或 S3 空间名称")
		}
		if p.AccessKey == "" || p.SecretKey == "" {
			return p, errors.New("请填写 Access Key 和 Secret Key")
		}
		if p.Provider == "r2" {
			p.Region = "auto"
		}
		if !regionPattern.MatchString(p.Region) {
			return p, errors.New("请填写有效的区域，例如 us-east-1、ap-guangzhou、cn-hangzhou")
		}
		switch p.Provider {
		case "s3":
			if p.Endpoint == "" {
				suffix := "amazonaws.com"
				if strings.HasPrefix(p.Region, "cn-") {
					suffix += ".cn"
				}
				p.Endpoint = "https://s3." + p.Region + "." + suffix
			}
		case "r2", "s3-compatible":
			if p.Endpoint == "" {
				return p, errors.New("此服务需要填写完整的 API Endpoint")
			}
		case "cos":
			if !cosBucketPattern.MatchString(p.Bucket) {
				return p, errors.New("腾讯 COS 桶名必须包含 APPID 后缀")
			}
			if p.PathStyle {
				return p, errors.New("腾讯 COS 请选择虚拟主机地址方式")
			}
			if p.Endpoint == "" {
				p.Endpoint = "https://cos." + p.Region + ".myqcloud.com"
			}
		case "oss":
			if p.Endpoint == "" {
				p.Endpoint = "https://oss-" + p.Region + ".aliyuncs.com"
			}
		case "qiniu":
			if p.Endpoint == "" {
				p.Endpoint = "https://s3." + p.Region + ".qiniucs.com"
			}
		default:
			return p, errors.New("不支持的存储服务")
		}
	} else {
		if p.Endpoint == "" || p.Username == "" || p.Password == "" || strings.Contains(p.Username, ":") {
			return p, errors.New("请填写 WebDAV 地址、用户名和独立 WebDAV 密码")
		}
		if p.Bucket != "" || p.AccessKey != "" || p.SecretKey != "" || p.SessionToken != "" {
			return p, errors.New("WebDAV 配置不使用存储桶或云端密钥")
		}
	}
	for _, secret := range []string{p.AccessKey, p.SecretKey, p.SessionToken, p.Username, p.Password} {
		if len(secret) > 8192 || strings.ContainsAny(secret, "\r\n\x00") {
			return p, errors.New("凭据格式不正确")
		}
	}
	u, err := validateStorageEndpoint(p)
	if err != nil {
		return p, err
	}
	if !webdav && u.Path != "" && u.Path != "/" {
		return p, errors.New("对象存储 Endpoint 只填写服务地址，桶名和目录前缀分别填写")
	}
	if p.PublicURL != "" {
		cdn, e := safehttp.ValidateURL(p.PublicURL)
		if e != nil || cdn.RawQuery != "" || cdn.ForceQuery || cdn.RawPath != "" || (cdn.Path != "" && path.Clean(cdn.Path) != cdn.Path) || strings.ContainsAny(cdn.Path, "\\%") {
			return p, errors.New("下载域名须为不带凭据、查询参数的公网 HTTPS 地址")
		}
	}
	return p, nil
}

func cleanKey(k string) bool {
	if k == "" || len(k) > 1024 || strings.HasPrefix(k, "/") || strings.ContainsAny(k, "\\\x00\r\n") || !utf8.ValidString(k) {
		return false
	}
	for _, s := range strings.Split(k, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}

// Identity excludes credentials so keys can rotate, but cannot reinterpret old
// object receipts as belonging to a different bucket, account or directory.
func profileIdentity(p Profile) string {
	b, _ := json.Marshal([]any{p.Provider, p.Endpoint, p.Region, p.Bucket, p.Prefix, p.Username, p.PathStyle})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func sanitized(c Settings) SettingsView {
	view := SettingsView{Settings: cloneRoutes(c), ProfileViews: []ProfileView{}}
	view.Profiles = append([]Profile(nil), c.Profiles...)
	for i, p := range c.Profiles {
		v := ProfileView{Profile: p, SecretSaved: p.SecretKey != "", TokenSaved: p.SessionToken != "", PasswordSaved: p.Password != ""}
		v.SecretKey = ""
		v.SessionToken = ""
		v.Password = ""
		view.Profiles[i] = v.Profile
		view.ProfileViews = append(view.ProfileViews, v)
	}
	return view
}

func publicObjectURL(base, key string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + key
	u.RawPath = ""
	return u.String()
}
