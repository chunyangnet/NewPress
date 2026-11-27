package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

func pgTaskControl(t *testing.T, slug string) plugin.TaskControl {
	t.Helper()
	epoch, err := plugin.NewTaskEpoch(1)
	if err != nil {
		t.Fatal(err)
	}
	return plugin.TaskControl{Schema: 1, Slug: slug, Revision: 1, ModuleDigest: strings.Repeat("a", 64), Enabled: true,
		Epoch: epoch, Manifest: plugin.Manifest{Schema: 1, Slug: slug, Name: "Authority fixture", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm",
			Capabilities: []string{"settings.read"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}, "event.deliver.v1": {"np_handle"}},
			Settings: []plugin.Setting{{Key: "count", Label: "Count", Type: "number", Default: float64(0), Max: 10}}},
		Configuration: plugin.Configuration{Version: 1, Values: map[string]any{"count": float64(0)}, Grants: []string{"settings.read"}}}
}

func nextPGTaskControl(t *testing.T, c plugin.TaskControl) plugin.TaskControl {
	t.Helper()
	next, err := plugin.NextTaskControl(c)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func loadPGTaskControl(t *testing.T, ctx context.Context, r *PluginTaskControlRepository, slug string) plugin.TaskControl {
	t.Helper()
	c, ok, err := r.LoadTaskControl(ctx, slug)
	if err != nil || !ok {
		t.Fatalf("shared authority not found: %v", err)
	}
	return c
}

func taskControlJSON(t *testing.T, c plugin.TaskControl) []byte {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPostgreSQLPluginTaskAuthorityCAS(t *testing.T) {
	ctx, pool, _ := newPluginJobsIntegrationStore(t)
	r := NewPluginTaskControlRepository(pool)
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	c := pgTaskControl(t, "authority")
	var wg sync.WaitGroup
	errorsOut := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errorsOut <- r.SaveTaskControl(ctx, 0, c) })
	}
	wg.Wait()
	close(errorsOut)
	winners, conflicts := 0, 0
	for err := range errorsOut {
		if err == nil {
			winners++
		} else if errors.Is(err, plugin.ErrTaskControlConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal("first insertion was not CAS protected", winners, conflicts)
	}
	loaded := loadPGTaskControl(t, ctx, r, c.Slug)
	if !bytes.Equal(taskControlJSON(t, c), taskControlJSON(t, loaded)) {
		t.Fatal("private authority did not round trip")
	}
	next := nextPGTaskControl(t, loaded)
	next.Enabled = false
	if err := r.SaveTaskControl(ctx, loaded.Revision, next); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveTaskControl(ctx, loaded.Revision, next); !errors.Is(err, plugin.ErrTaskControlConflict) {
		t.Fatal("stale CAS overwritten", err)
	}
}

func TestPostgreSQLPluginTaskAuthorityRejectsCorruptIndexes(t *testing.T) {
	ctx, pool, _ := newPluginJobsIntegrationStore(t)
	r := NewPluginTaskControlRepository(pool)
	for _, field := range []string{"slug", "revision", "epoch", "enabled", "retired", "duplicate_json", "unknown_json", "missing_configuration", "metadata_budget"} {
		t.Run(field, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE np_plugin_task_authorizations`); err != nil {
				t.Fatal(err)
			}
			c := pgTaskControl(t, "authority")
			if err := r.SaveTaskControl(ctx, 0, c); err != nil {
				t.Fatal(err)
			}
			statement := map[string]string{
				"slug":     `UPDATE np_plugin_task_authorizations SET slug='hidden-authority'`,
				"revision": `UPDATE np_plugin_task_authorizations SET revision=revision+1`,
				"epoch":    `UPDATE np_plugin_task_authorizations SET epoch='bad-epoch'`,
				"enabled":  `UPDATE np_plugin_task_authorizations SET enabled=false`,
				"retired":  `UPDATE np_plugin_task_authorizations SET retired=true`,
			}[field]
			if statement != "" {
				if _, err := pool.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			} else {
				raw := taskControlJSON(t, c)
				switch field {
				case "duplicate_json":
					raw = bytes.Replace(raw, []byte(`{"schema":1`), []byte(`{"schema":1,"schema":1`), 1)
				case "unknown_json":
					raw = bytes.Replace(raw, []byte(`{"schema":1`), []byte(`{"unexpected":true,"schema":1`), 1)
				case "missing_configuration":
					c.Configuration.Version = 0
					raw = taskControlJSON(t, c)
				case "metadata_budget":
					c.Manifest.Settings, c.Configuration.Values = nil, map[string]any{}
					for i := range 32 {
						key := fmt.Sprintf("text_%02d", i)
						c.Manifest.Settings = append(c.Manifest.Settings, plugin.Setting{Key: key, Label: key, Type: "text", Default: strings.Repeat("d", 3000), Max: 4000})
						c.Configuration.Values[key] = ""
					}
					remaining := plugin.MaxTaskControlBytes - 1 - len(taskControlJSON(t, c))
					for i := range 32 {
						n := min(4000, remaining)
						c.Configuration.Values[fmt.Sprintf("text_%02d", i)] = strings.Repeat("v", n)
						remaining -= n
					}
					if remaining != 0 || plugin.ValidateTaskControl(c) != nil {
						t.Fatal("metadata reservation fixture is not otherwise valid")
					}
					raw = taskControlJSON(t, c)
				}
				if _, err := pool.Exec(ctx, `UPDATE np_plugin_task_authorizations SET data=$1`, raw); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Ready(ctx); !errors.Is(err, plugin.ErrTaskControlCorrupt) {
				t.Fatal("readiness accepted malformed authority", err)
			}
			if field == "slug" {
				if _, found, err := r.LoadTaskControl(ctx, c.Slug); err != nil || found {
					t.Fatal("hidden row became executable", found, err)
				}
				if err := r.SaveTaskControl(ctx, 0, pgTaskControl(t, c.Slug)); !errors.Is(err, plugin.ErrTaskControlCorrupt) {
					t.Fatal("hidden index allowed fresh authority", err)
				}
			} else {
				if _, _, err := r.LoadTaskControl(ctx, c.Slug); !errors.Is(err, plugin.ErrTaskControlCorrupt) {
					t.Fatal("read accepted inconsistent authority", err)
				}
				if err := r.SaveTaskControl(ctx, 1, nextPGTaskControl(t, pgTaskControl(t, c.Slug))); !errors.Is(err, plugin.ErrTaskControlCorrupt) {
					t.Fatal("CAS replaced corrupt evidence", err)
				}
			}
		})
	}
}

func TestPostgreSQLPluginTaskAuthorityClaimGates(t *testing.T) {
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	controls := NewPluginTaskControlRepository(pool)
	for _, test := range []string{"valid", "disabled", "rotated", "retired", "transition", "missing", "undeclared_hook", "event_actor", "changed_payload", "database_clock", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `TRUNCATE np_plugin_jobs, np_plugin_task_authorizations`); err != nil {
				t.Fatal(err)
			}
			c := pgTaskControl(t, "authority")
			if err := controls.SaveTaskControl(ctx, 0, c); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if test == "database_clock" {
				now = now.Add(-2 * pluginjobs.LeaseDuration)
			}
			kind, actor := "task.execute.v1", int64(1)
			if test == "event_actor" {
				kind, actor = "event.deliver.v1", 2
			}
			if _, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: c.Slug, Epoch: c.Epoch.Token, Kind: kind, Key: test, ActorID: actor, Payload: json.RawMessage(`{"public":true}`)}, now); err != nil {
				t.Fatal(err)
			}
			claim, ok, err := jobs.Claim(ctx, now)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			next := nextPGTaskControl(t, c)
			switch test {
			case "disabled":
				next.Enabled = false
			case "rotated":
				next.Epoch, err = plugin.NewTaskEpoch(1)
			case "retired":
				next.Enabled, next.Retired, next.Epoch.ActorID = false, true, 0
			case "transition":
				next.Enabled, next.Epoch.ActorID, next.TransitionID = false, 0, next.Epoch.Token
				next.TransitionBytes, err = plugin.TaskControlBudgetBytes(c)
			case "undeclared_hook":
				delete(next.Manifest.Hooks, kind)
			case "changed_payload":
				claim.Payload = json.RawMessage(`{"different":true}`)
			case "missing":
				_, err = pool.Exec(ctx, `DELETE FROM np_plugin_task_authorizations`)
			case "cancelled":
				err = jobs.Change(ctx, claim.Plugin, claim.ID, claim.Version, "cancel", time.Now().UTC())
			}
			if err != nil {
				t.Fatal(err)
			}
			if test == "disabled" || test == "rotated" || test == "retired" || test == "transition" || test == "undeclared_hook" {
				if err := controls.SaveTaskControl(ctx, c.Revision, next); err != nil {
					t.Fatal(err)
				}
			}
			err = jobs.CheckSharedClaim(ctx, claim, now)
			if test == "valid" && err != nil || test != "valid" && !errors.Is(err, pluginjobs.ErrAuthorization) {
				t.Fatal("unexpected claim authorization", err)
			}
		})
	}
}

func copyTaskControls(t *testing.T, ctx context.Context, pool *pgxpool.Pool, controls []plugin.TaskControl) {
	t.Helper()
	_, err := pool.CopyFrom(ctx, pgx.Identifier{"np_plugin_task_authorizations"}, []string{"slug", "revision", "epoch", "enabled", "retired", "data"}, pgx.CopyFromSlice(len(controls), func(i int) ([]any, error) {
		c := controls[i]
		raw, err := json.Marshal(c)
		return []any{c.Slug, c.Revision, c.Epoch.Token, c.Enabled, c.Retired, raw}, err
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLPluginTaskAuthorityCapacityRetainsRetiredIdentities(t *testing.T) {
	ctx, pool, _ := newPluginJobsIntegrationStore(t)
	r := NewPluginTaskControlRepository(pool)
	controls := make([]plugin.TaskControl, plugin.MaxTaskControls)
	base := pgTaskControl(t, "capacity")
	for i := range controls {
		c := base
		c.Slug, c.Manifest.Slug = fmt.Sprintf("capacity-%05d", i), fmt.Sprintf("capacity-%05d", i)
		c.Enabled, c.Retired, c.Epoch.ActorID = false, i%2 == 0, 0
		controls[i] = c
	}
	copyTaskControls(t, ctx, pool, controls)
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveTaskControl(ctx, 0, pgTaskControl(t, "extra")); !errors.Is(err, plugin.ErrTaskControlCapacity) {
		t.Fatal("retired identities were evicted", err)
	}
	next := nextPGTaskControl(t, controls[0])
	if err := r.SaveTaskControl(ctx, 1, next); err != nil {
		t.Fatal("full count blocked replacing a retained identity", err)
	}
	if !loadPGTaskControl(t, ctx, r, next.Slug).Retired {
		t.Fatal("retired identity was renewed by capacity admission")
	}
}

func TestPostgreSQLPluginTaskAuthorityByteBudgetReservesRevocationAndSwitch(t *testing.T) {
	ctx, pool, _ := newPluginJobsIntegrationStore(t)
	r := NewPluginTaskControlRepository(pool)
	base := pgTaskControl(t, "budget-0000")
	base.Manifest.Settings, base.Configuration.Values = nil, map[string]any{}
	for i := range 16 {
		key := fmt.Sprintf("value_%02d", i)
		base.Manifest.Settings = append(base.Manifest.Settings, plugin.Setting{Key: key, Label: key, Type: "text", Default: "", Max: 4000})
		base.Configuration.Values[key] = ""
	}
	budget := func(c plugin.TaskControl) int {
		t.Helper()
		n, err := plugin.TaskControlBudgetBytes(c)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	big := base
	big.Configuration.Values = map[string]any{}
	for key := range base.Configuration.Values {
		big.Configuration.Values[key] = strings.Repeat("a", 3990)
	}
	minimum, maximum := budget(base), budget(big)+16*10
	controls, total := []plugin.TaskControl{}, 0
	add := func(c plugin.TaskControl) {
		c.Slug, c.Manifest.Slug = fmt.Sprintf("budget-%04d", len(controls)), fmt.Sprintf("budget-%04d", len(controls))
		controls = append(controls, c)
		total += budget(c)
	}
	for total+budget(big)+minimum <= plugin.MaxTaskControlTotal {
		add(big)
	}
	if plugin.MaxTaskControlTotal-total > maximum {
		add(base)
	}
	tail := base
	tail.Configuration.Values = map[string]any{}
	remaining := plugin.MaxTaskControlTotal - total - minimum
	for i := range 16 {
		n := min(4000, remaining)
		tail.Configuration.Values[fmt.Sprintf("value_%02d", i)] = strings.Repeat("a", n)
		remaining -= n
	}
	add(tail)
	if remaining != 0 || total != plugin.MaxTaskControlTotal {
		t.Fatal("byte boundary fixture did not reach the exact reserved budget", total, remaining)
	}
	copyTaskControls(t, ctx, pool, controls)
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	old := controls[0]
	disabled := nextPGTaskControl(t, old)
	disabled.Enabled, disabled.Epoch.ActorID = false, 0
	disabled.Epoch, _ = plugin.NewTaskEpoch(0)
	if err := r.SaveTaskControl(ctx, old.Revision, disabled); err != nil {
		t.Fatal("full byte budget prevented revocation", err)
	}
	desired := nextPGTaskControl(t, disabled)
	desired.Manifest.Version, desired.ModuleDigest = "1.1.0", strings.Repeat("b", 64)
	desired.Configuration.Values = map[string]any{}
	for key := range big.Configuration.Values {
		desired.Configuration.Values[key] = strings.Repeat("b", 4000)
	}
	blocked := nextPGTaskControl(t, disabled)
	blocked.TransitionID = blocked.Epoch.Token
	blocked.TransitionBytes = budget(desired)
	if err := r.SaveTaskControl(ctx, disabled.Revision, blocked); !errors.Is(err, plugin.ErrTaskControlCapacity) {
		t.Fatal("switch did not reserve its larger target before local commit", err)
	}
	other := nextPGTaskControl(t, controls[1])
	other.Configuration.Values = map[string]any{}
	for key, value := range controls[1].Configuration.Values {
		other.Configuration.Values[key] = value
	}
	other.Configuration.Values["value_00"] = ""
	if err := r.SaveTaskControl(ctx, 1, other); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveTaskControl(ctx, disabled.Revision, blocked); err != nil {
		t.Fatal(err)
	}
	free := 3990 - (budget(desired) - budget(disabled))
	competing := nextPGTaskControl(t, other)
	competing.Configuration.Values["value_00"] = strings.Repeat("c", free+1)
	if err := r.SaveTaskControl(ctx, other.Revision, competing); !errors.Is(err, plugin.ErrTaskControlCapacity) {
		t.Fatal("another identity consumed pending target capacity", err)
	}
	competing.Configuration.Values["value_00"] = strings.Repeat("c", free)
	if err := r.SaveTaskControl(ctx, other.Revision, competing); err != nil {
		t.Fatal(err)
	}
	desired.Revision = blocked.Revision + 1
	if err := r.SaveTaskControl(ctx, blocked.Revision, desired); err != nil {
		t.Fatal("reserved final switch no longer fitted", err)
	}
	if err := r.Ready(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLPluginTaskAuthorityRestoreRollsBackAndRetires(t *testing.T) {
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	controls := NewPluginTaskControlRepository(pool)
	c := pgTaskControl(t, "restore-authority")
	if err := controls.SaveTaskControl(ctx, 0, c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: c.Slug, Epoch: c.Epoch.Token, Kind: "task.execute.v1", Key: "restore", ActorID: 1, Payload: json.RawMessage(`{}`)}, now); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := jobs.Claim(ctx, now)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	event := pluginjobs.Event{Key: "restore-event", Kind: "content.updated", ObjectID: 1, Version: 2, At: now}
	if _, err := jobs.FreezeFanout(ctx, event, []pluginjobs.Subscription{}); err != nil {
		t.Fatal(err)
	}
	var beforeJob, beforeFanout []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, claim.ID).Scan(&beforeJob); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_fanouts WHERE event_key=$1`, event.Key).Scan(&beforeFanout); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_authority_restore() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'authority restore rollback fixture'; END $$;
 CREATE TRIGGER fail_authority_restore BEFORE UPDATE ON np_plugin_task_authorizations FOR EACH ROW EXECUTE FUNCTION fail_authority_restore()`); err != nil {
		t.Fatal(err)
	}
	if err := jobs.CancelRestored(ctx, time.Now().UTC()); err == nil {
		t.Fatal("restore succeeded through an injected authorization write failure")
	}
	var afterJob, afterFanout []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, claim.ID).Scan(&afterJob); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_fanouts WHERE event_key=$1`, event.Key).Scan(&afterFanout); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeJob, afterJob) || !bytes.Equal(beforeFanout, afterFanout) || !bytes.Equal(taskControlJSON(t, c), taskControlJSON(t, loadPGTaskControl(t, ctx, controls, c.Slug))) {
		t.Fatal("authorization failure partially cancelled restored facts")
	}
	if err := jobs.CheckSharedClaim(ctx, claim, time.Now().UTC()); err != nil {
		t.Fatal("failed restore invalidated the original claim", err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_authority_restore ON np_plugin_task_authorizations; DROP FUNCTION fail_authority_restore()`); err != nil {
		t.Fatal(err)
	}
	// Restoration must also clear a pending package transition and its capacity
	// reservation. It never resumes that local transition from restored data.
	blocked := nextPGTaskControl(t, c)
	blocked.Enabled, blocked.Epoch.ActorID, blocked.TransitionID = false, 0, c.Epoch.Token
	blocked.TransitionBytes, _ = plugin.TaskControlBudgetBytes(c)
	if err := controls.SaveTaskControl(ctx, c.Revision, blocked); err != nil {
		t.Fatal(err)
	}
	if err := jobs.CancelRestored(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	retired := loadPGTaskControl(t, ctx, controls, c.Slug)
	if retired.Enabled || !retired.Retired || retired.Epoch.ActorID != 0 || retired.TransitionID != "" || retired.TransitionBytes != 0 || retired.Epoch.Token == c.Epoch.Token {
		t.Fatal("restore failed to retire shared authority", retired)
	}
	beforeConfig, _ := json.Marshal(c.Configuration)
	afterConfig, _ := json.Marshal(retired.Configuration)
	if !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatal("restore discarded configuration needed for explicit review")
	}
	if err := jobs.CheckSharedClaim(ctx, claim, time.Now().UTC()); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("restored old claim remained authorized", err)
	}
	if err := jobs.CancelRestored(ctx, time.Now().UTC()); err != nil || !bytes.Equal(taskControlJSON(t, retired), taskControlJSON(t, loadPGTaskControl(t, ctx, controls, c.Slug))) {
		t.Fatal("repeat restoration renewed retired identity", err)
	}
}

func TestPostgreSQLPluginTaskAuthorityQuarantineFencesKnownAndUnknownClaims(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "known"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool, jobs := newPluginJobsIntegrationStore(t)
			controls := NewPluginTaskControlRepository(pool)
			c := pgTaskControl(t, "quarantine-authority")
			if err := controls.SaveTaskControl(ctx, 0, c); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if _, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: c.Slug, Epoch: c.Epoch.Token, Kind: "task.execute.v1", Key: "quarantine", ActorID: 1, Payload: json.RawMessage(`{}`)}, now); err != nil {
				t.Fatal(err)
			}
			claim, ok, err := jobs.Claim(ctx, now)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if unknown {
				_, err = pool.Exec(ctx, `INSERT INTO np_plugin_jobs(id,plugin,status,available_at,updated_at,data) VALUES($1,'quarantine-authority','pending',$2,$2,$3)`, strings.Repeat("f", 64), now, []byte(`{"unprovable":true}`))
			} else {
				_, err = pool.Exec(ctx, `UPDATE np_plugin_jobs SET plugin='wrong-index' WHERE id=$1`, claim.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			d := maintenanceDiagnostic(t, jobs, pluginjobs.RecordTask)
			if _, err := jobs.QuarantineRecord(ctx, pluginjobs.QuarantineRequest{Reference: d.Reference, Type: pluginjobs.RecordTask, Key: name, Operator: "fixture", Reason: "reviewed malformed task"}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := jobs.CheckSharedClaim(ctx, claim, time.Now().UTC()); !errors.Is(err, pluginjobs.ErrAuthorization) {
				t.Fatal("shared check bypassed quarantine policy", err)
			}
		})
	}
}

func TestPostgreSQLPluginTaskAuthorityRestoreAndWriterLockOrder(t *testing.T) {
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	controls := NewPluginTaskControlRepository(pool)
	for i := range 8 {
		if _, err := pool.Exec(ctx, `TRUNCATE np_plugin_task_authorizations`); err != nil {
			t.Fatal(err)
		}
		before := pgTaskControl(t, fmt.Sprintf("restore-race-%d", i))
		if err := controls.SaveTaskControl(ctx, 0, before); err != nil {
			t.Fatal(err)
		}
		next := nextPGTaskControl(t, before)
		next.Configuration.Version++
		start := make(chan struct{})
		writer, restore := make(chan error, 1), make(chan error, 1)
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		go func() { <-start; writer <- controls.SaveTaskControl(call, before.Revision, next) }()
		go func() { <-start; restore <- jobs.CancelRestored(call, time.Now().UTC()) }()
		close(start)
		writerErr, restoreErr := <-writer, <-restore
		cancel()
		if restoreErr != nil || writerErr != nil && !errors.Is(writerErr, plugin.ErrTaskControlConflict) {
			t.Fatal("restore/writer did not finish under the common lock order", restoreErr, writerErr)
		}
		if current := loadPGTaskControl(t, ctx, controls, before.Slug); current.Enabled || !current.Retired {
			t.Fatal("a stale writer renewed restored authority", current)
		}
	}
}
