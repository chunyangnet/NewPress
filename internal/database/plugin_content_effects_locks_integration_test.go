package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/pluginjobs"
)

func contentEffectNamedPool(t *testing.T, f *contentEffectFixture, name string) *pgxpool.Pool {
	t.Helper()
	config := f.pool.Config()
	config.ConnConfig.RuntimeParams["application_name"] = name
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func waitContentEffectLock(t *testing.T, f *contentEffectFixture, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	for {
		var blocked bool
		err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid))>0)`, name).Scan(&blocked)
		if err != nil {
			t.Fatal("observe lock wait", err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected database lock was not reached", name)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestPostgreSQLPluginContentCommitLocksFenceRevocation(t *testing.T) {
	for _, mode := range []string{"authority", "account", "role", "cancel", "restore"} {
		t.Run(mode, func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			writerName, revokeName := fmt.Sprintf("effect-writer-%d", time.Now().UnixNano()), fmt.Sprintf("effect-revoke-%d", time.Now().UnixNano())
			writerPool, revokePool := contentEffectNamedPool(t, f, writerName), contentEffectNamedPool(t, f, revokeName)
			blocker, err := f.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(f.ctx, `SELECT id FROM np_posts WHERE id=$1 FOR UPDATE`, f.post.ID); err != nil {
				t.Fatal(err)
			}
			written, revoked := make(chan error, 1), make(chan error, 1)
			go func() {
				_, err := NewPluginContentEffectRepository(writerPool).WriteContent(f.ctx, f.request)
				written <- err
			}()
			waitContentEffectLock(t, f, writerName)
			go func() {
				var err error
				switch mode {
				case "authority":
					c := f.control
					c.Revision++
					c.Enabled = false
					err = NewPluginTaskControlRepository(revokePool).SaveTaskControl(f.ctx, f.control.Revision, c)
				case "account":
					_, err = NewUserRepository(revokePool).UpdateUser(f.ctx, f.user.ID, auth.UserMutation{ExpectedVersion: f.user.Version, Actor: "revoke"}, auth.UserInput{Username: f.user.Username, DisplayName: f.user.DisplayName, Status: auth.UserStatusSuspended, RoleSlugs: []string{"plugin-editor"}})
				case "role":
					err = NewUserRepository(revokePool).EnsureRoles(f.ctx, []auth.Role{{Slug: "plugin-editor", Name: "Revoked editor", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityPluginsManage}}})
				case "cancel":
					err = NewPluginJobRepository(revokePool).Change(f.ctx, f.control.Slug, f.request.Authorization.Claim.ID, f.request.Authorization.Claim.Version, "cancel", time.Now().UTC())
				case "restore":
					err = NewPluginJobRepository(revokePool).CancelRestored(f.ctx, time.Now().UTC())
				}
				revoked <- err
			}()
			waitContentEffectLock(t, f, revokeName)
			if err = blocker.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-written; err != nil {
				t.Fatal("write already holding authority was not committed", err)
			}
			if err = <-revoked; err != nil {
				t.Fatal("revocation did not finish", err)
			}
			f.assertCounts(t, 2, 2, 1, 1, 1)
			next := f.request
			next.Slot = "after-revocation"
			next.Update.ExpectedVersion = 2
			if _, err = f.effects.WriteContent(f.ctx, next); !errors.Is(err, pluginjobs.ErrAuthorization) {
				t.Fatal("revocation did not fence later effect", err)
			}
			t.Log("database lock order proved: accepted write commits before concurrent", mode, "and subsequent write is denied")
		})
	}
}

func TestPostgreSQLPluginContentExpiryDuringCommitRollsBack(t *testing.T) {
	f := newContentEffectFixture(t, false)
	f.exec(t, `CREATE FUNCTION delay_content_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.6); RETURN NEW; END $$`)
	f.exec(t, `CREATE TRIGGER delay_receipt BEFORE INSERT ON np_plugin_content_effects FOR EACH ROW EXECUTE FUNCTION delay_content_receipt()`)
	claim := f.request.Authorization.Claim
	claim.LeaseUntil = time.Now().UTC().Add(350 * time.Millisecond)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = saveTask(f.ctx, tx, claim); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.request.Authorization.Claim = claim
	if _, err = f.effects.WriteContent(f.ctx, f.request); err == nil {
		t.Fatal("lease expired during receipt insertion but committed")
	}
	f.assertCounts(t, 1, 1, 0, 0, 0)
}

func TestPostgreSQLPluginContentQuarantineFencesEffects(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint("unknown_identity_", unknown), func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			job := f.request.Authorization.Claim
			if unknown {
				input := pluginjobs.Input{Plugin: f.control.Slug, Epoch: f.control.Epoch.Token, Kind: "task.execute.v1", Key: "damaged-other", ActorID: f.user.ID, Payload: []byte(`{}`)}
				var err error
				job, err = f.jobs.Enqueue(f.ctx, input, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				f.exec(t, `UPDATE np_plugin_jobs SET data=$2 WHERE id=$1`, job.ID, []byte(`{broken`))
			} else {
				f.exec(t, `UPDATE np_plugin_jobs SET plugin='wrong-plugin' WHERE id=$1`, job.ID)
			}
			diagnostic := maintenanceDiagnostic(t, f.jobs, pluginjobs.RecordTask)
			if _, err := f.jobs.QuarantineRecord(f.ctx, pluginjobs.QuarantineRequest{Reference: diagnostic.Reference, Type: diagnostic.Type, Key: "isolate-effect", Operator: "test-operator", Reason: "verify effects cannot bypass quarantine"}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.effects.WriteContent(f.ctx, f.request); !errors.Is(err, pluginjobs.ErrAuthorization) {
				t.Fatal("quarantine bypassed", err)
			}
			f.assertCounts(t, 1, 1, 0, 0, 0)
		})
	}
}
