package wechat

import (
	"net"
	"testing"
)

func TestWeChatRejectsReservedAndTranslatedAddresses(t *testing.T) {
	for _, raw := range []string{
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::a00:1",
		"::192.168.1.1", "fec0::1", "100::1", "100:0:0:1::1",
		"2001:2::1", "2001:20::1", "3fff::1", "5f00::1",
	} {
		t.Run(raw, func(t *testing.T) {
			if publicIP(net.ParseIP(raw)) {
				t.Error("outbound dial accepted a reserved or translated DNS answer")
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
