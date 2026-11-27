package database

import (
	"newpress.local/newpress/internal/virtualcurrency"
	"testing"
	"time"
)

func TestCurrencyCalendarSchedulePostgres(t *testing.T) {
	s, cfg, ids := currencyPostgres(t)
	cfg.CheckInEnabled = true
	cfg.CheckInMode = "calendar_day"
	cfg.CheckInTimezone = "Asia/Shanghai"
	var err error
	cfg, err = s.UpdateSettings(t.Context(), cfg.Version, cfg)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Settings(t.Context())
	if err != nil || saved.CheckInMode != cfg.CheckInMode || saved.CheckInTimezone != cfg.CheckInTimezone {
		t.Fatal(saved, err)
	}
	first, err := s.Reward(t.Context(), virtualcurrency.RewardInput{UserID: ids[0], Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: "calendar-postgres-first", VerifiedEmail: true})
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := s.Wallet(t.Context(), ids[0], 0, 30)
	if err != nil || !wallet.NextCheckInAt.Equal(cfg.CheckInRule().Next(first.Entry.CreatedAt)) {
		t.Fatal(wallet, err)
	}
	// Avoid a false failure if the test itself straddles midnight.
	if time.Now().Before(wallet.NextCheckInAt) {
		second, err := s.Reward(t.Context(), virtualcurrency.RewardInput{UserID: ids[0], Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: cfg.Version, RequestID: "calendar-postgres-second", VerifiedEmail: true})
		if err != nil || !second.Replayed {
			t.Fatal(second, err)
		}
	}
}
