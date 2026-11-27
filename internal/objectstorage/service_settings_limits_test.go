package objectstorage

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

type settingsLimitBackend struct {
	*memoryBackend
	closed bool
}

func (b *settingsLimitBackend) Close() error {
	b.closed = true
	return nil
}

func storageSettingsFile(t *testing.T, manager *plugin.Manager) string {
	t.Helper()
	var path string
	if err := manager.WithData(plugin.ObjectStorage, false, 0, func(dir string, _ uint64) error {
		path = filepath.Join(dir, "settings.json")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

func settingsLimitProfiles(c Settings, count int) Settings {
	profile := c.Profiles[0]
	c.Profiles = make([]Profile, count)
	for i := range c.Profiles {
		c.Profiles[i] = profile
		c.Profiles[i].ID = fmt.Sprintf("storage-%d", i)
	}
	c.ActiveProfile = c.Profiles[0].ID
	return c
}

func TestStorageSettingsRejectUnreadableSaves(t *testing.T) {
	for _, escaped := range []bool{false, true} {
		name := "aggregate_credentials"
		if escaped {
			name = "json_escaping"
		}
		t.Run(name, func(t *testing.T) {
			s, manager, library, memory := storageFixture(t)
			t.Cleanup(s.Close)
			backend := &settingsLimitBackend{memoryBackend: memory}
			s.factory = func(Profile) (Backend, error) { return backend, nil }
			beforeSettings := s.settings
			if _, err := s.backend(beforeSettings.Profiles[0]); err != nil {
				t.Fatal(err)
			}
			beforeContext := s.configContext
			path := storageSettingsFile(t, manager)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			clearStorageWake(s)
			count, credential := 8, "x"
			if escaped {
				count, credential = 1, "&"
			}
			candidate := settingsLimitProfiles(beforeSettings, count)
			for i := range candidate.Profiles {
				candidate.Profiles[i].AccessKey = strings.Repeat(credential, 8192)
				candidate.Profiles[i].SecretKey = strings.Repeat(credential, 8192)
				candidate.Profiles[i].SessionToken = strings.Repeat(credential, 8192)
			}
			if _, err := normalizeSettings(candidate); err != nil {
				t.Fatalf("fixture must satisfy individual field limits: %v", err)
			}
			encoded, err := json.Marshal(candidate)
			if err != nil || len(encoded) <= 128<<10 {
				t.Fatalf("fixture must exceed the reader limit: bytes=%d err=%v", len(encoded), err)
			}
			t.Logf("individually valid settings serialize to %d bytes", len(encoded))
			_, saveErr := s.Save(candidate, nil)
			if saveErr == nil {
				t.Error("save accepted settings that exceed the restart reader limit")
			} else if !strings.Contains(saveErr.Error(), "128 KiB") {
				t.Errorf("expected actionable size error, got %v", saveErr)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Errorf("rejected save changed persisted settings: bytes=%d err=%v", len(after), err)
			}
			if !reflect.DeepEqual(s.settings, beforeSettings) || s.configContext != beforeContext || beforeContext.Err() != nil || backend.closed {
				t.Error("rejected save changed configuration or cancelled the existing backend")
			}
			if cached, err := s.backend(beforeSettings.Profiles[0]); err != nil || cached != backend {
				t.Errorf("existing connection no longer usable: %v", err)
			}
			if len(s.wake) != 0 {
				t.Error("rejected save scheduled background work")
			}
			unchangedInput, err := json.Marshal(candidate)
			if err != nil || !bytes.Equal(encoded, unchangedInput) {
				t.Error("save modified the caller's settings")
			}
			restarted := New(manager, library)
			t.Cleanup(restarted.Close)
			if err := restarted.load(false); err != nil {
				t.Errorf("restart cannot read previous settings: %v", err)
			} else if !reflect.DeepEqual(restarted.settings, beforeSettings) {
				t.Error("restart did not preserve the previous settings")
			}
		})
	}
}

func TestStorageSettingsSerializedBoundary(t *testing.T) {
	for _, excess := range []int{0, 1} {
		t.Run(fmt.Sprintf("excess_%d", excess), func(t *testing.T) {
			s, manager, library, _ := storageFixture(t)
			t.Cleanup(s.Close)
			// The final version grows from one digit to two. Its serialized byte
			// must count toward the limit along with normalized profile defaults.
			for s.settings.Version < 9 {
				if _, err := s.Save(sanitized(s.settings).Settings, nil); err != nil {
					t.Fatal(err)
				}
			}
			candidate := settingsLimitProfiles(s.settings, 8)
			for i := range candidate.Profiles {
				candidate.Profiles[i].AccessKey = "a"
				candidate.Profiles[i].SecretKey = "s"
				candidate.Profiles[i].SessionToken = "t"
				candidate.Profiles[i].TimeoutSeconds = 0
			}
			persisted := candidate
			persisted.Profiles = append([]Profile(nil), candidate.Profiles...)
			persisted.Version++
			persisted, err := normalizeSettings(persisted)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(persisted)
			if err != nil {
				t.Fatal(err)
			}
			target := (128 << 10) + excess
			remaining := target - len(data)
			for i := range candidate.Profiles {
				p := &candidate.Profiles[i]
				for _, field := range []*string{&p.AccessKey, &p.SecretKey, &p.SessionToken} {
					n := min(8192-len(*field), remaining)
					*field += strings.Repeat("x", n)
					remaining -= n
				}
			}
			if remaining != 0 {
				t.Fatal("fixture cannot reach serialized boundary")
			}
			path := storageSettingsFile(t, manager)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Save(candidate, nil)
			if excess == 0 && err != nil {
				t.Fatalf("exactly 128 KiB must remain readable: %v", err)
			}
			if excess > 0 && err == nil {
				t.Error("accepted one byte beyond the serialized limit")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if excess == 0 && len(after) != target {
				t.Fatalf("boundary fixture wrote %d bytes, wanted %d", len(after), target)
			}
			if excess > 0 && !bytes.Equal(before, after) {
				t.Error("oversized save replaced the previous file")
			}
			restarted := New(manager, library)
			t.Cleanup(restarted.Close)
			if err := restarted.load(false); err != nil {
				t.Fatalf("restart at serialized boundary: %v", err)
			}
			if !reflect.DeepEqual(restarted.settings, s.settings) {
				t.Error("saved and reloaded settings differ")
			}
			if excess == 0 {
				if _, err := restarted.Save(sanitized(restarted.settings).Settings, nil); err != nil {
					t.Fatalf("blank secrets must retain a valid boundary configuration: %v", err)
				}
			}
		})
	}
}
