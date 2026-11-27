package notification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotificationStoreRejectsAliasedPreferenceKeys(t *testing.T) {
	for _, alias := range []string{"01", "+1", "0001"} {
		t.Run(alias, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "notifications.json")
			store, err := NewFileStore(filename)
			if err != nil {
				t.Fatal(err)
			}
			original, err := store.SavePreferences(t.Context(), Defaults(1), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			changed := original
			changed.Email, changed.EmailSince = true, original.UpdatedAt
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			// The visible first preference opts out; the alias tries to opt in.
			needle := []byte(`"preferences":{"1":`)
			injection := []byte(fmt.Sprintf(`"preferences":{"1":%s,%q:`, mustPreferenceJSON(t, original), alias))
			body = bytes.Replace(body, needle, injection, 1)
			firstJSON := mustPreferenceJSON(t, original)
			aliasStart := []byte(fmt.Sprintf(`%q:%s`, alias, firstJSON))
			body = bytes.Replace(body, aliasStart, []byte(fmt.Sprintf(`%q:%s`, alias, encoded)), 1)
			if !bytes.Contains(body, []byte(fmt.Sprintf(`%q:`, alias))) {
				t.Fatal("fixture did not inject integer alias")
			}
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if loaded, err := NewFileStore(filename); err == nil || loaded != nil {
				t.Error("ambiguous preference file reopened")
			}
			if _, err := store.SavePreferences(t.Context(), original, time.Now().UTC()); err == nil {
				t.Error("external ambiguous file was overwritten")
			}
			current, err := store.Preferences(t.Context(), 1)
			if err != nil || current != original {
				t.Errorf("existing store preference changed: %#v, %v", current, err)
			}
			if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(after, body) {
				t.Errorf("rejection changed file: %v", err)
			}
		})
	}
}

func mustPreferenceJSON(t *testing.T, value Preferences) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestNotificationStoreRejectsNoncanonicalPreferenceKey(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "notifications.json")
	store, err := NewFileStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SavePreferences(t.Context(), Defaults(1), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"preferences":{"1":`), []byte(`"preferences":{"01":`), 1)
	if err := os.WriteFile(filename, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(filename); err == nil {
		t.Fatal("accepted noncanonical preference key")
	}
	if after, err := os.ReadFile(filename); err != nil || !bytes.Equal(body, after) {
		t.Fatal("rejection modified stored preferences", err)
	}
}
