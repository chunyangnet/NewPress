package httpserver

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/plugin"
)

func TestCoreCDNOptInAndHTTP(t *testing.T) {
	s, _, _, _ := newCDNHTTP(t)
	c := cdnHTTPSettings()
	c.AssetBase = "https://static.example.test"
	if _, err := s.cdn.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.SetEnabled(plugin.CDN, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { s.cdn.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if base, _ := s.cdn.Delivery(); base != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivery not ready")
		}
		time.Sleep(time.Millisecond * 10)
	}
	if base, _ := s.cdn.CoreDelivery(); base != "" {
		t.Fatal("core CDN must default off")
	}
	c.CoreAssets = true
	c.Version = 2
	if _, err := s.cdn.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	for _, view := range []pageView{{Kind: "home"}, {Kind: "post"}, {Kind: "account"}, {Kind: "billing"}, {IsAdmin: true}, {IsPreview: true}} {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:")
		s.prepareCoreDelivery(w, &view)
		expected := view.Kind == "home" || view.Kind == "post"
		if (view.CoreCDNAssetBase != "") != expected {
			t.Fatalf("unexpected delivery: %+v", view)
		}
		if strings.Contains(w.Header().Get("Content-Security-Policy"), c.AssetBase) != expected {
			t.Fatal("CSP isolation")
		}
	}
	asset := coreui.AssetURL("js/newpress-components.js")
	etag := ""
	for _, tc := range []struct {
		method, key, value string
		status             int
	}{{"GET", "", "", 200}, {"HEAD", "", "", 200}, {"GET", "Range", "bytes=0-9", 206}, {"GET", "If-None-Match", "", 304}} {
		r := httptest.NewRequest(tc.method, asset, nil)
		if tc.key != "" {
			value := tc.value
			if tc.key == "If-None-Match" {
				value = etag
			}
			r.Header.Set(tc.key, value)
		}
		w := httptest.NewRecorder()
		s.coreAssets(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.key, w.Code)
		}
		if etag == "" {
			etag = w.Header().Get("ETag")
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Cross-Origin-Resource-Policy") != "cross-origin" {
			t.Fatal("missing cross-origin headers")
		}
		if !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			t.Fatal("lost versioned caching")
		}
		if tc.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
		if tc.status == 206 && w.Body.Len() != 10 {
			t.Fatal("range body")
		}
	}
	if err := s.plugins.SetEnabled(plugin.CDN, false); err != nil {
		t.Fatal(err)
	}
	if base, _ := s.cdn.CoreDelivery(); base != "" {
		t.Fatal("disabled CDN active")
	}
}
