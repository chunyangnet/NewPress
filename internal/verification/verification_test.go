package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testService(t *testing.T, id string) *Service {
	t.Helper()
	store, err := NewStore("")
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Snapshot()
	settings.Enabled = true
	settings.DefaultProvider = id
	if id != "local" {
		settings.Providers = []Provider{{ID: id, SiteKey: "site-key", Secret: "private-secret", SecretID: "cloud-id", APISecret: "cloud-secret", MinScore: 0.5}}
		if id == "tencent" {
			settings.Providers[0].SiteKey = "123456789"
		}
	}
	if _, err = store.Save(settings); err != nil {
		t.Fatal(err)
	}
	return NewService(store)
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mock(s *Service, body string, check func(*http.Request)) {
	s.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if check != nil {
			check(r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func TestLocalChallengeAndSingleUseReceipt(t *testing.T) {
	s := testService(t, "local")
	c := s.Config("comment", false, "")
	ch, err := s.Challenge(c, "source")
	if err != nil {
		t.Fatal(err)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ch.Image, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 190 {
		t.Fatal("invalid image", err)
	}
	entry := s.challenges[hash(ch.ID)]
	entry.answer = sha256.Sum256([]byte("12345"))
	s.challenges[hash(ch.ID)] = entry
	proof := Proof{ChallengeID: ch.ID, Answer: "12345"}
	if _, err = s.Verify(t.Context(), c, "another-source", "", "", proof); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-source challenge accepted")
	}
	wrongScene := c
	wrongScene.Scene = "register"
	if _, err = s.Verify(t.Context(), wrongScene, "source", "", "", proof); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-scene challenge accepted")
	}
	token, err := s.Verify(t.Context(), c, "source", "", "", proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Verify(t.Context(), c, "source", "", "", proof); !errors.Is(err, ErrInvalid) {
		t.Fatal("challenge reused")
	}
	if err = s.Consume(wrongScene, "source", token); !errors.Is(err, ErrInvalid) {
		t.Fatal("receipt changed scenes")
	}
	if err = s.Consume(c, "other", token); !errors.Is(err, ErrInvalid) {
		t.Fatal("receipt changed source")
	}
	var passed atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			if s.Consume(c, "source", token) == nil {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	if passed.Load() != 1 {
		t.Fatal("receipt is not single use", passed.Load())
	}
}

func TestLocalAttemptExpiryAndPolicyChange(t *testing.T) {
	s := testService(t, "local")
	c := s.Config("comment", false, "")
	ch, _ := s.Challenge(c, "source")
	for range 5 {
		if _, err := s.Verify(t.Context(), c, "source", "", "", Proof{ChallengeID: ch.ID, Answer: "not-a-number"}); err == nil {
			t.Fatal("wrong answer accepted")
		}
	}
	if _, ok := s.challenges[hash(ch.ID)]; ok {
		t.Fatal("attempt limit not enforced")
	}
	ch, _ = s.Challenge(c, "source")
	e := s.challenges[hash(ch.ID)]
	e.expires = time.Now().Add(-time.Second)
	s.challenges[hash(ch.ID)] = e
	if _, err := s.Verify(t.Context(), c, "source", "", "", Proof{ChallengeID: ch.ID}); err == nil {
		t.Fatal("expired challenge accepted")
	}
	settings := s.Store.Snapshot()
	settings.Scenes["comment"] = Rule{Enabled: true, SkipMembers: true}
	if _, err := s.Store.Save(settings); err != nil {
		t.Fatal(err)
	}
	if s.Config("comment", true, "").Enabled || !s.Config("comment", false, "").Enabled {
		t.Fatal("member exemption incorrect")
	}
	if _, err := s.Verify(t.Context(), c, "source", "", "", Proof{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("stale policy accepted")
	}
	s.receipts[hash("expired")] = ticket{scene: "comment", source: "source", revision: c.Revision, expires: time.Now().Add(-time.Second)}
	if s.Consume(c, "source", "expired") == nil {
		t.Fatal("expired receipt accepted")
	}
}

func TestProviderProtocolsAndReplay(t *testing.T) {
	for _, id := range []string{"turnstile", "hcaptcha", "recaptcha_v2", "recaptcha_v3", "geetest", "tencent"} {
		t.Run(id, func(t *testing.T) {
			s := testService(t, id)
			c := s.Config("comment", false, "")
			proof := Proof{Token: "one-use-token", LotNumber: "lot-number", CaptchaOutput: "output", PassToken: "pass", GenTime: "1700000000", Randstr: "random"}
			body := `{"success":true,"hostname":"example.test","action":"comment","score":0.9}`
			if id == "geetest" {
				body = `{"result":"success"}`
			}
			if id == "tencent" {
				body = `{"Response":{"CaptchaCode":1}}`
			}
			calls := 0
			mock(s, body, func(r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Scheme != "https" {
					t.Error("unsafe provider request")
				}
				if id == "tencent" {
					if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=cloud-id/") || r.Header.Get("X-TC-Action") != "DescribeCaptchaResult" {
						t.Error("missing API signature")
					}
					var payload map[string]any
					if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["AppSecretKey"] != "private-secret" || payload["CaptchaType"] != float64(9) {
						t.Error("wrong tencent payload")
					}
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if id == "geetest" {
					if r.PostForm.Get("sign_token") != "5a7a0611591844b8d221e746e91283e1287c7e1827299de6fd44fb997c0e59c3" {
						t.Error("bad geetest HMAC")
					}
					if r.URL.Query().Get("captcha_id") != "site-key" {
						t.Error("captcha id missing")
					}
				} else {
					if r.PostForm.Get("secret") != "private-secret" || r.PostForm.Get("response") != "one-use-token" {
						t.Error("missing secret or token")
					}
					if id == "hcaptcha" && r.PostForm.Get("sitekey") != "site-key" {
						t.Error("hcaptcha not bound to sitekey")
					}
				}
			})
			token, err := s.Verify(t.Context(), c, "source", "192.0.2.1", "example.test", proof)
			if err != nil {
				t.Fatal(err)
			}
			if s.Consume(c, "source", token) != nil {
				t.Fatal("receipt invalid")
			}
			if _, err = s.Verify(t.Context(), c, "different-source", "192.0.2.2", "example.test", proof); !errors.Is(err, ErrInvalid) || calls != 1 {
				t.Fatal("upstream proof replayed", calls, err)
			}
		})
	}
}

func TestProviderRejectsInvalidClaimsAndNetworkFailures(t *testing.T) {
	for _, tc := range []struct{ id, body string }{
		{"turnstile", `{"success":true,"hostname":"other.test","action":"comment"}`},
		{"turnstile", `{"success":true,"hostname":"example.test","action":"login"}`},
		{"turnstile", `{"success":false,"hostname":"example.test","action":"comment"}`},
		{"recaptcha_v3", `{"success":true,"hostname":"example.test","action":"comment","score":0.1}`},
		{"recaptcha_v3", `{"success":true,"hostname":"example.test","action":"comment"}`},
		{"geetest", `{"result":"fail"}`}, {"tencent", `{"Response":{"CaptchaCode":0}}`},
		{"hcaptcha", `not json`},
	} {
		t.Run(tc.id+tc.body, func(t *testing.T) {
			s := testService(t, tc.id)
			mock(s, tc.body, nil)
			_, err := s.Verify(t.Context(), s.Config("comment", false, ""), "source", "192.0.2.1", "example.test", Proof{Token: "token", LotNumber: "lot", CaptchaOutput: "output", PassToken: "pass", GenTime: "1", Randstr: "random"})
			if err == nil || len(s.receipts) != 0 {
				t.Fatal("invalid proof minted receipt")
			}
		})
	}
	s := testService(t, "turnstile")
	s.HTTP = &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	if _, err := s.Verify(t.Context(), s.Config("comment", false, ""), "source", "", "example.test", Proof{Token: "token"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("network error allowed through", err)
	}
}

func TestSettingsPersistenceRedactionAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification-settings.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Snapshot()
	settings.Enabled = true
	settings.DefaultProvider = "turnstile"
	if _, err = store.Save(settings); err == nil {
		t.Fatal("enabled without keys")
	}
	settings.Providers = []Provider{{ID: "turnstile", SiteKey: "public-key", Secret: "private-secret", MinScore: 0.5}}
	saved, err := store.Save(settings)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(saved)
	if strings.Contains(string(b), "private-secret") || !saved.Providers[0].HasSecret {
		t.Fatal("secret exposed or status lost")
	}
	if _, err = store.Save(settings); !errors.Is(err, ErrConflict) {
		t.Fatal("stale save accepted")
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err = reopened.Save(saved)
	if err != nil {
		t.Fatal(err)
	}
	if providerFor(reopened.runtime(), "turnstile").Secret != "private-secret" {
		t.Fatal("blank edit removed key")
	}
	saved.Providers[0].SiteKey = "different-key"
	if _, err = reopened.Save(saved); err == nil {
		t.Fatal("changed sitekey silently reused secret")
	}
	saved = reopened.Snapshot()
	saved.Enabled = false
	saved.Providers[0].ClearSecret = true
	saved, err = reopened.Save(saved)
	if err != nil || saved.Providers[0].HasSecret {
		t.Fatal("secret could not be cleared", err)
	}
	saved.Scenes["login"] = Rule{Enabled: true, SkipMembers: true}
	if _, err = reopened.Save(saved); err == nil {
		t.Fatal("login accepted member exemption")
	}
}
