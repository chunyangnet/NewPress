package theme

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type timezoneTestPage struct {
	Value    time.Time
	Timezone string
}

func (p timezoneTestPage) SiteTimezone() string { return p.Timezone }

const timezoneHelpersTemplate = `{{date .Value}}|{{dateIn .Value .Timezone}}|{{datetimeIn .Value .Timezone}}|{{datetimeSecondsIn .Value .Timezone}}|{{timeIn .Value .Timezone}}`

func newTimezoneTestManager(t *testing.T, body string) *Manager {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "templates"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"base.html": `{{define "base"}}{{template "content" .}}{{end}}`,
		"page.html": `{{define "content"}}` + body + `{{end}}`,
	} {
		if err := os.WriteFile(filepath.Join(directory, "templates", name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	templates, err := compileTemplates(directory, Manifest{Templates: map[string]string{"home": "page.html", "404": "page.html"}})
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{templates: templates}
}

func TestThemeTimezoneHelpers(t *testing.T) {
	t.Parallel()
	manager := newTimezoneTestManager(t, timezoneHelpersTemplate)
	for _, tt := range []struct {
		name, zone, instant, date, clock string
	}{
		{"UTC", "UTC", "2026-12-31T18:30:45Z", "2026-12-31", "18:30:45"},
		{"Shanghai cross year", "Asia/Shanghai", "2026-12-31T18:30:45Z", "2027-01-01", "02:30:45"},
		{"New York", "America/New_York", "2026-12-31T18:30:45Z", "2026-12-31", "13:30:45"},
		{"Kathmandu cross year", "Asia/Kathmandu", "2026-12-31T18:30:45Z", "2027-01-01", "00:15:45"},
		{"New York preceding year", "America/New_York", "2027-01-01T01:30:45Z", "2026-12-31", "20:30:45"},
		{"New York spring before", "America/New_York", "2026-03-08T06:59:59Z", "2026-03-08", "01:59:59"},
		{"New York spring after", "America/New_York", "2026-03-08T07:00:00Z", "2026-03-08", "03:00:00"},
		{"New York fall before", "America/New_York", "2026-11-01T05:59:59Z", "2026-11-01", "01:59:59"},
		{"New York fall after", "America/New_York", "2026-11-01T06:00:00Z", "2026-11-01", "01:00:00"},
		{"epoch UTC", "UTC", "1970-01-01T00:00:00Z", "1970-01-01", "00:00:00"},
		{"epoch Shanghai", "Asia/Shanghai", "1970-01-01T00:00:00Z", "1970-01-01", "08:00:00"},
		{"epoch New York", "America/New_York", "1970-01-01T00:00:00Z", "1969-12-31", "19:00:00"},
		{"epoch Kathmandu historical offset", "Asia/Kathmandu", "1970-01-01T00:00:00Z", "1970-01-01", "05:30:00"},
		{"blank", "", "2026-12-31T18:30:45Z", "2026-12-31", "18:30:45"},
		{"whitespace", " \t", "2026-12-31T18:30:45Z", "2026-12-31", "18:30:45"},
		{"invalid", "Not/A_Zone", "2026-12-31T18:30:45Z", "2026-12-31", "18:30:45"},
		{"Local", "Local", "2026-12-31T18:30:45Z", "2026-12-31", "18:30:45"},
		{"trimmed site zone", " Asia/Kathmandu ", "2026-12-31T18:30:45Z", "2027-01-01", "00:15:45"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339, tt.instant)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := manager.Render(&output, "home", timezoneTestPage{Value: instant, Timezone: tt.zone}); err != nil {
				t.Fatal(err)
			}
			want := strings.Join([]string{tt.date, tt.date, tt.date + " " + tt.clock[:5], tt.date + " " + tt.clock, tt.clock[:5]}, "|")
			if got := output.String(); got != want {
				t.Fatalf("rendered %q, want %q", got, want)
			}
		})
	}
}

func TestThemeTimezoneZeroValuesStayEmpty(t *testing.T) {
	t.Parallel()
	manager := newTimezoneTestManager(t, timezoneHelpersTemplate)
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York", "Asia/Kathmandu", "", "Not/A_Zone", "Local"} {
		for _, value := range []time.Time{{}, time.Time{}.In(themeLocation(zone))} {
			var output bytes.Buffer
			if err := manager.Render(&output, "home", timezoneTestPage{Value: value, Timezone: zone}); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != "||||" {
				t.Fatalf("zero time in %q rendered %q", zone, got)
			}
		}
	}
	var output bytes.Buffer
	if err := manager.Render(&output, "home", map[string]any{"Value": time.Time{}, "Timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "||||" {
		t.Fatalf("zero time without a provider rendered %q", got)
	}
}

func TestThemeLocationNeverUsesHostLocal(t *testing.T) {
	t.Parallel()
	for _, zone := range []string{"", " \t", "Local", " Local ", "Not/A_Zone"} {
		if got := themeLocation(zone); got != time.UTC {
			t.Errorf("themeLocation(%q) = %v, want time.UTC", zone, got)
		}
	}
}

type timezoneRenderCase struct {
	name string
	data any
	want string
}

func timezoneRenderCases() []timezoneRenderCase {
	instant := time.Date(2026, 12, 31, 18, 30, 45, 0, time.UTC)
	return []timezoneRenderCase{
		{"missing provider", map[string]any{"Value": instant}, "2026-12-31"},
		{"Shanghai", timezoneTestPage{instant, "Asia/Shanghai"}, "2027-01-01"},
		{"invalid", timezoneTestPage{instant, "Not/A_Zone"}, "2026-12-31"},
		{"Kathmandu", timezoneTestPage{instant, "Asia/Kathmandu"}, "2027-01-01"},
		{"Local", timezoneTestPage{instant, "Local"}, "2026-12-31"},
		{"New York", timezoneTestPage{instant, "America/New_York"}, "2026-12-31"},
		{"empty provider", timezoneTestPage{instant, ""}, "2026-12-31"},
		{"UTC", timezoneTestPage{instant, "UTC"}, "2026-12-31"},
	}
}

func TestThemeRenderRepeatedMixedTimezoneProviders(t *testing.T) {
	t.Parallel()
	manager := newTimezoneTestManager(t, `{{date .Value}}`)
	for range 3 {
		for _, page := range []string{"home", "404"} {
			for _, tt := range timezoneRenderCases() {
				var output bytes.Buffer
				if err := manager.Render(&output, page, tt.data); err != nil {
					t.Fatalf("%s/%s: %v", page, tt.name, err)
				}
				if got := output.String(); got != tt.want {
					t.Fatalf("%s/%s: rendered %q, want %q", page, tt.name, got, tt.want)
				}
			}
		}
	}
	for page, prototype := range manager.templates {
		if _, err := prototype.Clone(); err != nil {
			t.Fatalf("%s prototype was executed: %v", page, err)
		}
	}
}

func TestThemeRenderConcurrentTimezoneProviders(t *testing.T) {
	t.Parallel()
	manager := newTimezoneTestManager(t, `{{date .Value}}`)
	cases := timezoneRenderCases()
	errors := make(chan error, 16*len(cases))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 16 {
		for _, tt := range cases {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				var output bytes.Buffer
				if err := manager.Render(&output, "home", tt.data); err != nil {
					errors <- fmt.Errorf("%s: %w", tt.name, err)
				} else if got := output.String(); got != tt.want {
					errors <- fmt.Errorf("%s: rendered %q, want %q", tt.name, got, tt.want)
				}
			}()
		}
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if _, err := manager.templates["home"].Clone(); err != nil {
		t.Fatalf("shared prototype was executed: %v", err)
	}
}

type changingTimezoneTestPage struct {
	Value time.Time
	calls int
}

func (p *changingTimezoneTestPage) SiteTimezone() string {
	p.calls++
	if p.calls == 1 {
		return "Asia/Shanghai"
	}
	return "America/New_York"
}

func TestThemeRenderCapturesTimezoneOnce(t *testing.T) {
	t.Parallel()
	manager := newTimezoneTestManager(t, `{{date .Value}}|{{date .Value}}`)
	page := &changingTimezoneTestPage{Value: time.Date(2026, 12, 31, 18, 30, 0, 0, time.UTC)}
	for _, want := range []string{"2027-01-01|2027-01-01", "2026-12-31|2026-12-31"} {
		var output bytes.Buffer
		if err := manager.Render(&output, "home", page); err != nil {
			t.Fatal(err)
		}
		if got := output.String(); got != want {
			t.Fatalf("rendered %q, want %q", got, want)
		}
	}
	if page.calls != 2 {
		t.Fatalf("provider called %d times, want once per render", page.calls)
	}
}

func TestThemeRenderReturnsCloneErrorWithoutExecutingPrototype(t *testing.T) {
	t.Parallel()
	manager := newTimezoneTestManager(t, `must not render after clone failure: {{date .Value}}`)
	cases := timezoneRenderCases()
	if err := manager.templates["home"].ExecuteTemplate(io.Discard, "base", cases[0].data); err != nil {
		t.Fatal(err)
	}
	for _, tt := range cases {
		var output bytes.Buffer
		if err := manager.Render(&output, "home", tt.data); err == nil || !strings.Contains(err.Error(), "clone theme template") {
			t.Fatalf("%s: clone error = %v", tt.name, err)
		}
		if output.Len() != 0 {
			t.Fatalf("%s: executed the prototype after clone failure: %q", tt.name, output.String())
		}
	}
}
