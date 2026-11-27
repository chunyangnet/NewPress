package virtualcurrency

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

func PrepareSettings(current, value Settings, expected int64, active bool, now time.Time) (Settings, error) {
	value, err := NormalizeSettings(value)
	if err != nil || expected < 1 {
		return Settings{}, ErrInvalid
	}
	if current.Version != expected {
		return Settings{}, ErrConflict
	}
	if expected == math.MaxInt64 {
		return Settings{}, ErrStoreUnavailable
	}
	if active && current.Precision != value.Precision {
		return Settings{}, ErrPrecisionLocked
	}
	value.Version, value.PrecisionLocked = expected+1, active
	value.RegistrationSince, value.PublicationSince = current.RegistrationSince, current.PublicationSince
	if value.Enabled && value.RegistrationEnabled && value.RegistrationSince.IsZero() {
		// Account creation facts are stored at second precision in both stores.
		// Use the same boundary so accounts created in the activation second
		// are not permanently excluded from their registration reward.
		value.RegistrationSince = now.UTC().Truncate(time.Second)
	}
	if value.Enabled && value.PublicationEnabled && value.PublicationSince.IsZero() {
		value.PublicationSince = now.UTC()
	}
	return value, nil
}

func AdjustmentFingerprint(user, delta int64, reason string) string {
	return FingerprintForStore("admin_adjustment", strconv.FormatInt(user, 10), strconv.FormatInt(delta, 10), strings.TrimSpace(reason))
}
func TransferFingerprint(recipient, amount int64) string {
	return FingerprintForStore("transfer", strconv.FormatInt(recipient, 10), strconv.FormatInt(amount, 10))
}
func CreditFingerprint(user, amount int64, source string) string {
	return FingerprintForStore("credit", strconv.FormatInt(user, 10), strconv.FormatInt(amount, 10), strings.TrimSpace(source))
}

func RewardIdentity(in RewardInput) (actor int64, requestID, fingerprint string, err error) {
	if in.UserID < 1 {
		return 0, "", "", ErrInvalid
	}
	switch in.Kind {
	case RewardCheckIn:
		if in.SourceID != 0 || in.ExpectedVersion < 1 || !ValidRequestID(in.RequestID) {
			return 0, "", "", ErrInvalid
		}
		actor, requestID = in.UserID, in.RequestID
	case RewardRegistration:
		if in.SourceID != in.UserID || in.OccurredAt.IsZero() {
			return 0, "", "", ErrInvalid
		}
		requestID = fmt.Sprintf("reward-register-%020d", in.SourceID)
	case RewardPublication:
		if in.SourceID < 1 || in.OccurredAt.IsZero() || in.ContentLength < 0 {
			return 0, "", "", ErrInvalid
		}
		requestID = fmt.Sprintf("reward-publish-%020d", in.SourceID)
	default:
		return 0, "", "", ErrInvalid
	}
	fingerprint = FingerprintForStore(in.Kind, strconv.FormatInt(in.UserID, 10), strconv.FormatInt(in.SourceID, 10))
	return
}

func RewardAmount(s Settings, in RewardInput, now time.Time) (int64, error) {
	if !s.Enabled {
		return 0, ErrDisabled
	}
	if s.RequireVerifiedEmail && !in.VerifiedEmail {
		return 0, ErrRewardIneligible
	}
	switch in.Kind {
	case RewardCheckIn:
		if in.ExpectedVersion != s.Version {
			return 0, ErrConflict
		}
		if s.CheckInEnabled {
			return s.CheckInReward, nil
		}
	case RewardRegistration:
		if s.RegistrationEnabled && !s.RegistrationSince.IsZero() && !in.OccurredAt.Before(s.RegistrationSince) && !in.OccurredAt.After(now) {
			return s.RegistrationReward, nil
		}
	case RewardPublication:
		if s.PublicationEnabled && !s.PublicationSince.IsZero() && !in.OccurredAt.Before(s.PublicationSince) && !in.OccurredAt.After(now) && in.ContentLength >= s.PublicationMinLength {
			return s.PublicationReward, nil
		}
	}
	return 0, ErrRewardIneligible
}

func IsReward(kind string) bool {
	return kind == RewardCheckIn || kind == RewardRegistration || kind == RewardPublication
}
func RewardReason(kind string) string {
	switch kind {
	case RewardCheckIn:
		return "签到奖励"
	case RewardRegistration:
		return "注册奖励"
	case RewardPublication:
		return "发文奖励"
	}
	return ""
}

// FormatAmount handles MinInt64 without negating it or using floating point.
func FormatAmount(amount int64, precision int) string {
	value := strconv.FormatInt(amount, 10)
	if precision <= 0 || precision > 6 {
		return value
	}
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	if len(value) <= precision {
		value = strings.Repeat("0", precision+1-len(value)) + value
	}
	value = value[:len(value)-precision] + "." + value[len(value)-precision:]
	if negative {
		return "-" + value
	}
	return value
}

func ParseAmount(value string, precision int) (int64, error) {
	if precision < 0 || precision > 6 || value == "" || len(value) > 27 {
		return 0, ErrInvalid
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || len(parts[0]) > 19 || len(parts[0]) > 1 && parts[0][0] == '0' {
		return 0, ErrInvalid
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return 0, ErrInvalid
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, ErrInvalid
	}
	var fraction int64
	if len(parts) == 2 {
		if precision == 0 || len(parts[1]) == 0 || len(parts[1]) > precision {
			return 0, ErrInvalid
		}
		for _, digit := range parts[1] {
			if digit < '0' || digit > '9' {
				return 0, ErrInvalid
			}
			fraction = fraction*10 + int64(digit-'0')
		}
		for range precision - len(parts[1]) {
			fraction *= 10
		}
	}
	divisor := int64(1)
	for range precision {
		divisor *= 10
	}
	if whole > (math.MaxInt64-fraction)/divisor {
		return 0, ErrInvalid
	}
	amount := whole*divisor + fraction
	if amount < 1 {
		return 0, ErrInvalid
	}
	return amount, nil
}
