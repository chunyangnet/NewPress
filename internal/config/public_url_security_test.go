package config

import "testing"

func TestPublicURLRequiresTLSOutsideLoopback(t *testing.T) {
	for _, raw := range []string{"http://example.com", "http://192.168.1.2", "http://0.0.0.0:8080", "http://localhost.attacker.test", "http://[::]"} {
		if _, err := normalizePublicURL(raw); err == nil {
			t.Errorf("accepted insecure public origin %q", raw)
		}
	}
	for _, raw := range []string{"https://example.com", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := normalizePublicURL(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
}
