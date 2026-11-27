package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestSharedTaskSubscriptionsIgnoreLocalAvailabilityAndPreserveFailures(t *testing.T) {
	store := &testTaskControls{}
	installed, manifest := taskControlFixture(t, store, nil)
	config := configureTaskControlFixture(t, installed, manifest.Slug)
	if err := installed.ConfigureAs(manifest.Slug, config, 77); err != nil {
		t.Fatal(err)
	}
	empty := NewManager(t.TempDir(), WithTaskControlStore(store))
	subscriptions, err := empty.TaskSubscriptions(t.Context())
	if err != nil || len(subscriptions) != 1 || subscriptions[0].ActorID != 77 || subscriptions[0].Plugin != manifest.Slug {
		t.Fatal("shared subscription disappeared on an instance without the package", subscriptions, err)
	}
	scopes, err := empty.taskClaimScopes(t.Context())
	if err != nil || len(scopes) != 0 {
		t.Fatal("an absent local package acquired a claim scope", scopes, err)
	}
	for _, failure := range []error{ErrTaskControlUnavailable, ErrTaskControlCorrupt} {
		store.loadError = failure
		if _, err := installed.List(); !errors.Is(err, failure) {
			t.Fatal("local list concealed a shared read failure", err)
		}
		if _, err := empty.TaskSubscriptions(t.Context()); !errors.Is(err, failure) {
			t.Fatal("global discovery treated a shared read failure as no subscriptions", err)
		}
	}
	store.loadError = nil
	control := sharedControl(t, store, manifest.Slug)
	control.Manifest.Slug = "different-identity"
	store.rows[manifest.Slug] = control
	if _, err := empty.TaskSubscriptions(t.Context()); !errors.Is(err, ErrTaskControlCorrupt) {
		t.Fatal("global discovery accepted a corrupt shared identity", err)
	}
}

func TestSharedTaskExecutorSeparatesLocalRuntimeFromRevocation(t *testing.T) {
	for _, availability := range []string{"absent", "disabled", "different-manifest", "different-module"} {
		t.Run(availability, func(t *testing.T) {
			store := &testTaskControls{}
			owner, manifest := taskControlFixture(t, store, nil)
			configureTaskControlFixture(t, owner, manifest.Slug)
			local := NewManager(t.TempDir(), WithTaskControlStore(store))
			if availability != "absent" {
				var err error
				module, err := os.ReadFile(filepath.Join("testdata", "host-bridge.wasm"))
				if err != nil {
					t.Fatal(err)
				}
				localManifest := manifest
				if availability == "different-manifest" {
					localManifest.Version = "2.0.0"
				}
				if availability == "different-module" {
					module = configurationSequenceModule([]byte(`{"method":"settings.get"}`))
				}
				installed, err := local.InstallPackage(createPluginArchive(t, localManifest, module))
				if err != nil {
					t.Fatal(err)
				}
				if err := WritePrivate(filepath.Join(local.root, manifest.Slug, ".official.json"), Release{Manifest: installed}); err != nil {
					t.Fatal(err)
				}
				if availability != "disabled" {
					if err := os.Remove(filepath.Join(local.root, manifest.Slug, ".disabled")); err != nil {
						t.Fatal(err)
					}
				}
			}
			backend, err := pluginjobs.NewFileBackend("")
			if err != nil {
				t.Fatal(err)
			}
			queue := &pluginjobs.Queue{Backend: backend}
			control := sharedControl(t, store, manifest.Slug)
			if _, err := queue.Enqueue(t.Context(), pluginjobs.Input{Plugin: manifest.Slug, Epoch: control.Epoch.Token,
				Kind: "task.execute.v1", Key: "local-runtime", ActorID: 1, Payload: json.RawMessage(`{}`)}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			claim, ok, err := queue.Claim(t.Context(), time.Now().UTC())
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			reads := 0
			executor := &TaskExecutor{Manager: local, Authorize: func(context.Context, pluginjobs.Job) error { return nil },
				Read: func(context.Context, int64) (any, error) { reads++; return nil, nil }}
			if err := executor.Execute(t.Context(), claim); !errors.Is(err, ErrTaskRuntimeUnavailable) || errors.Is(err, pluginjobs.ErrAuthorization) || reads != 0 {
				t.Fatal("local availability became shared authorization cancellation", err, reads)
			}
			scopes, err := local.taskClaimScopes(t.Context())
			if err != nil || len(scopes) != 0 {
				t.Fatal("incompatible local runtime acquired a scope", scopes, err)
			}
			if err := owner.SetEnabled(manifest.Slug, false); err != nil {
				t.Fatal(err)
			}
			if err := executor.Execute(t.Context(), claim); !errors.Is(err, pluginjobs.ErrAuthorization) || reads != 0 {
				t.Fatal("genuine shared withdrawal was treated as local availability", err, reads)
			}
		})
	}
}

type sharedTaskClaimProbe struct {
	pluginjobs.Store
	scopes      []pluginjobs.ClaimScope
	globalCalls int
	scopedCalls int
}

func (p *sharedTaskClaimProbe) Claim(context.Context, time.Time) (pluginjobs.Job, bool, error) {
	p.globalCalls++
	return pluginjobs.Job{}, false, pluginjobs.ErrInvalid
}

func (p *sharedTaskClaimProbe) ClaimScoped(_ context.Context, _ time.Time, scopes []pluginjobs.ClaimScope) (pluginjobs.Job, bool, error) {
	p.scopedCalls++
	p.scopes = append([]pluginjobs.ClaimScope{}, scopes...)
	return pluginjobs.Job{}, false, nil
}

func TestSharedTaskWorkerSelectsLocalClaimScopeBeforeAcquisition(t *testing.T) {
	store := &testTaskControls{}
	manager, manifest := taskControlFixture(t, store, nil)
	configureTaskControlFixture(t, manager, manifest.Slug)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	queue := &pluginjobs.Queue{Backend: backend}
	probe := &sharedTaskClaimProbe{Store: queue}
	worker := &pluginjobs.Worker{Queue: probe, Executor: &TaskExecutor{Manager: manager}}
	if worked, err := worker.Tick(t.Context()); worked || err != nil || probe.globalCalls != 0 || probe.scopedCalls != 1 || len(probe.scopes) != 1 || probe.scopes[0].Epoch != sharedControl(t, store, manifest.Slug).Epoch.Token {
		t.Fatal("worker bypassed its shared scope", worked, err, probe)
	}
	store.loadError = ErrTaskControlUnavailable
	if worked, err := worker.Tick(t.Context()); worked || !errors.Is(err, ErrTaskControlUnavailable) || probe.scopedCalls != 1 {
		t.Fatal("authority failure reached acquisition", worked, err, probe)
	}
	store.loadError = nil
	worker.Queue = queue
	if worked, err := worker.Tick(t.Context()); worked || !errors.Is(err, ErrTaskControlUnavailable) {
		t.Fatal("shared executor silently used an unscoped store", worked, err)
	}
}
