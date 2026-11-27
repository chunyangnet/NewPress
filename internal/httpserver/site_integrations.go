package httpserver

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"newpress.local/newpress/internal/site"
)

//go:embed assets/site-integrations.js
var siteIntegrationScript []byte

var siteIntegrationScriptURL = func() string {
	digest := sha256.Sum256(siteIntegrationScript)
	return "/site-integrations.js?v=" + hex.EncodeToString(digest[:8])
}()

type siteIntegrationsView struct {
	ICPNumber, PoliceNumber, PoliceURL string
	FooterCopyright, FooterHTML        string
	FooterHideCredit                   bool
	Verification                       []site.VerificationMeta
	AnalyticsConfig, ScriptURL         string
}

func (s *Server) integrationScript(w http.ResponseWriter, r *http.Request) {
	serveGenerated(w, r, "text/javascript; charset=utf-8", siteIntegrationScript, "public, max-age=3600, must-revalidate")
}

func (s *Server) prepareSiteIntegrations(w http.ResponseWriter, status int, data *pageView, page string) {
	i := s.siteSnapshot(responseContext(w)).Settings.IntegrationValues()
	data.Integrations = siteIntegrationsView{ICPNumber: i.ICPNumber, PoliceNumber: i.PoliceNumber,
		FooterCopyright: i.FooterCopyright, FooterHTML: i.FooterHTML, FooterHideCredit: i.FooterHideCredit}
	if i.PoliceCode != "" {
		data.Integrations.PoliceURL = "https://beian.mps.gov.cn/#/query/webSearch?code=" + url.QueryEscape(i.PoliceCode)
	}
	if data.IsAdmin || data.IsPreview || status < 200 || status >= 300 || !trackingPage(page) {
		return
	}
	data.Integrations.Verification = i.Verification.Metas()
	if !i.Analytics.Active() {
		return
	}
	config, _ := json.Marshal(i.Analytics)
	data.Integrations.AnalyticsConfig = string(config)
	data.Integrations.ScriptURL = siteIntegrationScriptURL
	w.Header().Set("Content-Security-Policy", integrationCSP(w.Header().Get("Content-Security-Policy"), i.Analytics))
}

func integrationCSP(policy string, a site.AnalyticsSettings) string {
	sources := map[string][]string{}
	add := func(directive string, origins ...string) { sources[directive] = append(sources[directive], origins...) }
	if a.GoogleID != "" || a.GoogleTagID != "" {
		add("script-src", "https://www.googletagmanager.com")
		add("connect-src", "https://*.google-analytics.com", "https://*.analytics.google.com", "https://*.googletagmanager.com")
		add("img-src", "https://*.google-analytics.com", "https://*.googletagmanager.com")
		add("frame-src", "https://www.googletagmanager.com")
	}
	if a.BaiduID != "" {
		for _, directive := range []string{"script-src", "connect-src", "img-src"} {
			add(directive, "https://hm.baidu.com")
		}
	}
	if a.ClarityID != "" {
		add("script-src", "https://www.clarity.ms", "https://scripts.clarity.ms")
		add("connect-src", "https://*.clarity.ms", "https://c.bing.com")
		add("img-src", "https://*.clarity.ms", "https://c.bing.com")
	}
	if a.CloudflareToken != "" {
		add("script-src", "https://static.cloudflareinsights.com")
		add("connect-src", "https://cloudflareinsights.com")
	}
	for _, value := range []string{a.MatomoURL, a.UmamiURL, a.PlausibleURL} {
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		for _, directive := range []string{"script-src", "connect-src", "img-src"} {
			add(directive, origin)
		}
		// Umami Cloud serves its script and ingestion API on different origins.
		if u.Hostname() == "cloud.umami.is" {
			add("connect-src", "https://gateway.umami.is")
		}
	}
	for _, origin := range strings.Split(a.AllowedOrigins, "\n") {
		if origin == "" {
			continue
		}
		for _, directive := range []string{"script-src", "connect-src", "img-src", "frame-src"} {
			add(directive, origin)
		}
	}
	parts := strings.Split(policy, ";")
	for _, directive := range []string{"script-src", "connect-src", "img-src", "frame-src"} {
		origins := sources[directive]
		if len(origins) == 0 {
			continue
		}
		index := -1
		for n, part := range parts {
			if strings.HasPrefix(strings.TrimSpace(part), directive+" ") {
				index = n
				break
			}
		}
		if index == -1 {
			parts = append(parts, " "+directive+" 'self'")
			index = len(parts) - 1
		}
		seen := map[string]bool{}
		for _, source := range strings.Fields(parts[index]) {
			seen[source] = true
		}
		for _, origin := range origins {
			if !seen[origin] {
				parts[index] += " " + origin
				seen[origin] = true
			}
		}
	}
	return strings.Join(parts, ";")
}
