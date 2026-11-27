package commentorigin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderAdaptersPrecisionAndErrors(t *testing.T) {
	cases := []struct {
		provider, body, city, precision string
		fail                            bool
	}{
		{"ipwhois", `{"ip":"8.8.8.8","success":true,"country":"China","region":"Zhejiang","city":"Hangzhou"}`, "Hangzhou", "city", false},
		{"ipwhois", `{"ip":"1.1.1.1","success":true,"city":"wrong source"}`, "", "", true},
		{"ipwhois", `{"success":false,"message":"secret-reflected"}`, "", "", true},
		{"amap", `{"status":"1","province":"浙江省","city":"杭州市","infocode":"10000"}`, "杭州市", "city", false},
		{"amap", `{"status":"1","province":"浙江省","city":[]}`, "", "region", false},
		{"amap", `{"status":"1","province":[],"city":[]}`, "", "", true},
		{"baidu", `{"status":0,"content":{"address_detail":{"province":"北京市","city":"北京市"}}}`, "北京市", "city", false},
		{"baidu", `{"status":101,"message":"key echoed here"}`, "", "", true},
		{"custom", `{"ok":true,"data":{"region":"浙江","city":"杭州"}}`, "杭州", "city", false},
	}
	for _, c := range cases {
		t.Run(c.provider+c.city+c.precision, func(t *testing.T) {
			s := Defaults()
			s.Provider = c.provider
			s.Key = "sentinel-secret"
			s.Endpoint = "https://geo.example.test/lookup?ip={ip}&key={key}"
			s.SuccessPath = "ok"
			s.SuccessValue = "true"
			s.RegionPath = "data.region"
			s.CityPath = "data.city"
			client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" {
					t.Fatal("plaintext")
				}
				if c.provider == "amap" && r.URL.Query().Get("key") != s.Key {
					t.Fatal("missing key")
				}
				if c.provider == "baidu" && r.URL.Query().Get("ak") != s.Key {
					t.Fatal("missing AK")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(c.body)), Header: make(http.Header)}, nil
			})}
			l, err := lookup(context.Background(), client, s, "8.8.8.8")
			if (err != nil) != c.fail || l.City != c.city || l.Precision != c.precision {
				t.Fatalf("result=%+v err=%v", l, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("error echoed remote secret")
			}
		})
	}
}

func TestProviderTransportAndOversizeNeverExposeURLOrKey(t *testing.T) {
	s := Defaults()
	s.Provider = "amap"
	s.Key = "never-echo-this"
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })}
	_, err := lookup(t.Context(), client, s, "8.8.8.8")
	if err == nil || strings.Contains(err.Error(), s.Key) {
		t.Fatal(err)
	}
	if _, err = lookup(t.Context(), client, s, "2606:4700:4700::1111"); err == nil || !strings.Contains(err.Error(), "IPv6") {
		t.Fatal(err)
	}
	client.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat(" ", 65537)))}, nil
	})
	if _, err = lookup(t.Context(), client, s, "8.8.8.8"); err == nil {
		t.Fatal("accepted huge response")
	}
}

func TestProviderCountryCodes(t *testing.T) {
	for _, tc := range []struct{ provider, body, code string }{
		{"ipwhois", `{"success":true,"country":"Japan","country_code":"jp","city":"Tokyo"}`, "JP"},
		{"ipwhois", `{"success":true,"country":"Japan","city":"Tokyo"}`, ""},
		{"ipwhois", `{"success":true,"country_code":"../../us","city":"Tokyo"}`, ""},
		{"amap", `{"status":"1","province":"浙江省","city":"杭州市"}`, "CN"},
		{"baidu", `{"status":0,"address":"CN|浙江省|杭州市|None|None|0|0|0","content":{"address_detail":{"city":"杭州"}}}`, "CN"},
		{"baidu", `{"status":0,"content":{"address_detail":{"city":"杭州"}}}`, ""},
		{"custom", `{"success":true,"geo":{"iso":"US"},"city":"Seattle"}`, "US"},
		{"custom", `{"success":true,"geo":{"iso":"ZZ"},"city":"Seattle"}`, ""},
	} {
		s := Defaults()
		s.Provider, s.Key, s.Endpoint, s.CountryCodePath = tc.provider, "test-key", "https://geo.example.test/?ip={ip}", "geo.iso"
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if tc.provider == "ipwhois" && !strings.Contains(r.URL.Query().Get("fields"), "country_code") {
				t.Fatal("country_code not requested")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}
		l, err := lookup(t.Context(), client, s, "8.8.8.8")
		if err != nil || l.CountryCode != tc.code {
			t.Errorf("%s: %+v, %v; want code %q", tc.provider, l, err, tc.code)
		}
	}
}
