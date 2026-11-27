package virtualcurrency

import (
	"errors"
	"testing"
)

func TestMainPointsDefaultTenths(t *testing.T) {
	s := DefaultSettings()
	if s.Precision != 1 || s.MinTransfer != 1 || FormatAmount(s.CheckInReward, 1) != "10.0" || FormatAmount(s.RegistrationReward, 1) != "100.0" || FormatAmount(s.MaxBalance, 1) != "1000000000.0" {
		t.Fatal(s)
	}
	if amount, err := ParseAmount("0.1", s.Precision); err != nil || amount != 1 {
		t.Fatal(amount, err)
	}
	if _, err := ParseAmount("0.01", s.Precision); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
