package httpserver

import (
	"archive/zip"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/theme"
)

// Only mounted by the opt-in loopback browser test, never production routes.
func childLifecycleFixture(t *testing.T, manager *theme.Manager, root string, next http.Handler) http.Handler {
	t.Helper()
	type candidate struct {
		release theme.Release
		archive string
	}
	candidates := map[string]candidate{}
	for _, version := range []string{"1.1.0", "2.0.0"} {
		source := filepath.Join(root, "starter")
		raw, err := os.ReadFile(filepath.Join(source, "theme.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest theme.Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest.Version = version
		archive := filepath.Join(t.TempDir(), "parent.zip")
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		z := zip.NewWriter(f)
		err = filepath.WalkDir(source, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(source, name)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if rel == "theme.json" {
				data, err = json.Marshal(manifest)
				if err != nil {
					return err
				}
			}
			if filepath.ToSlash(rel) == "assets/css/starter.css" {
				data = append(data, []byte("\n/* parent "+version+" */\n")...)
			}
			out, err := z.Create(filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			_, err = out.Write(data)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = z.Close(); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
		candidates[version] = candidate{theme.Release{Manifest: manifest, Release: &extension.Metadata{Channel: "stable", PublishedAt: time.Now(), DataCompatible: true}}, archive}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__fixture/parent/{action}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "http://"+r.Host {
			http.Error(w, "origin rejected", 403)
			return
		}
		var err error
		if r.PathValue("action") == "rollback" {
			var state extension.VersionState
			state, err = manager.VersionState("starter")
			if err == nil {
				err = manager.Rollback(r.Context(), "starter", "1.1.0", state.Revision, "1.0.0", nil)
			}
		} else if c, ok := candidates[r.PathValue("action")]; ok {
			var target theme.UpdateTarget
			target, err = manager.CaptureUpdate("starter", "", "")
			if err == nil {
				err = manager.UpdatePackage(r.Context(), c.archive, c.release, target, nil)
			}
		} else {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "asset": manager.AssetURL("css/starter.css")})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"active": manager.Active().Slug, "asset": manager.AssetURL("css/starter.css")})
	})
	mux.Handle("/", next)
	return mux
}
