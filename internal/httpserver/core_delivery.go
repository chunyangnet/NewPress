package httpserver

import (
	"fmt"
	"net/http"
	"newpress.local/newpress/internal/coreui"
	"strings"
)

// publicCoreAsset only prefixes trusted, versioned asset URLs, never content/API URLs.
func publicCoreAsset(name string, bases ...string) string {
	base := ""
	if len(bases) > 0 {
		base = bases[0]
	}
	return base + coreui.AssetURL(name)
}

func (s *Server) prepareCoreDelivery(w http.ResponseWriter, data *pageView) {
	data.CoreCDNAssetBase = ""
	if data.IsAdmin || data.IsPreview {
		return
	}
	switch data.Kind {
	case "account", "account-access", "billing", "shop", "notification-unsubscribe", "maintenance":
		return
	}
	base, _ := s.cdn.CoreDelivery()
	if base == "" {
		return
	}
	data.CoreCDNAssetBase = base
	if data.ThemeCSSURL == coreui.AssetURL("css/core-defaults.css") {
		data.ThemeCSSURL = base + data.ThemeCSSURL
	}
	parts := strings.Split(w.Header().Get("Content-Security-Policy"), ";")
	for _, directive := range []string{"script-src", "style-src", "font-src", "img-src"} {
		found := false
		for i, part := range parts {
			fields := strings.Fields(part)
			if len(fields) > 0 && fields[0] == directive {
				if !strings.Contains(" "+strings.Join(fields[1:], " ")+" ", " "+base+" ") {
					parts[i] = strings.TrimSpace(part) + " " + base
				}
				found = true
			}
		}
		if !found {
			parts = append(parts, directive+" 'self' "+base)
		}
	}
	w.Header().Set("Content-Security-Policy", strings.Join(parts, ";"))
}

type coreDeliveryWriter struct {
	http.ResponseWriter
	ttl int
}

func (w coreDeliveryWriter) WriteHeader(status int) {
	if status == 200 || status == 206 || status == 304 {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
		if strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", w.ttl))
		}
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w coreDeliveryWriter) Write(b []byte) (int, error) { return w.ResponseWriter.Write(b) }
func (s *Server) coreAssets(w http.ResponseWriter, r *http.Request) {
	if base, ttl := s.cdn.CoreDelivery(); base != "" {
		coreui.Assets(coreDeliveryWriter{w, ttl}, r)
		return
	}
	coreui.Assets(w, r)
}
