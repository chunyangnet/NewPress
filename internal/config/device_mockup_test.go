package config

import (
	"strings"
	"testing"
)

func TestDeviceMockupWorkerOrigin(t *testing.T) {
	for _, test := range []struct {
		url   string
		valid bool
	}{
		{"http://127.0.0.1:4173", true},
		{"http://localhost:4180/", true},
		{"http://127.0.0.1", true},
		{"https://example.com", false},
		{"http://192.168.0.1:4173", false},
		{"http://127.0.0.1:0", false},
		{"http://127.0.0.1:99999", false},
		{"http://user:secret@127.0.0.1:4173", false},
		{"http://127.0.0.1:4173/api", false},
		{"http://127.0.0.1:4173?target=x", false},
		{"http://127.0.0.1:4173#fragment", false},
	} {
		t.Run(test.url, func(t *testing.T) {
			isolateConfigEnvironment(t)
			t.Setenv("NEWPRESS_DEVICE_MOCKUP_URL", test.url)
			cfg, err := Load()
			if test.valid {
				if err != nil || cfg.DeviceMockupURL != strings.TrimRight(test.url, "/") {
					t.Fatalf("worker URL = %q, error %v", cfg.DeviceMockupURL, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "NEWPRESS_DEVICE_MOCKUP_URL") {
				t.Fatalf("unsafe worker URL accepted: %v", err)
			}
		})
	}
}
