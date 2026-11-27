package plugin

import (
	"context"
	"errors"
	"time"

	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

// TaskExecutor binds durable work to a host-authorized principal. Authorize
// must consult current account state, not cookies or captured role snapshots.
// Effects must independently fence the claim, shared authority and current
// account in the same transaction as the content write and durable receipt.
type TaskExecutor struct {
	Manager   *Manager
	Read      ContentReader
	Authorize func(context.Context, pluginjobs.Job) error
	Effects   plugineffects.Store
	Webhooks  plugindispatch.Store
}

var ErrTaskRuntimeUnavailable = errors.New("当前实例的插件包暂不可执行")

// Claim leaves valid work for the instance with a matching local runtime.
// Directory-owned file mode keeps its existing acquisition behavior.
func (e *TaskExecutor) Claim(ctx context.Context, queue pluginjobs.Store, now time.Time) (pluginjobs.Job, bool, error) {
	if e.Manager == nil || e.Manager.taskControls == nil {
		return queue.Claim(ctx, now)
	}
	claimer, ok := queue.(pluginjobs.ScopedClaimer)
	if !ok {
		return pluginjobs.Job{}, false, ErrTaskControlUnavailable
	}
	scopes, err := e.Manager.taskClaimScopes(ctx)
	if err != nil {
		return pluginjobs.Job{}, false, err
	}
	return claimer.ClaimScoped(ctx, now, scopes)
}

func (e *TaskExecutor) taskEpochFailure(ctx context.Context, job pluginjobs.Job, err error) error {
	if !errors.Is(err, ErrInactive) {
		return err
	}
	if e.Manager.taskControls == nil {
		return pluginjobs.ErrAuthorization
	}
	control, found, failure := e.Manager.loadTaskControl(ctx, job.Plugin)
	if failure != nil {
		return failure
	}
	if !found || !TaskControlAllows(control, job, time.Now().UTC()) {
		return pluginjobs.ErrAuthorization
	}
	if failure = e.Authorize(ctx, job); failure != nil {
		return failure
	}
	return ErrTaskRuntimeUnavailable
}

func (e *TaskExecutor) Execute(ctx context.Context, j pluginjobs.Job) error {
	if e.Manager == nil || e.Authorize == nil {
		return pluginjobs.ErrAuthorization
	}
	if j.Kind != "task.execute.v1" && j.Kind != "event.deliver.v1" {
		return pluginjobs.ErrAuthorization
	}
	if j.Status != "running" || !j.LeaseUntil.After(time.Now()) || j.ID == "" || j.Epoch == "" {
		return pluginjobs.ErrAuthorization
	}
	ctx, cancel := context.WithDeadline(ctx, j.LeaseUntil)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Authorize(ctx, j); err != nil {
		return err
	}
	epoch, err := e.Manager.CurrentTaskEpoch(j.Plugin)
	if err != nil {
		return e.taskEpochFailure(ctx, j, err)
	}
	if epoch.Token != j.Epoch || (j.Kind == "event.deliver.v1" && epoch.ActorID != j.ActorID) {
		return e.taskEpochFailure(ctx, j, ErrInactive)
	}
	// Dispatch rechecks the claim/account before every allowed host call. The
	// content projection still excludes drafts/private objects and private fields.
	_, err = e.Manager.dispatchAuthorizedEffects(ctx, j.Plugin, BusinessInput{Event: j.Kind, Payload: j.Payload}, e.Read, j.Epoch,
		func(ctx context.Context) error { return e.Authorize(ctx, j) }, &taskEffectInvocation{claim: j, store: e.Effects, webhooks: e.Webhooks})
	if errors.Is(err, ErrInactive) {
		return e.taskEpochFailure(ctx, j, err)
	}
	if err != nil {
		return err
	}
	current, err := e.Manager.CurrentTaskEpoch(j.Plugin)
	if err != nil {
		return e.taskEpochFailure(ctx, j, err)
	}
	if current.Token != j.Epoch {
		return e.taskEpochFailure(ctx, j, ErrInactive)
	}
	if err = e.Authorize(ctx, j); err != nil {
		return err
	}
	return nil
}
