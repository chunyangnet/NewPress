package cdn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"newpress.local/newpress/internal/plugin"
)

func TestRoutingSaveOwnsPreferredLines(t *testing.T) {
	for _, valid := range []bool{true, false} {
		name := "accepted"
		if !valid {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			s, manager, settings := testService(t)
			r := testRoute()
			r.PreferredLines = []string{"overseas", "cn", "cn"}
			if !valid {
				r.HealthPath = "/admin/login"
			}
			input := RoutingConfig{Version: 1, DNS: []DNSProfile{testDNS()}, Routes: []Route{r}}
			beforeInput, beforeDisk := jsonBody(input), snapshotBytes(t, s)
			saved, err := s.SaveRouting(input, nil, settings.Version)
			if !bytes.Equal(jsonBody(input), beforeInput) {
				t.Error("normalization modified the caller's preferred lines")
			}
			if !valid {
				if err == nil || !bytes.Equal(snapshotBytes(t, s), beforeDisk) {
					t.Fatal("invalid routing settings changed durable state", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"cn", "overseas"}
			if !slices.Equal(saved.Config.Routes[0].PreferredLines, want) {
				t.Fatal("lines were not normalized", saved.Config.Routes[0].PreferredLines)
			}
			input.Routes[0].PreferredLines[0] = "default"
			saved.Config.Routes[0].PreferredLines[0] = "returned-view-edit"
			current, err := s.RoutingStatus()
			if err != nil || !slices.Equal(current.Config.Routes[0].PreferredLines, want) {
				t.Error("editing a caller-owned value changed saved routing", current.Config, err)
			}
			if _, err := s.EnqueueRouting(RoutingRequest{Action: "inspect", RouteID: r.ID, ExpectedVersion: current.Config.Version, CDNVersion: settings.Version}, "tester"); err != nil {
				t.Fatal(err)
			}
			reopened := New(manager, s.publicURL)
			defer reopened.Close()
			restored, err := reopened.RoutingStatus()
			if err != nil {
				t.Fatal("unvalidated caller edit made routing unreadable on restart", err)
			}
			if !slices.Equal(restored.Config.Routes[0].PreferredLines, want) {
				t.Fatal("unvalidated caller edit became durable", restored.Config.Routes[0].PreferredLines)
			}
		})
	}
}

func TestRoutingPlanStorageOwnsMutableValues(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "insert"
		if replace {
			name = "replace"
		}
		t.Run(name, func(t *testing.T) {
			s, manager, r, d, cf := routingService(t)
			r.ManageExisting = true
			before := DNSRecord{ID: "old", Name: r.Hostname, Type: "CNAME", Value: "old.example.net", Line: "cn", TTL: 600, Status: "ENABLE"}
			report := readyReport(r)
			report.Records = []DNSRecord{before}
			plan, err := buildRoutingPlan(RoutingTask{Action: "preview_route"}, r, d, cf, report, routingDefaults())
			if err != nil {
				t.Fatal(err)
			}
			generation := savePlan(t, s, plan)
			if replace {
				plan.Actor = "replacement"
				savePlan(t, s, plan)
			}
			stored, err := s.RoutingStatus()
			if err != nil {
				t.Fatal(err)
			}
			want, disk := jsonBody(stored.Plans), snapshotBytes(t, s)
			plan.Changes[0].State = "done"
			plan.Changes[0].After.Value = "unapproved.example.net"
			plan.Changes[1].Before.TTL = 60
			plan.Snapshots[r.Hostname][0].Value = "changed-before.example.net"
			plan.Snapshots["extra.customer.test"] = []DNSRecord{{ID: "extra"}}
			current, err := s.RoutingStatus()
			if err != nil || !bytes.Equal(jsonBody(current.Plans), want) {
				t.Error("working plan edits changed the committed plan", err)
			}
			if !bytes.Equal(snapshotBytes(t, s), disk) {
				t.Fatal("editing the working plan itself wrote the disk")
			}
			if err := s.with(false, generation, true, func(*diskState) error { return nil }); err != nil {
				t.Fatal(err)
			}
			reopened := New(manager, s.publicURL)
			defer reopened.Close()
			restored, err := reopened.RoutingStatus()
			if err != nil || !bytes.Equal(jsonBody(restored.Plans), want) {
				t.Error("uncommitted working edits became durable on a later save", err)
			}
		})
	}
}

func TestRoutingReceiptWriteFailureKeepsCommittedAttempt(t *testing.T) {
	for _, timing := range []string{"before-request", "after-response"} {
		t.Run(timing, func(t *testing.T) {
			s, manager, r, d, cf := routingService(t)
			fixture := &dnsFixture{}
			base := fixture.transport(t)
			var statePath string
			if err := manager.WithData(plugin.CDN, false, 0, func(dir string, _ uint64) error {
				statePath = filepath.Join(dir, "state.json")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			blocked := false
			var committed diskState
			var committedBytes []byte
			restore := func() {
				if !blocked {
					return
				}
				if err := os.Remove(statePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(statePath+".saved", statePath); err != nil {
					t.Fatal(err)
				}
				blocked = false
			}
			t.Cleanup(restore)
			reads := 0
			s.api.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				req.Body = io.NopCloser(bytes.NewReader(body))
				values, err := url.ParseQuery(string(body))
				if err != nil {
					return nil, err
				}
				if values.Get("Action") == "DescribeDomainRecords" {
					reads++
				}
				response, err := base(req)
				if !blocked && (timing == "before-request" && reads == 2 || timing == "after-response" && fixture.writes == 1) {
					committedBytes = snapshotBytes(t, s)
					if err := json.Unmarshal(committedBytes, &committed); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(statePath, statePath+".saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(statePath, 0700); err != nil {
						t.Fatal(err)
					}
					blocked = true
				}
				return response, err
			})}
			report := readyReport(r)
			plan, err := buildRoutingPlan(RoutingTask{Action: "preview_route"}, r, d, cf, report, routingDefaults())
			if err != nil {
				t.Fatal(err)
			}
			generation := savePlan(t, s, plan)
			if err := s.applyRoutingPlan(t.Context(), generation, RoutingTask{}, r, d, cf, report, plan); err == nil {
				t.Fatal("blocked receipt write unexpectedly succeeded")
			}
			wantWrites, wantState := 0, "pending"
			if timing == "after-response" {
				wantWrites, wantState = 1, "attempting"
			}
			if !blocked || fixture.writes != wantWrites || len(committed.Routing.Plans) != 1 || committed.Routing.Plans[0].Changes[0].State != wantState {
				t.Fatal("failure injection missed the intended persistence boundary", blocked, fixture.writes, committed.Routing.Plans)
			}
			current, err := s.RoutingStatus()
			want := routingView(committed.Routing).Plans
			if err != nil || !reflect.DeepEqual(current.Plans, want) {
				t.Errorf("failed checkpoint changed memory: got=%s want=%s err=%v", jsonBody(current.Plans), jsonBody(want), err)
			}
			saved, err := os.ReadFile(statePath + ".saved")
			if err != nil || !bytes.Equal(saved, committedBytes) {
				t.Fatal("preserved receipt was changed", err)
			}
			restore()
			reopened := New(manager, s.publicURL)
			defer reopened.Close()
			restored, err := reopened.RoutingStatus()
			if err != nil || len(restored.Plans) != 1 || restored.Plans[0].Status != "unknown" || restored.Plans[0].Changes[0].State != wantState {
				t.Fatal("restart lost the last durable attempt", restored.Plans, err)
			}
		})
	}
}

func TestRoutingRollbackReadFailurePreservesInspection(t *testing.T) {
	s, _, r, d, cf := routingService(t)
	report := readyReport(r)
	report.Records = []DNSRecord{
		{ID: "first", Name: r.Hostname, Type: "CNAME", Value: r.Target, Line: "default", TTL: 600, Status: "ENABLE"},
		{ID: "second", Name: "_acme-challenge." + r.Hostname, Type: "TXT", Value: "certificate-token", Line: "default", TTL: 600, Status: "ENABLE"},
	}
	want := jsonBody(report)
	plan := RoutingPlan{ID: randomID(), RouteID: r.ID, Kind: "route", Status: "applied", Snapshots: map[string][]DNSRecord{report.Records[0].Name: {report.Records[0]}, report.Records[1].Name: {report.Records[1]}}}
	generation := savePlan(t, s, plan)
	task := RoutingTask{ID: randomID(), RouteID: r.ID, Action: "preview_rollback", PlanID: plan.ID, Status: "running"}
	if err := s.with(true, generation, true, func(st *diskState) error {
		st.Routing.Tasks = append(st.Routing.Tasks, task)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.routeInspect = func(_ context.Context, _ Profile, _ DNSProfile, _ Route) (RouteReport, error) { return report, nil }
	fixture := &dnsFixture{records: slices.Clone(report.Records)}
	for i := range fixture.records {
		fixture.records[i].TTL = 1200
	}
	base := fixture.transport(t)
	reads := 0
	s.api.client = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
		reads++
		if reads == 2 {
			return nil, errors.New("second rollback record read failed")
		}
		return base(req)
	})}
	s.processRouting(t.Context(), generation, task, r, d, cf)
	current, err := s.RoutingStatus()
	if err != nil || reads != 2 || fixture.writes != 0 || len(current.Tasks) != 1 || current.Tasks[0].Status != "failed" {
		t.Fatal("rollback read failure was not observed", current.Tasks, reads, fixture.writes, err)
	}
	if !bytes.Equal(jsonBody(current.Reports[r.ID]), want) {
		t.Error("aborted rollback preparation modified the saved inspection report")
	}
	var disk diskState
	if err := json.Unmarshal(snapshotBytes(t, s), &disk); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(jsonBody(disk.Routing.Reports[r.ID]), want) {
		t.Error("partial rollback reads leaked into durable inspection history")
	}
}
