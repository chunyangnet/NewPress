package database

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/migrations"
)

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return OpenWithMaxConns(ctx, databaseURL, 16)
}

// OpenWithMaxConns opens a PostgreSQL pool with an explicit per-process
// connection ceiling. Open defaults to 16;
// deployments with small provider budgets should pass a lower value.
func OpenWithMaxConns(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	return OpenWithPoolLimits(ctx, databaseURL, 1, maxConns)
}

// OpenWithPoolLimits validates and opens the configured minimum before
// returning. The pool still grows on demand up to maxConns. MinConns is a
// maintenance target, not a promise that these connections can never expire.
func OpenWithPoolLimits(ctx context.Context, databaseURL string, minConns, maxConns int32) (*pgxpool.Pool, error) {
	if maxConns < 1 || maxConns > 64 {
		return nil, fmt.Errorf("database max connections must be between 1 and 64")
	}
	if minConns < 1 || minConns > maxConns {
		return nil, fmt.Errorf("database min connections must be between 1 and max connections")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConns = maxConns
	config.MinConns = minConns
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	return openPreparedPool(ctx, config)
}

func openPreparedPool(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := preparePoolConnections(ctx, pool, config.MinConns); err != nil {
		cancel() // Stop initial background connection construction before Close waits.
		pool.Close()
		return nil, fmt.Errorf("prepare database connections: %w", err)
	}
	return pool, nil
}

// Only used before exposing a newly created pool to callers. Keep each
// connection acquired until all have been checked, so a single connection
// cannot satisfy the configured minimum by being reused repeatedly.
func preparePoolConnections(ctx context.Context, pool *pgxpool.Pool, count int32) error {
	connections := make([]*pgxpool.Conn, 0, count)
	defer func() {
		for _, connection := range connections {
			connection.Release()
		}
	}()
	for range count {
		connection, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		connections = append(connections, connection)
		if err := connection.Ping(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('newpress-schema-migrations', 0))`); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('newpress-schema-migrations', 0))`)

	if _, err := connection.Exec(ctx, `CREATE TABLE IF NOT EXISTS newpress_schema_migrations (version text PRIMARY KEY, checksum text NOT NULL DEFAULT '', applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)

	for _, name := range entries {
		sqlBytes, err := migrations.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(sqlBytes))
		var appliedChecksum string
		err = connection.QueryRow(ctx, `SELECT checksum FROM newpress_schema_migrations WHERE version=$1`, name).Scan(&appliedChecksum)
		if err == nil {
			if appliedChecksum != checksum {
				return fmt.Errorf("migration %s checksum changed after it was applied", name)
			}

			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", name, err)
		}

		tx, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, `INSERT INTO newpress_schema_migrations(version,checksum) VALUES ($1,$2)`, name, checksum); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
