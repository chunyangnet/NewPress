package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
)

func TestSiteTimeRequestSnapshotSurvivesSettingsChange(t *testing.T) {
	s, routes, _, cookie, _ := newAuthenticatedContentServer(t)
	s.config.AnalyticsEnabled = true
	initial := s.siteSettings.Snapshot()
	instant := publicationTestInstant(t, "2026-01-01T23:30:45Z")
	var firstContext context.Context
	requests := 0
	handler := s.captureSiteTime(s.observeAnalytics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		wantZone, wantTitle := "Asia/Shanghai", initial.Settings.Identity.Title
		if requests == 1 {
			firstContext = r.Context()
			changed := s.siteSettings.Snapshot()
			changed.Settings.Identity.Timezone = "America/New_York"
			changed.Settings.Identity.Title = "Updated between request phases"
			if _, fields, err := s.siteSettings.Save(changed.Revision, changed.Settings); err != nil || len(fields) > 0 {
				t.Fatalf("change settings mid-request: fields=%v err=%v", fields, err)
			}
		} else {
			wantZone, wantTitle = "America/New_York", "Updated between request phases"
		}
		if _, ok := w.(*analyticsResponseWriter); !ok {
			t.Fatal("test must exercise response context through the analytics writer")
		}
		for name, ctx := range map[string]context.Context{"request": r.Context(), "response": responseContext(w)} {
			snapshot := s.siteSnapshot(ctx)
			if snapshot.Settings.Identity.Timezone != wantZone || snapshot.Settings.Identity.Title != wantTitle || s.siteLocation(ctx).String() != wantZone {
				t.Errorf("%s lost captured settings: timezone=%s title=%q location=%s", name, snapshot.Settings.Identity.Timezone, snapshot.Settings.Identity.Title, s.siteLocation(ctx))
			}
			if requests == 1 && snapshot.Revision != initial.Revision {
				t.Errorf("%s revision changed mid-request: %s, want %s", name, snapshot.Revision, initial.Revision)
			}
		}
		view := s.baseViewFor(w, "Snapshot test", "", "/")
		if view.Site.Timezone != wantZone || view.Site.Title != wantTitle || view.Now.Location().String() != wantZone {
			t.Errorf("render view mixed settings: timezone=%s title=%q clock=%s", view.Site.Timezone, view.Site.Title, view.Now.Location())
		}
		location := publicationTestLocation(t, wantZone)
		if got := s.dateTimeSecondsLabel(instant, r.Context()); got != instant.In(location).Format("2006-01-02 15:04:05") {
			t.Errorf("date label=%q, want request timezone %s", got, wantZone)
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
		request.Header.Set("User-Agent", "Mozilla/5.0 Chrome/131 Safari/537.36")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("snapshot handler status=%d", response.Code)
		}
	}
	if got := s.siteLocation(firstContext).String(); got != "Asia/Shanghai" {
		t.Fatalf("retained request context changed after response: %s", got)
	}
	if got := s.siteLocation().String(); got != "America/New_York" {
		t.Fatalf("out-of-request location is stale: %s", got)
	}
	response := serveCookieRequest(routes, http.MethodGet, "https://example.test/admin/content/new/", cookie, nil)
	if response.Code != http.StatusOK || publicationHTMLInput(t, response.Body.String(), "published_timezone") != "America/New_York" {
		t.Fatalf("next routed editor did not use updated timezone: status=%d", response.Code)
	}
}

func TestSiteTimeDefaultAndReplacementAnalyticsUseLiveTimezone(t *testing.T) {
	s, _, _, _, _ := newAuthenticatedContentServer(t)
	at := publicationTestInstant(t, "2026-01-01T23:30:00Z")
	now := publicationTestInstant(t, "2026-01-02T16:00:00Z")
	for _, mode := range []string{"constructor", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "replacement" {
				store, err := analytics.NewFileStore("")
				if err != nil {
					t.Fatal(err)
				}
				collector := analytics.NewCollector(store)
				collector.SetLocationFunc(func() *time.Location { return time.UTC })
				s.SetAnalytics(collector)
			}
			setPublicationTestTimezone(t, s, "Asia/Shanghai")
			s.analytics.Record(analytics.Event{At: at, Source: "direct"})
			state, err := s.analytics.Snapshot(t.Context(), now)
			if err != nil || len(state.Days) != 1 || state.Days["2026-01-02"].Views != 1 {
				t.Fatalf("Shanghai collector/store binding: days=%+v err=%v", state.Days, err)
			}
			setPublicationTestTimezone(t, s, "America/New_York")
			s.analytics.Record(analytics.Event{At: at, Source: "direct"})
			state, err = s.analytics.Snapshot(t.Context(), now)
			if err != nil || len(state.Days) != 2 || state.Days["2026-01-01"].Views != 1 || state.Days["2026-01-02"].Views != 1 {
				t.Fatalf("timezone change did not affect new bucket or rewrote history: days=%+v err=%v", state.Days, err)
			}
		})
	}
}

func TestAnalyticsReportKeepsRequestTimezoneAcrossStoreOperations(t *testing.T) {
	s, _, _, _, _ := newAuthenticatedContentServer(t)
	backing, err := analytics.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	store := &siteTimeAnalyticsStore{Store: backing}
	s.SetAnalytics(analytics.NewCollector(store))
	store.onOperation = func() { setPublicationTestTimezone(t, s, "America/New_York") }
	// Use a completed day inside both rolling windows. A Shanghai bucket for
	// today can be tomorrow in New York; historical buckets are not rewritten.
	s.analytics.Record(analytics.Event{At: time.Now().Add(-24 * time.Hour), Source: "direct"})
	var result analyticsView
	handler := s.captureSiteTime(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		result, err = s.analyticsReport(r.Context(), 7)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, wantZone := range []string{"Asia/Shanghai", "America/New_York"} {
		store.zones = nil
		before := time.Now()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "https://example.test/admin/analytics/", nil))
		after := time.Now()
		if len(store.zones) == 0 {
			t.Fatal("report did not call the analytics store")
		}
		for _, got := range store.zones {
			if got != wantZone {
				t.Errorf("store operation timezone=%q, want captured %q", got, wantZone)
			}
		}
		location := publicationTestLocation(t, wantZone)
		if result.Timezone != wantZone || result.End != before.In(location).Format(time.DateOnly) && result.End != after.In(location).Format(time.DateOnly) {
			t.Fatalf("report timezone/range diverged: timezone=%s end=%s want=%s", result.Timezone, result.End, wantZone)
		}
		end, err := time.ParseInLocation(time.DateOnly, result.End, location)
		if err != nil || result.Start != end.AddDate(0, 0, -6).Format(time.DateOnly) || len(result.Trend) != 7 || result.Trend[0].Date != result.Start || result.Trend[6].Date != result.End || result.Views != 1 {
			t.Fatalf("report range/trend mixed calendars: %+v err=%v", result, err)
		}
	}
}

type siteTimeAnalyticsStore struct {
	analytics.Store
	zones       []string
	onOperation func()
}

func (s *siteTimeAnalyticsStore) capture(ctx context.Context) {
	zone := "missing context location"
	if location, ok := analytics.LocationFromContext(ctx); ok {
		zone = location.String()
	}
	s.zones = append(s.zones, zone)
	if s.onOperation != nil {
		change := s.onOperation
		s.onOperation = nil
		change()
	}
}

func (s *siteTimeAnalyticsStore) Apply(ctx context.Context, batch analytics.Batch, now time.Time) error {
	s.capture(ctx)
	return s.Store.Apply(ctx, batch, now)
}

func (s *siteTimeAnalyticsStore) Snapshot(ctx context.Context, now time.Time) (analytics.State, error) {
	s.capture(ctx)
	return s.Store.Snapshot(ctx, now)
}

func TestSiteTimeDefaultAndReplacementCommentOriginUseLiveTimezone(t *testing.T) {
	s, _, _, _, _ := newAuthenticatedContentServer(t)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	settings, err := s.commentOrigin.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.DailyLimit = 1
	if _, err := s.commentOrigin.Save(settings, false); err != nil {
		t.Fatal(err)
	}
	// An already exhausted budget returns before querying a provider. A canceled
	// context also prevents network access if a broken binding resets that budget.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, mode := range []string{"constructor", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "replacement" {
				origin := commentorigin.New(s.plugins)
				origin.SetLocationFunc(func() *time.Location { return time.UTC })
				s.SetPluginServices(s.pluginMarket, origin)
			}
			// These calendars are 25 hours apart, so at least one differs from UTC
			// regardless of the wall-clock time at which the test runs.
			for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
				setPublicationTestTimezone(t, s, zone)
				day := time.Now().In(publicationTestLocation(t, zone)).Format(time.DateOnly)
				err := s.plugins.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error {
					return plugin.WritePrivate(filepath.Join(dir, "budget.json"), struct {
						Day      string `json:"day"`
						Requests int    `json:"requests"`
					}{Day: day, Requests: 1})
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.commentOrigin.Test(ctx, "8.8.8.8"); err == nil || !strings.Contains(err.Error(), "今日查询额度已用完") {
					t.Fatalf("%s comment-origin budget ignored live %s calendar: %v", mode, zone, err)
				}
			}
		})
	}
}

func TestCommentAutoCloseUsesSiteCalendarAcrossDST(t *testing.T) {
	for _, test := range []struct{ name, zone, published, deadline string }{
		{"new_york_23_hour_day", "America/New_York", "2026-03-07T17:00:00Z", "2026-03-08T16:00:00Z"},
		{"new_york_25_hour_day", "America/New_York", "2026-10-31T16:00:00Z", "2026-11-01T17:00:00Z"},
		{"lord_howe_23_and_half_hour_day", "Australia/Lord_Howe", "2026-10-03T01:30:00Z", "2026-10-04T01:00:00Z"},
		{"lord_howe_24_and_half_hour_day", "Australia/Lord_Howe", "2026-04-04T01:00:00Z", "2026-04-05T01:30:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := site.DefaultSettings("Timezone test", "")
			settings.Identity.Timezone = test.zone
			settings.Discussion.PublicSubmissionsEnabled = true
			settings.Discussion.AutoCloseDays = 1
			published := publicationTestInstant(t, test.published)
			deadline := publicationTestInstant(t, test.deadline)
			for _, storedLocation := range []*time.Location{time.UTC, time.FixedZone("stored-offset", 8*60*60)} {
				post := content.Post{CommentStatus: content.CommentOpen, PublishedAt: published.In(storedLocation)}
				for _, beforeDeadline := range []time.Time{deadline.Add(-time.Nanosecond), deadline} {
					if open, reason := publicCommentSubmissionsOpen(post, settings, beforeDeadline); !open || reason != "" {
						t.Errorf("closed early with stored zone %s at %s: %q", storedLocation, beforeDeadline, reason)
					}
				}
				if open, reason := publicCommentSubmissionsOpen(post, settings, deadline.Add(time.Nanosecond)); open || !strings.Contains(reason, "超过评论开放期限") {
					t.Errorf("did not close at site-calendar deadline with stored zone %s: open=%v reason=%q", storedLocation, open, reason)
				}
			}
		})
	}
}

func TestSiteTimeZeroIsEmptyAndNilLocationIsUTC(t *testing.T) {
	if got := siteTime(publicationTestLocation(t, "Asia/Shanghai"), time.Time{}, "2006-01-02 15:04:05"); got != "" {
		t.Fatalf("zero timestamp rendered as %q", got)
	}
	instant := publicationTestInstant(t, "2026-01-02T07:30:45+08:00")
	if got := siteTime(nil, instant, "2006-01-02 15:04:05"); got != "2026-01-01 23:30:45" {
		t.Fatalf("nil location must use UTC: %q", got)
	}
}
