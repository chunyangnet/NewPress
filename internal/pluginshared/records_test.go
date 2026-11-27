package pluginshared

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugineffects"
)

func recordFixture(t *testing.T, kind string) Record {
	t.Helper()
	r := Record{Type: kind}
	var index Index
	if kind == Authority {
		epoch, err := plugin.NewTaskEpoch(7)
		if err != nil {
			t.Fatal(err)
		}
		c := plugin.TaskControl{Schema: 1, Slug: "test-writer", Revision: 3, ModuleDigest: strings.Repeat("a", 64), Enabled: true, Epoch: epoch,
			Manifest:      plugin.Manifest{Schema: 1, Slug: "test-writer", Name: "Test writer", Version: "1.0.0", Runtime: "wasm/core-v1", Entry: "plugin.wasm", Capabilities: []string{"content.write"}, Hooks: map[string][]string{"task.execute.v1": {"np_handle"}}},
			Configuration: plugin.Configuration{Version: 2, Values: map[string]any{}, Grants: []string{"content.write"}}}
		r.Data, _ = json.Marshal(c)
		r.Key = c.Slug
		index = Index{Revision: c.Revision, Epoch: c.Epoch.Token, Enabled: true}
	} else {
		job := strings.Repeat("b", 64)
		id, _ := plugineffects.OperationID(job, "private-slot")
		e := plugineffects.Receipt{Schema: 1, JobID: job, Plugin: "test-writer", Slot: "private-slot", Fingerprint: strings.Repeat("c", 64), ActorID: 7, ManifestDigest: strings.Repeat("d", 64), ModuleDigest: strings.Repeat("a", 64),
			Result: plugineffects.Result{OperationID: id, PostID: 42, PreviousVersion: 3, Version: 4, Changed: true, CommittedAt: time.Now().UTC()}}
		r.Data, _ = json.Marshal(e)
		r.Key = id
		index = Index{JobID: job, Slot: e.Slot}
	}
	r.Index, _ = json.Marshal(index)
	if d := Diagnose(r); d.Fault != "" {
		t.Fatal(d)
	}
	return r
}
func isolateFixture(t *testing.T, r Record) Quarantine {
	t.Helper()
	r.Key = "wrong-index"
	request := CaptureRequest{Type: r.Type, Reference: Reference(r), Key: "command-1", Operator: "operator", Reason: "verified record index"}
	q, err := Capture(r, "index_mismatch", request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return q
}
func TestSharedRecordRepairPreservesFactsAndRetiresAuthority(t *testing.T) {
	for _, kind := range []string{Authority, ContentEffect} {
		t.Run(kind, func(t *testing.T) {
			r := recordFixture(t, kind)
			q := isolateFixture(t, r)
			request := RepairRequest{ID: q.ID, Operator: "operator", Reason: "verified original JSON"}
			out, audit, err := BuildRepair(q, request, time.Now().UTC())
			if err != nil || Diagnose(out).Fault != "" {
				t.Fatal(out, audit, err)
			}
			if kind == ContentEffect {
				if !bytes.Equal(out.Data, r.Data) || audit.Disposition != "receipt_preserved" {
					t.Fatal("rewrote receipt")
				}
			} else {
				c, err := decode(kind, out.Data, false)
				old, _ := decode(kind, r.Data, false)
				if err != nil || c.control.Enabled || !c.control.Retired || c.control.Epoch.ActorID != 0 || c.control.Epoch.Token == old.control.Epoch.Token || c.control.Revision != old.control.Revision+1 {
					t.Fatal("repair revived authority", err)
				}
			}
			q.Repair = &audit
			if ValidateQuarantine(q) != nil || MatchRepair(q, request) != nil {
				t.Fatal("repeat confirmation lost")
			}
			request.Reason = "changed reason"
			if !errors.Is(MatchRepair(q, request), ErrConflict) {
				t.Fatal("changed repair accepted")
			}
		})
	}
}
func TestSharedRecordReplacementCannotInventOriginalFacts(t *testing.T) {
	r := recordFixture(t, ContentEffect)
	original := append([]byte(nil), r.Data...)
	r.Data = bytes.Replace(r.Data, []byte(`{"schema":1`), []byte(`{"unknown":true,"schema":1`), 1)
	q := isolateFixture(t, r)
	request := RepairRequest{ID: q.ID, Operator: "operator", Reason: "verified original backup", Replacement: original, ReplacementDigest: plugineffects.Digest(original)}
	if _, _, err := BuildRepair(q, request, time.Now().UTC()); err != nil {
		t.Fatal("known facts with extra unknown field cannot be restored", err)
	}
	for _, mode := range []string{"post", "fingerprint", "missing", "duplicate", "alias", "unicode"} {
		t.Run(mode, func(t *testing.T) {
			changed := q
			changed.Original = r
			switch mode {
			case "post":
				request := request
				request.Replacement = bytes.Replace(original, []byte(`"post_id":42`), []byte(`"post_id":43`), 1)
				request.ReplacementDigest = plugineffects.Digest(request.Replacement)
				if _, _, err := BuildRepair(q, request, time.Now().UTC()); err == nil {
					t.Fatal("invented result accepted")
				}
				return
			case "fingerprint":
				changed.Original.Data = bytes.Replace(r.Data, []byte(strings.Repeat("c", 64)), []byte(strings.Repeat("e", 64)), 1)
			case "missing":
				changed.Original.Data = []byte(`{}`)
			case "duplicate":
				changed.Original.Data = bytes.Replace(r.Data, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1)
			case "alias":
				changed.Original.Data = bytes.Replace(r.Data, []byte(`"schema":1`), []byte(`"schema":1,"SCHEMA":1`), 1)
			case "unicode":
				changed.Original.Data = bytes.Replace(r.Data, []byte(`"test-writer"`), []byte(`"\ud800"`), 1)
			}
			changed.Reference = Reference(changed.Original)
			if _, _, err := BuildRepair(changed, request, time.Now().UTC()); err == nil {
				t.Fatal("unprovable original accepted")
			}
		})
	}
}
func TestSharedRecordDiagnosticsRedactAndKeepDuplicateVisible(t *testing.T) {
	r := recordFixture(t, ContentEffect)
	q := isolateFixture(t, r)
	d := DiagnoseWithQuarantines([]Record{r}, []Quarantine{q})
	if len(d) != 1 || d[0].Fault != "duplicate_identity" {
		t.Fatal(d)
	}
	page := PaginateDiagnostics(d, Query{Limit: 1, Plugin: "test-writer"})
	body, _ := json.Marshal(page)
	for _, value := range []string{"private-slot", "actor_id", "fingerprint", "manifest_digest", "module_digest", "epoch", "configuration", "original", "index"} {
		if strings.Contains(string(body), `"`+value+`"`) {
			t.Fatal("private data in projection", value)
		}
	}
	if page.Items[0].Result.PostID != 42 || page.Items[0].Result.Version != 4 {
		t.Fatal("result unavailable")
	}
	audit, err := Reconcile(q, RepairRequest{ID: q.ID, Operator: "operator", Reason: "same receipt checked", KeepReference: Reference(r)}, r, time.Now().UTC())
	if err != nil || audit.Disposition != "duplicate_preserved" {
		t.Fatal(audit, err)
	}
}
func TestSharedRecordAuthorityDuplicateRequiresRetiredSurvivor(t *testing.T) {
	r := recordFixture(t, Authority)
	q := isolateFixture(t, r)
	request := RepairRequest{ID: q.ID, Operator: "operator", Reason: "same authority checked", KeepReference: Reference(r)}
	if _, err := Reconcile(q, request, r, time.Now().UTC()); err == nil {
		t.Fatal("enabled authority retained by repair")
	}
	out, _, err := BuildRepair(q, RepairRequest{ID: q.ID, Operator: "operator", Reason: request.Reason}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request.KeepReference = Reference(out)
	if audit, err := Reconcile(q, request, out, time.Now().UTC()); err != nil || audit.Disposition != "duplicate_authority_retired" {
		t.Fatal(audit, err)
	}
}
func TestSharedRecordQueryAndCommandBounds(t *testing.T) {
	for _, q := range []Query{{Type: "task"}, {JobID: "bad"}, {Plugin: "../bad"}, {Limit: -1}, {Limit: 1001}, {Offset: -1}, {Offset: 1000000}} {
		if _, err := NormalizeQuery(q); err == nil {
			t.Fatal(q)
		}
	}
	r := recordFixture(t, Authority)
	q := isolateFixture(t, r)
	for _, text := range []string{"", " bad ", "bad\nreason", strings.Repeat("x", 1025), string([]byte{255})} {
		if ValidateRepair(RepairRequest{ID: q.ID, Operator: "operator", Reason: text}) == nil {
			t.Fatal("bad reason accepted")
		}
	}
	request := RepairRequest{ID: q.ID, Operator: "operator", Reason: "checked", Replacement: r.Data, ReplacementDigest: strings.Repeat("a", 64)}
	if ValidateRepair(request) == nil {
		t.Fatal("wrong replacement checksum accepted")
	}
}

func TestSharedRecordQuarantineRejectsInconsistentAudit(t *testing.T) {
	for _, kind := range []string{Authority, ContentEffect} {
		q := isolateFixture(t, recordFixture(t, kind))
		_, audit, err := BuildRepair(q, RepairRequest{ID: q.ID, Operator: "operator", Reason: "verified original"}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		q.Repair = &audit
		for _, mode := range []string{"type", "time", "operator", "reason", "output", "kept", "duplicate-output"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				changed := q
				copy := audit
				changed.Repair = &copy
				switch mode {
				case "type":
					if kind == Authority {
						copy.Disposition = "receipt_preserved"
					} else {
						copy.Disposition = "authority_retired"
					}
				case "time":
					copy.At = q.CapturedAt.Add(-time.Second)
				case "operator":
					copy.Operator = "bad\noperator"
				case "reason":
					copy.Reason = strings.Repeat("x", 1025)
				case "output":
					copy.OutputDigest = "bad"
				case "kept":
					copy.KeptReference = strings.Repeat("e", 64)
				case "duplicate-output":
					copy.Disposition = "duplicate_preserved"
					if kind == Authority {
						copy.Disposition = "duplicate_authority_retired"
					}
					copy.KeptReference = strings.Repeat("e", 64)
					copy.OutputDigest = strings.Repeat("f", 64)
				}
				if ValidateQuarantine(changed) == nil {
					t.Fatal("invalid repair audit accepted")
				}
			})
		}
	}
}

func TestSharedRecordRepairReserveCoversEscapedAudit(t *testing.T) {
	q := isolateFixture(t, recordFixture(t, ContentEffect))
	before, _ := json.Marshal(q)
	_, a, err := BuildRepair(q, RepairRequest{ID: q.ID, Operator: strings.Repeat("<", 128), Reason: strings.Repeat("<", 1024)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	q.Repair = &a
	after, _ := json.Marshal(q)
	if len(after)-len(before) > RepairReserve {
		t.Fatal("reserved audit bytes insufficient", len(after)-len(before))
	}
}
