package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func backgroundReaderFixture(t *testing.T) (Manifest, []byte) {
	t.Helper()
	source := filepath.Join("..", "..", "examples", "plugins", "background-reader")
	manifest, err := readManifest(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	build, err := os.ReadFile(filepath.Join("testdata", "background-reader.build.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		SourceSHA256 string `json:"source_sha256"`
		WasmSHA256   string `json:"wasm_sha256"`
	}
	if err := json.Unmarshal(build, &recorded); err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join("testdata", "background-reader.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := os.ReadFile(filepath.Join(source, "plugin.c"))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		data []byte
		want string
	}{{c, recorded.SourceSHA256}, {code, recorded.WasmSHA256}} {
		sum := sha256.Sum256(item.data)
		if hex.EncodeToString(sum[:]) != item.want {
			t.Fatal("rebuild background-reader.wasm from the example and update its build record")
		}
	}
	return manifest, code
}

func TestBackgroundReaderExampleManifest(t *testing.T) {
	manifest, _ := backgroundReaderFixture(t)
	if manifest.Slug != "background-reader" || len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != "settings.read" {
		t.Fatalf("unexpected example permissions: %+v", manifest)
	}
	for _, hook := range []string{"task.execute.v1", "event.deliver.v1"} {
		if exports := manifest.Hooks[hook]; len(exports) != 1 || exports[0] != "np_handle" {
			t.Fatalf("missing example hook %s", hook)
		}
	}
	if len(manifest.Hooks) != 2 {
		t.Fatal("example must remain background-only")
	}
}

func backgroundReaderWorker(t *testing.T, grant bool) (*Manager, *pluginjobs.Queue, *pluginjobs.Worker, TaskEpoch, string) {
	t.Helper()
	manifest, code := backgroundReaderFixture(t)
	m := NewManager(t.TempDir())
	if _, err := m.InstallPackage(createPluginArchive(t, manifest, code)); err != nil {
		t.Fatal(err)
	}
	// Test-only installation credentials stay inside the disposable manager.
	if err := WritePrivate(filepath.Join(m.root, manifest.Slug, ".official.json"), Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfiguration(manifest)
	if grant {
		cfg.Grants = manifest.Capabilities
	}
	if err := m.Configure(manifest.Slug, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnabled(manifest.Slug, true); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := m.Configuration(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ConfigureAs(manifest.Slug, cfg, 1); err != nil {
		t.Fatal(err)
	}
	epoch, err := m.CurrentTaskEpoch(manifest.Slug)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plugin-jobs.json")
	backend, err := pluginjobs.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	worker := &pluginjobs.Worker{Queue: q}
	executor := &TaskExecutor{Manager: m, Authorize: func(ctx context.Context, job pluginjobs.Job) error {
		if job.ActorID != 1 {
			return pluginjobs.ErrAuthorization
		}
		return worker.Queue.(pluginjobs.ManagedStore).CheckClaim(ctx, job, time.Now().UTC())
	}}
	worker.Executor = executor
	return m, q, worker, epoch, path
}

func TestBackgroundReaderExampleWorker(t *testing.T) {
	t.Run("tasks_and_events_survive_restart", func(t *testing.T) {
		m, q, worker, epoch, path := backgroundReaderWorker(t, true)
		for _, hook := range []string{"task.execute.v1", "event.deliver.v1"} {
			out, err := m.dispatch(t.Context(), "background-reader", BusinessInput{Event: hook, Payload: json.RawMessage(`{}`)}, nil, epoch.Token)
			if err != nil || string(out.Data) != `{"processed":true}` || out.Fragment != nil {
				t.Fatal("unexpected compiled example output", hook, string(out.Data), err)
			}
		}
		now := time.Now().UTC()
		in := pluginjobs.Input{Plugin: "background-reader", Epoch: epoch.Token, Kind: "task.execute.v1", Key: "manual", ActorID: 1, Payload: json.RawMessage(`{}`)}
		manual, err := q.Enqueue(t.Context(), in, now)
		if err != nil {
			t.Fatal(err)
		}
		event := pluginjobs.Event{Key: "example:1", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
		subs := []pluginjobs.Subscription{{Plugin: in.Plugin, Epoch: epoch.Token, Since: epoch.StartedAt, ActorID: 1}}
		if err := pluginjobs.DeliverFrozen(t.Context(), q, q, event, subs, now); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if worked, err := worker.Tick(t.Context()); err != nil || !worked {
				t.Fatal("real worker", worked, err)
			}
		}
		backend, err := pluginjobs.NewFileBackend(path)
		if err != nil {
			t.Fatal(err)
		}
		reopened := &pluginjobs.Queue{Backend: backend}
		page, err := reopened.List(t.Context(), pluginjobs.Query{Plugin: in.Plugin})
		if err != nil || page.Total != 2 {
			t.Fatal("completion lost on restart", page.Total, err)
		}
		for _, item := range page.Items {
			if item.Status != "succeeded" || item.Attempts != 1 {
				t.Fatal("compiled example did not finish", item)
			}
		}
		if replay, err := reopened.Enqueue(t.Context(), in, now); err != nil || replay.ID != manual.ID || replay.Status != "succeeded" {
			t.Fatal("manual replay", err)
		}
		if err := pluginjobs.DeliverFrozen(t.Context(), reopened, reopened, event, nil, now); err != nil {
			t.Fatal("event replay", err)
		}
		worker.Queue = reopened
		if worked, err := worker.Tick(t.Context()); err != nil || worked {
			t.Fatal("completed replay executed again", worked, err)
		}
		in.Key = "after-restart"
		fresh, err := reopened.Enqueue(t.Context(), in, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if worked, err := worker.Tick(t.Context()); err != nil || !worked {
			t.Fatal("reopened queue did not execute new work", worked, err)
		}
		state, err := reopened.Backend.Read(t.Context())
		if err != nil || state.Jobs[fresh.ID].Status != "succeeded" || len(state.Jobs) != 3 {
			t.Fatal("reopened queue completion", err)
		}
	})
	for _, scenario := range []string{"denied_settings_retry", "revoked_principal_cancels", "old_epoch_cancels"} {
		t.Run(scenario, func(t *testing.T) {
			m, q, worker, epoch, _ := backgroundReaderWorker(t, scenario != "denied_settings_retry")
			job, err := q.Enqueue(t.Context(), pluginjobs.Input{Plugin: "background-reader", Epoch: epoch.Token, Kind: "task.execute.v1", Key: scenario, ActorID: 1, Payload: json.RawMessage(`{}`)}, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "revoked_principal_cancels":
				worker.Executor.(*TaskExecutor).Authorize = func(context.Context, pluginjobs.Job) error { return pluginjobs.ErrAuthorization }
			case "old_epoch_cancels":
				if err := m.SetEnabled(job.Plugin, false); err != nil {
					t.Fatal(err)
				}
				if err := m.SetEnabled(job.Plugin, true); err != nil {
					t.Fatal(err)
				}
			}
			if worked, err := worker.Tick(t.Context()); err != nil || !worked {
				t.Fatal("failed worker outcome", worked, err)
			}
			state, err := q.Backend.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got := state.Jobs[job.ID]
			status, code := "cancelled", "authorization_changed"
			if scenario == "denied_settings_retry" {
				status, code = "retry", "execution_failed"
			}
			if got.Status != status || got.ErrorCode != code || got.Attempts != 1 || got.LeaseToken != "" || !got.LeaseUntil.IsZero() {
				t.Fatal("failed example marked successful", got.Status, got.ErrorCode, got.Attempts)
			}
		})
	}
}
