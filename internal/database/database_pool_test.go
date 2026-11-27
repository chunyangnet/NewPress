package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDatabasePoolLimitsRejectInvalidValuesBeforeConnecting(t *testing.T) {
	for _, pair := range [][2]int32{{0, 16}, {-1, 16}, {17, 16}, {1, 0}, {1, 65}} {
		if pool, err := OpenWithPoolLimits(t.Context(), "not a database URL", pair[0], pair[1]); pool != nil || err == nil {
			t.Fatal("invalid pool bounds accepted", pair, err)
		}
	}
}

func TestPostgreSQLPoolMinimumReadyBeforeReturnAndCeilingHolds(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	for _, size := range []int32{1, 2, 6} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool, err := OpenWithPoolLimits(ctx, dsn, size, size)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if stat := pool.Stat(); stat.IdleConns() != size || stat.AcquiredConns() != 0 || stat.ConstructingConns() != 0 {
				t.Fatal("configured connections were not all checked and returned", stat)
			}
			created := pool.Stat().NewConnsCount()
			var held []*pgxpool.Conn
			defer func() {
				for _, conn := range held {
					conn.Release()
				}
			}()
			pids := map[uint32]bool{}
			for range size {
				conn, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				held = append(held, conn)
				pid := conn.Conn().PgConn().PID()
				if pids[pid] {
					t.Fatal("minimum counted the same connection twice", pid)
				}
				pids[pid] = true
			}
			if pool.Stat().NewConnsCount() != created {
				t.Fatal("first concurrent demand had to construct connections")
			}
			limited, stop := context.WithTimeout(ctx, 50*time.Millisecond)
			defer stop()
			if extra, err := pool.Acquire(limited); extra != nil || !errors.Is(err, context.DeadlineExceeded) {
				if extra != nil {
					extra.Release()
				}
				t.Fatal("pool exceeded configured maximum", err)
			}
		})
	}
}

func TestPostgreSQLPoolWarmupFailureAndCancellationCloseConnections(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	observer, err := Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	for _, cancellation := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel-%v", cancellation), func(t *testing.T) {
			config, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			config.MinConns, config.MaxConns = 2, 2
			name := fmt.Sprintf("np_pool_warmup_%d", time.Now().UnixNano())
			config.ConnConfig.RuntimeParams["application_name"] = name
			injected := errors.New("injected additional connection failure")
			var connected atomic.Int32
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			config.AfterConnect = func(connectCtx context.Context, conn *pgx.Conn) error {
				if connected.Add(1) == 1 {
					return nil
				}
				if cancellation {
					cancel()
					<-connectCtx.Done()
					return connectCtx.Err()
				}
				return injected
			}
			pool, err := openPreparedPool(ctx, config)
			if pool != nil {
				pool.Close()
				t.Fatal("startup exposed a partially prepared pool")
			}
			if (!cancellation && !errors.Is(err, injected)) || (cancellation && !errors.Is(err, context.Canceled)) || connected.Load() < 2 {
				t.Fatal("partial startup failure", connected.Load(), err)
			}
			// Check actual server sessions, including background construction.
			check, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			for {
				var count int
				if err := observer.QueryRow(check, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1`, name).Scan(&count); err != nil {
					t.Fatal("failed pool retained sessions", err)
				}
				if count == 0 {
					break
				}
				select {
				case <-check.Done():
					t.Fatal("failed pool retained sessions", count)
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
}
