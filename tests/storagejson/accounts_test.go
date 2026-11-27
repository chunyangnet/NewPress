package storagejson_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/accessconfig"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/messaging"
)

func TestAccountStorageJSONIntegrity(t *testing.T) {
	const marker = "stored-fixture"
	stores := []struct {
		name, field string
		create      func(string) error
		read        func(string) (string, error)
	}{
		{"credentials", "secret", func(path string) error {
			s, err := accessconfig.NewStore(path)
			if err != nil {
				return err
			}
			settings := s.Runtime()
			settings.Providers = []accessconfig.Provider{{ID: "github", Enabled: true, ClientID: "fixture", Secret: marker}}
			_, err = s.Save(settings)
			return err
		}, func(path string) (string, error) {
			s, err := accessconfig.NewStore(path)
			if err != nil {
				return "", err
			}
			return s.Runtime().Providers[0].Secret, nil
		}},
		{"bookmarks", "collection", func(path string) error {
			s, err := engagement.NewFileStore(path)
			if err != nil {
				return err
			}
			return s.Set(t.Context(), engagement.Input{UserID: 1, TargetID: 2, Kind: engagement.Bookmark, Collection: marker, Active: true})
		}, func(path string) (string, error) {
			s, err := engagement.NewFileStore(path)
			if err != nil {
				return "", err
			}
			entries, err := s.Entries(t.Context(), 1, engagement.Bookmark)
			if err != nil {
				return "", err
			}
			return entries[0].Collection, nil
		}},
		{"messages", "content", func(path string) error {
			s, err := messaging.NewFileStore(path)
			if err != nil {
				return err
			}
			_, err = s.Send(t.Context(), messaging.Input{SenderID: 1, RecipientID: 2, Content: marker, Nonce: "json-fixture-message"})
			return err
		}, func(path string) (string, error) {
			s, err := messaging.NewFileStore(path)
			if err != nil {
				return "", err
			}
			page, err := s.Messages(t.Context(), 1, 2, messaging.MessageQuery{})
			if err != nil {
				return "", err
			}
			return page.Items[0].Content, nil
		}},
	}
	for _, store := range stores {
		t.Run(store.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := store.create(path); err != nil {
				t.Fatal(err)
			}
			if got, err := store.read(path); err != nil || got != marker {
				t.Fatal("invalid fixture", err)
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, replacement, want string
				valid                   bool
			}{
				{"invalid UTF8", "private-\xff", "", false},
				{"unpaired surrogate", `private-\ud800`, "", false},
				{"duplicate", `first","` + store.field + `":"second`, "", false},
				{"alias", `first","` + strings.ToUpper(store.field) + `":"second`, "", false},
				{"paired surrogate", `中文\ud83d\ude00`, "中文😀", true},
				{"intentional replacement", `文本�\ufffd`, "文本��", true},
			} {
				t.Run(test.name, func(t *testing.T) {
					payload := bytes.ReplaceAll(original, []byte(marker), []byte(test.replacement))
					if !json.Valid(payload) {
						t.Fatal("fixture has invalid JSON syntax")
					}
					if err := os.WriteFile(path, payload, 0600); err != nil {
						t.Fatal(err)
					}
					got, err := store.read(path)
					if test.valid {
						if err != nil || got != test.want {
							t.Fatal("valid text did not survive loading", err)
						}
					} else if err == nil {
						t.Error("accepted damaged or ambiguous persisted data")
					} else if strings.Contains(err.Error(), "private-") {
						t.Error("error disclosed persisted private text")
					}
					if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, payload) {
						t.Fatal("load modified source data", err)
					}
				})
			}
		})
	}
}

func TestAccountStorageRejectsIncompleteRoot(t *testing.T) {
	for _, store := range []struct {
		name, empty string
		open        func(string) error
	}{
		{"messages", `{"next_id":1,"messages":[],"notices":[]}`, func(path string) error { _, err := messaging.NewFileStore(path); return err }},
		{"bookmarks", `{"version":1,"entries":[]}`, func(path string) error { _, err := engagement.NewFileStore(path); return err }},
	} {
		t.Run(store.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			for _, body := range []string{" \nnull\t", "{}"} {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if err := store.open(path); err == nil {
					t.Errorf("incomplete document %q was treated as an empty account store", body)
				}
			}
			if err := os.WriteFile(path, []byte(store.empty), 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.open(path); err != nil {
				t.Fatal("valid empty account store rejected", err)
			}
		})
	}
}
