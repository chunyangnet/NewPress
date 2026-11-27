package payment

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestDialResolvedAddressesFallsBackAfterConnectFailure(t *testing.T) {
	first, peer := net.Pipe()
	defer first.Close()
	defer peer.Close()
	addresses := []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("1.1.1.1")}
	attempted := make([]string, 0, len(addresses))
	conn, err := dialResolvedAddresses(context.Background(), "tcp", "443", addresses, func(_ context.Context, _, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		if len(attempted) == 1 {
			return nil, errors.New("first address is unavailable")
		}
		return first, nil
	})
	if err != nil || conn != first {
		t.Fatalf("fallback dial = %v, %v", conn, err)
	}
	if len(attempted) != 2 || attempted[1] != "1.1.1.1:443" {
		t.Fatalf("dial attempts = %v", attempted)
	}
}

func TestPaymentRejectsReservedAndTranslatedAddresses(t *testing.T) {
	for _, raw := range []string{
		"192.0.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1",
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::a00:1",
		"::192.168.1.1", "fec0::1", "100::1", "100:0:0:1::1",
		"2001:2::1", "2001:20::1", "2001:db8::1", "2002:7f00:1::1", "3fff::1", "5f00::1",
	} {
		t.Run(raw, func(t *testing.T) {
			if publicIP(net.ParseIP(raw)) {
				t.Error("outbound dial accepted a reserved or translated address")
			}
			endpoint := "https://" + net.JoinHostPort(raw, "443")
			if _, err := New(Config{Provider: "epay", Endpoint: endpoint, MerchantID: "1", Secret: "test-secret"}, nil); err == nil {
				t.Error("payment configuration accepted a reserved or translated address")
			}
		})
	}
	for _, raw := range []string{"1.1.1.1", "2606:4700:4700::1111", "::ffff:1.1.1.1"} {
		if !publicIP(net.ParseIP(raw)) {
			t.Errorf("public address rejected: %s", raw)
		}
	}
	if publicIP(nil) {
		t.Error("invalid address accepted")
	}
}
