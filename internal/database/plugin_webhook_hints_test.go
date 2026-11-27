package database

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"newpress.local/newpress/internal/plugindispatch"
)

func TestPostgreSQLWebhookHintsBinaryParametersRoundTrip(t *testing.T) {
	f := newWebhookFixture(t)
	for _, values := range [][]string{{}, {"", "NULL", `a,b{c}"d\e`, "中文\n行"}} {
		hints := &webhookValidationHints{rows: map[string]*validatedWebhook{}}
		for n, value := range values {
			hints.rows[value] = &validatedWebhook{token: [16]byte{byte(n + 1), 255}, tuple: value + ":(12,3)"}
		}
		ids, tokens, tuples := hints.parameters()
		connection, err := f.pool.Acquire(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		result := connection.Conn().PgConn().ExecParams(f.ctx,
			`SELECT * FROM unnest($1::text[],$2::uuid[],$3::text[])`,
			[][]byte{ids, tokens, tuples}, []uint32{pgtype.TextArrayOID, pgtype.UUIDArrayOID, pgtype.TextArrayOID},
			[]int16{pgx.BinaryFormatCode}, []int16{pgx.BinaryFormatCode})
		rows := pgx.RowsFromResultReader(connection.Conn().TypeMap(), result)
		seen := map[string]bool{}
		for rows.Next() {
			var id, tuple string
			var token pgtype.UUID
			if err := rows.Scan(&id, &token, &tuple); err != nil {
				t.Fatal(err)
			}
			want := hints.rows[id]
			if want == nil || seen[id] || !token.Valid || token.Bytes != want.token || tuple != want.tuple {
				t.Fatal("binary array altered a parameter", id, tuple, token)
			}
			seen[id] = true
		}
		err = rows.Err()
		rows.Close()
		connection.Release()
		if err != nil || len(seen) != len(values) {
			t.Fatal("binary array result", len(seen), err)
		}
	}
}

func TestPostgreSQLWebhookHintsOldSnapshotParametersRemainOwned(t *testing.T) {
	f, id, raw := webhookTokenFixture(t)
	_, _, before := f.webhooks.validated.snapshotWithHints()
	// Concurrent first readers initialize the same immutable parameter set.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 4 {
				before.parameters()
			}
		})
	}
	wg.Wait()
	a, b, c := before.parameters()
	saved := [][]byte{bytes.Clone(a), bytes.Clone(b), bytes.Clone(c)}
	f.exec(t, `UPDATE np_plugin_webhook_intents SET data=$2 WHERE id=$1`, id, append([]byte(" "), raw...))
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, _, after := f.webhooks.validated.snapshotWithHints()
	if before == after {
		t.Fatal("changed row retained old parameter snapshot")
	}
	after.parameters()
	a, b, c = before.parameters()
	if !bytes.Equal(a, saved[0]) || !bytes.Equal(b, saved[1]) || !bytes.Equal(c, saved[2]) {
		t.Fatal("later query rewrote old snapshot parameters")
	}
	if err := f.webhooks.Ready(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, _, unchanged := f.webhooks.validated.snapshotWithHints()
	if unchanged != after {
		t.Fatal("unchanged current rows recreated encoded parameters")
	}
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := queryCurrentWebhookRows(f.ctx, tx, nil, nil, nil, ""); !errors.Is(err, plugindispatch.ErrUnavailable) {
		t.Fatal("reader ran outside transaction", err)
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	if err := f.webhooks.Ready(cancelled); err == nil {
		t.Fatal("hints bypassed cancelled database read")
	}
}
