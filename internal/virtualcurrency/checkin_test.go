package virtualcurrency

import (
	"testing"
	"time"
)

func TestCalendarCheckIn(t *testing.T) {
	s, cfg := testCurrency(t, "")
	now := time.Date(2026, 11, 21, 15, 59, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	cfg.CheckInEnabled = true
	cfg.CheckInMode = "calendar_day"
	cfg.CheckInTimezone = "Asia/Shanghai"
	var err error
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(id string) RewardResult {
		t.Helper()
		r, e := s.Reward(t.Context(), RewardInput{UserID: 1, Kind: RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: id, VerifiedEmail: true})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	first := claim("calendar-first-request")
	if first.Replayed {
		t.Fatal("first replayed")
	}
	if !claim("calendar-second-request").Replayed {
		t.Fatal("same day duplicate")
	}
	wallet, err := s.Wallet(t.Context(), 1, 0, 30)
	if err != nil || !wallet.NextCheckInAt.Equal(time.Date(2026, 11, 21, 16, 0, 0, 0, time.UTC)) {
		t.Fatal(wallet, err)
	}
	now = wallet.NextCheckInAt
	if claim("calendar-midnight-request").Replayed {
		t.Fatal("midnight blocked")
	}
	if !claim("calendar-midnight-repeat").Replayed {
		t.Fatal("midnight duplicate")
	}
	cfg.CheckInMode = "interval"
	cfg.CheckInIntervalHours = 1
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !claim("calendar-switched-request").Replayed {
		t.Fatal("switch bypassed cooldown")
	}
	now = now.Add(time.Hour)
	if claim("calendar-hour-request").Replayed {
		t.Fatal("hour boundary blocked")
	}
}
