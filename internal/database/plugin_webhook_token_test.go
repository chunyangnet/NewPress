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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/plugindispatch"
)

func webhookTokenFixture(t *testing.T) (*webhookFixture, string, []byte) {
	t.Helper()
	f := newWebhookFixture(t)
	f.req.Body = json.RawMessage(`{"data":"` + strings.Repeat("x", 16000) + `"}`)
	a := f.accept(t, f.req.Slot)
	var raw []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, a.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original plugindispatch.Intent
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	rows := make([][]any, 0, 599)
	for n := 1; n < 600; n++ {
		i := original
		i.JobID = fmt.Sprintf("%064x", n)
		i.ID, _ = plugindispatch.OperationID(i.JobID, i.Slot)
		i.State, i.Version = "cancelled", 2
		i.Outcome = plugindispatch.Outcome{Code: "maintenance"}
		if err := plugindispatch.Validate(i); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(i)
		rows = append(rows, []any{i.ID, i.JobID, i.Slot, i.Version, i.State, data})
	}
	if _, err := f.pool.CopyFrom(f.ctx, pgx.Identifier{"np_plugin_webhook_intents"}, []string{"id", "job_id", "slot", "version", "state", "data"}, pgx.CopyFromRows(rows)); err != nil {
		t.Fatal(err)
	}
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	encoded := 0
	for _, i := range f.webhooks.validated.snapshot() {
		encoded += len(i.raw)
	}
	if encoded <= webhookTokenTransferThreshold {
		t.Fatal("fixture does not exercise token reuse", encoded)
	}
	return f, a.OperationID, raw
}

func TestPostgreSQLWebhookTokenRejectsChangedContractAndReenableBypass(t *testing.T) {
	for _, mode := range []string{"disabled", "replica-only", "conditional", "extra-trigger", "function-changed", "disable-write-enable", "transient-trigger"} {
		t.Run(mode, func(t *testing.T) {
			f, id, raw := webhookTokenFixture(t)
			switch mode {
			case "disabled":
				f.exec(t, `ALTER TABLE np_plugin_webhook_intents DISABLE TRIGGER np_plugin_webhook_validation_token`)
			case "replica-only":
				f.exec(t, `ALTER TABLE np_plugin_webhook_intents ENABLE REPLICA TRIGGER np_plugin_webhook_validation_token`)
			case "conditional":
				f.exec(t, `DROP TRIGGER np_plugin_webhook_validation_token ON np_plugin_webhook_intents`)
				f.exec(t, `CREATE TRIGGER np_plugin_webhook_validation_token BEFORE INSERT OR UPDATE ON np_plugin_webhook_intents FOR EACH ROW WHEN (false) EXECUTE FUNCTION np_plugin_webhook_validation_token()`)
				f.exec(t, `ALTER TABLE np_plugin_webhook_intents ENABLE ALWAYS TRIGGER np_plugin_webhook_validation_token`)
			case "extra-trigger":
				f.exec(t, `CREATE TRIGGER unexpected_webhook_write BEFORE UPDATE ON np_plugin_webhook_intents FOR EACH ROW EXECUTE FUNCTION np_plugin_webhook_validation_token()`)
			case "transient-trigger":
				// A later trigger can restore OLD's token and then be removed,
				// leaving the original trigger/function catalogue rows unchanged.
				f.exec(t, `CREATE FUNCTION webhook_preserve_old_token() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN NEW.validation_token := OLD.validation_token; RETURN NEW; END;'`)
				f.exec(t, `CREATE TRIGGER z_webhook_preserve_old_token BEFORE UPDATE ON np_plugin_webhook_intents FOR EACH ROW EXECUTE FUNCTION webhook_preserve_old_token()`)
				changed := bytes.Replace(raw, []byte(strings.Repeat("x", 16)), []byte("y"+strings.Repeat("x", 15)), 1)
				f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, changed)
				f.exec(t, `DROP TRIGGER z_webhook_preserve_old_token ON np_plugin_webhook_intents`)
				f.exec(t, `DROP FUNCTION webhook_preserve_old_token()`)
			case "function-changed":
				f.exec(t, `CREATE OR REPLACE FUNCTION np_plugin_webhook_validation_token() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END;'`)
			case "disable-write-enable":
				f.exec(t, `ALTER TABLE np_plugin_webhook_intents DISABLE TRIGGER np_plugin_webhook_validation_token`)
				changed := bytes.Replace(raw, []byte(strings.Repeat("x", 16)), []byte("y"+strings.Repeat("x", 15)), 1)
				if len(changed) != len(raw) || bytes.Equal(changed, raw) {
					t.Fatal("fixture must corrupt bytes without changing their length")
				}
				f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, changed)
				f.exec(t, `ALTER TABLE np_plugin_webhook_intents ENABLE ALWAYS TRIGGER np_plugin_webhook_validation_token`)
			}
			if err := f.webhooks.Ready(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("changed trigger/data reused old bytes", err)
			}
			if _, err := f.webhooks.List(f.ctx, plugindispatch.Query{State: "http_confirmed"}); !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("empty filter bypassed invalid contract", err)
			}
			if _, err := f.webhooks.Enqueue(f.ctx, f.req); !errors.Is(err, plugindispatch.ErrCorrupt) {
				t.Fatal("replay bypassed invalid contract", err)
			}
		})
	}
}

func TestPostgreSQLWebhookTokenAlwaysRotatesAndTransfersChangedRows(t *testing.T) {
	f, id, raw := webhookTokenFixture(t)
	previous, stamp := f.webhooks.validated.snapshotWithStamp()
	ids, tokens := []string{}, []pgtype.UUID{}
	tuples := []string{}
	for id, i := range previous {
		ids = append(ids, id)
		tokens = append(tokens, pgtype.UUID{Bytes: i.token, Valid: true})
		tuples = append(tuples, i.tuple)
	}
	transferred := func(want int) {
		t.Helper()
		rows, err := f.pool.Query(f.ctx, currentWebhookRows, pgx.QueryExecModeCacheDescribe, plugindispatch.MaxRecords+1, ids, tokens, webhookTokenFunction, []byte(stamp), tuples)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		count, changed := 0, 0
		for rows.Next() {
			var a, b, c, d, tuple string
			var contract pgtype.DriverBytes
			var present bool
			var version int64
			var token pgtype.UUID
			var size int
			var data pgtype.DriverBytes
			if err := rows.Scan(&contract, &present, &a, &b, &c, &version, &d, &token, &tuple, &size, &data); err != nil {
				t.Fatal(err)
			}
			if string(contract) != stamp || !present {
				t.Fatal("unexpected schema change")
			}
			if data != nil {
				changed++
			}
			count++
		}
		if rows.Err() != nil || count != 600 || changed != want {
			t.Fatal("unexpected transfer", count, changed, rows.Err())
		}
	}
	for range 8 {
		transferred(0)
	}
	old := pgtype.UUID{Bytes: previous[id].token, Valid: true}
	f.exec(t, `UPDATE np_plugin_webhook_intents SET validation_token=$2,data=$3 WHERE id=$1`, id, old, append([]byte(" "), raw...))
	transferred(1)
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.webhooks.validated.snapshot()[id].token == old.Bytes {
		t.Fatal("caller supplied the old validation token")
	}
	// ALWAYS also covers a replica-role write, which ordinary enabled triggers
	// skip. A direct malformed update must invalidate the retained parse.
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(f.ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `UPDATE np_plugin_webhook_intents SET validation_token=$2,data=$3 WHERE id=$1`, id, old, []byte(`{`)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.webhooks.Ready(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
		t.Fatal("replica write reused old bytes", err)
	}
}

func TestPostgreSQLWebhookTokenChecksEmptyTableContract(t *testing.T) {
	f := newWebhookFixture(t)
	f.exec(t, `ALTER TABLE np_plugin_webhook_intents DISABLE TRIGGER np_plugin_webhook_validation_token`)
	if err := f.webhooks.Ready(f.ctx); !errors.Is(err, plugindispatch.ErrCorrupt) {
		t.Fatal("empty table bypassed trigger validation", err)
	}
}

func TestPostgreSQLWebhookTokenSnapshotsAndRollbackKeepVisibility(t *testing.T) {
	f, id, raw := webhookTokenFixture(t)
	var original plugindispatch.Intent
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	old, err := f.pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Rollback(context.Background())
	selectTarget := func(tx pgx.Tx) plugindispatch.Intent {
		t.Helper()
		items, total, _, err := f.webhooks.selectIntents(f.ctx, tx, func(i plugindispatch.Intent) bool { return i.ID == id })
		if err != nil || len(items) != 1 || total != 600 {
			t.Fatal("snapshot selection", len(items), total, err)
		}
		return items[0]
	}
	first := selectTarget(old)
	first.Body[0] = '!'
	changed := cloneWebhook(original)
	changed.CreatedAt = original.CreatedAt.Add(time.Second)
	changed.UpdatedAt = changed.CreatedAt
	if err := plugindispatch.Validate(changed); err != nil {
		t.Fatal(err)
	}
	updated, _ := json.Marshal(changed)
	f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, updated)
	check := func() {
		t.Helper()
		page, err := f.webhooks.List(f.ctx, plugindispatch.Query{JobID: original.JobID})
		if err != nil || len(page.Items) != 1 || !page.Items[0].CreatedAt.Equal(changed.CreatedAt) {
			t.Fatal("memo changed current visibility", page, err)
		}
	}
	check()
	stale := selectTarget(old)
	if !stale.CreatedAt.Equal(original.CreatedAt) || !bytes.Equal(stale.Body, original.Body) {
		t.Fatal("old snapshot or caller ownership lost")
	}
	if err := old.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	check()
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(f.ctx, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, raw); err != nil {
		t.Fatal(err)
	}
	if inside := selectTarget(tx); !inside.CreatedAt.Equal(original.CreatedAt) {
		t.Fatal("transaction did not see its own update")
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	check()
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			for range 3 {
				page, err := f.webhooks.List(f.ctx, plugindispatch.Query{JobID: original.JobID})
				if err != nil || len(page.Items) != 1 || !page.Items[0].CreatedAt.Equal(changed.CreatedAt) {
					t.Error("concurrent token read changed visibility", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestPostgreSQLWebhookTokenMigrationPreservesExistingBytes(t *testing.T) {
	f := newWebhookFixture(t)
	id := f.accept(t, f.req.Slot).OperationID
	var before []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_webhook_intents WHERE id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the pre-0064 table in this isolated schema while retaining a
	// valid pending operation. Applying the real migrator must leave it intact.
	f.exec(t, `DROP TRIGGER np_plugin_webhook_validation_token ON np_plugin_webhook_intents`)
	f.exec(t, `DROP FUNCTION np_plugin_webhook_validation_token()`)
	f.exec(t, `ALTER TABLE np_plugin_webhook_intents DROP COLUMN validation_token`)
	f.exec(t, `DELETE FROM newpress_schema_migrations WHERE version='0064_plugin_webhook_validation_token.sql'`)
	for range 2 {
		if err := Migrate(f.ctx, f.pool); err != nil {
			t.Fatal(err)
		}
	}
	var after []byte
	var token pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT data,validation_token FROM np_plugin_webhook_intents WHERE id=$1`, id).Scan(&after, &token); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !token.Valid {
		t.Fatal("upgrade changed durable bytes or failed to backfill the token")
	}
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLWebhookTokenRewriteAndTruncateInvalidateHints(t *testing.T) {
	f, id, raw := webhookTokenFixture(t)
	before, stamp := f.webhooks.validated.snapshotWithStamp()
	f.exec(t, `VACUUM FULL np_plugin_webhook_intents`)
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, rewritten := f.webhooks.validated.snapshotWithStamp()
	if rewritten == stamp {
		t.Fatal("table rewrite did not invalidate catalogue stamp")
	}
	f.exec(t, `TRUNCATE np_plugin_webhook_intents`)
	if err := f.webhooks.Ready(f.ctx); err != nil || len(f.webhooks.validated.snapshot()) != 0 {
		t.Fatal("truncated rows retained", err)
	}
	var original plugindispatch.Intent
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data,validation_token) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		id, original.JobID, original.Slot, original.Version, original.State, raw, pgtype.UUID{Bytes: before[id].token, Valid: true})
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.webhooks.validated.snapshot()[id].token == before[id].token {
		t.Fatal("reinsert reused supplied token")
	}
}

func TestPostgreSQLWebhookTokenWithoutDescriptionCache(t *testing.T) {
	f, _, _ := webhookTokenFixture(t)
	config := f.pool.Config()
	config.ConnConfig.DescriptionCacheCapacity = 0
	config.ConnConfig.StatementCacheCapacity = 0
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := NewPluginWebhookRepository(pool, true)
	for range 2 {
		if err := repository.Ready(f.ctx); err != nil {
			t.Fatal("uncached connection could not validate rows", err)
		}
	}
	if page, err := repository.List(f.ctx, plugindispatch.Query{JobID: f.req.Authorization.Claim.ID}); err != nil || len(page.Items) != 1 {
		t.Fatal("uncached connection could not project result", page, err)
	}
}
