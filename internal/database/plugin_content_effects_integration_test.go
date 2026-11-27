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
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

type contentEffectFixture struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	jobs     *PluginJobRepository
	effects  *PluginContentEffectRepository
	controls *PluginTaskControlRepository
	control  plugin.TaskControl
	request  plugineffects.Request
	post     content.Post
	user     auth.User
}

func newContentEffectFixture(t *testing.T, own bool) *contentEffectFixture {
	t.Helper()
	ctx, pool, jobs := newPluginJobsIntegrationStore(t)
	users := NewUserRepository(pool)
	write := auth.CapabilityContentWrite
	if own {
		write = auth.CapabilityContentWriteOwn
	}
	role := auth.Role{Slug: "plugin-editor", Name: "Plugin editor", Capabilities: []string{auth.CapabilityAdminAccess, auth.CapabilityPluginsManage, write}}
	if err := users.EnsureRoles(ctx, []auth.Role{role}); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Plugin effect integration fixture")
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "fixture"}, auth.UserInput{Username: "effect-editor", DisplayName: "Effect editor", RoleSlugs: []string{role.Slug}, Status: auth.UserStatusActive}, hash)
	if err != nil {
		t.Fatal(err)
	}
	posts := NewPostRepository(pool)
	post, err := posts.Create(ctx, content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "effect-post", Title: "Original title", Excerpt: "Retained excerpt",
		ContentSource: "<p>Original body</p>", AuthorID: user.ID, AuthorName: user.DisplayName, TagNames: []string{"keep-tag"}, Category: "Keep category", Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	control := pgTaskControl(t, "content-writer")
	control.Manifest.Capabilities = append(control.Manifest.Capabilities, "content.write")
	control.Configuration.Grants = append(control.Configuration.Grants, "content.write")
	control.Epoch.ActorID = user.ID
	controls := NewPluginTaskControlRepository(pool)
	if err = controls.SaveTaskControl(ctx, 0, control); err != nil {
		t.Fatal(err)
	}
	_, err = jobs.Enqueue(ctx, pluginjobs.Input{Plugin: control.Slug, Epoch: control.Epoch.Token, Kind: "task.execute.v1", Key: "content-operation", ActorID: user.ID, Payload: json.RawMessage(`{}`)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	claim, ok, err := jobs.Claim(ctx, time.Now().UTC())
	if err != nil || !ok {
		t.Fatal("claim", ok, err)
	}
	raw, _ := json.Marshal(control.Manifest)
	title := "Updated title"
	request := plugineffects.Request{Authorization: plugineffects.Authorization{Claim: claim, ManifestDigest: plugineffects.Digest(raw), ModuleDigest: control.ModuleDigest}, Slot: "summary", PostID: post.ID,
		Update: plugineffects.ContentUpdate{ExpectedVersion: post.Version, Patch: content.TextPatch{Title: &title}}}
	f := &contentEffectFixture{ctx, pool, jobs, NewPluginContentEffectRepository(pool), controls, control, request, post, user}
	if err = f.effects.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *contentEffectFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *contentEffectFixture) assertCounts(t *testing.T, version, revisions, events, audits, receipts int) {
	t.Helper()
	var actualVersion, actualRevisions, actualEvents, actualAudits, actualReceipts int
	err := f.pool.QueryRow(f.ctx, `SELECT (SELECT version FROM np_posts WHERE id=$1),
 (SELECT count(*) FROM np_post_revisions WHERE post_id=$1),(SELECT count(*) FROM np_outbox WHERE topic='notification.post'),
 (SELECT count(*) FROM np_audit_events WHERE action='plugin.content.updated'),(SELECT count(*) FROM np_plugin_content_effects)`, f.post.ID).
		Scan(&actualVersion, &actualRevisions, &actualEvents, &actualAudits, &actualReceipts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := [5]int{actualVersion, actualRevisions, actualEvents, actualAudits, actualReceipts}, [5]int{version, revisions, events, audits, receipts}; got != want {
		t.Fatal("transaction facts", got, "want", want)
	}
}

func TestPostgreSQLPluginContentAtomicReplayAndNoop(t *testing.T) {
	f := newContentEffectFixture(t, false)
	posts := NewPostRepository(f.pool)
	owner := fmt.Sprintf("user:%d", f.user.ID)
	if _, err := posts.SaveAutosave(f.ctx, content.Autosave{PostID: f.post.ID, OwnerKey: owner, SourceVersion: f.post.Version, Snapshot: f.post, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	first, err := f.effects.WriteContent(f.ctx, f.request)
	if err != nil || !first.Changed || first.Version != 2 || first.PreviousVersion != 1 {
		t.Fatal(first, err)
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
	updated, err := posts.GetByID(f.ctx, f.post.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := f.post
	want.Title = *f.request.Update.Patch.Title
	want.Version, want.UpdatedAt = updated.Version, updated.UpdatedAt
	if !reflect.DeepEqual(updated, want) {
		t.Fatal("omitted metadata/terms/body changed", updated, want)
	}
	if _, err = posts.GetAutosave(f.ctx, f.post.ID, owner); !errors.Is(err, content.ErrAutosaveNotFound) {
		t.Fatal("committed write retained autosave", err)
	}
	// Reopen the repository as if the original response was lost.
	replayed, err := NewPluginContentEffectRepository(f.pool).WriteContent(f.ctx, f.request)
	if err != nil || replayed != first {
		t.Fatal("response loss repeated the write", replayed, err)
	}
	changed := f.request
	changed.PostID++
	if _, err = f.effects.WriteContent(f.ctx, changed); !errors.Is(err, plugineffects.ErrConflict) {
		t.Fatal("target conflict", err)
	}
	changed = f.request
	changed.Update.ExpectedVersion++
	if _, err = f.effects.WriteContent(f.ctx, changed); !errors.Is(err, plugineffects.ErrConflict) {
		t.Fatal("version conflict was not operation conflict", err)
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
	noop := f.request
	noop.Slot = "same-text"
	noop.Update.ExpectedVersion = 2
	noChange, err := f.effects.WriteContent(f.ctx, noop)
	if err != nil || noChange.Changed || noChange.Version != 2 {
		t.Fatal("identical text caused update", noChange, err)
	}
	f.assertCounts(t, 2, 2, 1, 2, 2)
	// A later authorized content edit does not change the original result.
	body := "<p>Later</p><script>unsafe()</script>"
	later := noop
	later.Slot = "body"
	later.Update.Patch = content.TextPatch{ContentSource: &body}
	if _, err = f.effects.WriteContent(f.ctx, later); err != nil {
		t.Fatal(err)
	}
	replayed, err = f.effects.WriteContent(f.ctx, f.request)
	if err != nil || replayed != first {
		t.Fatal("old result followed latest version", replayed, err)
	}
	updated, err = posts.GetByID(f.ctx, f.post.ID)
	if err != nil || strings.Contains(updated.ContentHTML, "<script") || updated.Excerpt != f.post.Excerpt {
		t.Fatal("sanitization/preservation", updated, err)
	}
	f.assertCounts(t, 3, 3, 2, 3, 3)
}

func TestPostgreSQLPluginContentConcurrentReceipt(t *testing.T) {
	f := newContentEffectFixture(t, false)
	var wg sync.WaitGroup
	results := make(chan plugineffects.Result, 12)
	failures := make(chan error, 12)
	for range 12 {
		wg.Go(func() { r, err := f.effects.WriteContent(f.ctx, f.request); results <- r; failures <- err })
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first plugineffects.Result
	for r := range results {
		if first.OperationID == "" {
			first = r
		}
		if first != r {
			t.Fatal("different concurrent results", first, r)
		}
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
}

func TestPostgreSQLPluginContentLegacyAuthorAndScheduledStatus(t *testing.T) {
	f := newContentEffectFixture(t, false)
	due := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	f.exec(t, `UPDATE np_posts SET author_id=NULL,status='future',published_at=$2,metadata=metadata || '{"retained_private":"keep"}'::jsonb WHERE id=$1`, f.post.ID, due)
	if _, err := f.effects.WriteContent(f.ctx, f.request); err != nil {
		t.Fatal(err)
	}
	post, err := NewPostRepository(f.pool).GetByID(f.ctx, f.post.ID)
	if err != nil || post.AuthorID != 0 || post.Status != content.StatusFuture || !post.PublishedAt.Equal(due) || post.Meta["retained_private"] != "keep" {
		t.Fatal("partial write normalized unrelated legacy or scheduled fields", post, err)
	}
	events, err := NewActivitySource(f.pool, "notification.post").PendingActivity(f.ctx, time.Now().UTC(), 10)
	if err != nil || len(events) != 1 || events[0].Kind != activity.ContentUpdated || events[0].UserID != f.user.ID {
		t.Fatal("legacy update lost durable event", events, err)
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
}

func TestPostgreSQLPluginContentRollbackAtEveryWrite(t *testing.T) {
	for _, table := range []string{"np_posts", "np_post_revisions", "np_post_autosaves", "np_outbox", "np_audit_events", "np_plugin_content_effects"} {
		t.Run(table, func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			owner := fmt.Sprintf("user:%d", f.user.ID)
			if _, err := NewPostRepository(f.pool).SaveAutosave(f.ctx, content.Autosave{PostID: f.post.ID, OwnerKey: owner, SourceVersion: f.post.Version, Snapshot: f.post, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			f.exec(t, `CREATE FUNCTION fail_content_effect() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'effect transaction injection'; END $$`)
			f.exec(t, `CREATE TRIGGER effect_failure BEFORE INSERT OR UPDATE OR DELETE ON `+pgx.Identifier{table}.Sanitize()+` FOR EACH ROW EXECUTE FUNCTION fail_content_effect()`)
			if _, err := f.effects.WriteContent(f.ctx, f.request); err == nil {
				t.Fatal("injected failure succeeded")
			}
			f.assertCounts(t, 1, 1, 0, 0, 0)
			if _, err := NewPostRepository(f.pool).GetAutosave(f.ctx, f.post.ID, owner); err != nil {
				t.Fatal("rollback lost draft", err)
			}
			f.exec(t, `DROP TRIGGER effect_failure ON `+pgx.Identifier{table}.Sanitize())
			if _, err := f.effects.WriteContent(f.ctx, f.request); err != nil {
				t.Fatal("retry after rollback", err)
			}
			f.assertCounts(t, 2, 2, 1, 1, 1)
		})
	}
}

func TestPostgreSQLPluginContentCurrentAuthorityAndAccount(t *testing.T) {
	for _, mode := range []string{"disabled", "new_epoch", "grant_removed", "module_changed", "manifest_changed", "actor_suspended", "role_revoked", "role_unassigned", "wrong_actor", "wrong_token", "wrong_version", "cancelled", "own_other", "object_version"} {
		t.Run(mode, func(t *testing.T) {
			f := newContentEffectFixture(t, mode == "own_other")
			request := f.request
			expected := pluginjobs.ErrAuthorization
			switch mode {
			case "disabled", "new_epoch", "grant_removed", "module_changed", "manifest_changed":
				c := nextPGTaskControl(t, f.control)
				switch mode {
				case "disabled":
					c.Enabled = false
				case "new_epoch":
					c.Epoch, _ = plugin.NewTaskEpoch(f.user.ID)
				case "grant_removed":
					c.Configuration.Grants = []string{"settings.read"}
				case "module_changed":
					c.ModuleDigest = strings.Repeat("d", 64)
				case "manifest_changed":
					c.Manifest.Name = "Changed package"
				}
				if err := f.controls.SaveTaskControl(f.ctx, f.control.Revision, c); err != nil {
					t.Fatal(err)
				}
			case "actor_suspended":
				f.exec(t, `UPDATE np_users SET status='suspended' WHERE id=$1`, f.user.ID)
			case "role_revoked":
				f.exec(t, `UPDATE np_roles SET capabilities='["admin:access","plugins:manage"]'::jsonb WHERE slug='plugin-editor'`)
			case "role_unassigned":
				f.exec(t, `DELETE FROM np_user_roles WHERE user_id=$1`, f.user.ID)
			case "wrong_actor":
				request.Authorization.Claim.ActorID++
			case "wrong_token":
				request.Authorization.Claim.LeaseToken = "wrong"
			case "wrong_version":
				request.Authorization.Claim.Version++
			case "cancelled":
				if err := f.jobs.Change(f.ctx, f.control.Slug, request.Authorization.Claim.ID, request.Authorization.Claim.Version, "cancel", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "own_other":
				f.exec(t, `UPDATE np_posts SET author_id=NULL WHERE id=$1`, f.post.ID)
			case "object_version":
				request.Update.ExpectedVersion++
				expected = content.ErrVersionConflict
			}
			if _, err := f.effects.WriteContent(f.ctx, request); !errors.Is(err, expected) {
				t.Fatal("invalid authority accepted/wrong failure", mode, err)
			}
			f.assertCounts(t, 1, 1, 0, 0, 0)
		})
	}
	t.Run("own_content_allowed", func(t *testing.T) {
		f := newContentEffectFixture(t, true)
		if _, err := f.effects.WriteContent(f.ctx, f.request); err != nil {
			t.Fatal(err)
		}
		f.assertCounts(t, 2, 2, 1, 1, 1)
	})
}

func TestPostgreSQLPluginContentReceiptSurvivesReclaimArchiveRestore(t *testing.T) {
	f := newContentEffectFixture(t, false)
	claim := f.request.Authorization.Claim
	claim.LeaseUntil = time.Now().UTC().Add(1200 * time.Millisecond)
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveTask(f.ctx, tx, claim); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.request.Authorization.Claim = claim
	first, err := f.effects.WriteContent(f.ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	<-time.After(time.Until(claim.LeaseUntil) + 20*time.Millisecond)
	reclaimed, ok, err := f.jobs.Claim(f.ctx, time.Now().UTC())
	if err != nil || !ok || reclaimed.ID != claim.ID || reclaimed.LeaseToken == claim.LeaseToken {
		t.Fatal("reclaim", ok, err)
	}
	if _, err = f.effects.WriteContent(f.ctx, f.request); err == nil {
		t.Fatal("old lease wrote")
	}
	f.request.Authorization.Claim = reclaimed
	result, err := f.effects.WriteContent(f.ctx, f.request)
	if err != nil || result != first {
		t.Fatal("new lease duplicated operation", result, err)
	}
	if err = f.jobs.Finish(f.ctx, reclaimed, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.jobs.Archive(f.ctx, pluginjobs.ArchiveQuery{Plugin: f.control.Slug, Tasks: true, Before: time.Now().UTC(), Limit: 1}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, first.OperationID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = f.jobs.CancelRestored(f.ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err = f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, first.OperationID).Scan(&after); err != nil || !bytes.Equal(original, after) {
		t.Fatal("archive/restore changed receipt bytes", err)
	}
	if _, err = f.effects.WriteContent(f.ctx, f.request); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("restored authority replayed write", err)
	}
	f.assertCounts(t, 2, 2, 1, 1, 1)
}

func TestPostgreSQLPluginContentReceiptIntegrityAndSlotBudget(t *testing.T) {
	f := newContentEffectFixture(t, false)
	noop := f.request
	title := f.post.Title
	noop.Update.Patch.Title = &title
	for i := range plugineffects.MaxSlotsPerTask {
		noop.Slot = fmt.Sprintf("slot-%d", i)
		if _, err := f.effects.WriteContent(f.ctx, noop); err != nil {
			t.Fatal(i, err)
		}
	}
	noop.Slot = "overflow"
	if _, err := f.effects.WriteContent(f.ctx, noop); !errors.Is(err, plugineffects.ErrCapacity) {
		t.Fatal("slot budget", err)
	}
	noop.Slot = "slot-0"
	if _, err := f.effects.WriteContent(f.ctx, noop); err != nil {
		t.Fatal("full capacity lost replay", err)
	}
	f.assertCounts(t, 1, 1, 0, plugineffects.MaxSlotsPerTask, plugineffects.MaxSlotsPerTask)
	id, _, _ := noop.Identity()
	var saved []byte
	if err := f.pool.QueryRow(f.ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, id).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"index", "duplicate_json", "result", "hidden_job", "wrong_target"} {
		t.Run(mode, func(t *testing.T) {
			switch mode {
			case "index":
				f.exec(t, `UPDATE np_plugin_content_effects SET slot='moved' WHERE id=$1`, id)
			case "hidden_job":
				f.exec(t, `UPDATE np_plugin_content_effects SET job_id=$2 WHERE id=$1`, id, strings.Repeat("d", 64))
			case "duplicate_json":
				f.exec(t, `UPDATE np_plugin_content_effects SET data=$2 WHERE id=$1`, id, bytes.Replace(saved, []byte(`{"schema":1`), []byte(`{"schema":1,"schema":1`), 1))
			case "result":
				var r plugineffects.Receipt
				if err := json.Unmarshal(saved, &r); err != nil {
					t.Fatal(err)
				}
				r.Result.Version++
				raw, _ := json.Marshal(r)
				f.exec(t, `UPDATE np_plugin_content_effects SET data=$2 WHERE id=$1`, id, raw)
			case "wrong_target":
				var r plugineffects.Receipt
				if err := json.Unmarshal(saved, &r); err != nil {
					t.Fatal(err)
				}
				r.Result.PostID++
				raw, _ := json.Marshal(r)
				f.exec(t, `UPDATE np_plugin_content_effects SET data=$2 WHERE id=$1`, id, raw)
			}
			if err := f.effects.Ready(f.ctx); mode != "wrong_target" && !errors.Is(err, plugineffects.ErrCorrupt) {
				t.Fatal("corrupt receipt ready", err)
			}
			if _, err := f.effects.WriteContent(f.ctx, noop); !errors.Is(err, plugineffects.ErrCorrupt) {
				t.Fatal("corrupt receipt replay", err)
			}
			if mode != "wrong_target" {
				if err := f.jobs.CancelRestored(f.ctx, time.Now().UTC()); !errors.Is(err, plugineffects.ErrCorrupt) {
					t.Fatal("corrupt restore allowed", err)
				}
			}
			f.exec(t, `UPDATE np_plugin_content_effects SET slot=$2,job_id=$3,data=$4 WHERE id=$1`, id, noop.Slot, noop.Authorization.Claim.ID, saved)
		})
	}
	if _, err := f.effects.WriteContent(f.ctx, noop); err != nil {
		t.Fatal("failed restore was not atomic", err)
	}
}

func TestPostgreSQLPluginContentGlobalStorageBounds(t *testing.T) {
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			f := newContentEffectFixture(t, false)
			count, size := plugineffects.MaxReceipts+1, 2
			if kind == "bytes" {
				count, size = plugineffects.MaxTotalBytes/plugineffects.MaxReceiptBytes+1, plugineffects.MaxReceiptBytes
			}
			// Deliberately invalid JSON rows make it observable that aggregate
			// bounds reject before decoding an attacker-sized collection.
			f.exec(t, `INSERT INTO np_plugin_content_effects(id,job_id,slot,data)
 SELECT lpad(to_hex(n),64,'0'),lpad(to_hex(n),64,'0'),'bounded',convert_to(repeat('x',$2),'UTF8') FROM generate_series(1,$1) n`, count, size)
			if err := f.effects.Ready(f.ctx); !errors.Is(err, plugineffects.ErrCorrupt) {
				t.Fatal("unbounded receipt ready", err)
			}
			if _, err := f.effects.WriteContent(f.ctx, f.request); !errors.Is(err, plugineffects.ErrCorrupt) {
				t.Fatal("unbounded receipt write", err)
			}
			var version int
			if err := f.pool.QueryRow(f.ctx, `SELECT version FROM np_posts WHERE id=$1`, f.post.ID).Scan(&version); err != nil || version != 1 {
				t.Fatal("capacity failure wrote content", version, err)
			}
		})
	}
}
