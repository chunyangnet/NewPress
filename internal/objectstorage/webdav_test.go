package objectstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"
)

func davTestMetadata(size string, directory bool) string {
	kind := ""
	if directory {
		kind = "<d:collection/>"
	}
	return `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/entry</d:href><d:propstat><d:prop><d:getcontentlength>` + size + `</d:getcontentlength><d:getcontenttype>application/octet-stream</d:getcontenttype><d:getetag>&quot;opaque-dav-etag&quot;</d:getetag><d:resourcetype>` + kind + `</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
}

func TestWebDAVRoundTripAndDirectoryReuse(t *testing.T) {
	var mu sync.Mutex
	files := map[string][]byte{}
	directories := map[string]bool{"/dav/root": true}
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		user, password, ok := r.BasicAuth()
		if !ok || user != "member@example.test" || password != "dav-only-secret" {
			t.Error("missing WebDAV Basic credentials")
			w.WriteHeader(401)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/dav/root/") {
			t.Errorf("escaped configured root: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		switch r.Method {
		case "MKCOL":
			created++
			if directories[r.URL.Path] {
				w.WriteHeader(405)
				return
			}
			if !directories[path.Dir(r.URL.Path)] {
				w.WriteHeader(409)
				return
			}
			directories[r.URL.Path] = true
			w.WriteHeader(201)
		case "PROPFIND":
			if r.Header.Get("Depth") != "0" {
				t.Error("metadata query must not recurse")
			}
			body, ok := files[r.URL.Path]
			if !ok && !directories[r.URL.Path] {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			_, _ = io.WriteString(w, davTestMetadata(fmt.Sprint(len(body)), directories[r.URL.Path]))
		case "PUT":
			if !directories[path.Dir(r.URL.Path)] {
				w.WriteHeader(409)
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			if r.ContentLength != int64(len(data)) || len(r.TransferEncoding) != 0 {
				t.Errorf("upload must use a fixed content length: %d", r.ContentLength)
			}
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Error("content type missing")
			}
			files[r.URL.Path] = data
			w.WriteHeader(201)
		case "GET":
			data, ok := files[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
		case "DELETE":
			if _, ok := files[r.URL.Path]; !ok {
				w.WriteHeader(404)
				return
			}
			delete(files, r.URL.Path)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected method %s; metadata must use PROPFIND", r.Method)
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	backend, err := newWebDAVBackend(Profile{Provider: "cloudreve", Endpoint: server.URL + "/dav/root/", Username: "member@example.test", Password: "dav-only-secret", AllowPrivate: true, AllowHTTP: true, TimeoutSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "newpress/中文 %+#?.bin"
	data := bytes.Repeat([]byte("storage-byte-stream"), 4096)
	info := ObjectInfo{Size: int64(len(data)), ContentType: "application/octet-stream"}
	if err := backend.Put(ctx, key, bytes.NewReader(data), info); err != nil {
		t.Fatal(err)
	}
	if err := backend.Put(ctx, key, bytes.NewReader(data), info); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if created != 1 {
		t.Errorf("directory cache should avoid repeat MKCOL, got %d", created)
	}
	mu.Unlock()
	head, err := backend.Head(ctx, key)
	if err != nil || head.Size != int64(len(data)) || head.SHA256 != "" || head.ETag != `"opaque-dav-etag"` {
		t.Fatalf("Head = %+v, %v", head, err)
	}
	reader, gotInfo, err := backend.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(got, data) || gotInfo.Size != int64(len(data)) {
		t.Fatal("download bytes or metadata did not match")
	}
	// Deleting a remote parent invalidates the backend's directory hint. The next
	// upload must rebuild the parent and retry the exact same immutable bytes.
	mu.Lock()
	delete(directories, "/dav/root/newpress")
	mu.Unlock()
	if err := backend.Put(ctx, key, bytes.NewReader(data), info); err != nil {
		t.Fatalf("cached parent recovery: %v", err)
	}
	if err := backend.Put(ctx, "newpress/empty", bytes.NewReader(nil), ObjectInfo{Size: 0, ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := backend.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backend.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	if _, err := backend.SignedURL(ctx, key, 0); !errors.Is(err, ErrUnsupported) {
		t.Fatal("WebDAV must not expose credential URLs")
	}
}

type davTestRoundTripper func(*http.Request) (*http.Response, error)

func (f davTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type davTestBody struct {
	io.Reader
	closed bool
}

func (r *davTestBody) Close() error { r.closed = true; return nil }

func davTestBackend(t *testing.T, transport davTestRoundTripper) *webDAVBackend {
	t.Helper()
	u, err := url.Parse("https://cloud.example.test/dav/root")
	if err != nil {
		t.Fatal(err)
	}
	return &webDAVBackend{profile: Profile{Endpoint: u.String(), Provider: "cloudreve", Username: "site@example.test", Password: "private-dav-password", AllowPrivate: true}, base: u, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, newClient: newStorageHTTPClient, directories: map[string]struct{}{}, redirectClients: map[string]*http.Client{}}
}

func TestWebDAVMetadataRejectsErrorsAndMalformedResponses(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		missing bool
	}{
		{"missing", 404, "secret provider details", true},
		{"forbidden", 403, "private-dav-password", false},
		{"unauthorized", 401, "site@example.test", false},
		{"not-xml", 207, "private-dav-password", false},
		{"negative-size", 207, davTestMetadata("-10", false), false},
		{"missing-size", 207, davTestMetadata("", false), false},
		{"directory", 207, davTestMetadata("", true), false},
		{"oversized", 207, strings.Repeat("x", webDAVMetadataLimit+1), false},
		{"status-in-xml", 207, `<d:multistatus xmlns:d="DAV:"><d:response><d:status>HTTP/1.1 404 Not Found</d:status></d:response></d:multistatus>`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &davTestBody{Reader: strings.NewReader(tc.body)}
			backend := davTestBackend(t, func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header), Request: r}, nil
			})
			_, err := backend.Head(context.Background(), "object")
			if err == nil || errors.Is(err, ErrNotFound) != tc.missing {
				t.Fatalf("Head error %v", err)
			}
			if strings.Contains(err.Error(), "private-dav-password") || strings.Contains(err.Error(), "site@example.test") {
				t.Fatal("provider response leaked")
			}
			if !body.closed {
				t.Fatal("response body not closed")
			}
		})
	}
}

func TestWebDAVRedirectStripsCredentialsAndPinsPublicTarget(t *testing.T) {
	redirectBody := &davTestBody{Reader: strings.NewReader("redirect response")}
	backend := davTestBackend(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "" {
			t.Error("initial request requires WebDAV credentials")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://assets.example.test/blob?signature=private-signature"}}, Body: redirectBody, Request: r}, nil
	})
	created := 0
	backend.newClient = func(p Profile) (*http.Client, error) {
		created++
		if p.Endpoint != "https://assets.example.test" || p.AllowPrivate || p.AllowHTTP || p.Username != "" || p.Password != "" {
			t.Errorf("unsafe redirect profile %+v", p)
		}
		return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: davTestRoundTripper(func(r *http.Request) (*http.Response, error) {
			if len(r.Header) != 0 || r.URL.Query().Get("signature") != "private-signature" {
				t.Error("redirect forwarded credentials or lost signing query")
			}
			return &http.Response{StatusCode: 200, ContentLength: 7, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("payload")), Request: r}, nil
		})}, nil
	}
	for i := 0; i < 2; i++ {
		reader, info, err := backend.Open(context.Background(), "object")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(reader)
		_ = reader.Close()
		if string(got) != "payload" || info.Size != 7 {
			t.Fatal("bad redirected download")
		}
	}
	if !redirectBody.closed || created != 1 {
		t.Fatalf("response close or transport reuse failed: closed=%v clients=%d", redirectBody.closed, created)
	}
}

func TestWebDAVRedirectPolicyAndLimit(t *testing.T) {
	for _, location := range []string{"http://cloud.example.test/downgrade", "http://elsewhere.example.test/blob", "https://name:secret@assets.example.test/blob", "https://assets.example.test/blob#fragment", "file:///etc/passwd", ":bad-url"} {
		t.Run(location, func(t *testing.T) {
			closed := &davTestBody{Reader: strings.NewReader("")}
			backend := davTestBackend(t, func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: closed, Request: r}, nil
			})
			if _, _, err := backend.Open(context.Background(), "object"); err == nil {
				t.Fatal("unsafe redirect accepted")
			}
			if !closed.closed {
				t.Fatal("redirect body leaked")
			}
		})
	}
	calls := 0
	backend := davTestBackend(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 && r.Header.Get("Authorization") != "" {
			t.Error("same-origin redirect retained credentials")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"/dav/root/loop"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, _, err := backend.Open(context.Background(), "object"); err == nil || calls != 6 {
		t.Fatalf("redirect loop: calls=%d err=%v", calls, err)
	}
}

func TestWebDAVRejectsTraversalAndEncodesSegments(t *testing.T) {
	backend := davTestBackend(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid key reached network"); return nil, nil })
	for _, key := range []string{"", "/object", "a//b", "a/../b", "a/./b", "a\\b", "a\x00b", "a/"} {
		if _, err := backend.objectURL(key); err == nil {
			t.Errorf("accepted key %q", key)
		}
	}
	u, err := backend.objectURL("中文/100% +?#.png")
	if err != nil {
		t.Fatal(err)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.EscapedPath() != "/dav/root/%E4%B8%AD%E6%96%87/100%25%20%2B%3F%23.png" {
		t.Fatalf("escaped URL %s", u.String())
	}
}

func TestWebDAVExistingCollectionMustBeDirectory(t *testing.T) {
	backend := davTestBackend(t, func(r *http.Request) (*http.Response, error) {
		status, body := 405, ""
		if r.Method == "PROPFIND" {
			status, body = 207, davTestMetadata("15", false)
		}
		if r.Method == "PUT" {
			t.Error("PUT must not run through a file parent")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if err := backend.Put(context.Background(), "occupied/file", strings.NewReader("data"), ObjectInfo{Size: 4}); err == nil {
		t.Fatal("non-directory parent accepted")
	}
}

func TestWebDAVRejectsCrossOriginPrivateRedirect(t *testing.T) {
	backend := davTestBackend(t, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://127.0.0.1:8443/private"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, _, err := backend.Open(context.Background(), "object"); err == nil {
		t.Fatal("private access permission escaped original endpoint")
	}
}

func TestWebDAVRequestCancellationAndSafeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := davTestBackend(t, func(r *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})
	if _, _, err := backend.Open(ctx, "object"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context lost: %v", err)
	}
	backend.client.Transport = davTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("provider leaked private-dav-password and signature=secret")
	})
	if _, _, err := backend.Open(context.Background(), "object"); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") {
		t.Fatalf("network errors must be sanitized: %v", err)
	}
}
