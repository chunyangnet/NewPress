package checkin

import (
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	parse := func(s string) time.Time {
		v, e := time.Parse(time.RFC3339, s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	for _, tc := range []struct {
		name, mode, zone, last, next string
		hours                        int
	}{
		{"beijing", "calendar_day", "Asia/Shanghai", "2026-11-21T15:59:59Z", "2026-11-21T16:00:00Z", 24},
		{"midnight", "calendar_day", "Asia/Shanghai", "2026-11-21T16:00:00Z", "2026-11-22T16:00:00Z", 24},
		{"utc", "calendar_day", "UTC", "2026-11-21T15:59:59Z", "2026-11-22T00:00:00Z", 24},
		{"legacy", "", "", "2026-11-21T15:59:59Z", "2026-11-22T15:59:59Z", 24},
		{"hour", "interval", "UTC", "2026-11-21T15:59:59Z", "2026-11-21T16:59:59Z", 1},
		{"spring", "calendar_day", "America/New_York", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z", 24},
		{"fall", "calendar_day", "America/New_York", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z", 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Rule{tc.mode, tc.zone, tc.hours}
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			next := r.Next(parse(tc.last))
			if !next.Equal(parse(tc.next)) {
				t.Fatal(next)
			}
			if !r.Next(time.Time{}).IsZero() {
				t.Fatal("first check-in blocked")
			}
			if !parse(tc.last).After(r.Since(next.Add(-time.Nanosecond))) {
				t.Fatal("active entry omitted")
			}
		})
	}
	for _, r := range []Rule{{"bad", "UTC", 24}, {Interval, "Local", 24}, {CalendarDay, "Missing/Zone", 24}, {Interval, "UTC", 0}, {Interval, "UTC", 721}} {
		if r.Validate() == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	r := Rule{Hours: 720}
	at := parse("2026-11-21T00:00:00Z")
	if r.Next(at).Sub(at) != 720*time.Hour {
		t.Fatal("max interval")
	}
}
