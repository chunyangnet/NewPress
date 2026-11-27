package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

// The executable backup test calls this before introducing quarantined rows.
// It commits a real content write and archives the task; the returned check
// compares the original post, revision/audit counts and receipt bytes in either DB.
func seedBackupContentEffect(t *testing.T, ctx context.Context, pool *pgxpool.Pool) func(*pgxpool.Pool) {
	t.Helper()
	users := database.NewUserRepository(pool)
	if err := users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("Backup content effect fixture")
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: "backup-fixture"}, auth.UserInput{Username: "backup-content-editor", RoleSlugs: []string{"administrator"}, Status: auth.UserStatusActive}, hash)
	if err != nil {
		t.Fatal(err)
	}
	posts := database.NewPostRepository(pool)
	post, err := posts.Create(ctx, content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Slug: "backup-content", Title: "Before backup", ContentSource: "<p>Retained body</p>", AuthorID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := plugin.NewTaskEpoch(0)
	if err != nil {
		t.Fatal(err)
	}
	control := plugin.TaskControl{Schema: 1, Slug: "backup-content-writer", Revision: 1, ModuleDigest: strings.Repeat("f", 64), Enabled: true, Epoch: epoch,
		Manifest:      plugin.Manifest{Schema: 1, Slug: "backup-content-writer", Name: "Backup writer", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"content.write"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}}},
		Configuration: plugin.Configuration{Version: 1, Values: map[string]any{}, Grants: []string{"content.write"}}}
	if err = database.NewPluginTaskControlRepository(pool).SaveTaskControl(ctx, 0, control); err != nil {
		t.Fatal(err)
	}
	jobs := database.NewPluginJobRepository(pool)
	if _, err = jobs.Enqueue(ctx, pluginjobs.Input{Plugin: control.Slug, Epoch: epoch.Token, Kind: "task.execute.v1", Key: "stable-backup-operation", ActorID: user.ID, Payload: json.RawMessage(`{}`)}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := jobs.Claim(ctx, time.Now().UTC())
	if err != nil || !ok {
		t.Fatal("backup effect claim", ok, err)
	}
	title := "Committed before backup"
	raw, _ := json.Marshal(control.Manifest)
	request := plugineffects.Request{Authorization: plugineffects.Authorization{Claim: claim, ManifestDigest: plugineffects.Digest(raw), ModuleDigest: control.ModuleDigest}, Slot: "update-title", PostID: post.ID, Update: plugineffects.ContentUpdate{ExpectedVersion: post.Version, Patch: content.TextPatch{Title: &title}}}
	result, err := database.NewPluginContentEffectRepository(pool).WriteContent(ctx, request)
	if err != nil || result.Version != 2 {
		t.Fatal("backup content commit", result, err)
	}
	if err = jobs.Finish(ctx, claim, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	archived, err := jobs.Archive(ctx, pluginjobs.ArchiveQuery{Plugin: control.Slug, Tasks: true, Before: time.Now().UTC(), Limit: 1}, time.Now().UTC())
	if err != nil || archived.Tasks != 1 {
		t.Fatal("archive backup effect task", archived, err)
	}
	var receipt []byte
	if err = pool.QueryRow(ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, result.OperationID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	updated, err := posts.GetByID(ctx, post.ID)
	if err != nil {
		t.Fatal(err)
	}
	return func(other *pgxpool.Pool) {
		t.Helper()
		var actual []byte
		if err := other.QueryRow(ctx, `SELECT data FROM np_plugin_content_effects WHERE id=$1`, result.OperationID).Scan(&actual); err != nil || !bytes.Equal(receipt, actual) {
			t.Fatal("dump/restore/archive changed internal receipt", err)
		}
		if err := database.NewPluginContentEffectRepository(other).Ready(ctx); err != nil {
			t.Fatal("restored receipt readiness", err)
		}
		got, err := database.NewPostRepository(other).GetByID(ctx, post.ID)
		if err != nil || !reflect.DeepEqual(got, updated) {
			t.Fatal("restored content was rewritten", got, err)
		}
		var revisions, audits int
		if err = other.QueryRow(ctx, `SELECT (SELECT count(*) FROM np_post_revisions WHERE post_id=$1),(SELECT count(*) FROM np_audit_events WHERE action='plugin.content.updated' AND metadata->>'operation_id'=$2)`, post.ID, result.OperationID).Scan(&revisions, &audits); err != nil || revisions != 2 || audits != 1 {
			t.Fatal("restored write/audit repeated or lost", revisions, audits, err)
		}
	}
}
