// Package checkin defines the shared, server-authoritative check-in schedule.
package checkin

import (
	"errors"
	"time"
	_ "time/tzdata"
)

const (
	Interval        = "interval"
	CalendarDay     = "calendar_day"
	DefaultTimezone = "Asia/Shanghai"
)

type Rule struct {
	Mode     string
	Timezone string
	Hours    int
}

func (r Rule) Defaults() Rule {
	if r.Mode == "" {
		r.Mode = Interval
	}
	if r.Timezone == "" {
		r.Timezone = DefaultTimezone
	}
	return r
}

func (r Rule) Validate() error {
	r = r.Defaults()
	if (r.Mode != Interval && r.Mode != CalendarDay) || r.Hours < 1 || r.Hours > 720 || r.Timezone == "Local" {
		return errors.New("invalid check-in rule")
	}
	_, err := time.LoadLocation(r.Timezone)
	return err
}

// Next returns the first eligible instant after last. Callers validate settings
// before using them; an invalid zone fails closed rather than using host time.
func (r Rule) Next(last time.Time) time.Time {
	if last.IsZero() {
		return time.Time{}
	}
	r = r.Defaults()
	if r.Mode == Interval {
		return last.Add(time.Duration(r.Hours) * time.Hour)
	}
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil || r.Mode != CalendarDay {
		return time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	local := last.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc).UTC()
}

// Since is an exclusive query lower bound, covering both the reward budget and
// the current check-in period, including a check-in exactly at local midnight.
func (r Rule) Since(now time.Time) time.Time {
	r = r.Defaults()
	since := now.Add(-24 * time.Hour)
	if r.Mode == Interval {
		return now.Add(-time.Duration(max(24, r.Hours)) * time.Hour)
	}
	loc, err := time.LoadLocation(r.Timezone)
	if err != nil {
		return time.Time{}
	}
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).Add(-time.Microsecond)
	if start.Before(since) {
		return start
	}
	return since
}
