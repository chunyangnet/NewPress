package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/safehttp"
)

type PluginWebhookRepository struct {
	pool      *pgxpool.Pool
	enabled   bool
	validated webhookValidationMemo
}

// enabled gates admission and new sends, not result recording or human review.
func NewPluginWebhookRepository(pool *pgxpool.Pool, enabled bool) *PluginWebhookRepository {
	return &PluginWebhookRepository{pool: pool, enabled: enabled}
}

var _ plugindispatch.Store = (*PluginWebhookRepository)(nil)
var _ plugindispatch.Reviewer = (*PluginWebhookRepository)(nil)

func loadPluginWebhookIntents(ctx context.Context, tx pgx.Tx) ([]plugindispatch.Intent, int, error) {
	var items []plugindispatch.Intent
	_, used, err := scanPluginWebhookIntents(ctx, tx, nil, func(i plugindispatch.Intent) {
		items = append(items, cloneWebhook(i))
	})
	return items, used, err
}

// Selection affects only retained output. Every row is still read and validated,
// and total/used include all rows. Callers must discard output on any error.
func (r *PluginWebhookRepository) selectIntents(ctx context.Context, tx pgx.Tx, keep func(plugindispatch.Intent) bool) ([]plugindispatch.Intent, int, int, error) {
	var items []plugindispatch.Intent
	total, used, err := scanPluginWebhookIntents(ctx, tx, &r.validated, func(i plugindispatch.Intent) {
		if keep == nil || keep(i) {
			items = append(items, cloneWebhook(i))
		}
	})
	return items, total, used, err
}

// visit borrows an immutable validated value. It must not mutate Body/Review or
// retain mutable aliases; use cloneWebhook when returning an intent to callers.
// Validation continues after visiting a selected row, including unrelated rows.
func scanPluginWebhookIntents(ctx context.Context, tx pgx.Tx, memo *webhookValidationMemo, visit func(plugindispatch.Intent)) (int, int, error) {
	if memo != nil {
		return scanCurrentWebhookIntents(ctx, tx, memo, visit)
	}
	var count, size int64
	if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(octet_length(data)),0) FROM np_plugin_webhook_intents`).Scan(&count, &size); err != nil {
		return 0, 0, plugindispatch.ErrUnavailable
	}
	if count > plugindispatch.MaxRecords || size > plugindispatch.MaxTotalBytes {
		return 0, 0, plugindispatch.ErrCorrupt
	}
	rows, err := tx.Query(ctx, `SELECT id,job_id,slot,version,state,data FROM np_plugin_webhook_intents ORDER BY id LIMIT $1`, plugindispatch.MaxRecords+1)
	if err != nil {
		return 0, 0, plugindispatch.ErrUnavailable
	}
	defer rows.Close()
	slots, used := map[string]int{}, 0
	total := 0
	for rows.Next() {
		if ctx.Err() != nil {
			return 0, 0, plugindispatch.ErrUnavailable
		}
		var id, job, slot, state string
		var version int64
		// DriverBytes expires at Next/Close. validateWebhookBytes compares it
		// immediately and copies any bytes kept for a newly parsed memo entry.
		var raw pgtype.DriverBytes
		if err = rows.Scan(&id, &job, &slot, &version, &state, &raw); err != nil {
			return 0, 0, plugindispatch.ErrUnavailable
		}
		checked, err := validateWebhookBytes(raw, nil)
		if err != nil {
			return 0, 0, err
		}
		i := checked.intent
		if i.ID != id || i.JobID != job || i.Slot != slot || i.Version != version || i.State != state {
			return 0, 0, plugindispatch.ErrCorrupt
		}
		slots[job]++
		used += checked.budget
		if slots[job] > plugindispatch.MaxSlotsPerTask || used > plugindispatch.MaxTotalBytes || total >= plugindispatch.MaxRecords {
			return 0, 0, plugindispatch.ErrCorrupt
		}
		total++
		if visit != nil {
			visit(i)
		}
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return 0, 0, plugindispatch.ErrUnavailable
	}
	return total, used, nil
}

func webhookLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-plugin-webhook-admission',0))`)
	if err != nil {
		return plugindispatch.ErrUnavailable
	}
	return nil
}

func webhookClock(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return at, plugindispatch.ErrUnavailable
	}
	return at.UTC(), nil
}

func webhookAudit(ctx context.Context, tx pgx.Tx, i plugindispatch.Intent, actor int64, action string) error {
	metadata := map[string]any{"plugin": i.Plugin, "task_id": i.JobID, "operation_id": i.ID, "version": i.Version, "state": i.State, "outcome": i.Outcome.Code}
	if i.Review != nil {
		metadata["decision"], metadata["reason"] = i.Review.Decision, i.Review.Reason
	}
	label := fmt.Sprintf("user:%d", actor)
	if actor == 0 {
		label = "system:plugin-webhook"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata) VALUES($1,$2,'plugin_webhook',$3,$4)`, label, action, i.ID, metadata); err != nil {
		return plugindispatch.ErrUnavailable
	}
	return nil
}

func savePluginWebhook(ctx context.Context, tx pgx.Tx, i plugindispatch.Intent) error {
	if err := plugindispatch.Validate(i); err != nil {
		return err
	}
	raw, err := json.Marshal(i)
	if err != nil {
		return plugindispatch.ErrCorrupt
	}
	tag, err := tx.Exec(ctx, `UPDATE np_plugin_webhook_intents SET version=$2,state=$3,data=$4 WHERE id=$1 AND version=$5`, i.ID, i.Version, i.State, raw, i.Version-1)
	if err != nil {
		return plugindispatch.ErrUnavailable
	}
	if tag.RowsAffected() != 1 {
		return plugindispatch.ErrConflict
	}
	return nil
}

func (r *PluginWebhookRepository) Ready(ctx context.Context) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return plugindispatch.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	if _, _, err = scanPluginWebhookIntents(ctx, tx, &r.validated, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PluginWebhookRepository) Enqueue(ctx context.Context, request plugindispatch.Request) (plugindispatch.Accepted, error) {
	if !r.enabled {
		return plugindispatch.Accepted{}, plugindispatch.ErrUnavailable
	}
	id, fp, body, err := request.Identity()
	if err != nil {
		return plugindispatch.Accepted{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, request.Authorization.Claim.LeaseUntil)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return plugindispatch.Accepted{}, plugindispatch.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	// Match dispatch order: task/quarantine tables, webhook admission, then
	// authority/source/account rows. A queued authority writer must not sit
	// between an enqueue holding authority and a dispatch holding admission.
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		return plugindispatch.Accepted{}, plugindispatch.ErrUnavailable
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return plugindispatch.Accepted{}, err
	}
	// Existence selects the admission lock; it is never an acceptance check.
	// Confirmations do not write, so they can share admission with each other.
	// New identities still serialize all writes and capacity decisions below.
	var replay bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM np_plugin_webhook_intents WHERE id=$1)`, id).Scan(&replay); err != nil {
		return plugindispatch.Accepted{}, plugindispatch.ErrUnavailable
	}
	if replay {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('newpress-plugin-webhook-admission',0))`); err != nil {
			return plugindispatch.Accepted{}, plugindispatch.ErrUnavailable
		}
	} else if err = webhookLock(ctx, tx); err != nil {
		return plugindispatch.Accepted{}, err
	}
	current, control, err := lockPluginEffectClaim(ctx, tx, request.Authorization, "http.send")
	if err != nil {
		return plugindispatch.Accepted{}, err
	}
	u, _ := safehttp.ValidateURL(request.URL)
	if !slices.Contains(control.Manifest.OutboundHosts, strings.ToLower(u.Hostname())) {
		return plugindispatch.Accepted{}, pluginjobs.ErrAuthorization
	}
	if _, err = lockPluginEffectAccount(ctx, tx, current.ActorID); err != nil {
		return plugindispatch.Accepted{}, err
	}
	intents, total, used, err := r.selectIntents(ctx, tx, func(i plugindispatch.Intent) bool { return i.JobID == current.ID })
	if err != nil {
		return plugindispatch.Accepted{}, err
	}
	perTask := 0
	var accepted plugindispatch.Accepted
	for _, i := range intents {
		if i.JobID == current.ID {
			perTask++
		}
		if i.ID != id {
			continue
		}
		if i.Plugin != current.Plugin || i.Kind != current.Kind || i.Epoch != current.Epoch || i.ActorID != current.ActorID || i.SourceFingerprint != current.Fingerprint || i.ManifestDigest != request.Authorization.ManifestDigest || i.ModuleDigest != request.Authorization.ModuleDigest {
			return plugindispatch.Accepted{}, plugindispatch.ErrCorrupt
		}
		if i.Fingerprint != fp {
			return plugindispatch.Accepted{}, plugindispatch.ErrConflict
		}
		accepted = i.Accepted()
	}
	if accepted.OperationID == "" {
		if replay {
			// An identity disappeared outside the repository protocol. Never
			// recreate it or upgrade admission while holding the row locks.
			return plugindispatch.Accepted{}, plugindispatch.ErrConflict
		}
		at, err := webhookClock(ctx, tx)
		if err != nil {
			return plugindispatch.Accepted{}, err
		}
		i := plugindispatch.Intent{Schema: 1, ID: id, JobID: current.ID, SourceFingerprint: current.Fingerprint, Plugin: current.Plugin, Kind: current.Kind, Epoch: current.Epoch, ActorID: current.ActorID, ManifestDigest: request.Authorization.ManifestDigest, ModuleDigest: request.Authorization.ModuleDigest, Slot: request.Slot, Fingerprint: fp, URL: request.URL, Body: body, Version: 1, State: "pending", CreatedAt: at, UpdatedAt: at}
		if plugindispatch.Validate(i) != nil {
			return plugindispatch.Accepted{}, plugindispatch.ErrInvalid
		}
		if perTask >= plugindispatch.MaxSlotsPerTask || total >= plugindispatch.MaxRecords || plugindispatch.BudgetBytes(i) > plugindispatch.MaxTotalBytes-used {
			return plugindispatch.Accepted{}, plugindispatch.ErrCapacity
		}
		raw, _ := json.Marshal(i)
		if _, err = tx.Exec(ctx, `INSERT INTO np_plugin_webhook_intents(id,job_id,slot,version,state,data) VALUES($1,$2,$3,$4,$5,$6)`, id, current.ID, request.Slot, i.Version, i.State, raw); err != nil {
			return plugindispatch.Accepted{}, plugindispatch.ErrUnavailable
		}
		if err = webhookAudit(ctx, tx, i, current.ActorID, "plugin.webhook.accepted"); err != nil {
			return plugindispatch.Accepted{}, err
		}
		accepted = i.Accepted()
	}
	if err = checkPluginContentClaimTime(ctx, tx, current, request.Authorization.Claim, control); err != nil {
		return plugindispatch.Accepted{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return plugindispatch.Accepted{}, plugindispatch.ErrUnavailable
	}
	return accepted, nil
}

// Source rows are held until the send claim commits. An archive keeps the
// immutable fingerprint and terminal summary required to validate this link.
func lockWebhookSource(ctx context.Context, tx pgx.Tx, i plugindispatch.Intent) (string, error) {
	j, err := scanTask(tx.QueryRow(ctx, `SELECT `+pluginTaskColumns+` FROM np_plugin_jobs WHERE id=$1 FOR SHARE`, i.JobID))
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	a, err := scanArchivedTask(tx.QueryRow(ctx, `SELECT `+archivedTaskColumns+` FROM np_plugin_job_archives WHERE id=$1 FOR SHARE`, i.JobID))
	archived := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if found == archived {
		return "", plugindispatch.ErrCorrupt
	}
	if found {
		if j.Plugin != i.Plugin || j.Kind != i.Kind || j.Epoch != i.Epoch || j.ActorID != i.ActorID || j.Fingerprint != i.SourceFingerprint {
			return "", plugindispatch.ErrCorrupt
		}
		return j.Status, nil
	}
	if a.Plugin != i.Plugin || a.Kind != i.Kind || a.Fingerprint != i.SourceFingerprint {
		return "", plugindispatch.ErrCorrupt
	}
	return a.Status, nil
}

func lockWebhookAuthority(ctx context.Context, tx pgx.Tx, i plugindispatch.Intent) error {
	c, err := scanTaskControl(tx.QueryRow(ctx, `SELECT `+pluginTaskControlColumns+` FROM np_plugin_task_authorizations WHERE slug=$1 FOR SHARE`, i.Plugin))
	if errors.Is(err, pgx.ErrNoRows) {
		return pluginjobs.ErrAuthorization
	}
	if err != nil {
		return taskControlReadError(err)
	}
	manifest, err := json.Marshal(c.Manifest)
	u, _ := safehttp.ValidateURL(i.URL)
	at, clockErr := webhookClock(ctx, tx)
	if clockErr != nil {
		return clockErr
	}
	job := pluginjobs.Job{Plugin: i.Plugin, Kind: i.Kind, Epoch: i.Epoch, ActorID: i.ActorID}
	if err != nil || plugineffects.Digest(manifest) != i.ManifestDigest || c.ModuleDigest != i.ModuleDigest || !plugin.TaskControlAllows(c, job, at) ||
		!slices.Contains(c.Manifest.Capabilities, "http.send") || !slices.Contains(c.Configuration.Grants, "http.send") || !slices.Contains(c.Manifest.OutboundHosts, strings.ToLower(u.Hostname())) {
		return pluginjobs.ErrAuthorization
	}
	return nil
}

func (r *PluginWebhookRepository) expire(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return plugindispatch.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	if err = webhookLock(ctx, tx); err != nil {
		return err
	}
	items, _, _, err := r.selectIntents(ctx, tx, func(i plugindispatch.Intent) bool { return i.State == "sending" })
	if err != nil {
		return err
	}
	at, err := webhookClock(ctx, tx)
	if err != nil {
		return err
	}
	for _, i := range items {
		if i.State != "sending" || i.AttemptUntil.After(at) {
			continue
		}
		i.Version++
		i.State = "uncertain"
		i.UpdatedAt = at
		i.Outcome = plugindispatch.Outcome{Code: "worker_expired"}
		i.ResultAt = at
		if err = savePluginWebhook(ctx, tx, i); err != nil {
			return err
		}
		if err = webhookAudit(ctx, tx, i, 0, "plugin.webhook.uncertain"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *PluginWebhookRepository) Claim(ctx context.Context) (plugindispatch.Intent, bool, error) {
	// Expiry is recorded even on instances that have outbound sending disabled,
	// and remains independent of quarantine/authority needed for a new send.
	if err := r.expire(ctx); err != nil {
		return plugindispatch.Intent{}, false, err
	}
	if !r.enabled {
		return plugindispatch.Intent{}, false, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return plugindispatch.Intent{}, false, plugindispatch.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `LOCK TABLE np_plugin_job_archives, np_plugin_jobs IN ROW SHARE MODE`); err != nil {
		return plugindispatch.Intent{}, false, plugindispatch.ErrUnavailable
	}
	if err = sharedQuarantineGate(ctx, tx); err != nil {
		return plugindispatch.Intent{}, false, err
	}
	quarantines, err := loadPGPluginQuarantines(ctx, tx)
	if err != nil {
		return plugindispatch.Intent{}, false, err
	}
	blocked, err := pluginjobs.QuarantineWorkPolicy(quarantines)
	if err != nil {
		return plugindispatch.Intent{}, false, err
	}
	if err = webhookLock(ctx, tx); err != nil {
		return plugindispatch.Intent{}, false, err
	}
	items, _, _, err := r.selectIntents(ctx, tx, func(i plugindispatch.Intent) bool { return i.State == "pending" })
	if err != nil {
		return plugindispatch.Intent{}, false, err
	}
	sort.Slice(items, func(a, b int) bool {
		if items[a].CreatedAt.Equal(items[b].CreatedAt) {
			return items[a].ID < items[b].ID
		}
		return items[a].CreatedAt.Before(items[b].CreatedAt)
	})
	var claimed plugindispatch.Intent
	for _, i := range items {
		if i.State != "pending" || blocked[i.JobID] {
			continue
		}
		// Admission serializes this path with enqueue before row locks. Authority,
		// source and account remain locked through the durable send claim.
		err = lockWebhookAuthority(ctx, tx, i)
		reason := ""
		if errors.Is(err, pluginjobs.ErrAuthorization) {
			reason = "authority_changed"
		} else if err != nil {
			return plugindispatch.Intent{}, false, err
		}
		status, err := lockWebhookSource(ctx, tx, i)
		if err != nil {
			return plugindispatch.Intent{}, false, err
		}
		if status == "cancelled" {
			reason = "source_cancelled"
		}
		if reason == "" {
			_, err = lockPluginEffectAccount(ctx, tx, i.ActorID)
			if errors.Is(err, pluginjobs.ErrAuthorization) {
				reason = "authority_changed"
			} else if err != nil {
				return plugindispatch.Intent{}, false, err
			}
		}
		if reason == "" && status != "succeeded" {
			continue
		}
		at, err := webhookClock(ctx, tx)
		if err != nil {
			return plugindispatch.Intent{}, false, err
		}
		i.Version++
		i.UpdatedAt = at
		if reason != "" {
			i.State = "cancelled"
			i.Outcome = plugindispatch.Outcome{Code: reason}
			if err = savePluginWebhook(ctx, tx, i); err != nil {
				return plugindispatch.Intent{}, false, err
			}
			if err = webhookAudit(ctx, tx, i, 0, "plugin.webhook.cancelled"); err != nil {
				return plugindispatch.Intent{}, false, err
			}
			continue
		}
		var token [32]byte
		if _, err = rand.Read(token[:]); err != nil {
			return plugindispatch.Intent{}, false, plugindispatch.ErrUnavailable
		}
		i.State = "sending"
		i.AttemptToken = hex.EncodeToString(token[:])
		i.AttemptAt = at
		i.AttemptUntil = at.Add(plugindispatch.AttemptDuration)
		if err = savePluginWebhook(ctx, tx, i); err != nil {
			return plugindispatch.Intent{}, false, err
		}
		if err = webhookAudit(ctx, tx, i, i.ActorID, "plugin.webhook.claimed"); err != nil {
			return plugindispatch.Intent{}, false, err
		}
		claimed = i
		break
	}
	if err = tx.Commit(ctx); err != nil {
		return plugindispatch.Intent{}, false, plugindispatch.ErrUnavailable
	}
	return claimed, claimed.ID != "", nil
}

func (r *PluginWebhookRepository) Finish(ctx context.Context, attempt plugindispatch.Intent, outcome plugindispatch.Outcome) error {
	if plugindispatch.Validate(attempt) != nil || attempt.State != "sending" || !plugindispatch.ValidOutcome(outcome) || outcome.Code != "http_2xx" && outcome.Code != "http_status" && outcome.Code != "transport_unconfirmed" && outcome.Code != "response_unconfirmed" {
		return plugindispatch.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return plugindispatch.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	if err = webhookLock(ctx, tx); err != nil {
		return err
	}
	items, _, _, err := r.selectIntents(ctx, tx, func(i plugindispatch.Intent) bool { return i.ID == attempt.ID })
	if err != nil {
		return err
	}
	for _, i := range items {
		if i.ID != attempt.ID {
			continue
		}
		if i.AttemptToken != attempt.AttemptToken || i.Fingerprint != attempt.Fingerprint {
			return plugindispatch.ErrConflict
		}
		if i.State != "sending" {
			if i.Version == 3 && i.Outcome == outcome {
				return tx.Commit(ctx)
			}
			return plugindispatch.ErrConflict
		}
		at, err := webhookClock(ctx, tx)
		if err != nil {
			return err
		}
		i.Version++
		i.UpdatedAt = at
		i.State = "uncertain"
		i.Outcome = outcome
		i.ResultAt = at
		if !i.AttemptUntil.After(at) {
			i.Outcome = plugindispatch.Outcome{Code: "worker_expired"}
		} else if outcome.Code == "http_2xx" {
			i.State = "http_confirmed"
		}
		if err = savePluginWebhook(ctx, tx, i); err != nil {
			return err
		}
		if err = webhookAudit(ctx, tx, i, i.ActorID, "plugin.webhook.result"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return plugindispatch.ErrConflict
}

func webhookSummary(i plugindispatch.Intent) plugindispatch.Summary {
	s := plugindispatch.Summary{ID: i.ID, JobID: i.JobID, Plugin: i.Plugin, Slot: i.Slot, Target: plugindispatch.TargetOrigin(i.URL), Fingerprint: i.Fingerprint, Version: i.Version, State: i.State, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, Outcome: i.Outcome, ResultAt: i.ResultAt}
	if i.Review != nil {
		s.Review = &plugindispatch.ReviewSummary{Decision: i.Review.Decision, Reason: i.Review.Reason, ActorID: strconv.FormatInt(i.Review.ActorID, 10), At: i.Review.At}
	}
	return s
}

func (r *PluginWebhookRepository) List(ctx context.Context, query plugindispatch.Query) (plugindispatch.Page, error) {
	q, err := plugindispatch.NormalizeQuery(query)
	if err != nil {
		return plugindispatch.Page{}, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return plugindispatch.Page{}, plugindispatch.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	items := []plugindispatch.Summary{}
	_, _, err = scanPluginWebhookIntents(ctx, tx, &r.validated, func(i plugindispatch.Intent) {
		if (q.Plugin == "" || i.Plugin == q.Plugin) && (q.JobID == "" || i.JobID == q.JobID) && (q.State == "" || i.State == q.State) {
			items = append(items, webhookSummary(i))
		}
	})
	if err != nil {
		return plugindispatch.Page{}, err
	}
	sort.Slice(items, func(a, b int) bool {
		if items[a].CreatedAt.Equal(items[b].CreatedAt) {
			return items[a].ID < items[b].ID
		}
		return items[a].CreatedAt.After(items[b].CreatedAt)
	})
	start := min(q.Offset, len(items))
	end := min(start+q.Limit, len(items))
	page := plugindispatch.Page{Items: slices.Clone(items[start:end]), Total: len(items), Limit: q.Limit, Offset: q.Offset}
	if page.Items == nil {
		page.Items = []plugindispatch.Summary{}
	}
	if err = tx.Commit(ctx); err != nil {
		return plugindispatch.Page{}, plugindispatch.ErrUnavailable
	}
	return page, nil
}

func (r *PluginWebhookRepository) Review(ctx context.Context, request plugindispatch.ReviewRequest) (plugindispatch.Summary, error) {
	if !plugindispatch.ValidReview(request) {
		return plugindispatch.Summary{}, plugindispatch.ErrInvalid
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return plugindispatch.Summary{}, plugindispatch.ErrUnavailable
	}
	defer tx.Rollback(context.Background())
	if err = webhookLock(ctx, tx); err != nil {
		return plugindispatch.Summary{}, err
	}
	if _, err = lockPluginEffectAccount(ctx, tx, request.ActorID); err != nil {
		return plugindispatch.Summary{}, err
	}
	items, _, _, err := r.selectIntents(ctx, tx, func(i plugindispatch.Intent) bool { return i.ID == request.ID })
	if err != nil {
		return plugindispatch.Summary{}, err
	}
	for _, i := range items {
		if i.ID != request.ID {
			continue
		}
		if i.Version == request.Version+1 && i.Review != nil && i.Review.ActorID == request.ActorID && i.Review.Decision == request.Decision && i.Review.Reason == request.Reason {
			if err = tx.Commit(ctx); err != nil {
				return plugindispatch.Summary{}, plugindispatch.ErrUnavailable
			}
			return webhookSummary(i), nil
		}
		if i.Version != request.Version || i.State != "uncertain" {
			return plugindispatch.Summary{}, plugindispatch.ErrConflict
		}
		at, err := webhookClock(ctx, tx)
		if err != nil {
			return plugindispatch.Summary{}, err
		}
		i.Version++
		i.State = "reviewed_" + request.Decision
		i.UpdatedAt = at
		i.Review = &plugindispatch.Review{Decision: request.Decision, Reason: request.Reason, ActorID: request.ActorID, At: at}
		if err = savePluginWebhook(ctx, tx, i); err != nil {
			return plugindispatch.Summary{}, err
		}
		if err = webhookAudit(ctx, tx, i, request.ActorID, "plugin.webhook.reviewed"); err != nil {
			return plugindispatch.Summary{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return plugindispatch.Summary{}, plugindispatch.ErrUnavailable
		}
		return webhookSummary(i), nil
	}
	return plugindispatch.Summary{}, plugindispatch.ErrConflict
}

// Called inside the existing offline restore transaction, after table locks.
// A pending intent is suppressed, an attempted one becomes uncertain, and all
// terminal operation identities, request bytes and reviews are retained.
func cancelRestoredWebhooks(ctx context.Context, tx pgx.Tx) error {
	items, _, err := loadPluginWebhookIntents(ctx, tx)
	if err != nil {
		return err
	}
	at, err := webhookClock(ctx, tx)
	if err != nil {
		return err
	}
	for _, i := range items {
		if i.State != "pending" && i.State != "sending" {
			continue
		}
		i.Version++
		i.UpdatedAt = at
		i.Outcome = plugindispatch.Outcome{Code: "restored"}
		if i.State == "pending" {
			i.State = "cancelled"
		} else {
			i.State = "uncertain"
			i.ResultAt = at
		}
		if err = savePluginWebhook(ctx, tx, i); err != nil {
			return err
		}
		if err = webhookAudit(ctx, tx, i, 0, "plugin.webhook.restored"); err != nil {
			return err
		}
	}
	return nil
}
