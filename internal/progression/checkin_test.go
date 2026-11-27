package progression

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestLegacyCheckInSettingsReload(t *testing.T) {
	_, _, _, _, path := fixture(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	cfg := doc["settings"].(map[string]any)["check_in"].(map[string]any)
	delete(cfg, "mode")
	delete(cfg, "timezone")
	body, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := store.Settings(t.Context())
	if err != nil || settings.CheckIn.Mode != "interval" || settings.CheckIn.Timezone != "Asia/Shanghai" {
		t.Fatal(settings, err)
	}
}

func TestCalendarCheckIn(t *testing.T) {
	s, _, cfg, now, _ := fixture(t)
	*now = time.Date(2027, 11, 21, 15, 59, 0, 0, time.UTC)
	cfg.CheckIn.Mode = "calendar_day"
	cfg.CheckIn.Timezone = "Asia/Shanghai"
	var err error
	cfg, err = s.Store.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(id string) Result {
		t.Helper()
		r, e := s.CheckIn(t.Context(), 2, cfg.Version, id)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	if claim("calendar-first-request").Replayed {
		t.Fatal("first replayed")
	}
	if !claim("calendar-repeat-request").Replayed {
		t.Fatal("same day duplicate")
	}
	view, err := s.Store.Snapshot(t.Context(), 2, 0)
	if err != nil || !view.NextCheckIn.Equal(time.Date(2027, 11, 21, 16, 0, 0, 0, time.UTC)) {
		t.Fatal(view, err)
	}
	*now = view.NextCheckIn
	if claim("calendar-midnight-request").Replayed {
		t.Fatal("midnight blocked")
	}
	if !claim("calendar-midnight-repeat").Replayed {
		t.Fatal("midnight duplicate")
	}
	*now = now.Add(23*time.Hour + 59*time.Minute)
	if !claim("calendar-end-day-repeat").Replayed {
		t.Fatal("zero timestamp omitted")
	}
}
