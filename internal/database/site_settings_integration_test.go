package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"newpress.local/newpress/internal/site"
)

func TestPostgreSQLSiteSettingsRepositoryCAS(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	repository := NewSiteSettingsRepository(pool)
	if _, found, err := repository.Load(ctx); err != nil || found {
		t.Fatalf("empty site settings: found=%t err=%v", found, err)
	}

	first := []byte(`{"revision":"0123456789abcdef","value":1}`)
	created, err := repository.Create(ctx, "0123456789abcdef", first)
	if err != nil || !created {
		t.Fatalf("create site settings: created=%v err=%v", created, err)
	}
	created, err = repository.Create(ctx, "fedcba9876543210", []byte(`{"revision":"fedcba9876543210"}`))
	if err != nil || created {
		t.Fatalf("second create must not replace existing settings: created=%v err=%v", created, err)
	}

	payload, found, err := repository.Load(ctx)
	if err != nil || !found || !sameJSON(payload, first) {
		t.Fatalf("load site settings: payload=%s found=%v err=%v", payload, found, err)
	}
	stale, err := repository.CompareAndSwap(ctx, "fedcba9876543210", "1111111111111111", []byte(`{"revision":"1111111111111111"}`))
	if err != nil || stale {
		t.Fatalf("stale compare-and-swap: updated=%v err=%v", stale, err)
	}

	second := []byte(`{"revision":"1111111111111111","value":2}`)
	updated, err := repository.CompareAndSwap(ctx, "0123456789abcdef", "1111111111111111", second)
	if err != nil || !updated {
		t.Fatalf("valid compare-and-swap: updated=%v err=%v", updated, err)
	}
	payload, found, err = repository.Load(ctx)
	if err != nil || !found || !sameJSON(payload, second) {
		t.Fatalf("load updated site settings: payload=%s found=%v err=%v", payload, found, err)
	}
}

func TestPostgreSQLSiteSettingsConcurrentCASHasOneWinner(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewSiteSettingsRepository(pool)
	const initialRevision = "0123456789abcdef"
	if created, err := repository.Create(ctx, initialRevision, []byte(`{"revision":"0123456789abcdef"}`)); err != nil || !created {
		t.Fatalf("create settings: created=%t err=%v", created, err)
	}
	const contenders = 8
	type result struct {
		updated bool
		payload []byte
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			revision := fmt.Sprintf("%016x", index+1)
			payload := []byte(fmt.Sprintf(`{"revision":%q,"winner":%d}`, revision, index))
			<-start
			updated, err := repository.CompareAndSwap(ctx, initialRevision, revision, payload)
			results <- result{updated: updated, payload: payload, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	var committed []byte
	winners := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.updated {
			winners++
			committed = result.payload
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent setting updates had %d winners, want 1", winners)
	}
	payload, found, err := NewSiteSettingsRepository(pool).Load(ctx)
	if err != nil || !found || !sameJSON(payload, committed) {
		t.Fatalf("reopened repository did not retain winning update: found=%t err=%v", found, err)
	}
}

func TestPostgreSQLSiteSettingsManagerRestartAndConflict(t *testing.T) {
	ctx, pool := openSetupIntegrationPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first, err := site.NewManagerWithRepository(ctx, NewSiteSettingsRepository(pool), "", site.DefaultSettings("Initial", "Initial"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := site.NewManagerWithRepository(ctx, NewSiteSettingsRepository(pool), "", site.DefaultSettings("Ignored", "Ignored"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := first.Snapshot()
	updated := snapshot.Settings
	updated.Identity.Title = "Persistent site title"
	if _, fields, err := first.Save(snapshot.Revision, updated); err != nil || len(fields) != 0 {
		t.Fatalf("save durable site settings: fields=%v err=%v", fields, err)
	}
	stale := second.Snapshot()
	staleSettings := stale.Settings
	staleSettings.Identity.Title = "Stale overwrite"
	if _, _, err := second.Save(stale.Revision, staleSettings); !errors.Is(err, site.ErrRevisionConflict) {
		t.Fatalf("stale manager update error = %v", err)
	}
	if second.Snapshot().Settings.Identity.Title != updated.Identity.Title {
		t.Fatal("conflicting manager did not reload the committed settings")
	}
	restarted, err := site.NewManagerWithRepository(ctx, NewSiteSettingsRepository(pool), "", site.DefaultSettings("New defaults", "New defaults"))
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Snapshot().Settings.Identity.Title != updated.Identity.Title {
		t.Fatal("restarted manager overwrote persisted settings with defaults")
	}
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	return leftDecoder.Decode(&leftValue) == nil && rightDecoder.Decode(&rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}
