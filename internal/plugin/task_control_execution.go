package plugin

import (
	"context"
	"errors"
	"sort"
	"time"

	"newpress.local/newpress/internal/pluginjobs"
)

// TaskControlAllows checks shared host authority without consulting local
// package availability. Local absence cannot revoke another instance's work.
func TaskControlAllows(control TaskControl, job pluginjobs.Job, now time.Time) bool {
	return control.Slug == job.Plugin && control.Enabled && !control.Retired &&
		control.TransitionID == "" && control.Epoch.Token == job.Epoch &&
		!control.Epoch.StartedAt.After(now) && len(control.Manifest.Hooks[job.Kind]) == 1 &&
		(job.Kind != "event.deliver.v1" || (control.Epoch.ActorID > 0 && control.Epoch.ActorID == job.ActorID))
}

// TaskSubscriptions discovers shared consent independently of this instance's
// installed packages. The event adapter separately checks each current account.
func (m *Manager) TaskSubscriptions(ctx context.Context) ([]pluginjobs.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	subscriptions := []pluginjobs.Subscription{}
	if m.taskControls != nil {
		controls, err := m.taskControls.ListTaskControls(ctx)
		if err != nil {
			return nil, err
		}
		if len(controls) > MaxTaskControls {
			return nil, ErrTaskControlCorrupt
		}
		seen, budget := map[string]bool{}, 0
		for _, control := range controls {
			reserved, err := TaskControlBudgetBytes(control)
			if err != nil || seen[control.Slug] || reserved > MaxTaskControlTotal-budget {
				return nil, ErrTaskControlCorrupt
			}
			seen[control.Slug], budget = true, budget+reserved
			if !control.Enabled || control.Retired || control.TransitionID != "" ||
				control.Epoch.ActorID <= 0 || control.Epoch.StartedAt.After(time.Now()) ||
				len(control.Manifest.Hooks["event.deliver.v1"]) != 1 {
				continue
			}
			subscriptions = append(subscriptions, pluginjobs.Subscription{Plugin: control.Slug,
				Epoch: control.Epoch.Token, Since: control.Epoch.StartedAt, ActorID: control.Epoch.ActorID})
		}
	} else {
		installed, err := m.List()
		if err != nil {
			return nil, err
		}
		for _, item := range installed {
			if !item.Enabled || !item.Official || item.Error != "" || len(item.Manifest.Hooks["event.deliver.v1"]) != 1 {
				continue
			}
			epoch, err := m.CurrentTaskEpoch(item.Slug)
			if errors.Is(err, ErrInactive) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if epoch.ActorID > 0 {
				subscriptions = append(subscriptions, pluginjobs.Subscription{Plugin: item.Slug,
					Epoch: epoch.Token, Since: epoch.StartedAt, ActorID: epoch.ActorID})
			}
		}
	}
	sort.Slice(subscriptions, func(i, j int) bool { return subscriptions[i].Plugin < subscriptions[j].Plugin })
	return subscriptions, nil
}

func (m *Manager) taskClaimScopes(ctx context.Context) ([]pluginjobs.ClaimScope, error) {
	installed, err := m.List()
	if err != nil {
		return nil, err
	}
	scopes := []pluginjobs.ClaimScope{}
	for _, item := range installed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !item.Enabled || !item.Official || item.Error != "" || item.Manifest.Runtime != "wasm/core-v1" {
			continue
		}
		epoch, err := m.CurrentTaskEpoch(item.Slug)
		if errors.Is(err, ErrInactive) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, pluginjobs.ClaimScope{Plugin: item.Slug, Epoch: epoch.Token})
	}
	return scopes, nil
}
