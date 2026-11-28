package httpserver

import (
	"context"
	"time"

	"newpress.local/newpress/internal/aieditor"
)

func (a *aiAdminAuthority) models(connection aieditor.Connection) ([]aieditor.Model, error) {
	var models []aieditor.Model
	err := a.wait(func(ctx context.Context, check func() error) error {
		var err error
		models, err = a.server.aiClient.ModelsChecked(ctx, connection, check)
		return err
	})
	return models, err
}

func (a *aiAdminAuthority) balance(provider string) (aieditor.BalanceResult, error) {
	var result aieditor.BalanceResult
	err := a.wait(func(ctx context.Context, _ func() error) error {
		var err error
		result, err = a.server.aiBalances.Query(ctx, a.server.aiSettings, provider)
		return err
	})
	return result, err
}

func (a *aiAdminAuthority) wait(run func(context.Context, func() error) error) error {
	ctx, cancel := context.WithCancelCause(a.request.Context())
	defer cancel(nil)
	stopModule := context.AfterFunc(a.module, func() { cancel(errAIAdminInactive) })
	defer stopModule()
	checked := *a
	checked.request = a.request.WithContext(ctx)
	guard := &aiGenerationGuard{ctx: ctx, cancel: cancel, access: checked.access}
	if err := guard.check(true); err != nil {
		return err
	}
	// Upstream queries may wait for headers or a response body. Refresh the
	// original session even when there is no model data to trigger a check.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if guard.check(false) != nil {
					return
				}
			}
		}
	}()
	defer func() { cancel(nil); <-done }()
	err := run(ctx, func() error { return guard.check(true) })
	if accessErr := guard.check(true); accessErr != nil {
		return accessErr
	}
	return err
}
