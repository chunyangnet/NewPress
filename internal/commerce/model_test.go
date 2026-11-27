package commerce

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/payment"
)

func TestOrderIDsUseCurrentLength(t *testing.T) {
	if id := NewID(); !ValidOrderID(id) || len(id) != 31 {
		t.Fatalf("generated order ID is invalid: %q", id)
	}
	for _, id := range []string{"np_" + strings.Repeat("a", 32), "np_" + strings.Repeat("a", 27), "np_" + strings.Repeat("A", 28), "np_" + strings.Repeat("z", 28)} {
		if ValidOrderID(id) {
			t.Fatalf("accepted invalid or retired order ID %q", id)
		}
	}
}

func TestExchangeAmountUsesExactIntegerFeesAndRejectsOverflow(t *testing.T) {
	for _, test := range []struct {
		name             string
		amount, from, to int64
		fee              int
		want             int64
		invalid          bool
	}{
		{"fee rounds down once", 10000, 100, 3, 250, 292, false},
		{"fractional minor unit", 101, 100, 3, 250, 2, false},
		{"precision conversion", 100, 100, 10000, 125, 9875, false},
		{"large exact integer", 9007199254740993, 1, 1, 0, 9007199254740993, false},
		{"maximum exact", math.MaxInt64, 1, 1, 0, math.MaxInt64, false},
		{"overflow", math.MaxInt64, 1, 2, 0, 0, true},
		{"zero rounded result", 1, 100, 1, 0, 0, true},
		{"invalid fee", 100, 1, 1, 5001, 0, true},
		{"negative fee", 100, 1, 1, -1, 0, true},
		{"zero divisor", 100, 0, 1, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := ExchangeAmount(test.amount, Exchange{FromUnits: test.from, ToUnits: test.to, FeeBPS: test.fee, Minimum: 1, Maximum: math.MaxInt64})
			if test.invalid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("accepted invalid result: %d %v", value, err)
				}
			} else if err != nil || value != test.want {
				t.Fatalf("got %d %v, want %d", value, err, test.want)
			}
		})
	}
	for _, amount := range []int64{0, 99, 1001} {
		if _, err := ExchangeAmount(amount, Exchange{FromUnits: 1, ToUnits: 1, Minimum: 100, Maximum: 1000}); !errors.Is(err, ErrInvalid) {
			t.Fatal("out-of-bounds exchange accepted", amount, err)
		}
	}
}

func TestPaymentVaultRejectsTamperingAndPublicOrderOmitsSecrets(t *testing.T) {
	vault, err := NewVault(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := payment.Config{Provider: "epay", Secret: "never-expose-this-secret", MerchantID: "fixture-merchant"}
	one, err := vault.Seal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	two, err := vault.Seal(cfg)
	if err != nil || one == two {
		t.Fatal("encryption reused nonce", err)
	}
	plain, err := vault.Open(one)
	if err != nil || plain != cfg {
		t.Fatal("encrypted config round trip failed", plain, err)
	}
	data, _ := base64.RawStdEncoding.DecodeString(one)
	data[len(data)-1] ^= 1
	if _, err = vault.Open(base64.RawStdEncoding.EncodeToString(data)); !errors.Is(err, ErrUnavailable) {
		t.Fatal("modified ciphertext accepted", err)
	}
	other, _ := NewVault(bytes.Repeat([]byte{2}, 32))
	if _, err = other.Open(one); !errors.Is(err, ErrUnavailable) {
		t.Fatal("different key accepted ciphertext", err)
	}
	for _, raw := range []string{"", "not-base64", base64.RawStdEncoding.EncodeToString([]byte("short"))} {
		if _, err = vault.Open(raw); err == nil {
			t.Fatal("invalid ciphertext accepted")
		}
	}
	o := Order{ID: NewID(), SealedConfig: one, Fingerprint: "private-fingerprint", UserID: 2, Amount: 1250, Precision: 2, Currency: "CNY", Credit: 12345, TargetPrecision: 2}
	public, err := json.Marshal(PublicOrder(o))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{one, cfg.Secret, o.Fingerprint, "sealed_config", "lease_until"} {
		if strings.Contains(string(public), private) {
			t.Fatal("private payment state exposed", private)
		}
	}
	if !strings.Contains(string(public), `"amount":"12.50"`) || !strings.Contains(string(public), `"credit":"123.45"`) {
		t.Fatal("public amount lost precision", string(public))
	}
}

func TestCommerceBackendDetectsExternalWritesAndDoesNotLeakReadAliases(t *testing.T) {
	f := newCommerceFixture(t)
	first, err := f.service.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first.Settings.Channels[0].Name = "mutated outside transaction"
	first.Orders[NewID()] = Order{}
	again, err := f.service.Backend.Read(t.Context())
	if err != nil || again.Settings.Channels[0].Name == first.Settings.Channels[0].Name || len(again.Orders) != 0 {
		t.Fatal("read mutation changed stored state", err)
	}
	path := filepath.Join(f.dir, "commerce.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Backend.Read(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("external state write ignored", err)
	}
	if err = f.service.Backend.Update(t.Context(), func(*State) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal("external write overwritten", err)
	}
}

func TestCheckoutURLRequiresHTTPSWithoutEmbeddedCredentials(t *testing.T) {
	for _, raw := range []string{"https://pay.example/checkout?q=one", "https://pay.example:8443/checkout"} {
		if !SafeCheckoutURL(raw) {
			t.Fatal("valid checkout URL rejected", raw)
		}
	}
	for _, raw := range []string{"http://pay.example", "javascript:alert(1)", "//pay.example/checkout", "https://user:secret@pay.example/checkout", "https:///missing-host", "data:text/html,payment"} {
		if SafeCheckoutURL(raw) {
			t.Fatal("unsafe checkout URL accepted", raw)
		}
	}
}

func TestCommerceSettingsWriteDoesNotRetainCallerSlices(t *testing.T) {
	f := newCommerceFixture(t)
	cfg, err := f.service.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := f.service.UpdateSettings(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Channels[0].Name = "unsaved caller edit"
	saved.Products[0].Credit = 1
	stored, err := f.service.Settings(t.Context())
	if err != nil || stored.Channels[0].Name == cfg.Channels[0].Name || stored.Products[0].Credit != 12345 {
		t.Fatal("caller aliases changed committed configuration without a transaction", stored.Channels[0].Name, stored.Products[0].Credit, err)
	}
}
