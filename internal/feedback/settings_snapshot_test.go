package feedback

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFeedbackSettingsInputsDoNotMutateStoredBoard(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, action := range []string{"create", "configure"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				path := ""
				if mode == "file" {
					path = filepath.Join(t.TempDir(), "feedback.json")
				}
				s, err := NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				ctx := t.Context()
				now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
				admin := Actor{ID: 1, Admin: true}
				reader := Actor{ID: 2, Roles: []string{"subscriber"}}
				settings := Defaults()
				created, err := s.Apply(ctx, "ideas", admin, Command{
					Action: "create", Key: "settings-create-01", Settings: &settings,
				}, now)
				if err != nil {
					t.Fatal(err)
				}
				if action == "configure" {
					settings = Defaults()
					settings.Name = "Updated board"
					if _, err := s.Apply(ctx, "ideas", admin, Command{
						Action: "configure", Key: "settings-update-01", Version: created.Version, Settings: &settings,
					}, now); err != nil {
						t.Fatal(err)
					}
				}
				before, err := s.Get(ctx, "ideas")
				if err != nil {
					t.Fatal(err)
				}
				var saved []byte
				if path != "" {
					saved, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}

				// A caller may reuse its input after saving. This must not silently
				// change permissions, accepted kinds, or categories in the store.
				settings.Permissions[1].Moderate = true
				settings.Kinds[0] = "feedback"
				settings.Categories[0] = "Unsaved category"
				got, err := s.Get(ctx, "ideas")
				if err != nil {
					t.Fatal(err)
				}
				if Can(got, reader, "moderate") {
					t.Error("mutating the command granted moderation without a saved configuration")
				}
				if !reflect.DeepEqual(got, before) {
					t.Error("mutating the command changed the stored board")
				}
				headers, err := s.List(ctx)
				if err != nil || len(headers) != 1 || !reflect.DeepEqual(headers[0], Header(before)) {
					t.Errorf("List did not preserve the saved configuration: %v", err)
				}
				if path != "" {
					current, err := os.ReadFile(path)
					if err != nil || string(current) != string(saved) {
						t.Fatalf("configuration file changed without Apply: %v", err)
					}
				}

				// The next unrelated write must use and persist the saved settings.
				if _, err := s.Apply(ctx, "ideas", reader, Command{
					Action: "submit", Key: "settings-submit-01", Version: before.Version,
					Title: "Keep saved configuration", Body: "The caller reused its configuration object.",
					Kind: before.Settings.Kinds[0], Category: before.Settings.Categories[0],
				}, now); err != nil {
					t.Fatalf("submission using saved settings failed: %v", err)
				}
				if path != "" {
					s, err = NewFileStore(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				got, err = s.Get(ctx, "ideas")
				if err != nil || !reflect.DeepEqual(got.Settings, before.Settings) || len(got.Items) != 1 {
					t.Fatalf("saved settings were not retained after the next write: %v", err)
				}
			})
		}
	}
}
