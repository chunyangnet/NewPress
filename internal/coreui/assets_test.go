package coreui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCoreAssetRepresentations(t *testing.T) {
	target := AssetURL("js/newpress-client.js")
	full := httptest.NewRecorder()
	Assets(full, httptest.NewRequest(http.MethodGet, target, nil))
	for _, tc := range []struct {
		name, method, header, value string
		status                      int
	}{
		{"head", "HEAD", "", "", 200},
		{"range", "GET", "Range", "bytes=0-15", 206},
		{"weak conditional", "GET", "If-None-Match", "W/" + full.Header().Get("ETag"), 304},
		{"failed precondition", "GET", "If-Match", `"stale"`, 412},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, target, nil)
			if tc.header != "" {
				request.Header.Set(tc.header, tc.value)
			}
			response := httptest.NewRecorder()
			Assets(response, request)
			if response.Code != tc.status {
				t.Fatalf("status = %d", response.Code)
			}
			if tc.name == "range" && !bytes.Equal(response.Body.Bytes(), full.Body.Bytes()[:16]) {
				t.Fatal("wrong range bytes")
			}
			if (tc.method == "HEAD" || tc.status == 304) && response.Body.Len() != 0 {
				t.Fatal("unexpected response body")
			}
		})
	}
	for _, name := range []string{"js/chunnel-admin.source.js", "css/chunnel-admin.source.css", "css/admin/shell.css", "css"} {
		response := httptest.NewRecorder()
		Assets(response, httptest.NewRequest("GET", AssetURL(name), nil))
		if response.Code != 404 {
			t.Errorf("build-only asset %s is public: %d", name, response.Code)
		}
	}
}

func BenchmarkCoreAssetConditional(b *testing.B) {
	request := httptest.NewRequest("GET", AssetURL("css/chunnel-admin.css"), nil)
	first := httptest.NewRecorder()
	Assets(first, request)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		response := httptest.NewRecorder()
		Assets(response, request)
		if response.Code != 304 {
			b.Fatal(response.Code)
		}
	}
}
