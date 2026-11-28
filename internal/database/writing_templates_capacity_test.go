package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/writingtemplate"
	"newpress.local/newpress/internal/writingtemplate/testkit"
)

func TestPostgreSQLWritingTemplateCapacityRestore(t *testing.T) {
	bin := os.Getenv("NEWPRESS_TEST_PG_BIN")
	if bin == "" {
		t.Skip("NEWPRESS_TEST_PG_BIN is not set")
	}
	for _, shape := range []string{"revisions", "bytes"} {
		t.Run(shape, func(t *testing.T) {
			ctx, pool := openSetupIntegrationPool(t)
			if err := Migrate(ctx, pool); err != nil {
				t.Fatal(err)
			}
			f := testkit.Capacity(t, shape)
			store := NewWritingTemplateStorage(pool)
			if err := store.Write(ctx, sha256.Sum256(nil), f.Raw); err != nil {
				t.Fatal(err)
			}
			config := pool.Config()
			conn := config.ConnConfig
			name := fmt.Sprintf("writing_template_capacity_%d", time.Now().UnixNano())
			quoted := pgx.Identifier{name}.Sanitize()
			if _, err := pool.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// Dropping a database can wait for a cluster-wide checkpoint.
				// A full integration run can leave tens of thousands of files
				// to sync; give teardown its own bounded I/O budget.
				cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if _, err := pool.Exec(cleanup, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
					t.Error(err)
				}
			})
			env := append(os.Environ(), "PGHOST="+conn.Host, "PGPORT="+strconv.Itoa(int(conn.Port)), "PGUSER="+conn.User, "PGPASSWORD="+conn.Password, "PGSSLMODE=disable")
			dump := filepath.Join(t.TempDir(), "template-capacity.dump")
			command := exec.CommandContext(ctx, filepath.Join(bin, "pg_dump"), "--format=custom", "--no-owner", "--schema="+conn.RuntimeParams["search_path"], "--file="+dump, conn.Database)
			command.Env = env
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("dump: %v %s", err, out)
			}
			command = exec.CommandContext(ctx, filepath.Join(bin, "pg_restore"), "--no-owner", "--exit-on-error", "--dbname="+name, dump)
			command.Env = env
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("restore: %v %s", err, out)
			}
			config.ConnConfig.Database = name
			clone, err := pgxpool.NewWithConfig(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(clone.Close)
			for n, candidate := range []*pgxpool.Pool{pool, clone} {
				storage := NewWritingTemplateStorage(candidate)
				s, err := writingtemplate.New(ctx, storage)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					items, err := s.Catalog(ctx)
					if err != nil || len(items) != f.Templates {
						t.Fatal("cold/warm capacity catalog", n, len(items), err)
					}
					item, err := s.Get(ctx, f.ID)
					if err != nil || len(item.Revisions) != int(f.Version) || item.Revisions[0].Definition != *f.Create.Definition {
						t.Fatal("capacity history", n, err)
					}
				}
				if replay, err := s.Apply(ctx, f.Create); err != nil || !replay.Replayed || replay.ID != f.ID || replay.Version != 1 {
					t.Fatal("capacity receipt after restore", n, replay, err)
				}
				if _, err := s.Apply(ctx, writingtemplate.Command{Action: "archive", ID: f.ID, ExpectedVersion: f.Version, Actor: f.Create.Actor, RequestKey: fmt.Sprintf("%064x", 2)}); !errors.Is(err, writingtemplate.ErrCapacity) {
					t.Fatal("capacity refusal", n, err)
				}
				raw, err := storage.Read(ctx)
				if err != nil || !bytes.Equal(raw, f.Raw) {
					t.Fatal("source/restore bytes changed", n, err)
				}
			}
			t.Logf("constructed %s: templates=%d revisions=%d encoded_bytes=%d; PostgreSQL write, actual dump/restore, cold/warm reads, original receipt and full refusal verified", shape, f.Templates, f.Revisions, len(f.Raw))
		})
	}
}
