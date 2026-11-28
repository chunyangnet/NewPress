package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/questions"
)

func TestPostgreSQLQuestionsSnapshotAndConcurrentWrites(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("np_questions_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	config := admin.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := NewQuestionsRepository(pool)
	now := time.Now().UTC()
	owner, reader := questions.Actor{ID: 1}, questions.Actor{ID: 2}
	id, version, sequence := int64(0), int64(0), 0
	apply := func(actor questions.Actor, c questions.Command) {
		t.Helper()
		sequence++
		c.Key, c.Version = fmt.Sprintf("question-fixture-%d", sequence), version
		result, err := store.Apply(ctx, id, actor, c, now)
		if err != nil {
			t.Fatal(c.Action, err)
		}
		id, version = result.ID, result.Version
	}
	apply(owner, questions.Command{Action: "create", Title: "Persistent question", Body: "Question body", Tags: []string{"storage"}})
	apply(reader, questions.Command{Action: "answer", Body: "Answer body"})
	apply(owner, questions.Command{Action: "note", Body: "Question note"})
	apply(owner, questions.Command{Action: "note", AnswerID: 1, Body: "Answer note"})
	apply(reader, questions.Command{Action: "report", AnswerID: 1, Body: "Review this answer"})
	apply(owner, questions.Command{Action: "accept", AnswerID: 1})
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(snapshot)
	snapshot.Items[0].Hidden = true
	snapshot.Items[0].Tags[0] = "changed"
	snapshot.Items[0].Answers[0].Hidden = true
	snapshot.Items[0].Answers[0].Notes[0].Body = "changed"
	snapshot.Items[0].Reports[0].Resolved = true
	snapshot.Events[0].Recipients[0] = 99
	snapshot.Receipts[0].Result.Version = 99
	loaded, err := NewQuestionsRepository(pool).Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(loaded)
	if string(before) != string(after) {
		t.Fatal("returned snapshot changed persisted questions")
	}
	var wg sync.WaitGroup
	type outcome struct {
		command questions.Command
		result  questions.Result
		err     error
	}
	results := make(chan outcome, 8)
	for i := range 8 {
		wg.Go(func() {
			cmd := questions.Command{Action: "answer", Key: fmt.Sprintf("concurrent-answer-%d", i), Version: version, Body: "Concurrent answer"}
			result, err := NewQuestionsRepository(pool).Apply(ctx, id, reader, cmd, now)
			results <- outcome{cmd, result, err}
		})
	}
	wg.Wait()
	close(results)
	accepted, conflicts := 0, 0
	for out := range results {
		switch {
		case out.err == nil:
			accepted++
			replay, err := store.Apply(ctx, id, reader, out.command, now)
			if err != nil || !replay.Replay || replay.AnswerID != out.result.AnswerID || replay.Version != out.result.Version {
				t.Fatal("lost durable receipt after concurrent write", replay, err)
			}
		case errors.Is(out.err, questions.ErrConflict):
			conflicts++
		default:
			t.Fatal(out.err)
		}
	}
	if accepted != 1 || conflicts != 7 {
		t.Fatalf("version fencing: accepted=%d conflicts=%d", accepted, conflicts)
	}
	if err := store.AckActivity(ctx, loaded.Events[0].Key); err != nil {
		t.Fatal(err)
	}
	final, err := store.Snapshot(ctx)
	if err != nil || len(final.Items) != 1 || len(final.Items[0].Answers) != 2 || final.Items[0].AcceptedID != 1 || len(final.Events) != len(loaded.Events) {
		t.Fatal("completion changed question or outbox facts", err)
	}
}
