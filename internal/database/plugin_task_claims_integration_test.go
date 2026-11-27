package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

func TestPostgreSQLPluginTaskAuthorityScopedClaimsPreserveUnmatchedWork(t *testing.T) {
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	controls := NewPluginTaskControlRepository(pool)
	remote, local := pgTaskControl(t, "remote-only"), pgTaskControl(t, "local-reader")
	for _, control := range []plugin.TaskControl{remote, local} {
		if err := controls.SaveTaskControl(ctx, 0, control); err != nil {
			t.Fatal(err)
		}
	}
	enqueue := func(control plugin.TaskControl, key string) pluginjobs.Job {
		job, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: control.Slug, Epoch: control.Epoch.Token,
			Kind: "task.execute.v1", Key: key, ActorID: 1, Payload: json.RawMessage(`{}`)}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	stored := func(id string) []byte {
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	unmatched := enqueue(remote, "valid-unmatched")
	before := stored(unmatched.ID)
	matched := enqueue(local, "valid-local")
	claim, ok, err := jobs.ClaimScoped(ctx, time.Now().UTC(), []pluginjobs.ClaimScope{{Plugin: local.Slug, Epoch: local.Epoch.Token}})
	if err != nil || !ok || claim.ID != matched.ID || !bytes.Equal(before, stored(unmatched.ID)) {
		t.Fatal("an earlier unmatched task blocked or changed local work", ok, err, claim.ID)
	}
	if err := jobs.Finish(ctx, claim, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := jobs.ClaimScoped(ctx, time.Now().UTC(), nil); err != nil || ok || !bytes.Equal(before, stored(unmatched.ID)) {
		t.Fatal("empty local scope changed a valid remote task", ok, err)
	}
	updated := nextPGTaskControl(t, remote)
	updated.Epoch, err = plugin.NewTaskEpoch(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.SaveTaskControl(ctx, remote.Revision, updated); err != nil {
		t.Fatal(err)
	}
	newJob := enqueue(updated, "new-package-lifetime")
	newBefore := stored(newJob.ID)
	// The old task is truly revoked and can be cancelled even on an instance
	// with no executable package. The new lifetime must survive its stale scope.
	claim, ok, err = jobs.ClaimScoped(ctx, time.Now().UTC(), []pluginjobs.ClaimScope{{Plugin: remote.Slug, Epoch: remote.Epoch.Token}})
	if err != nil || !ok || claim.ID != unmatched.ID {
		t.Fatal("revoked work was hidden by local availability", ok, err, claim.ID)
	}
	if err := jobs.CheckSharedClaim(ctx, claim, time.Now().UTC()); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("old lifetime retained shared authority", err)
	}
	if err := jobs.Finish(ctx, claim, "authorization_changed", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := jobs.ClaimScoped(ctx, time.Now().UTC(), []pluginjobs.ClaimScope{{Plugin: remote.Slug, Epoch: remote.Epoch.Token}}); err != nil || ok || !bytes.Equal(newBefore, stored(newJob.ID)) {
		t.Fatal("stale package scope acquired a current valid task", ok, err)
	}
	claim, ok, err = jobs.ClaimScoped(ctx, time.Now().UTC(), []pluginjobs.ClaimScope{{Plugin: updated.Slug, Epoch: updated.Epoch.Token}})
	if err != nil || !ok || claim.ID != newJob.ID {
		t.Fatal("matching current runtime could not claim", ok, err)
	}
	if err := jobs.CheckSharedClaim(ctx, claim, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Finish(ctx, claim, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLPluginTaskAuthorityScopedExpiredLeaseAndCorruptRead(t *testing.T) {
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	controls := NewPluginTaskControlRepository(pool)
	control := pgTaskControl(t, "scoped-lease")
	if err := controls.SaveTaskControl(ctx, 0, control); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: control.Slug, Epoch: control.Epoch.Token,
		Kind: "task.execute.v1", Key: "expired", ActorID: 1, Payload: json.RawMessage(`{}`)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	scopes := []pluginjobs.ClaimScope{{Plugin: control.Slug, Epoch: control.Epoch.Token}}
	claim, ok, err := jobs.ClaimScoped(ctx, time.Now().UTC(), scopes)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var before, after []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, job.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	later := claim.LeaseUntil.Add(time.Second)
	if _, ok, err := jobs.ClaimScoped(ctx, later, nil); err != nil || ok {
		t.Fatal("unmatched expired lease consumed an attempt", ok, err)
	}
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, job.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("unmatched expired lease was mutated", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE np_plugin_task_authorizations SET epoch=$1 WHERE slug=$2`, strings.Repeat("b", 64), control.Slug); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := jobs.ClaimScoped(ctx, later, scopes); ok || !errors.Is(err, plugin.ErrTaskControlCorrupt) {
		t.Fatal("corrupt authority became a cancellation claim", ok, err)
	}
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, job.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("corrupt authority changed a lease", err)
	}
}

func TestPostgreSQLPluginTaskAuthorityScopedConcurrentAndInvalidScopes(t *testing.T) {
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	controls := NewPluginTaskControlRepository(pool)
	control := pgTaskControl(t, "concurrent-scoped")
	if err := controls.SaveTaskControl(ctx, 0, control); err != nil {
		t.Fatal(err)
	}
	job, err := jobs.Enqueue(ctx, pluginjobs.Input{Plugin: control.Slug, Epoch: control.Epoch.Token,
		Kind: "task.execute.v1", Key: "one-claim", ActorID: 1, Payload: json.RawMessage(`{}`)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	valid := pluginjobs.ClaimScope{Plugin: control.Slug, Epoch: control.Epoch.Token}
	for _, invalid := range [][]pluginjobs.ClaimScope{{valid, valid}, {{Plugin: "../escape", Epoch: control.Epoch.Token}}, {{Plugin: control.Slug, Epoch: strings.Repeat("A", 64)}}} {
		if _, ok, err := jobs.ClaimScoped(ctx, time.Now().UTC(), invalid); ok || !errors.Is(err, pluginjobs.ErrInvalid) {
			t.Fatal("invalid scope reached acquisition", ok, err)
		}
	}
	claims := make(chan pluginjobs.Job, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Go(func() {
			claim, ok, err := jobs.ClaimScoped(ctx, time.Now().UTC(), []pluginjobs.ClaimScope{valid})
			if err != nil {
				t.Error(err)
			}
			if ok {
				claims <- claim
			}
		})
	}
	wait.Wait()
	close(claims)
	winners := 0
	for claim := range claims {
		winners++
		if claim.ID != job.ID {
			t.Fatal("wrong concurrent claim")
		}
	}
	if winners != 1 {
		t.Fatal("scoped claim did not have exactly one winner", winners)
	}
}
