package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"newpress.local/newpress/internal/pluginjobs"
)

func TestPostgreSQLPluginArchiveReclaimsTaskCapacity(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Date(2026, 11, 24, 0, 0, 0, 123456789, time.UTC)
	in := pluginjobs.Input{Plugin: "archive-reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-key-0", ActorID: 1, Payload: json.RawMessage(`{"private":"drop private archive payload"}`)}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var original pluginjobs.Job
	for i := range pluginjobs.MaxPluginJobs {
		q, err := taskQueue(nil)
		if err != nil {
			t.Fatal(err)
		}
		item := in
		item.Key = fmt.Sprintf("private-key-%d", i)
		j, err := q.Enqueue(ctx, item, now)
		if err != nil {
			t.Fatal(err)
		}
		j.Status = "cancelled"
		if err = saveTask(ctx, tx, j); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			original = j
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := in
	fresh.Key = "fresh-work"
	if _, err = r.Enqueue(ctx, fresh, now); !errors.Is(err, pluginjobs.ErrCapacity) {
		t.Fatal("fixture is not at task capacity", err)
	}
	result, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Plugin: in.Plugin, Tasks: true, Before: now, Limit: pluginjobs.MaxArchiveBatch}, now.Add(time.Second))
	if err != nil || result.Tasks != pluginjobs.MaxPluginJobs {
		t.Fatal("archive did not reclaim task slots", result, err)
	}
	r = NewPluginJobRepository(pool)
	replay, err := r.Enqueue(ctx, in, now.Add(2*time.Second))
	if err != nil || replay.ID != original.ID || replay.Status != "cancelled" || replay.ArchivedAt.IsZero() || replay.Version != original.Version {
		t.Fatal("archived task did not replay", replay, err)
	}
	changed := in
	changed.Payload = json.RawMessage(`{"changed":true}`)
	if _, err = r.Enqueue(ctx, changed, now); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("archive lost payload fingerprint", err)
	}
	changed = in
	changed.ActorID++
	if _, err = r.Enqueue(ctx, changed, now); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("archive lost actor fingerprint", err)
	}
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_job_archives WHERE id=$1`, original.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"drop private archive payload", "private-key-", `"payload"`, `"epoch"`, `"actor_id"`, `"lease_token"`} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("archive retained %q", secret)
		}
	}
	if page, err := r.List(ctx, pluginjobs.Query{Plugin: in.Plugin, Archived: true, Limit: 2, Offset: 1}); err != nil || page.Total != pluginjobs.MaxPluginJobs || len(page.Items) != 2 || page.Items[0].ArchivedAt.IsZero() {
		t.Fatal("archive list failed", page, err)
	}
	if err = r.Change(ctx, in.Plugin, original.ID, original.Version, "cancel", now); !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("archive accepted active mutation", err)
	}
	if err = r.CheckClaim(ctx, replay, now); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("archive authorized a claim", err)
	}
	job, err := r.Enqueue(ctx, fresh, now)
	if err != nil {
		t.Fatal("archive did not admit new work", err)
	}
	claim, ok, err := r.Claim(ctx, now)
	if err != nil || !ok || claim.ID != job.ID {
		t.Fatal("old replay became runnable", claim.ID, ok, err)
	}
}

func TestPostgreSQLPluginArchiveReclaimsFanoutCapacity(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Date(2026, 11, 24, 0, 0, 0, 123456789, time.UTC)
	subs := []pluginjobs.Subscription{{Plugin: "frozen-reader", Epoch: strings.Repeat("a", 64), Since: now, ActorID: 1}}
	values := make([][]any, 0, pluginjobs.MaxFanouts)
	for i := range pluginjobs.MaxFanouts {
		cohort := []pluginjobs.Subscription{}
		if i == 0 {
			cohort = subs
		}
		event := pluginjobs.Event{Key: fmt.Sprintf("old-%05d", i), Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
		raw, err := json.Marshal(pluginjobs.FanoutReceipt{Event: event, Subscriptions: cohort, Delivered: true})
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, []any{event.Key, raw})
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"np_plugin_fanouts"}, []string{"event_key", "data"}, pgx.CopyFromRows(values)); err != nil {
		t.Fatal(err)
	}
	event := pluginjobs.Event{Key: "new-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err := r.FreezeFanout(ctx, event, nil); !errors.Is(err, pluginjobs.ErrCapacity) {
		t.Fatal("fixture is not at fanout capacity", err)
	}
	result, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Fanouts: true, Before: now, Limit: 2}, now.Add(time.Second))
	if err != nil || result.Fanouts != 2 {
		t.Fatal("archive did not release fanout slots", result, err)
	}
	if _, err = r.FreezeFanout(ctx, event, nil); err != nil {
		t.Fatal("new event could not use reclaimed capacity", err)
	}
	newSubs := []pluginjobs.Subscription{{Plugin: "later-reader", Epoch: strings.Repeat("b", 64), Since: now, ActorID: 2}}
	for i := range 2 {
		old := event
		old.Key = fmt.Sprintf("old-%05d", i)
		want := []pluginjobs.Subscription{}
		if i == 0 {
			want = subs
		}
		frozen, err := r.FreezeFanout(ctx, old, newSubs)
		if err != nil || !frozen.Delivered || !reflect.DeepEqual(frozen.Subscriptions, want) {
			t.Fatal("archive changed a frozen cohort", frozen, err)
		}
		if err = pluginjobs.DeliverFrozen(ctx, r, r, old, newSubs, now.Add(2*time.Second)); err != nil {
			t.Fatal("lost ACK could not reuse archive", err)
		}
		if err = r.CompleteFanout(ctx, old); err != nil {
			t.Fatal("completion retry failed after archive", err)
		}
		changed := old
		changed.Version++
		if _, _, err := r.FindFanout(ctx, changed); !errors.Is(err, pluginjobs.ErrConflict) {
			t.Fatal("archive did not bind event identity", err)
		}
	}
	if page, err := r.List(ctx, pluginjobs.Query{Plugin: "later-reader"}); err != nil || page.Total != 0 {
		t.Fatal("archived fanout rediscovered recipients", err)
	}
}

func TestPostgreSQLPluginArchiveConcurrentReplayAndRestore(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC()
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "old-request", ActorID: 1, Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Change(ctx, j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	event := pluginjobs.Event{Key: "old-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err = r.FreezeFanout(ctx, event, nil); err != nil {
		t.Fatal(err)
	}
	if err = r.CompleteFanout(ctx, event); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsOut := make(chan error, 30)
	var wg sync.WaitGroup
	for i := range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			repository := NewPluginJobRepository(pool)
			var err error
			switch i % 6 {
			case 0:
				_, err = repository.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Fanouts: true, Before: now}, now.Add(time.Second))
			case 1:
				var replay pluginjobs.Job
				replay, err = repository.Enqueue(ctx, in, now.Add(time.Second))
				if err == nil && (replay.ID != j.ID || replay.Status != "cancelled") {
					err = errors.New("old request became runnable")
				}
			case 2:
				var found bool
				_, found, err = repository.FindFanout(ctx, event)
				if err == nil && !found {
					err = errors.New("archive lookup observed a gap")
				}
			case 3:
				err = repository.CompleteFanout(ctx, event)
			case 4:
				err = pluginjobs.DeliverFrozen(ctx, repository, repository, event, []pluginjobs.Subscription{{Plugin: "new-reader", Epoch: strings.Repeat("b", 64), Since: now, ActorID: 2}}, now.Add(time.Second))
			case 5:
				// Workers are absent in this isolated fixture. Concurrent storage
				// maintenance still must have a consistent lock order.
				err = repository.CancelRestored(ctx, now.Add(time.Second))
			}
			errorsOut <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	var live, history int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_plugin_jobs),(SELECT count(*) FROM np_plugin_job_archives)`).Scan(&live, &history); err != nil || live != 0 || history != 1 {
		t.Fatal("concurrent moves duplicated or lost task history", live, history, err)
	}
	if receipt, found, err := r.FindFanout(ctx, event); err != nil || !found || !receipt.Delivered || !receipt.Cancelled {
		t.Fatal("concurrent restore lost event facts", receipt, err)
	}
	if _, ok, err := r.Claim(ctx, now.Add(time.Hour)); err != nil || ok {
		t.Fatal("replay admitted old work", ok, err)
	}
}

func TestPostgreSQLPluginArchiveRollsBackBothClassesOnWriteFailure(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC()
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "rollback", ActorID: 1, Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Change(ctx, j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	event := pluginjobs.Event{Key: "rollback-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err = r.FreezeFanout(ctx, event, nil); err != nil {
		t.Fatal(err)
	}
	if err = r.CompleteFanout(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_archive() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'archive write rejected'; END $$;
 CREATE TRIGGER reject_archive BEFORE INSERT ON np_plugin_fanout_archives FOR EACH ROW EXECUTE FUNCTION reject_archive()`); err != nil {
		t.Fatal(err)
	}
	if result, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Fanouts: true, Before: now}, now.Add(time.Second)); err == nil || result != (pluginjobs.ArchiveResult{}) {
		t.Fatal("partial archive reported success", result, err)
	}
	var liveTasks, archivedTasks, liveFanouts, archivedFanouts int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_plugin_jobs),(SELECT count(*) FROM np_plugin_job_archives),
 (SELECT count(*) FROM np_plugin_fanouts),(SELECT count(*) FROM np_plugin_fanout_archives)`).Scan(&liveTasks, &archivedTasks, &liveFanouts, &archivedFanouts); err != nil || liveTasks != 1 || archivedTasks != 0 || liveFanouts != 1 || archivedFanouts != 0 {
		t.Fatal("archive failure committed a partial move", liveTasks, archivedTasks, liveFanouts, archivedFanouts, err)
	}
	if replay, err := r.Enqueue(ctx, in, now); err != nil || !replay.ArchivedAt.IsZero() || replay.Status != "cancelled" {
		t.Fatal("rollback lost original receipt", replay, err)
	}
}

func TestPostgreSQLPluginArchivedMetadataAndRestoreIntegrity(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond).Add(123 * time.Nanosecond)
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "integrity", ActorID: 1, Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Change(ctx, j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	event := pluginjobs.Event{Key: "integrity-event", Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if _, err = r.FreezeFanout(ctx, event, nil); err != nil {
		t.Fatal(err)
	}
	if err = r.CompleteFanout(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Before: now, Tasks: true, Fanouts: true}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	fresh := in
	fresh.Key = "keep-pending-on-failure"
	pending, err := r.Enqueue(ctx, fresh, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"plugin", "status", "updated_at", "archived_at"} {
		t.Run(field, func(t *testing.T) {
			var value any = now.Add(2 * time.Second)
			plugin := "reader"
			if field == "plugin" {
				plugin, value = "wrong-reader", "wrong-reader"
			}
			if field == "status" {
				value = "succeeded"
			}
			if field == "updated_at" {
				value = now.Add(500 * time.Millisecond)
			}
			if _, err := pool.Exec(ctx, "UPDATE np_plugin_job_archives SET "+field+"=$2 WHERE id=$1", j.ID, value); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Enqueue(ctx, in, now); !errors.Is(err, errPluginTaskRow) {
				t.Fatal("corrupted archive could replay", err)
			}
			if _, err := r.List(ctx, pluginjobs.Query{Plugin: plugin, Archived: true}); !errors.Is(err, errPluginTaskRow) {
				t.Fatal("corrupted archive could be listed", err)
			}
			if err := r.CancelRestored(ctx, now.Add(3*time.Second)); !errors.Is(err, errPluginTaskRow) {
				t.Fatal("corrupt archive accepted restore", err)
			}
			if current, err := scanTask(pool.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1`, pending.ID)); err != nil || current.Status != "pending" {
				t.Fatal("failed restore committed active cancellation", err)
			}
			if _, err := pool.Exec(ctx, `UPDATE np_plugin_job_archives SET plugin=$2,status='cancelled',updated_at=$3,archived_at=$4 WHERE id=$1`, j.ID, in.Plugin, now, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		})
	}
	archived, err := scanArchivedFanout(pool.QueryRow(ctx, `SELECT event_key,archived_at,cancelled,data FROM np_plugin_fanout_archives WHERE event_key=$1`, event.Key))
	if err != nil {
		t.Fatal(err)
	}
	bad := archived
	bad.Receipt.Delivered = false
	raw, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_plugin_fanout_archives SET data=$2 WHERE event_key=$1`, event.Key, raw); err != nil {
		t.Fatal(err)
	}
	if err = r.CancelRestored(ctx, now.Add(3*time.Second)); !errors.Is(err, pluginjobs.ErrInvalid) {
		t.Fatal("corrupt archived fanout accepted restore", err)
	}
	raw, err = json.Marshal(archived)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE np_plugin_fanout_archives SET data=$2 WHERE event_key=$1`, event.Key, raw); err != nil {
		t.Fatal(err)
	}
	if err = r.CancelRestored(ctx, now.Add(3*time.Second)); err != nil {
		t.Fatal("valid archive rejected restore", err)
	}
	if replay, err := r.Enqueue(ctx, in, now); err != nil || replay.Status != "cancelled" || replay.ArchivedAt.IsZero() {
		t.Fatal("restore dropped archived task facts", replay, err)
	}
	if receipt, found, err := r.FindFanout(ctx, event); err != nil || !found || !receipt.Cancelled || !receipt.Delivered {
		t.Fatal("restore dropped archived event facts", receipt, err)
	}
}

func TestPostgreSQLPluginArchiveHistoryCountNeverEvictsOldKeys(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Now().UTC()
	in := pluginjobs.Input{Plugin: "reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "still-live", Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Change(ctx, j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	j, err = r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := pluginjobs.ArchiveJob(j, now)
	if err != nil {
		t.Fatal(err)
	}
	values := make([][]any, 0, pluginjobs.MaxArchivedJobs)
	for i := range pluginjobs.MaxArchivedJobs {
		h := sha256.Sum256([]byte(fmt.Sprintf("history-%d", i)))
		item := archived
		item.ID = hex.EncodeToString(h[:])
		item.Plugin, item.Kind = "a", "b"
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, []any{item.ID, item.Plugin, item.Status, item.UpdatedAt, item.ArchivedAt, raw})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"np_plugin_job_archives"}, []string{"id", "plugin", "status", "updated_at", "archived_at", "data"}, pgx.CopyFromRows(values)); err != nil {
		t.Fatal(err)
	}
	result, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Before: now, Tasks: true}, now.Add(time.Second))
	if !errors.Is(err, pluginjobs.ErrCapacity) || result != (pluginjobs.ArchiveResult{}) {
		t.Fatal("archive silently discarded history at capacity", result, err)
	}
	if replay, err := r.Enqueue(ctx, in, now); err != nil || !replay.ArchivedAt.IsZero() || replay.Status != "cancelled" {
		t.Fatal("failed reclamation removed the active receipt", replay, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_plugin_job_archives`).Scan(&count); err != nil || count != pluginjobs.MaxArchivedJobs {
		t.Fatal("history was evicted", count, err)
	}
}

func TestPostgreSQLPluginArchiveByteBudgetRollbackAndRestoreReservation(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	in := pluginjobs.Input{Plugin: strings.Repeat("a", 96), Epoch: strings.Repeat("a", 64), Kind: strings.Repeat("b", 96), Key: "byte-budget", Payload: json.RawMessage(`{}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Change(ctx, j.Plugin, j.ID, j.Version, "cancel", now); err != nil {
		t.Fatal(err)
	}
	j, err = r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	subs := make([]pluginjobs.Subscription, 1000)
	for i := range subs {
		subs[i] = pluginjobs.Subscription{Plugin: fmt.Sprintf("reader-%04d-", i) + strings.Repeat("a", 84), Epoch: strings.Repeat("b", 64), Since: now, ActorID: 1}
	}
	var required int64
	fanoutRows := [][]any{}
	for i := 0; ; i++ {
		f := pluginjobs.ArchivedFanout{Receipt: pluginjobs.FanoutReceipt{Event: pluginjobs.Event{Key: fmt.Sprintf("fill-%06d", i), Kind: "content.updated", ObjectID: 1, Version: 1, At: now}, Subscriptions: subs, Delivered: true}, ArchivedAt: now}
		if err := pluginjobs.ValidateArchivedFanout(f); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		added := int64(len(raw) + len(`,"cancelled":true`))
		if required+added > pluginjobs.MaxArchiveBytes {
			break
		}
		required += added
		fanoutRows = append(fanoutRows, []any{f.Receipt.Event.Key, f.ArchivedAt, false, raw})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"np_plugin_fanout_archives"}, []string{"event_key", "archived_at", "cancelled", "data"}, pgx.CopyFromRows(fanoutRows)); err != nil {
		t.Fatal(err)
	}
	base, err := pluginjobs.ArchiveJob(j, now)
	if err != nil {
		t.Fatal(err)
	}
	base.Plugin, base.Kind = "a", "b"
	taskRows := [][]any{}
	for i := 0; ; i++ {
		item := base
		h := sha256.Sum256([]byte(fmt.Sprintf("budget-%d", i)))
		item.ID = hex.EncodeToString(h[:])
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if required+int64(len(raw)) > pluginjobs.MaxArchiveBytes {
			break
		}
		required += int64(len(raw))
		taskRows = append(taskRows, []any{item.ID, item.Plugin, item.Status, item.UpdatedAt, item.ArchivedAt, raw})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"np_plugin_job_archives"}, []string{"id", "plugin", "status", "updated_at", "archived_at", "data"}, pgx.CopyFromRows(taskRows)); err != nil {
		t.Fatal(err)
	}
	if required > pluginjobs.MaxArchiveBytes || pluginjobs.MaxArchiveBytes-required > 600 {
		t.Fatal("invalid history byte fixture", required)
	}
	if result, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now}, now); !errors.Is(err, pluginjobs.ErrCapacity) || result != (pluginjobs.ArchiveResult{}) {
		t.Fatal("archive discarded history at the byte boundary", result, err)
	}
	if replay, err := r.Enqueue(ctx, in, now); err != nil || replay.Status != "cancelled" || !replay.ArchivedAt.IsZero() {
		t.Fatal("failed archive removed original receipt", replay, err)
	}
	if err = r.CancelRestored(ctx, now); err != nil {
		t.Fatal("history did not reserve restore cancellation space", err)
	}
	var storedBytes int64
	var uncancelled int
	if err = pool.QueryRow(ctx, `SELECT (SELECT coalesce(sum(octet_length(data)),0) FROM np_plugin_job_archives)
 +(SELECT coalesce(sum(octet_length(data)),0) FROM np_plugin_fanout_archives),
 (SELECT count(*) FROM np_plugin_fanout_archives WHERE NOT cancelled)`).Scan(&storedBytes, &uncancelled); err != nil || storedBytes > pluginjobs.MaxArchiveBytes || uncancelled != 0 {
		t.Fatal("restore exceeded history byte budget", storedBytes, uncancelled, err)
	}
}
