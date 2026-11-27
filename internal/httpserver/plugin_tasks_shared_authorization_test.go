package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/pluginjobs"
)

type sharedClaimProbe struct {
	pluginjobs.ManagedStore
	sharedCalls int
	localCalls  int
	failure     error
}

func (p *sharedClaimProbe) CheckClaim(context.Context, pluginjobs.Job, time.Time) error {
	p.localCalls++
	return errors.New("directory claim check must not be selected for shared storage")
}

func (p *sharedClaimProbe) CheckSharedClaim(context.Context, pluginjobs.Job, time.Time) error {
	p.sharedCalls++
	return p.failure
}

func TestPluginTaskSharedClaimAdapterPreservesAuthorizationAndAvailability(t *testing.T) {
	s, _, _, principal := newReviewedTaskHTTPTestServer(t)
	backend, err := pluginjobs.NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	q := &pluginjobs.Queue{Backend: backend}
	now := time.Now().UTC()
	if _, err := q.Enqueue(t.Context(), pluginjobs.Input{Plugin: "adapter", Epoch: strings.Repeat("a", 64), Kind: "task.execute.v1", Key: "adapter", ActorID: principal.UserID, Payload: json.RawMessage(`{}`)}, now); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := q.Claim(t.Context(), now)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	probe := &sharedClaimProbe{ManagedStore: q}
	s.SetPluginTaskStore(probe)
	if err := s.authorizePluginTask(t.Context(), claim); err != nil || probe.sharedCalls != 1 || probe.localCalls != 0 {
		t.Fatal("HTTP executor did not select shared claim authorization", err, probe)
	}
	probe.failure = plugin.ErrTaskControlUnavailable
	if err := s.authorizePluginTask(t.Context(), claim); !errors.Is(err, plugin.ErrTaskControlUnavailable) || errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("shared availability failure became an authorization cancellation", err)
	}
	probe.failure = pluginjobs.ErrAuthorization
	if err := s.authorizePluginTask(t.Context(), claim); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("revoked shared claim reached account authorization", err)
	}
	s.SetPluginTaskStore(q)
	if err := s.authorizePluginTask(t.Context(), claim); err != nil {
		t.Fatal("directory queue lost its original claim adapter", err)
	}
	if err := q.Change(t.Context(), claim.Plugin, claim.ID, claim.Version, "cancel", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.authorizePluginTask(t.Context(), claim); !errors.Is(err, pluginjobs.ErrAuthorization) {
		t.Fatal("directory claim adapter bypassed cancellation", err)
	}
}
