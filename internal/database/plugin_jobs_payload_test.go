package database

import (
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

func TestPluginTaskRejectsPayloadLargerThanDurableReader(t *testing.T) {
	queue, err := taskQueue(nil)
	if err != nil {
		t.Fatal(err)
	}
	in := pluginjobs.Input{Plugin: "payload-boundary", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "large", ActorID: 1,
		Payload: json.RawMessage(`{"text":"` + strings.Repeat("<", 12000) + `"}`)}
	job, err := queue.Enqueue(t.Context(), in, time.Now().UTC())
	if !errors.Is(err, pluginjobs.ErrInvalid) || job.ID != "" {
		raw, _ := json.Marshal(job)
		_, decodeErr := decodeTask(raw)
		t.Fatalf("accepted task that exceeds persistent reader: raw=%d id=%q enqueue=%v decode=%v", len(raw), job.ID, err, decodeErr)
	}
}

func TestPluginTaskEscapedPayloadBoundarySurvivesDecoder(t *testing.T) {
	queue, err := taskQueue(nil)
	if err != nil {
		t.Fatal(err)
	}
	in := pluginjobs.Input{Plugin: "payload-boundary", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "exact", ActorID: 1,
		Payload: json.RawMessage(`{"text":"` + strings.Repeat("<", (pluginjobs.MaxPayloadBytes-11)/6) + strings.Repeat("x", (pluginjobs.MaxPayloadBytes-11)%6) + `"}`)}
	encoded, err := json.Marshal(in.Payload)
	if err != nil || len(encoded) != pluginjobs.MaxPayloadBytes {
		t.Fatalf("bad boundary: %d %v", len(encoded), err)
	}
	now := time.Now().UTC()
	original, err := queue.Enqueue(t.Context(), in, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := decodeTask(raw)
	if err != nil {
		t.Fatal(err)
	}
	queue, err = taskQueue(&restored)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := queue.Enqueue(t.Context(), in, now)
	if err != nil || replay.ID != original.ID || replay.Fingerprint != original.Fingerprint {
		t.Fatal("encoded boundary changed task identity", err)
	}
	claim, ok, err := queue.Claim(t.Context(), now)
	if err != nil || !ok {
		t.Fatal("restored boundary task cannot execute", err)
	}
	if err = queue.Finish(t.Context(), claim, "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestPostgreSQLPluginTaskPayloadPersistence(t *testing.T) {
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
	schema := fmt.Sprintf("plugin_payload_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	if err = Migrate(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	repository := NewPluginJobRepository(isolated)
	now := time.Now().UTC()
	in := pluginjobs.Input{Plugin: "payload-boundary", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "stable", ActorID: 1}
	for _, raw := range []string{`{"text":"` + strings.Repeat("<", 12000) + `"}`, `{"x":1,"x":2}`, `{"text":"\ud800"}`} {
		in.Payload = json.RawMessage(raw)
		if job, err := repository.Enqueue(ctx, in, now); !errors.Is(err, pluginjobs.ErrInvalid) || job.ID != "" {
			t.Fatalf("unreadable payload reached SQL storage: id=%q err=%v", job.ID, err)
		}
	}
	page, err := repository.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || page.Total != 0 {
		t.Fatal("rejected tasks left unreadable SQL rows", err)
	}
	in.Payload = json.RawMessage(`{"text":"` + strings.Repeat("<", (pluginjobs.MaxPayloadBytes-11)/6) + strings.Repeat("x", (pluginjobs.MaxPayloadBytes-11)%6) + `"}`)
	original, err := repository.Enqueue(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	repository = NewPluginJobRepository(isolated)
	replay, err := repository.Enqueue(ctx, in, now.Add(time.Second))
	if err != nil || replay.ID != original.ID || replay.Fingerprint != original.Fingerprint {
		t.Fatal("SQL reload changed boundary identity", err)
	}
	claim, ok, err := repository.Claim(ctx, now.Add(time.Second))
	if err != nil || !ok {
		t.Fatal("SQL boundary task cannot be claimed", err)
	}
	if err = repository.Finish(ctx, claim, "", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	page, err = repository.List(ctx, pluginjobs.Query{Plugin: in.Plugin})
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != "succeeded" {
		t.Fatalf("SQL boundary completion is unreadable: %+v %v", page, err)
	}
}
