package storagejson_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/linkpage"
)

// Exercise both persisted documents and the aggregate decoders used by database
// adapters. Neither path may silently replace text or accept field aliases.
func TestStorageJSONIntegrity(t *testing.T) {
	const marker = "storage-json-fixture"
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	circle, _, err := community.Mutate(nil, "fixture", community.Actor{ID: 1, Admin: true}, community.Command{Action: "create", Key: "json-fixture-create", Name: marker, OwnerID: 1, JoinPolicy: "open", Visibility: "public"}, now)
	if err != nil {
		t.Fatal(err)
	}
	feedbackSettings := feedback.Defaults()
	feedbackSettings.Name = marker
	board, _, err := feedback.Mutate(nil, "fixture", feedback.Actor{ID: 1, Admin: true}, feedback.Command{Action: "create", Key: "json-fixture-create", Settings: &feedbackSettings}, now)
	if err != nil {
		t.Fatal(err)
	}
	linkSettings := linkpage.Defaults()
	linkSettings.Name = marker
	directory, _, err := linkpage.Mutate(nil, "fixture", linkpage.Actor{ID: 1, Admin: true}, linkpage.Command{Action: "create", Key: "json-fixture-create", ExpectedUserID: 1, Settings: &linkSettings}, now)
	if err != nil {
		t.Fatal(err)
	}
	stores := []struct {
		name, collection string
		value            any
		load             func(string) (string, error)
		decode           func([]byte) (string, error)
	}{
		{"community", "circles", circle, func(path string) (string, error) {
			s, err := community.NewFileStore(path)
			if err != nil {
				return "", err
			}
			v, err := s.Get(t.Context(), "fixture")
			return v.Name, err
		}, func(data []byte) (string, error) { v, err := community.DecodeCircle(data); return v.Name, err }},
		{"feedback", "boards", board, func(path string) (string, error) {
			s, err := feedback.NewFileStore(path)
			if err != nil {
				return "", err
			}
			v, err := s.Get(t.Context(), "fixture")
			return v.Settings.Name, err
		}, func(data []byte) (string, error) { v, err := feedback.DecodeBoard(data); return v.Settings.Name, err }},
		{"linkpage", "boards", directory, func(path string) (string, error) {
			s, err := linkpage.NewFileStore(path)
			if err != nil {
				return "", err
			}
			v, err := s.Get(t.Context(), "fixture")
			return v.Settings.Name, err
		}, func(data []byte) (string, error) { v, err := linkpage.DecodeBoard(data); return v.Settings.Name, err }},
	}
	for _, store := range stores {
		t.Run(store.name, func(t *testing.T) {
			original, err := json.Marshal(store.value)
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, field, want string
				valid             bool
			}{
				{"invalid UTF8", "\"name\":\"private-\xff\"", "", false},
				{"truncated UTF8", "\"name\":\"private-\xe4\xb8\"", "", false},
				{"high surrogate", `"name":"private-\ud800"`, "", false},
				{"low surrogate", `"name":"private-\udfff"`, "", false},
				{"field alias", `"name":"first","NAME":"second"`, "", false},
				{"duplicate field", `"name":"first","name":"second"`, "", false},
				{"Chinese and emoji", `"name":"中文😀"`, "中文😀", true},
				{"paired surrogate", `"name":"\u4e2d\u6587\ud83d\ude00"`, "中文😀", true},
				{"intentional replacement", `"name":"替换�\ufffd"`, "替换��", true},
				{"literal escape", `"name":"literal-\\ud800"`, `literal-\ud800`, true},
			} {
				t.Run(test.name, func(t *testing.T) {
					payload := strings.Replace(string(original), `"name":"`+marker+`"`, test.field, 1)
					if payload == string(original) {
						t.Fatal("fixture name was not replaced")
					}
					check := func(name string, value string, err error) {
						t.Helper()
						if test.valid {
							if err != nil || value != test.want {
								t.Errorf("%s changed valid text: got %q, want %q, error %v", name, value, test.want, err)
							}
						} else if err == nil {
							t.Errorf("%s accepted lossy or ambiguous JSON as %q", name, value)
						}
					}
					value, err := store.decode([]byte(payload))
					check("aggregate", value, err)
					document := []byte(`{"schema":1,"` + store.collection + `":[` + payload + `]}`)
					path := filepath.Join(t.TempDir(), "state.json")
					if err := os.WriteFile(path, document, 0600); err != nil {
						t.Fatal(err)
					}
					value, err = store.load(path)
					check("file", value, err)
					after, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(after, document) {
						t.Fatal("opening the store changed the source document", err)
					}
				})
			}
			t.Run("schema alias", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "state.json")
				data := []byte(`{"schema":2,"SCHEMA":1,"` + store.collection + `":[` + string(original) + `]}`)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := store.load(path); err == nil {
					t.Fatal("conflicting schema aliases were accepted")
				}
			})
		})
	}
}
