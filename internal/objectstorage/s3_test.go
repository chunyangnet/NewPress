package objectstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type sdkBackendConstructor func(Profile) (Backend, error)

func sdkTestProfile(endpoint string) Profile {
	return Profile{ID: "storage", Name: "Test storage", Provider: "s3-compatible", Endpoint: endpoint,
		Region: "us-east-1", Bucket: "media-test", PathStyle: true,
		AccessKey: "test-access", SecretKey: "test-secret", SessionToken: "test-token",
		AllowHTTP: true, AllowPrivate: true, TimeoutSeconds: 10}
}

func exerciseSDKContract(t *testing.T, constructor sdkBackendConstructor, headerPrefix, authScheme string) {
	t.Helper()
	data := []byte("small streaming media payload")
	checksum := fmt.Sprintf("%x", sha256.Sum256(data))
	key := "media/中文/100% +?#.png"
	var stored []byte
	var mu sync.Mutex
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/media-test/"+key {
			t.Errorf("object path changed: %q", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), authScheme) || !strings.Contains(r.Header.Get("Authorization"), "test-access") {
			t.Error("request is not signed by the explicit profile credentials")
		}
		if r.Header.Get(headerPrefix+"security-token") != "test-token" {
			t.Error("temporary credential token is missing")
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if r.ContentLength != int64(len(data)) || len(r.TransferEncoding) != 0 || strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
				t.Errorf("upload did not retain a known content length: %d, %v", r.ContentLength, r.TransferEncoding)
			}
			if r.Header.Get(headerPrefix+"meta-"+storageChecksumMetadata) != checksum {
				t.Error("SHA-256 metadata missing")
			}
			if r.Header.Get("Content-Type") != "image/png" {
				t.Error("content type missing")
			}
			var err error
			stored, err = io.ReadAll(io.LimitReader(r.Body, 1024))
			if err != nil || !bytes.Equal(stored, data) {
				t.Errorf("upload body mismatch: %v", err)
			}
			w.Header().Set("ETag", `"opaque-etag-not-md5"`)
			if headerPrefix == "x-oss-" {
				w.Header().Set("x-oss-hash-crc64ecma", strconv.FormatUint(crc64.Checksum(stored, crc64.MakeTable(crc64.ECMA)), 10))
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodHead, http.MethodGet:
			if stored == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(stored)))
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set(headerPrefix+"meta-"+storageChecksumMetadata, checksum)
			w.Header().Set("ETag", `"opaque-etag-not-md5"`)
			if r.Method == http.MethodGet {
				_, _ = w.Write(stored)
			}
		case http.MethodDelete:
			stored = nil
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected operation: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	backend, err := constructor(sdkTestProfile(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := backend.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing HEAD: %v", err)
	}
	body := bytes.NewReader(data)
	_, _ = body.Seek(5, io.SeekStart)
	info := ObjectInfo{Size: int64(len(data)), ContentType: "image/png", SHA256: checksum}
	if err := backend.Put(ctx, key, body, info); err != nil {
		t.Fatalf("PUT: %v", err)
	}
	head, err := backend.Head(ctx, key)
	if err != nil || head.Size != info.Size || head.SHA256 != checksum || head.ContentType != info.ContentType || head.ETag != `"opaque-etag-not-md5"` {
		t.Fatalf("HEAD metadata: %#v, %v", head, err)
	}
	stream, opened, err := backend.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	received, err := io.ReadAll(stream)
	closeErr := stream.Close()
	if err != nil || closeErr != nil || !bytes.Equal(received, data) || opened.Size != info.Size || opened.SHA256 != checksum {
		t.Fatalf("GET: data=%q info=%#v read=%v close=%v", received, opened, err, closeErr)
	}
	beforeSign := requests.Load()
	signed, err := backend.SignedURL(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil || parsed.Path != "/media-test/"+key || requests.Load() != beforeSign {
		t.Fatal("signing modified the object path or issued a network request")
	}
	if parsed.Query().Get("X-Amz-Signature") == "" && parsed.Query().Get("x-oss-signature") == "" {
		t.Fatal("signed URL has no signature")
	}
	if err := backend.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := backend.Open(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GET after delete: %v", err)
	}
	if err := backend.Delete(ctx, key); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestS3BackendStreamingContract(t *testing.T) {
	exerciseSDKContract(t, newS3Backend, "x-amz-", "AWS4-HMAC-SHA256")
}

func TestS3PresignProviderAddressing(t *testing.T) {
	for _, tc := range []struct {
		provider, region, endpoint, bucket, host string
		pathStyle                                bool
	}{
		{"s3", "us-east-1", "https://s3.us-east-1.amazonaws.com", "my-media", "my-media.s3.us-east-1.amazonaws.com", false},
		{"r2", "auto", "https://account.r2.cloudflarestorage.com", "my-media", "account.r2.cloudflarestorage.com", true},
		{"cos", "ap-guangzhou", "https://cos.ap-guangzhou.myqcloud.com", "my-media-1250000000", "my-media-1250000000.cos.ap-guangzhou.myqcloud.com", false},
		{"qiniu", "cn-east-1", "https://s3.cn-east-1.qiniucs.com", "my-media", "my-media.s3.cn-east-1.qiniucs.com", false},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			profile := sdkTestProfile(tc.endpoint)
			profile.Provider, profile.Region, profile.Bucket, profile.PathStyle = tc.provider, tc.region, tc.bucket, tc.pathStyle
			backend, err := newS3Backend(profile)
			if err != nil {
				t.Fatal(err)
			}
			signed, err := backend.SignedURL(context.Background(), "media/photo.png", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(signed)
			if u.Host != tc.host || !strings.Contains(u.Query().Get("X-Amz-Credential"), "/"+tc.region+"/s3/aws4_request") {
				t.Fatalf("incorrect provider host/credential scope: host=%s", u.Host)
			}
			if u.Query().Get("X-Amz-Security-Token") != profile.SessionToken {
				t.Error("presign session token missing")
			}
		})
	}
}

func exerciseSDKSafeErrors(t *testing.T, constructor sdkBackendConstructor) {
	t.Helper()
	const sensitive = "sensitive-secret-token-and-url"
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "<Error><Code>"+sensitive+"</Code><Message>"+sensitive+"</Message></Error>")
			}))
			defer server.Close()
			backend, err := constructor(sdkTestProfile(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = backend.Open(context.Background(), "media/missing.png")
			if err == nil || strings.Contains(err.Error(), sensitive) || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("unsafe error: %v", err)
			}
			if errors.Is(err, ErrNotFound) != (status == http.StatusNotFound) {
				t.Fatalf("permission error confused with not found: %v", err)
			}
			if status != http.StatusNotFound && !strings.Contains(err.Error(), strconv.Itoa(status)) {
				t.Fatalf("HTTP status missing: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("permanent error retried %d times", calls.Load())
			}
		})
	}
}

func TestS3ErrorsAreSafe(t *testing.T) { exerciseSDKSafeErrors(t, newS3Backend) }

func TestS3RetriesRewindAndStop(t *testing.T) {
	for _, alwaysFail := range []bool{false, true} {
		t.Run(strconv.FormatBool(alwaysFail), func(t *testing.T) {
			var calls atomic.Int32
			payload := []byte("replay exactly the same bytes")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := calls.Add(1)
				data, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
				if !bytes.Equal(data, payload) {
					t.Errorf("retry changed bytes: %q", data)
				}
				if alwaysFail || attempt < 3 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, "<Error><Code>ServiceUnavailable</Code><Message>private details</Message></Error>")
				}
			}))
			defer server.Close()
			backend, err := newS3Backend(sdkTestProfile(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			sdk := backend.(*s3Backend)
			options := sdk.client.Options()
			if options.RetryMaxAttempts != 3 {
				t.Fatalf("unbounded retry policy: %d", options.RetryMaxAttempts)
			}
			options.Retryer = retry.AddWithMaxBackoffDelay(options.Retryer, time.Millisecond)
			sdk.client = s3.New(options)
			err = sdk.Put(context.Background(), "media/test", bytes.NewReader(payload), ObjectInfo{Size: int64(len(payload))})
			if (err != nil) != alwaysFail || calls.Load() != 3 {
				t.Fatalf("attempts=%d error=%v", calls.Load(), err)
			}
			if err != nil && strings.Contains(err.Error(), "private details") {
				t.Fatal("error body leaked")
			}
		})
	}
}

func exerciseSDKNoRedirect(t *testing.T, constructor sdkBackendConstructor) {
	t.Helper()
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	backend, err := constructor(sdkTestProfile(source.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = backend.Open(context.Background(), "media/test")
	if err == nil || leaked.Load() != 0 {
		t.Fatalf("redirect forwarded credentials: calls=%d err=%v", leaked.Load(), err)
	}
}

func TestS3RedirectsCannotForwardCredentials(t *testing.T) { exerciseSDKNoRedirect(t, newS3Backend) }

func exerciseSDKValidation(t *testing.T, constructor sdkBackendConstructor) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	backend, err := constructor(sdkTestProfile(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "/root", "../root", "media/../root", "media//root", "media\\root", "media/\x01root"} {
		if _, err := backend.Head(context.Background(), key); err == nil {
			t.Errorf("accepted invalid key %q", key)
		}
		if _, err := backend.SignedURL(context.Background(), key, time.Minute); err == nil {
			t.Errorf("signed invalid key %q", key)
		}
	}
	if err := backend.Put(context.Background(), "media/test", bytes.NewReader([]byte("abc")), ObjectInfo{Size: 2}); err == nil {
		t.Error("accepted wrong content length")
	}
	if err := backend.Put(context.Background(), "media/test", bytes.NewReader([]byte("abc")), ObjectInfo{Size: 3, SHA256: "invalid"}); err == nil {
		t.Error("accepted malformed checksum")
	}
	for _, ttl := range []time.Duration{0, -time.Second, 8 * 24 * time.Hour} {
		if _, err := backend.SignedURL(context.Background(), "media/test", ttl); err == nil {
			t.Errorf("accepted invalid expiration %v", ttl)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backend.Head(ctx, "media/test"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation lost: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid/canceled requests reached server: %d", calls.Load())
	}
}

func TestS3ValidatesBeforeSending(t *testing.T) { exerciseSDKValidation(t, newS3Backend) }
