package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/pluginjobs"
)

func TestPostgreSQLPluginTaskEncodingRoundTrip(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema := fmt.Sprintf("plugin_encoding_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	if err := Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	repository := NewPluginJobRepository(isolated)
	now := time.Now().UTC()
	in := pluginjobs.Input{Plugin: "encoding-boundary", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "stable", ActorID: 1}
	nested := func(depth int) json.RawMessage {
		return json.RawMessage(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
	}
	for _, invalid := range []struct {
		key     string
		payload json.RawMessage
	}{
		{"invalid-\xff", json.RawMessage(`{}`)},
		{strings.Repeat("\xff", 86), json.RawMessage(`{}`)},
		{"stable", nested(9998)},
		{"stable", nested(9999)},
		{"stable", nested(10000)},
	} {
		in.Key, in.Payload = invalid.key, invalid.payload
		if job, err := repository.Enqueue(ctx, in, now); !errors.Is(err, pluginjobs.ErrInvalid) || job.ID != "" {
			t.Fatalf("invalid encoding reached SQL: id=%q err=%v", job.ID, err)
		}
	}
	page, err := repository.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || page.Total != 0 {
		t.Fatal("invalid inputs left unreadable task rows", err)
	}
	in.Key, in.Payload = strings.Repeat("\ufffd", 85)+"x", nested(9997)
	original, err := repository.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal("valid boundary rejected", err)
	}
	repository = NewPluginJobRepository(isolated)
	replay, err := repository.Enqueue(ctx, in, now)
	if err != nil || replay.ID != original.ID || replay.Fingerprint != original.Fingerprint || replay.Key != in.Key {
		t.Fatal("SQL reload changed task identity", err)
	}
	claim, ok, err := repository.Claim(ctx, now)
	if err != nil || !ok || !bytes.Equal(claim.Payload, in.Payload) {
		t.Fatal("SQL boundary task cannot execute", err)
	}
	if err := repository.Finish(ctx, claim, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err = repository.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != "succeeded" {
		t.Fatal("completion cannot reload", err)
	}

	event := pluginjobs.Event{Key: strings.Repeat("\xff", 67), Kind: "content.updated", ObjectID: 1, Version: 1, At: now}
	if receipt, err := repository.FreezeFanout(ctx, event, nil); !errors.Is(err, pluginjobs.ErrInvalid) || receipt.Event.Key != "" {
		t.Fatalf("invalid event key reached SQL: %v", err)
	}
	var receipts int
	if err := isolated.QueryRow(ctx, `SELECT count(*) FROM np_plugin_fanouts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("invalid event left a receipt row", receipts, err)
	}
	event.Key = strings.Repeat("\ufffd", 66) + "xx"
	first, err := repository.FreezeFanout(ctx, event, nil)
	if err != nil {
		t.Fatal("valid Unicode event key rejected", err)
	}
	restored, err := NewPluginJobRepository(isolated).FreezeFanout(ctx, event, nil)
	if err != nil || !pluginjobs.SameEvent(first.Event, restored.Event) {
		t.Fatal("SQL receipt identity changed", err)
	}
}
