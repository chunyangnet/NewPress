package objectstorage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOSSBackendStreamingContract(t *testing.T) {
	exerciseSDKContract(t, newOSSBackend, "x-oss-", "OSS4-HMAC-SHA256")
}

func TestOSSErrorsAreSafe(t *testing.T) { exerciseSDKSafeErrors(t, newOSSBackend) }

func TestOSSRedirectsCannotForwardCredentials(t *testing.T) { exerciseSDKNoRedirect(t, newOSSBackend) }

func TestOSSValidatesBeforeSending(t *testing.T) { exerciseSDKValidation(t, newOSSBackend) }

func TestOSSPresignUsesNativeSignature(t *testing.T) {
	profile := sdkTestProfile("https://oss-cn-hangzhou.aliyuncs.com")
	profile.Provider, profile.Region, profile.PathStyle = "oss", "cn-hangzhou", false
	backend, err := newOSSBackend(profile)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := backend.SignedURL(context.Background(), "media/图片.png", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(signed)
	if u.Host != "media-test.oss-cn-hangzhou.aliyuncs.com" || u.Path != "/media/图片.png" {
		t.Fatalf("invalid OSS URL host/path: %s %s", u.Host, u.Path)
	}
	if u.Query().Get("x-oss-signature-version") != "OSS4-HMAC-SHA256" || !strings.Contains(u.Query().Get("x-oss-credential"), "/cn-hangzhou/oss/aliyun_v4_request") || u.Query().Get("x-oss-signature") == "" {
		t.Fatal("native OSS v4 credential scope or signature missing")
	}
	if u.Query().Get("x-oss-security-token") != profile.SessionToken {
		t.Error("OSS session token missing")
	}
}

func TestOSSRetriesRewindAndStop(t *testing.T) {
	for _, alwaysFail := range []bool{false, true} {
		t.Run(strconv.FormatBool(alwaysFail), func(t *testing.T) {
			var calls atomic.Int32
			payload := []byte("retryable native OSS upload")
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
			backend, err := newOSSBackend(sdkTestProfile(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			err = backend.Put(context.Background(), "media/test", bytes.NewReader(payload), ObjectInfo{Size: int64(len(payload))})
			if (err != nil) != alwaysFail || calls.Load() != 3 {
				t.Fatalf("attempts=%d error=%v", calls.Load(), err)
			}
			if err != nil && strings.Contains(err.Error(), "private details") {
				t.Fatal("error body leaked")
			}
		})
	}
}

func TestSDKZeroLengthUpload(t *testing.T) {
	for _, tc := range []struct {
		name        string
		constructor sdkBackendConstructor
	}{
		{"s3", newS3Backend}, {"oss", newOSSBackend},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				data, err := io.ReadAll(r.Body)
				if err != nil || len(data) != 0 || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
					t.Errorf("empty upload has unexpected framing: size=%d data=%d err=%v", r.ContentLength, len(data), err)
				}
			}))
			defer server.Close()
			backend, err := tc.constructor(sdkTestProfile(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.Put(context.Background(), "media/empty", bytes.NewReader(nil), ObjectInfo{}); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("expected one PUT, got %d", calls.Load())
			}
		})
	}
}
