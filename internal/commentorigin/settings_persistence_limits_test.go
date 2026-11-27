package commentorigin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/plugin"
)

func originSettingsSnapshot(t *testing.T, service *Service, root string) (Settings, []byte) {
	t.Helper()
	settings, err := service.Settings()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, plugin.CommentOrigin, "data", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return settings, data
}

func TestOriginSettingsRejectUnreadableSave(t *testing.T) {
	for _, scenario := range []string{"plain_size", "json_escaping", "retained_key"} {
		t.Run(scenario, func(t *testing.T) {
			service, _, root := originFixture(t)
			initial := Defaults()
			initial.Provider = "baidu"
			initial.Key = "retained-key-sentinel" + strings.Repeat("&", 480)
			if _, err := service.Save(initial, false); err != nil {
				t.Fatal(err)
			}
			before, original := originSettingsSnapshot(t, service, root)
			<-service.wake
			candidate := before
			candidate.Key = ""
			switch scenario {
			case "plain_size":
				candidate.Endpoint = strings.Repeat("x", 32<<10)
			case "json_escaping":
				candidate.Endpoint = strings.Repeat("&", 6000)
			case "retained_key":
				encoded, err := json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
				candidate.Endpoint = strings.Repeat("x", (32<<10)-512-len(encoded))
			}
			merged := candidate
			merged.Key = before.Key
			merged.Version++
			if err := validate(merged); err != nil {
				t.Fatalf("fixture must satisfy provider validation: %v", err)
			}
			encoded, err := json.Marshal(merged)
			if err != nil || len(encoded) <= 32<<10 {
				t.Fatal("fixture must exceed the persisted size limit")
			}
			t.Logf("merged settings: %d bytes", len(encoded))
			view, err := service.Save(candidate, false)
			if err == nil {
				t.Error("saved a configuration larger than the restart reader accepts")
			} else if !strings.Contains(err.Error(), "32 KiB") {
				t.Errorf("expected actionable size error: %v", err)
			}
			if view.Key != "" || err != nil && strings.Contains(err.Error(), "retained-key-sentinel") {
				t.Error("rejected save exposed a retained key")
			}
			after, readErr := os.ReadFile(filepath.Join(root, plugin.CommentOrigin, "data", "settings.json"))
			if readErr != nil || !bytes.Equal(after, original) {
				t.Errorf("rejected save changed file: bytes=%d err=%v", len(after), readErr)
			}
			if len(service.wake) != 0 {
				t.Error("rejected save scheduled work")
			}
			restarted := New(plugin.NewManager(root))
			reloaded, err := restarted.Settings()
			if err != nil {
				t.Fatalf("restart cannot read previous settings: %v", err)
			}
			if !reflect.DeepEqual(reloaded, before) {
				t.Error("rejected save changed values, key or version")
			}
			candidate.Endpoint = "https://geo.example.test/?ip={ip}"
			if _, err := restarted.Save(candidate, false); err != nil {
				t.Fatalf("valid retry failed: %v", err)
			}
			reloaded, err = New(plugin.NewManager(root)).Settings()
			if err != nil || reloaded.Key != before.Key || reloaded.Version != before.Version+1 {
				t.Errorf("valid retry lost key or version: %v", err)
			}
		})
	}
}

func TestOriginSettingsSerializedBoundary(t *testing.T) {
	for _, excess := range []int{0, 1} {
		t.Run(fmt.Sprintf("excess_%d", excess), func(t *testing.T) {
			service, _, root := originFixture(t)
			config := Defaults()
			config.Key = "boundary-private-key"
			for config.Version < 9 {
				view, err := service.Save(config, false)
				if err != nil {
					t.Fatal(err)
				}
				config = view
			}
			before, original := originSettingsSnapshot(t, service, root)
			candidate := before
			candidate.Key = ""
			candidate.FlagMode = "off"
			persisted := candidate
			persisted.Key = before.Key
			persisted.FlagMode = "off"
			persisted.Version++
			encoded, err := json.Marshal(persisted)
			if err != nil {
				t.Fatal(err)
			}
			target := (32 << 10) + excess
			candidate.Endpoint = strings.Repeat("x", target-len(encoded))
			view, err := service.Save(candidate, false)
			if excess > 0 {
				if err == nil {
					t.Error("accepted one byte beyond the serialized limit")
				}
				after, readErr := os.ReadFile(filepath.Join(root, plugin.CommentOrigin, "data", "settings.json"))
				if readErr != nil || !bytes.Equal(after, original) {
					t.Error("oversized boundary save replaced previous configuration")
				}
				reloaded, readErr := New(plugin.NewManager(root)).Settings()
				if readErr != nil || !reflect.DeepEqual(reloaded, before) {
					t.Errorf("previous configuration cannot be recovered: %v", readErr)
				}
				return
			}
			if err != nil || view.Key != "" || view.Version != 10 || view.FlagMode != "off" {
				t.Fatalf("valid boundary was rejected or exposed key: %v", err)
			}
			restarted := New(plugin.NewManager(root))
			reloaded, data := originSettingsSnapshot(t, restarted, root)
			if len(data) != target || reloaded.Endpoint != candidate.Endpoint || reloaded.Key != before.Key {
				t.Fatal("exact boundary configuration did not round-trip")
			}
			if view, err = restarted.Save(view, false); err != nil {
				t.Fatalf("retained-key resave at limit failed: %v", err)
			}
			if _, err := restarted.Save(view, true); err != nil {
				t.Fatalf("explicit key removal failed: %v", err)
			}
			cleared, err := New(plugin.NewManager(root)).Settings()
			if err != nil || cleared.Key != "" || cleared.Endpoint != candidate.Endpoint {
				t.Errorf("clear key did not preserve other settings: %v", err)
			}
		})
	}
}

func TestOriginProviderSwitchPreservesCustomFields(t *testing.T) {
	service, _, root := originFixture(t)
	config := Defaults()
	config.Provider = "custom"
	config.Endpoint = "https://geo.example.test/lookup?ip={ip}&key={key}"
	config.CountryPath, config.CountryCodePath = "data.country", "data.country_code"
	config.RegionPath, config.CityPath = "data.region", "data.city"
	config.SuccessPath, config.SuccessValue = "status", "ok"
	config.Key = "custom-key-sentinel"
	view, err := service.Save(config, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"ipwhois", "custom"} {
		view.Provider = provider
		view, err = service.Save(view, false)
		if err != nil {
			t.Fatal(err)
		}
		reloaded, err := New(plugin.NewManager(root)).Settings()
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Key != "" {
			t.Fatal("provider change carried over a private key")
		}
		want := config
		want.Provider, want.Key, want.Version = provider, "", reloaded.Version
		if !reflect.DeepEqual(reloaded, want) {
			t.Error("provider switch discarded custom endpoint or field mappings")
		}
	}
}
