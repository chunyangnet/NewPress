package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

type PluginContentEffectRepository struct{ pool *pgxpool.Pool }

func NewPluginContentEffectRepository(pool *pgxpool.Pool) *PluginContentEffectRepository {
	return &PluginContentEffectRepository{pool: pool}
}

var _ plugineffects.Store = (*PluginContentEffectRepository)(nil)

func loadPluginContentEffects(ctx context.Context, tx pgx.Tx) ([]plugineffects.Receipt, int, error) {
	var count, size int64
	if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0) FROM np_plugin_content_effects`).Scan(&count, &size); err != nil {
		return nil, 0, plugineffects.ErrUnavailable
	}
	if count > plugineffects.MaxReceipts || size > plugineffects.MaxTotalBytes {
		return nil, 0, plugineffects.ErrCorrupt
	}
	rows, err := tx.Query(ctx, `SELECT id,job_id,slot,data FROM np_plugin_content_effects ORDER BY id LIMIT $1`, plugineffects.MaxReceipts+1)
	if err != nil {
		return nil, 0, plugineffects.ErrUnavailable
	}
	defer rows.Close()
	receipts := make([]plugineffects.Receipt, 0, count)
	perTask := map[string]int{}
	total := 0
	for rows.Next() {
		var id, jobID, slot string
		var raw []byte
		if err = rows.Scan(&id, &jobID, &slot, &raw); err != nil {
			return nil, 0, plugineffects.ErrUnavailable
		}
		r, err := packagefs.DecodeJSON[plugineffects.Receipt](bytes.NewReader(raw), plugineffects.MaxReceiptBytes, "plugin content receipt")
		if err != nil || plugineffects.ValidateReceipt(r) != nil || r.Result.OperationID != id || r.JobID != jobID || r.Slot != slot {
			return nil, 0, plugineffects.ErrCorrupt
		}
		perTask[jobID]++
		total += len(raw)
		if perTask[jobID] > plugineffects.MaxSlotsPerTask || total > plugineffects.MaxTotalBytes || len(receipts) >= plugineffects.MaxReceipts {
			return nil, 0, plugineffects.ErrCorrupt
		}
		receipts = append(receipts, r)
	}
	if rows.Err() != nil {
		return nil, 0, plugineffects.ErrUnavailable
	}
	return receipts, total, nil
}

func (r *PluginContentEffectRepository) Ready(ctx context.Context) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return plugineffects.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_content_effects IN ACCESS SHARE MODE`); err != nil {
		return plugineffects.ErrUnavailable
	}
	if _, err = loadSharedQuarantines(ctx, tx); err != nil {
		return err
	}
	if _, _, err = loadPluginContentEffects(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return plugineffects.ErrUnavailable
	}
	return nil
}

// lockPluginContentAccount retains the canonical user and current role rows
// through commit. User/role replacement in UserRepository locks the user first;
// capability changes also conflict with the role SHARE locks acquired here.
func lockPluginEffectAccount(ctx context.Context, tx pgx.Tx, actorID int64) (auth.User, error) {
	u, err := scanAuthUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM np_users u WHERE u.id=$1 FOR SHARE OF u`, actorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, pluginjobs.ErrAuthorization
	}
	if err != nil {
		return auth.User{}, plugineffects.ErrUnavailable
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.slug,r.name,r.description,r.capabilities,r.built_in
 FROM np_user_roles ur JOIN np_roles r ON r.id=ur.role_id WHERE ur.user_id=$1 ORDER BY r.slug FOR SHARE OF ur,r`, actorID)
	if err != nil {
		return auth.User{}, plugineffects.ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		role, err := scanAuthRole(rows, false)
		if err != nil {
			return auth.User{}, plugineffects.ErrUnavailable
		}
		u.Roles = append(u.Roles, role)
	}
	if rows.Err() != nil {
		return auth.User{}, plugineffects.ErrUnavailable
	}
	if u.ID <= 0 || u.Status != auth.UserStatusActive || !u.Can(auth.CapabilityAdminAccess) ||
		!u.Can(auth.CapabilityPluginsManage) {
		return auth.User{}, pluginjobs.ErrAuthorization
	}
	return u, nil
}

func lockPluginContentAccount(ctx context.Context, tx pgx.Tx, actorID int64) (auth.User, error) {
	u, err := lockPluginEffectAccount(ctx, tx, actorID)
	if err != nil {
		return auth.User{}, err
	}
	if !u.Can(auth.CapabilityContentWrite) && !u.Can(auth.CapabilityContentWriteOwn) {
		return auth.User{}, pluginjobs.ErrAuthorization
	}
	return u, nil
}

func lockPluginContentClaim(ctx context.Context, tx pgx.Tx, binding plugineffects.Authorization) (pluginjobs.Job, plugin.TaskControl, error) {
	return lockPluginEffectClaim(ctx, tx, binding, "content.write")
}

func lockPluginEffectClaim(ctx context.Context, tx pgx.Tx, binding plugineffects.Authorization, capability string) (pluginjobs.Job, plugin.TaskControl, error) {
	claim := binding.Claim
	// Maintenance and restore acquire task tables before quarantine/authority.
	// Holding this lock prevents a later quarantine from passing our check.
	if _, err := tx.Exec(ctx, `LOCK TABLE np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		return pluginjobs.Job{}, plugin.TaskControl{}, plugineffects.ErrUnavailable
	}
	if err := sharedQuarantineGate(ctx, tx); err != nil {
		return pluginjobs.Job{}, plugin.TaskControl{}, err
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return pluginjobs.Job{}, plugin.TaskControl{}, err
	}
	blocked, err := pluginjobs.QuarantineWorkPolicy(quarantines)
	if errors.Is(err, pluginjobs.ErrQuarantined) || blocked[claim.ID] {
		return pluginjobs.Job{}, plugin.TaskControl{}, pluginjobs.ErrAuthorization
	}
	if err != nil {
		return pluginjobs.Job{}, plugin.TaskControl{}, err
	}
	control, err := scanTaskControl(tx.QueryRow(ctx, `SELECT `+pluginTaskControlColumns+` FROM np_plugin_task_authorizations WHERE slug=$1 FOR SHARE`, claim.Plugin))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.Job{}, plugin.TaskControl{}, pluginjobs.ErrAuthorization
	}
	if err != nil {
		return pluginjobs.Job{}, plugin.TaskControl{}, taskControlReadError(err)
	}
	manifest, err := json.Marshal(control.Manifest)
	if err != nil || plugineffects.Digest(manifest) != binding.ManifestDigest || control.ModuleDigest != binding.ModuleDigest ||
		!slices.Contains(control.Manifest.Capabilities, capability) || !slices.Contains(control.Configuration.Grants, capability) ||
		(claim.Kind != "task.execute.v1" && claim.Kind != "event.deliver.v1") {
		return pluginjobs.Job{}, plugin.TaskControl{}, pluginjobs.ErrAuthorization
	}
	current, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1 FOR SHARE`, claim.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.Job{}, plugin.TaskControl{}, pluginjobs.ErrAuthorization
	}
	if err != nil {
		return pluginjobs.Job{}, plugin.TaskControl{}, err
	}
	if err = checkPluginContentClaimTime(ctx, tx, current, claim, control); err != nil {
		return pluginjobs.Job{}, plugin.TaskControl{}, err
	}
	return current, control, nil
}

func checkPluginContentClaimTime(ctx context.Context, tx pgx.Tx, current, claim pluginjobs.Job, control plugin.TaskControl) error {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return plugineffects.ErrUnavailable
	}
	if !plugin.TaskControlAllows(control, current, now) {
		return pluginjobs.ErrAuthorization
	}
	return pluginjobs.ValidateClaim(current, claim, now)
}

func (r *PluginContentEffectRepository) WriteContent(ctx context.Context, request plugineffects.Request) (plugineffects.Result, error) {
	id, fingerprint, err := request.Identity()
	if err != nil {
		return plugineffects.Result{}, err
	}
	claim := request.Authorization.Claim
	ctx, cancel := context.WithDeadline(ctx, claim.LeaseUntil)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return plugineffects.Result{}, plugineffects.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	current, control, err := lockPluginContentClaim(ctx, tx, request.Authorization)
	if err != nil {
		return plugineffects.Result{}, err
	}
	// A single bounded admission transaction serializes receipts and capacity.
	// No guest callbacks, filesystem work or outbound requests run inside it.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-content-effects-admission',0))`); err != nil {
		return plugineffects.Result{}, plugineffects.ErrUnavailable
	}
	u, err := lockPluginContentAccount(ctx, tx, current.ActorID)
	if err != nil {
		return plugineffects.Result{}, err
	}
	receipts, usedBytes, err := loadPluginContentEffects(ctx, tx)
	if err != nil {
		return plugineffects.Result{}, err
	}
	taskSlots := 0
	for _, receipt := range receipts {
		if receipt.JobID == current.ID {
			taskSlots++
		}
		if receipt.Result.OperationID != id {
			continue
		}
		if receipt.Plugin != current.Plugin || receipt.ActorID != current.ActorID ||
			receipt.ManifestDigest != request.Authorization.ManifestDigest || receipt.ModuleDigest != request.Authorization.ModuleDigest {
			return plugineffects.Result{}, plugineffects.ErrCorrupt
		}
		if receipt.Fingerprint != fingerprint {
			return plugineffects.Result{}, plugineffects.ErrConflict
		}
		if receipt.Result.PostID != request.PostID || receipt.Result.PreviousVersion != request.Update.ExpectedVersion {
			return plugineffects.Result{}, plugineffects.ErrCorrupt
		}
		if err = checkPluginContentClaimTime(ctx, tx, current, claim, control); err != nil {
			return plugineffects.Result{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return plugineffects.Result{}, plugineffects.ErrUnavailable
		}
		return receipt.Result, nil
	}
	if len(receipts) >= plugineffects.MaxReceipts || taskSlots >= plugineffects.MaxSlotsPerTask {
		return plugineffects.Result{}, plugineffects.ErrCapacity
	}
	post, err := queryPostByID(ctx, tx, request.PostID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return plugineffects.Result{}, content.ErrNotFound
	}
	if err != nil {
		return plugineffects.Result{}, plugineffects.ErrUnavailable
	}
	if !u.Can(auth.CapabilityContentWrite) && post.AuthorID != u.ID {
		return plugineffects.Result{}, pluginjobs.ErrAuthorization
	}
	if post.Version != request.Update.ExpectedVersion || post.Version == math.MaxInt64 {
		return plugineffects.Result{}, content.ErrVersionConflict
	}
	next, changed, err := request.Update.Patch.Apply(post)
	if err != nil {
		return plugineffects.Result{}, plugineffects.ErrInvalid
	}
	var at time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return plugineffects.Result{}, plugineffects.ErrUnavailable
	}
	actor := fmt.Sprintf("plugin:%s user:%d", current.Plugin, current.ActorID)
	if changed {
		if _, err = tx.Exec(ctx, `UPDATE np_posts SET title=$2,excerpt=$3,content_source=$4,content_html=$5,version=version+1,updated_at=$6 WHERE id=$1`, post.ID, next.Title, next.Excerpt, next.ContentSource, next.ContentHTML, at); err != nil {
			return plugineffects.Result{}, plugineffects.ErrUnavailable
		}
		next.Version++
		next.UpdatedAt = at
		posts := []content.Post{next}
		if err = hydratePostTerms(ctx, tx, posts); err != nil {
			return plugineffects.Result{}, plugineffects.ErrUnavailable
		}
		next = posts[0]
		if err = insertRevision(ctx, tx, next, actor); err != nil {
			return plugineffects.Result{}, plugineffects.ErrUnavailable
		}
		// Legacy/system-authored content has no persistent author, so the normal
		// revision helper emits no activity. This authorized editor still commits
		// a content update event for cache invalidation and plugin subscriptions.
		if next.AuthorID <= 0 {
			event := activity.New(activity.ContentUpdated, next.ID, next.ID, current.ActorID, next.Version, next.Status, at)
			if err = insertActivity(ctx, tx, "notification.post", []activity.Event{event}); err != nil {
				return plugineffects.Result{}, plugineffects.ErrUnavailable
			}
		}
		if err = deletePostAutosaves(ctx, tx, post.ID); err != nil {
			return plugineffects.Result{}, plugineffects.ErrUnavailable
		}
	}
	result := plugineffects.Result{OperationID: id, PostID: post.ID, PreviousVersion: post.Version, Version: next.Version, Changed: changed, CommittedAt: at}
	receipt := plugineffects.Receipt{Schema: 1, JobID: current.ID, Plugin: current.Plugin, Slot: request.Slot, Fingerprint: fingerprint,
		ActorID: current.ActorID, ManifestDigest: request.Authorization.ManifestDigest, ModuleDigest: request.Authorization.ModuleDigest, Result: result}
	if plugineffects.ValidateReceipt(receipt) != nil {
		return plugineffects.Result{}, plugineffects.ErrCorrupt
	}
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > plugineffects.MaxTotalBytes-usedBytes {
		return plugineffects.Result{}, plugineffects.ErrCapacity
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata) VALUES ($1,'plugin.content.updated','post',$2,$3)`, actor, fmt.Sprint(post.ID), map[string]any{
		"plugin": current.Plugin, "task_id": current.ID, "operation_id": id, "actor_id": current.ActorID,
		"previous_version": post.Version, "version": next.Version, "changed": changed,
	}); err != nil {
		return plugineffects.Result{}, plugineffects.ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO np_plugin_content_effects(id,job_id,slot,data) VALUES($1,$2,$3,$4)`, id, current.ID, request.Slot, raw); err != nil {
		return plugineffects.Result{}, plugineffects.ErrUnavailable
	}
	// Recheck database time after every potentially blocking operation, retaining
	// authority/account/task locks until commit. Expiry rolls back all writes.
	if err = checkPluginContentClaimTime(ctx, tx, current, claim, control); err != nil {
		return plugineffects.Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return plugineffects.Result{}, plugineffects.ErrUnavailable
	}
	return result, nil
}
