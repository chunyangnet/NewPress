package commentorigin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func originFixture(t *testing.T) (*Service, *plugin.Manager, string) {
	t.Helper()
	root := t.TempDir()
	m := plugin.NewManager(root)
	if err := m.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(plugin.CommentOrigin, true); err != nil {
		t.Fatal(err)
	}
	return New(m), m, root
}

func TestCaptureLookupPrivacyCacheAndRoleChanges(t *testing.T) {
	s, m, root := originFixture(t)
	var calls atomic.Int32
	s.lookup = func(context.Context, Settings, string) (Location, error) {
		calls.Add(1)
		return Location{Country: "中国", Region: "浙江", City: "杭州", Precision: "city", Provider: "fixture"}, nil
	}
	if err := s.Capture(1, "8.8.8.8", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0 Safari/537.36 Edg/131.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(1, "1.1.1.1", "different"); err != nil {
		t.Fatal(err)
	}
	s.process(t.Context(), <-s.queue)
	r, err := s.PrivateRecord(1)
	if err != nil || r.IP != "8.8.8.8" || r.Status != "resolved" {
		t.Fatalf("record=%+v err=%v", r, err)
	}
	if b := s.Badges(1, []string{"subscriber"}); b.Location != "浙江 · 杭州" || b.Device != "Windows · Edge" {
		t.Fatalf("badges=%+v", b)
	}
	for _, roles := range [][]string{{"administrator"}, {"editor", "administrator"}, {"new-group"}} {
		if b := s.Badges(1, roles); b.Location != "" || b.Device != "" {
			t.Fatalf("private role exposed: %+v", b)
		}
	}
	if err = s.Capture(2, "8.8.8.8", "iPhone Safari/604.1 Version/17"); err != nil {
		t.Fatal(err)
	}
	s.process(t.Context(), <-s.queue)
	if calls.Load() != 1 {
		t.Fatalf("cache missed: %d", calls.Load())
	}
	if err = m.SetEnabled(plugin.CommentOrigin, false); err != nil {
		t.Fatal(err)
	}
	if b := s.Badges(1, nil); b.Location != "" || b.Device != "" {
		t.Fatalf("disabled plugin displayed: %+v", b)
	}
	if err = s.Capture(3, "8.8.8.8", "private"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, plugin.CommentOrigin, "data", "records", "3.json")); !os.IsNotExist(err) {
		t.Fatal("captured while disabled")
	}
}

func TestUninstallWhileLookupCannotRecreateOrContaminateReinstall(t *testing.T) {
	s, m, root := originFixture(t)
	started, finish, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.lookup = func(context.Context, Settings, string) (Location, error) {
		close(started)
		<-finish
		return Location{City: "旧结果", Precision: "city"}, nil
	}
	if err := s.Capture(1, "8.8.8.8", "Windows"); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(done); s.process(context.Background(), <-s.queue) }()
	<-started
	if err := m.Uninstall(plugin.CommentOrigin); err != nil {
		t.Fatal(err)
	}
	if err := m.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(plugin.CommentOrigin, true); err != nil {
		t.Fatal(err)
	}
	close(finish)
	<-done
	if _, err := os.Stat(filepath.Join(root, plugin.CommentOrigin, "data", "records", "1.json")); !os.IsNotExist(err) {
		t.Fatal("old worker recreated data in new install")
	}
	if err := m.Uninstall(plugin.CommentOrigin); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(); err != nil {
		t.Fatal(err)
	}
	items, _ := m.List()
	if len(items) != 0 {
		t.Fatal("uninstalled plugin returned")
	}
}

func TestDisableCancelsInFlightProviderRequest(t *testing.T) {
	s, m, _ := originFixture(t)
	started, done := make(chan struct{}), make(chan struct{})
	var cancelled atomic.Bool
	s.lookup = func(ctx context.Context, _ Settings, _ string) (Location, error) {
		close(started)
		<-ctx.Done()
		cancelled.Store(true)
		return Location{}, ctx.Err()
	}
	if err := s.Capture(1, "8.8.8.8", "Windows"); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(done); s.process(t.Context(), <-s.queue) }()
	<-started
	if err := m.SetEnabled(plugin.CommentOrigin, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disable did not cancel request")
	}
	if !cancelled.Load() {
		t.Fatal("provider context remained active")
	}
}

func TestPersistenceRetentionAndConfigurationConflict(t *testing.T) {
	s, m, root := originFixture(t)
	settings, _ := s.Settings()
	settings.Provider = "baidu"
	settings.Key = "test-private-key"
	saved, err := s.Save(settings, false)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Key != "" {
		t.Fatal("save returned secret")
	}
	if _, err = s.Save(settings, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version accepted: %v", err)
	}
	if err = s.Capture(1, "8.8.8.8", "Android Mobile"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, plugin.CommentOrigin, "data", "records", "1.json")
	r, _ := s.PrivateRecord(1)
	r.Created = time.Now().Add(-60 * 24 * time.Hour)
	r.Location = Location{City: "杭州", Precision: "city"}
	r.Status = "resolved"
	if err = plugin.WritePrivate(p, r); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled(plugin.CommentOrigin, false); err != nil {
		t.Fatal(err)
	}
	reloaded := New(plugin.NewManager(root))
	reloaded.scan()
	r, err = reloaded.PrivateRecord(1)
	if err != nil || r.IP != "" || r.UA != "" || r.Location.City != "杭州" {
		t.Fatalf("retention=%+v %v", r, err)
	}
	view, err := reloaded.Status()
	if err != nil || !view.KeySaved || view.Settings.Key != "" {
		t.Fatalf("settings privacy %+v %v", view, err)
	}
}

func TestLocalSourcesNeverQueryAndRetriesAreBounded(t *testing.T) {
	s, _, _ := originFixture(t)
	s.lookup = func(context.Context, Settings, string) (Location, error) {
		t.Fatal("queried private source")
		return Location{}, nil
	}
	for i, ip := range []string{"127.0.0.1", "10.0.0.1", "100.64.1.1", "::1", "2001:db8::1"} {
		if err := s.Capture(int64(i+1), ip, "UA"); err != nil {
			t.Fatal(err)
		}
		s.process(t.Context(), <-s.queue)
		r, _ := s.PrivateRecord(int64(i + 1))
		if r.Status != "local" {
			t.Fatal(r.Status)
		}
	}
}

func TestSettingsRejectUnsafeCustomEndpointsAndBlankKeys(t *testing.T) {
	for _, raw := range []string{"http://example.com/?ip={ip}", "https://127.0.0.1/?ip={ip}", "https://user:secret@example.com/?ip={ip}", "https://{ip}/api", "https://example.com:8080/?ip={ip}", "https://example.com/?ip=8.8.8.8"} {
		s := Defaults()
		s.Provider = "custom"
		s.Endpoint = raw
		if err := validate(s); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	s := Defaults()
	s.Provider = "baidu"
	if validate(s) == nil {
		t.Fatal("accepted empty AK")
	}
}

func TestProviderChangeDoesNotReuseSecretAndRetryRespectsRetention(t *testing.T) {
	s, _, root := originFixture(t)
	config, _ := s.Settings()
	config.Provider = "baidu"
	config.Key = "baidu-secret"
	if _, err := s.Save(config, false); err != nil {
		t.Fatal(err)
	}
	config, _ = s.Settings()
	config.Provider = "amap"
	config.Key = ""
	if _, err := s.Save(config, false); err == nil {
		t.Fatal("reused Baidu secret for Amap")
	}
	if err := s.Capture(1, "8.8.8.8", "Windows"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.PrivateRecord(1)
	r.Status = "failed"
	r.Attempts = 3
	r.Error = "provider failed"
	p := filepath.Join(root, plugin.CommentOrigin, "data", "records", "1.json")
	if err := plugin.WritePrivate(p, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Retry(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.PrivateRecord(1)
	if r.Status != "pending" || r.Attempts != 0 || r.Error != "" {
		t.Fatalf("retry=%+v", r)
	}
	r.Created = time.Now().Add(-400 * 24 * time.Hour)
	r.Status = "failed"
	plugin.WritePrivate(p, r)
	if err := s.Retry(); err != nil {
		t.Fatal(err)
	}
	r, _ = s.PrivateRecord(1)
	if r.Status == "pending" || r.IP != "" {
		t.Fatalf("expired retry=%+v", r)
	}
}

func TestDeviceRecognition(t *testing.T) {
	cases := []struct{ ua, os, kind, browser string }{{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Version/18 Mobile/15 Safari/604", "iOS", "mobile", "Safari"}, {"Mozilla/5.0 (Macintosh; Intel Mac OS X) Version/18 Mobile/15 Safari/604", "iPadOS", "tablet", "Safari"}, {"Mozilla/5.0 (Linux; Android 15) Chrome/135 Mobile Safari/537 SamsungBrowser/27", "Android", "mobile", "Samsung Internet"}, {"Windows Phone Android Mobile Edge/18", "Windows Phone", "mobile", ""}, {"Windows NT 10.0 Chrome/135 Safari/537 Edg/135", "Windows", "desktop", "Edge"}, {"Mozilla/5.0 (X11; Linux x86_64) Firefox/130", "Linux", "desktop", "Firefox"}, {"bot unknown", "自动化客户端", "bot", ""}}
	for _, c := range cases {
		d := ParseUA(c.ua)
		if d.OS != c.os || d.Kind != c.kind || d.Browser != c.browser {
			t.Errorf("%s: %+v", c.ua, d)
		}
	}
	if d := ParseUA("<script>bad</script>"); strings.Contains(d.Label(true), "script") {
		t.Fatal("raw UA surfaced")
	}
}

func TestQuotaResetsAtSiteMidnightNotUTCMidnight(t *testing.T) {
	for _, zone := range []string{"Asia/Shanghai", "America/New_York"} {
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, boundary := range []string{"site-midnight", "UTC-midnight"} {
			t.Run(zone+"/"+boundary, func(t *testing.T) {
				_, m, _ := originFixture(t)
				after := time.Date(2026, 11, 14, 0, 0, 0, 0, location)
				wantRequests := 1
				if boundary == "UTC-midnight" {
					after = time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
					wantRequests = 3
				}
				err := m.WithData(plugin.CommentOrigin, true, 0, func(dir string, _ uint64) error {
					// Reserve a fallback worker's two requests, then a manual query.
					if err := reserveBudget(dir, after.Add(-time.Nanosecond), location, 2, 3); err != nil {
						return err
					}
					if err := reserveBudget(dir, after, location, 1, 3); err != nil {
						return err
					}
					b, err := plugin.ReadPrivate[budget](filepath.Join(dir, "budget.json"), 1024)
					if err != nil {
						return err
					}
					if b.Day != after.In(location).Format("2006-01-02") || b.Requests != wantRequests {
						t.Fatalf("quota at %s = %+v, want %d requests on site day", boundary, b, wantRequests)
					}
					if remaining := 3 - b.Requests; remaining > 0 {
						if err := reserveBudget(dir, after, location, remaining, 3); err != nil {
							return err
						}
					}
					if err := reserveBudget(dir, after, location, 1, 3); !errors.Is(err, errDailyLimit) {
						t.Fatalf("full site-day quota accepted a query: %v", err)
					}
					b, err = plugin.ReadPrivate[budget](filepath.Join(dir, "budget.json"), 1024)
					if err == nil && b.Requests != 3 {
						t.Fatalf("rejected query changed quota: %+v", b)
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Pick a real zone whose date differs from UTC, without depending on the hour
// at which the test suite runs or on the host's local timezone.
func differentQuotaDayZone(t *testing.T) *time.Location {
	t.Helper()
	name := "Pacific/Kiritimati"
	if time.Now().UTC().Hour() < 10 {
		name = "Etc/GMT+12"
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestWorkerAndManualQueriesShareSiteQuotaAndKeepCosts(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		fallback       bool
		cost           int
	}{
		{"single-provider", "amap", false, 1},
		{"fallback", "amap", true, 2},
		{"ipwhois-fallback-is-one", "ipwhois", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, root := originFixture(t)
			zone := differentQuotaDayZone(t)
			var zoneCalls int
			s.SetLocationFunc(func() *time.Location { zoneCalls++; return zone })
			config, err := s.Settings()
			if err != nil {
				t.Fatal(err)
			}
			config.Provider, config.Key, config.Fallback = tc.provider, "test-key", tc.fallback
			config.DailyLimit = tc.cost + 1
			if _, err = s.Save(config, false); err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.lookup = func(context.Context, Settings, string) (Location, error) {
				calls++
				return Location{City: "测试", Precision: "city"}, nil
			}
			if err := s.Capture(1, "8.8.8.8", "UA"); err != nil {
				t.Fatal(err)
			}
			s.process(t.Context(), <-s.queue)
			data := filepath.Join(root, plugin.CommentOrigin, "data")
			b, err := plugin.ReadPrivate[budget](filepath.Join(data, "budget.json"), 1024)
			if err != nil || b.Day != time.Now().In(zone).Format("2006-01-02") || b.Requests != tc.cost {
				t.Fatalf("worker quota = %+v, err = %v", b, err)
			}
			entries, err := os.ReadDir(filepath.Join(data, "cache"))
			if err != nil || len(entries) != 1 {
				t.Fatalf("cache entries = %v, err = %v", entries, err)
			}
			cachePath := filepath.Join(data, "cache", entries[0].Name())
			cached, err := plugin.ReadPrivate[cacheEntry](cachePath, 2048)
			if err != nil {
				t.Fatal(err)
			}
			s.lastLookup = time.Time{} // No rate-limit wait between completed test lookups.
			if _, err := s.Test(t.Context(), "1.1.1.1"); err != nil {
				t.Fatal(err)
			}
			// A cache hit succeeds with a full budget, without renewing its TTL.
			if err := s.Capture(2, "8.8.8.8", "UA"); err != nil {
				t.Fatal(err)
			}
			s.process(t.Context(), <-s.queue)
			r, err := s.PrivateRecord(2)
			if err != nil || r.Status != "resolved" || r.Attempts != 0 {
				t.Fatalf("cached record = %+v, err = %v", r, err)
			}
			afterCache, err := plugin.ReadPrivate[cacheEntry](cachePath, 2048)
			if err != nil || !afterCache.Expires.Equal(cached.Expires) {
				t.Fatalf("cache lifetime changed: before=%v after=%v err=%v", cached.Expires, afterCache.Expires, err)
			}
			if _, err := s.Test(t.Context(), "9.9.9.9"); !errors.Is(err, errDailyLimit) {
				t.Fatalf("manual query exceeded shared quota: %v", err)
			}
			if err := s.Capture(3, "9.9.9.9", "UA"); err != nil {
				t.Fatal(err)
			}
			s.process(t.Context(), <-s.queue)
			r, err = s.PrivateRecord(3)
			if err != nil || r.Status != "pending" || r.Attempts != 0 || !r.Next.IsZero() {
				t.Fatalf("quota rejection changed retry state: %+v, err = %v", r, err)
			}
			b, err = plugin.ReadPrivate[budget](filepath.Join(data, "budget.json"), 1024)
			if err != nil || b.Requests != tc.cost+1 || b.Day != time.Now().In(zone).Format("2006-01-02") || calls != 2 || zoneCalls != 5 {
				t.Fatalf("shared quota = %+v, lookups=%d timezone calls=%d err=%v", b, calls, zoneCalls, err)
			}
		})
	}
}

func TestQuotaOperationsSnapshotCallbackOutsideLocks(t *testing.T) {
	for _, operation := range []string{"worker", "manual"} {
		t.Run(operation, func(t *testing.T) {
			s, _, root := originFixture(t)
			zone := differentQuotaDayZone(t)
			var calls atomic.Int32
			s.SetLocationFunc(func() *time.Location {
				calls.Add(1)
				// Both manager and service locks must be released before callbacks.
				if _, err := s.Settings(); err != nil {
					t.Error(err)
				}
				s.SetLocationFunc(func() *time.Location { calls.Add(1); return time.UTC })
				return zone
			})
			s.lookup = func(context.Context, Settings, string) (Location, error) {
				return Location{City: "测试", Precision: "city"}, nil
			}
			if err := s.Capture(1, "8.8.8.8", "UA"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "worker" {
					s.process(t.Context(), <-s.queue)
					done <- nil
				} else {
					_, err := s.Test(t.Context(), "8.8.8.8")
					done <- err
				}
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timezone callback deadlocked against service or plugin manager")
			}
			b, err := plugin.ReadPrivate[budget](filepath.Join(root, plugin.CommentOrigin, "data", "budget.json"), 1024)
			if err != nil || b.Day != time.Now().In(zone).Format("2006-01-02") || b.Requests != 1 || calls.Load() != 1 {
				t.Fatalf("operation did not use one callback snapshot: budget=%+v calls=%d err=%v", b, calls.Load(), err)
			}
			if got := s.siteZone(); got != time.UTC || calls.Load() != 2 {
				t.Fatalf("next operation missed callback change: zone=%v calls=%d", got, calls.Load())
			}
		})
	}
}

func TestSiteZoneConcurrentCallbackUpdatesAndUTCFallback(t *testing.T) {
	s := New(nil)
	zones := []*time.Location{time.UTC, time.FixedZone("east", 8*60*60), time.FixedZone("west", -5*60*60)}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 500 {
				zone := zones[(i+j)%len(zones)]
				s.SetLocationFunc(func() *time.Location { return zone })
				got := s.siteZone()
				if got != zones[0] && got != zones[1] && got != zones[2] {
					t.Errorf("unexpected callback snapshot: %v", got)
				}
			}
		})
	}
	wg.Wait()
	for _, callback := range []func() *time.Location{nil, func() *time.Location { return nil }, func() *time.Location { return time.Local }, func() *time.Location { return time.FixedZone("Local", 8*60*60) }} {
		s.SetLocationFunc(callback)
		if got := s.siteZone(); got != time.UTC {
			t.Errorf("unspecified or host-local zone did not fall back to UTC: %v", got)
		}
	}
	if got := (*Service)(nil).siteZone(); got != time.UTC {
		t.Errorf("nil service timezone = %v", got)
	}
}
