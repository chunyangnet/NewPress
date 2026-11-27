package commentorigin

import "strings"

type Device struct {
	Kind    string `json:"kind"`
	OS      string `json:"os"`
	Browser string `json:"browser"`
}

// Client-reported UA is descriptive only; it is not an identity/security signal.
func ParseUA(ua string) Device {
	v := strings.ToLower(ua)
	d := Device{Kind: "unknown"}
	switch {
	case strings.Contains(v, "bot") || strings.Contains(v, "spider") || strings.Contains(v, "crawler"):
		d.Kind = "bot"
		d.OS = "自动化客户端"
	case strings.Contains(v, "ipad") || strings.Contains(v, "macintosh") && strings.Contains(v, "mobile/"):
		d.Kind = "tablet"
		d.OS = "iPadOS"
	case strings.Contains(v, "iphone") || strings.Contains(v, "ipod"):
		d.Kind = "mobile"
		d.OS = "iOS"
	case strings.Contains(v, "harmonyos") || strings.Contains(v, "openharmony"):
		d.Kind = "mobile"
		d.OS = "HarmonyOS"
	case strings.Contains(v, "windows phone"):
		d.Kind = "mobile"
		d.OS = "Windows Phone"
	case strings.Contains(v, "android"):
		d.Kind = "mobile"
		if !strings.Contains(v, "mobile") {
			d.Kind = "tablet"
		}
		d.OS = "Android"
	case strings.Contains(v, "windows"):
		d.Kind = "desktop"
		d.OS = "Windows"
	case strings.Contains(v, "cros"):
		d.Kind = "desktop"
		d.OS = "ChromeOS"
	case strings.Contains(v, "macintosh") || strings.Contains(v, "mac os x"):
		d.Kind = "desktop"
		d.OS = "macOS"
	case strings.Contains(v, "linux") || strings.Contains(v, "x11"):
		d.Kind = "desktop"
		d.OS = "Linux"
	}
	switch {
	case strings.Contains(v, "micromessenger"):
		d.Browser = "微信"
	case strings.Contains(v, "edg/") || strings.Contains(v, "edga/") || strings.Contains(v, "edgios/"):
		d.Browser = "Edge"
	case strings.Contains(v, "samsungbrowser"):
		d.Browser = "Samsung Internet"
	case strings.Contains(v, "opr/") || strings.Contains(v, "opera"):
		d.Browser = "Opera"
	case strings.Contains(v, "ucbrowser"):
		d.Browser = "UC"
	case strings.Contains(v, "qqbrowser"):
		d.Browser = "QQ 浏览器"
	case strings.Contains(v, "fxios/") || strings.Contains(v, "firefox/"):
		d.Browser = "Firefox"
	case strings.Contains(v, "chrome/") || strings.Contains(v, "crios/"):
		d.Browser = "Chrome"
	case strings.Contains(v, "safari/") && strings.Contains(v, "version/"):
		d.Browser = "Safari"
	}
	return d
}

func (d Device) Label(browser bool) string {
	v := d.OS
	if browser && d.Browser != "" {
		if v != "" {
			v += " · "
		}
		v += d.Browser
	}
	return v
}
