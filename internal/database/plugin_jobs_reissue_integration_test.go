package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/pluginjobs"
)

func seedReviewedPGSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, r *PluginJobRepository, status string) (pluginjobs.Job, pluginjobs.ReissueRequest, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 24, 0, 0, 0, 123456789, time.UTC)
	in := pluginjobs.Input{Plugin: "review-reader", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "private-source-key", ActorID: 7, Payload: json.RawMessage(`{"private":"original task parameters"}`)}
	j, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	j.Status, j.Version, j.UpdatedAt = status, j.Version+1, now.Add(time.Second)
	if status == "failed" {
		j.Attempts, j.ErrorCode = pluginjobs.MaxAttempts, "execution_failed"
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = saveTask(ctx, tx, j); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	in.Key, in.Payload = "reviewed-command", json.RawMessage(`{"reviewed":"explicit new parameters"}`)
	return j, pluginjobs.ReissueRequest{SourceID: j.ID, SourceVersion: j.Version, Input: in, Reason: "Checked prior business results", AllowNew: true}, now.Add(2 * time.Second)
}

func TestPostgreSQLPluginReissuePreservesSourceAcrossArchiveAndRestore(t *testing.T) {
	for _, kind := range []string{"failed", "cancelled", "archived-cancelled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, pool, r := newPluginJobsIntegrationStore(t)
			status := kind
			if kind == "archived-cancelled" {
				status = "cancelled"
			}
			source, request, now := seedReviewedPGSource(t, ctx, pool, r, status)
			table := "np_plugin_jobs"
			if kind == "archived-cancelled" {
				if _, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: source.UpdatedAt}, now); err != nil {
					t.Fatal(err)
				}
				table = "np_plugin_job_archives"
			}
			var before []byte
			if err := pool.QueryRow(ctx, "SELECT data FROM "+table+" WHERE id=$1", source.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			child, err := r.Reissue(ctx, request, now)
			if err != nil || child.ID == source.ID || child.Reissue == nil || child.Reissue.SourceVersion != source.Version || child.Reissue.SourceStatus != status {
				t.Fatal("reviewed admission", child, err)
			}
			var after []byte
			if err = pool.QueryRow(ctx, "SELECT data FROM "+table+" WHERE id=$1", source.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
				t.Fatal("review changed original task", err)
			}
			r = NewPluginJobRepository(pool)
			details, err := r.GetReview(ctx, source.Plugin, child.ID)
			if err != nil || details.Provenance == nil || details.Provenance.ReviewerID != request.Input.ActorID || details.Provenance.Reason != request.Reason || details.Reissuable {
				t.Fatal("reopened review", details, err)
			}
			raw, _ := json.Marshal(child)
			for _, secret := range []string{"explicit new parameters", "Checked prior business results", "reviewer_id", "request_fingerprint", "source_fingerprint", "private-source-key"} {
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatal("ordinary summary exposed private review", secret)
				}
			}
			replayRequest := request
			replayRequest.Input.Epoch, replayRequest.AllowNew = strings.Repeat("b", 64), false
			replay, err := r.Reissue(ctx, replayRequest, now.Add(time.Second))
			if err != nil || replay.ID != child.ID {
				t.Fatal("epoch change lost command receipt", replay, err)
			}
			claim, ok, err := r.Claim(ctx, now.Add(time.Second))
			if err != nil || !ok || claim.ID != child.ID {
				t.Fatal("reviewed task not executable", claim.ID, ok, err)
			}
			if err = r.Finish(ctx, claim, "", now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			replay, err = r.Reissue(ctx, replayRequest, now.Add(3*time.Second))
			if err != nil || replay.ID != child.ID || replay.Status != "succeeded" || replay.ArchivedAt.IsZero() {
				t.Fatal("child archive lost command receipt", replay, err)
			}
			var archive []byte
			if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_job_archives WHERE id=$1`, child.ID).Scan(&archive); err != nil || bytes.Contains(archive, []byte("explicit new parameters")) {
				t.Fatal("child archive retained original parameters", err)
			}
			request.Input.Key = "second-reviewed-command"
			second, err := r.Reissue(ctx, request, now.Add(4*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CancelRestored(ctx, now.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			request.Input.Epoch, request.AllowNew = strings.Repeat("c", 64), false
			replay, err = r.Reissue(ctx, request, now.Add(6*time.Second))
			if err != nil || replay.ID != second.ID || replay.Status != "cancelled" || replay.Reissue == nil {
				t.Fatal("restoration resumed or lost a reviewed command", replay, err)
			}
			if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(6 * time.Second)}, now.Add(7*time.Second)); err != nil {
				t.Fatal(err)
			}
			details, err = r.GetReview(ctx, source.Plugin, second.ID)
			if err != nil || details.Task.ArchivedAt.IsZero() || details.Provenance == nil || details.Provenance.Reason != request.Reason || details.Provenance.SourceStatus != status {
				t.Fatal("restored archive changed the checked source snapshot", details, err)
			}
		})
	}
}

func TestPostgreSQLPluginReissueConcurrentAdmissionAndArchiveLookup(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	source, request, now := seedReviewedPGSource(t, ctx, pool, r, "cancelled")
	start := make(chan struct{})
	type result struct {
		summary pluginjobs.Summary
		err     error
	}
	results := make(chan result, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			child, err := NewPluginJobRepository(pool).Reissue(ctx, request, now)
			results <- result{child, err}
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var id string
	for item := range results {
		if item.err != nil || id != "" && id != item.summary.ID {
			t.Fatal("same reissue command diverged", item.err)
		}
		id = item.summary.ID
	}
	if page, err := r.List(ctx, pluginjobs.Query{Plugin: source.Plugin}); err != nil || page.Total != 2 {
		t.Fatal("concurrent command admitted extra children", page.Total, err)
	}
	// There is no executing worker in this maintenance-concurrency fixture.
	if err := r.CancelRestored(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	errorsOut := make(chan error, 32)
	request.Input.Epoch, request.AllowNew = strings.Repeat("b", 64), false
	for i := range 32 {
		wg.Go(func() {
			<-start
			repository := NewPluginJobRepository(pool)
			var err error
			switch i % 4 {
			case 0:
				_, err = repository.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(2*time.Second))
			case 1:
				var child pluginjobs.Summary
				child, err = repository.Reissue(ctx, request, now.Add(2*time.Second))
				if err == nil && (child.ID != id || child.Status != "cancelled") {
					err = errors.New("archive confirmation resumed reviewed work")
				}
			case 2:
				var details pluginjobs.ReviewDetails
				details, err = repository.GetReview(ctx, source.Plugin, id)
				if err == nil && (details.Task.ID != id || details.Provenance == nil) {
					err = errors.New("review lookup observed an archive gap")
				}
			case 3:
				err = repository.CancelRestored(ctx, now.Add(2*time.Second))
			}
			errorsOut <- err
		})
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
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_plugin_jobs),(SELECT count(*) FROM np_plugin_job_archives)`).Scan(&live, &history); err != nil || live != 0 || history != 2 {
		t.Fatal("concurrent maintenance lost or duplicated reissues", live, history, err)
	}
}

func TestPostgreSQLPluginReissueFencesConcurrentSourceRetry(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	source, request, now := seedReviewedPGSource(t, ctx, pool, r, "failed")
	gate, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Release()
	if _, err = gate.Exec(ctx, `SELECT pg_advisory_lock(560056)`); err != nil {
		t.Fatal(err)
	}
	defer gate.Exec(context.Background(), `SELECT pg_advisory_unlock(560056)`)
	if _, err = pool.Exec(ctx, `CREATE FUNCTION gate_review_retry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.status='failed' AND NEW.status='pending' THEN PERFORM pg_advisory_xact_lock(560056); END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER gate_review_retry BEFORE UPDATE ON np_plugin_jobs FOR EACH ROW EXECUTE FUNCTION gate_review_retry()`); err != nil {
		t.Fatal(err)
	}
	retryDone := make(chan error, 1)
	go func() {
		retryDone <- r.Change(ctx, source.Plugin, source.ID, source.Version, "retry", now.Add(time.Second))
	}()
	for {
		var blocked bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=560056 AND NOT granted)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	reissueDone := make(chan error, 1)
	go func() { _, err := r.Reissue(ctx, request, now.Add(2*time.Second)); reissueDone <- err }()
	if _, err = gate.Exec(ctx, `SELECT pg_advisory_unlock(560056)`); err != nil {
		t.Fatal(err)
	}
	if err = <-retryDone; err != nil {
		t.Fatal(err)
	}
	if err = <-reissueDone; !errors.Is(err, pluginjobs.ErrConflict) {
		t.Fatal("review admitted a stale source version", err)
	}
	if page, err := r.List(ctx, pluginjobs.Query{Plugin: source.Plugin}); err != nil || page.Total != 1 || page.Items[0].Status != "pending" {
		t.Fatal("conflicting review created work", page, err)
	}
}

func TestPostgreSQLPluginReissueRollsBackAdmissionOnInsertFailure(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	source, request, now := seedReviewedPGSource(t, ctx, pool, r, "failed")
	var before, after []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, source.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_review_child() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.reissue_id IS NOT NULL THEN RAISE EXCEPTION 'review child rejected'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_review_child BEFORE INSERT ON np_plugin_jobs FOR EACH ROW EXECUTE FUNCTION reject_review_child()`); err != nil {
		t.Fatal(err)
	}
	if child, err := r.Reissue(ctx, request, now); err == nil || child.ID != "" {
		t.Fatal("failed insert returned an acceptance", child, err)
	}
	if err := pool.QueryRow(ctx, `SELECT data FROM np_plugin_jobs WHERE id=$1`, source.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed admission changed source", err)
	}
	if page, err := r.List(ctx, pluginjobs.Query{Plugin: source.Plugin}); err != nil || page.Total != 1 {
		t.Fatal("failed insert left a receipt", page, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_review_child ON np_plugin_jobs`); err != nil {
		t.Fatal(err)
	}
	if child, err := r.Reissue(ctx, request, now); err != nil || child.Reissue == nil {
		t.Fatal("failed command could not be retried", child, err)
	}
}

func TestPostgreSQLPluginReissueRejectsMismatchedReceiptIndexes(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	for _, archived := range []bool{false, true} {
		for _, corruption := range []string{"missing-index", "wrong-index", "reviewer"} {
			t.Run(fmt.Sprintf("archived=%t/%s", archived, corruption), func(t *testing.T) {
				if _, err := pool.Exec(ctx, `TRUNCATE np_plugin_jobs,np_plugin_job_archives`); err != nil {
					t.Fatal(err)
				}
				source, request, now := seedReviewedPGSource(t, ctx, pool, r, "cancelled")
				child, err := r.Reissue(ctx, request, now)
				if err != nil {
					t.Fatal(err)
				}
				table := "np_plugin_jobs"
				if archived {
					if err = r.Change(ctx, source.Plugin, child.ID, child.Version, "cancel", now.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
					if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(2*time.Second)); err != nil {
						t.Fatal(err)
					}
					table = "np_plugin_job_archives"
				}
				switch corruption {
				case "missing-index":
					_, err = pool.Exec(ctx, "UPDATE "+table+" SET reissue_id=NULL WHERE id=$1", child.ID)
				case "wrong-index":
					_, err = pool.Exec(ctx, "UPDATE "+table+" SET reissue_id=$2 WHERE id=$1", child.ID, strings.Repeat("f", 64))
				case "reviewer":
					_, err = pool.Exec(ctx, "UPDATE "+table+" SET data=convert_to((jsonb_set(convert_from(data,'UTF8')::jsonb,'{review,reviewer_id}','999'))::text,'UTF8') WHERE id=$1", child.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				var before []byte
				if err = pool.QueryRow(ctx, "SELECT data FROM "+table+" WHERE id=$1", child.ID).Scan(&before); err != nil {
					t.Fatal(err)
				}
				checks := []struct {
					name string
					run  func() error
				}{
					{"review", func() error { _, err := r.GetReview(ctx, source.Plugin, child.ID); return err }},
					{"list", func() error {
						_, err := r.List(ctx, pluginjobs.Query{Plugin: source.Plugin, Archived: archived})
						return err
					}},
					{"replay-after-epoch-change", func() error {
						request.Input.Epoch = strings.Repeat("b", 64)
						_, err := r.Reissue(ctx, request, now.Add(3*time.Second))
						return err
					}},
					{"restore", func() error { return r.CancelRestored(ctx, now.Add(3*time.Second)) }},
				}
				for _, check := range checks {
					t.Run(check.name, func(t *testing.T) {
						if err := check.run(); err == nil {
							t.Fatal("corrupt review accepted")
						}
					})
				}
				var after []byte
				if err = pool.QueryRow(ctx, "SELECT data FROM "+table+" WHERE id=$1", child.ID).Scan(&after); err != nil || !bytes.Equal(before, after) {
					t.Fatal("rejected operation rewrote evidence", err)
				}
				var count int
				if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_plugin_jobs)+(SELECT count(*) FROM np_plugin_job_archives)`).Scan(&count); err != nil || count != 2 {
					t.Fatal("corrupt receipt admitted another epoch", count, err)
				}
			})
		}
	}
}

func TestPostgreSQLPluginReissueRejectsCrossTableDuplicateCommands(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	source, request, now := seedReviewedPGSource(t, ctx, pool, r, "cancelled")
	child, err := r.Reissue(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Change(ctx, source.Plugin, child.ID, child.Version, "cancel", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(time.Second)}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	request.Input.Epoch = strings.Repeat("b", 64)
	duplicate, err := pluginjobs.BuildReissuedJob(request, pluginjobs.Summarize(source), source.Fingerprint, now.Add(3*time.Second))
	if err != nil || duplicate.ID == child.ID {
		t.Fatal("duplicate fixture", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = saveTask(ctx, tx, duplicate); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Reissue(ctx, request, now.Add(4*time.Second)); !errors.Is(err, errPluginTaskRow) {
		t.Fatal("duplicate command was confirmed", err)
	}
	if err = r.CancelRestored(ctx, now.Add(4*time.Second)); !errors.Is(err, errPluginTaskRow) {
		t.Fatal("restore accepted cross-table duplicate commands", err)
	}
	if details, err := r.GetReview(ctx, source.Plugin, duplicate.ID); err != nil || details.Task.Status != "pending" {
		t.Fatal("rejected restore was not atomic", details, err)
	}
}

func TestPostgreSQLPluginReissueCapacityAndMaximumReason(t *testing.T) {
	ctx, pool, r := newPluginJobsIntegrationStore(t)
	source, request, now := seedReviewedPGSource(t, ctx, pool, r, "failed")
	request.Reason = "a" + strings.Repeat("\t", pluginjobs.MaxReviewReasonBytes-2) + "z"
	child, err := r.Reissue(ctx, request, now)
	if err != nil {
		t.Fatal("maximum encoded review reason rejected", err)
	}
	values := make([][]any, 0, pluginjobs.MaxPluginJobs-2)
	for i := range pluginjobs.MaxPluginJobs - 2 {
		q, err := taskQueue(nil)
		if err != nil {
			t.Fatal(err)
		}
		in := request.Input
		in.Key = fmt.Sprintf("capacity-%d", i)
		job, err := q.Enqueue(ctx, in, source.UpdatedAt)
		if err != nil {
			t.Fatal(err)
		}
		job.Status = "cancelled"
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, []any{job.ID, job.Plugin, job.Status, job.AvailableAt, job.UpdatedAt, nil, raw, nil})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"np_plugin_jobs"}, []string{"id", "plugin", "status", "available_at", "updated_at", "lease_until", "data", "reissue_id"}, pgx.CopyFromRows(values)); err != nil {
		t.Fatal(err)
	}
	request.Input.Epoch, request.AllowNew = strings.Repeat("b", 64), false
	if replay, err := r.Reissue(ctx, request, now.Add(time.Second)); err != nil || replay.ID != child.ID {
		t.Fatal("capacity blocked existing confirmation", replay, err)
	}
	fresh := request
	fresh.Input.Key, fresh.AllowNew = "fresh-capacity-command", true
	if _, err = r.Reissue(ctx, fresh, now.Add(time.Second)); !errors.Is(err, pluginjobs.ErrCapacity) {
		t.Fatal("reissue exceeded per-plugin capacity", err)
	}
	if result, err := r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: source.UpdatedAt, Limit: 1}, now.Add(2*time.Second)); err != nil || result.Tasks != 1 {
		t.Fatal("capacity archive", result, err)
	}
	if _, err = r.Reissue(ctx, fresh, now.Add(3*time.Second)); err != nil {
		t.Fatal("reissue could not use reclaimed capacity", err)
	}
	if err = r.Change(ctx, source.Plugin, child.ID, child.Version, "cancel", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Archive(ctx, pluginjobs.ArchiveQuery{Tasks: true, Before: now.Add(4 * time.Second), Limit: pluginjobs.MaxArchiveBatch}, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_job_archives WHERE id=$1`, child.ID).Scan(&raw); err != nil || len(raw) > 4096 {
		t.Fatal("maximum review exceeded archive decoder budget", len(raw), err)
	}
	details, err := r.GetReview(ctx, source.Plugin, child.ID)
	if err != nil || details.Provenance == nil || !reflect.DeepEqual(details.Provenance.Reason, request.Reason) {
		t.Fatal("maximum review did not survive archive decoding", err)
	}
}

func TestPostgreSQLPluginReissueReceiptLookupIgnoresUnrelatedPayloadDigest(t *testing.T) {
	ctx, _, r := newPluginJobsIntegrationStore(t)
	_, request, now := seedReviewedPGSource(t, ctx, r.pool, r, "cancelled")
	_, identity, err := pluginjobs.PrepareReissue(request)
	if err != nil {
		t.Fatal(err)
	}
	in := request.Input
	in.Key = "unrelated-digest-payload"
	in.Payload, err = json.Marshal(map[string]string{"contains": identity.ID})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := r.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	child, err := r.Reissue(ctx, request, now)
	if err != nil || child.ID == unrelated.ID {
		t.Fatal("unrelated payload digest treated as receipt", child, err)
	}
	request.Input.Epoch = strings.Repeat("b", 64)
	request.AllowNew = false
	if replay, err := r.Reissue(ctx, request, now.Add(time.Second)); err != nil || replay.ID != child.ID {
		t.Fatal("unrelated payload digest interfered with receipt confirmation", replay, err)
	}
}
