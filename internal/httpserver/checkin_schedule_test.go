package httpserver

import (
	"encoding/json"
	"testing"

	"newpress.local/newpress/internal/virtualcurrency"
)

func TestCheckInScheduleModuleValidation(t *testing.T) {
	cfg := virtualcurrency.DefaultSettings()
	module, _ := currencyModule("check-in")
	values := map[string]json.RawMessage{"check_in_mode": json.RawMessage(`"calendar_day"`), "check_in_timezone": json.RawMessage(`"Asia/Shanghai"`)}
	next, err := mergeCurrencyModule(cfg, module, "1", values)
	if err != nil || next.CheckInMode != "calendar_day" || next.CheckInTimezone != "Asia/Shanghai" {
		t.Fatal(next, err)
	}
	for _, bad := range []string{`"Local"`, `"Missing/Zone"`, `42`} {
		values["check_in_timezone"] = json.RawMessage(bad)
		if _, err = mergeCurrencyModule(cfg, module, "1", values); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	values = map[string]json.RawMessage{"check_in_mode": json.RawMessage(`"unknown"`)}
	if _, err = mergeCurrencyModule(cfg, module, "1", values); err == nil {
		t.Fatal("accepted mode")
	}
}
