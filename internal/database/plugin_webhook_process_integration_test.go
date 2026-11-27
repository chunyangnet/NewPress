package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/plugineffects"
)

type webhookChildReply struct {
	PID     int    `json:"pid"`
	Schema  string `json:"schema"`
	ID      string `json:"id"`
	Found   bool   `json:"found"`
	State   string `json:"state"`
	Failure string `json:"failure,omitempty"`
}

// Only this test child has a loopback adapter. Production PublicSender keeps
// its public-HTTPS policy; transport failure semantics are tested in that package.
type testWebhookLoopbackSender struct{ target string }

func (s testWebhookLoopbackSender) Send(ctx context.Context, i plugindispatch.Intent) plugindispatch.Outcome {
	r, err := http.NewRequestWithContext(ctx, "POST", s.target, io.NopCloser(bytes.NewReader(i.Body)))
	if err != nil {
		return plugindispatch.Outcome{Code: "transport_unconfirmed"}
	}
	r.Header.Set("X-NewPress-Operation", i.ID)
	tr := &http.Transport{DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Do(r)
	if err != nil {
		return plugindispatch.Outcome{Code: "transport_unconfirmed"}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, plugindispatch.MaxResponseBytes+1))
	if err != nil || len(body) > plugindispatch.MaxResponseBytes {
		return plugindispatch.Outcome{Code: "response_unconfirmed", HTTPStatus: res.StatusCode}
	}
	code := "http_status"
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		code = "http_2xx"
	}
	return plugindispatch.Outcome{Code: code, HTTPStatus: res.StatusCode, ResponseDigest: plugineffects.Digest(body), ResponseBytes: len(body)}
}

func TestPluginWebhookProcessChild(t *testing.T) {
	if os.Getenv("NEWPRESS_WEBHOOK_CHILD") != "1" {
		t.Skip("opt-in subprocess entry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid temporary database config")
	}
	schema := os.Getenv("NEWPRESS_WEBHOOK_SCHEMA")
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("temporary database unavailable")
	}
	defer pool.Close()
	store := NewPluginWebhookRepository(pool, true)
	reply := webhookChildReply{PID: os.Getpid(), Schema: schema}
	operation := os.Getenv("NEWPRESS_WEBHOOK_OPERATION")
	if operation == "enqueue" {
		raw, err := os.ReadFile(os.Getenv("NEWPRESS_WEBHOOK_INPUT"))
		if err != nil {
			t.Fatal(err)
		}
		var r plugindispatch.Request
		if err = json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		a, err := store.Enqueue(ctx, r)
		if err != nil {
			reply.Failure = err.Error()
		}
		reply.ID = a.OperationID
	} else {
		i, found, err := store.Claim(ctx)
		reply.Found = found
		reply.ID = i.ID
		if err != nil {
			reply.Failure = err.Error()
		}
		if found && err == nil {
			if operation == "claim-crash" {
				raw, _ := json.Marshal(reply)
				fmt.Println("WEBHOOK_CHILD " + string(raw))
				os.Exit(23)
			}
			out := (testWebhookLoopbackSender{os.Getenv("NEWPRESS_WEBHOOK_TARGET")}).Send(ctx, i)
			if operation == "send-crash" {
				raw, _ := json.Marshal(reply)
				fmt.Println("WEBHOOK_CHILD " + string(raw))
				os.Exit(24)
			}
			if err = store.Finish(ctx, i, out); err != nil {
				reply.Failure = err.Error()
			}
			reply.State = out.Code
		}
	}
	raw, _ := json.Marshal(reply)
	fmt.Println("WEBHOOK_CHILD " + string(raw))
}

func (f *webhookFixture) child(t *testing.T, operation, target string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	input := filepath.Join(t.TempDir(), "private-request.json")
	raw, _ := json.Marshal(f.req)
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(f.ctx, os.Args[0], "-test.run=^TestPluginWebhookProcessChild$", "-test.v")
	cmd.Env = append(os.Environ(), "NEWPRESS_WEBHOOK_CHILD=1", "NEWPRESS_WEBHOOK_SCHEMA="+f.pool.Config().ConnConfig.RuntimeParams["search_path"], "NEWPRESS_WEBHOOK_OPERATION="+operation, "NEWPRESS_WEBHOOK_INPUT="+input, "NEWPRESS_WEBHOOK_TARGET="+target)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &out
}

func waitWebhookChild(t *testing.T, cmd *exec.Cmd, out *bytes.Buffer, expected int) webhookChildReply {
	t.Helper()
	err := cmd.Wait()
	if cmd.ProcessState.ExitCode() != expected {
		t.Fatalf("child exit %v: %s", err, out.String())
	}
	var reply webhookChildReply
	for _, line := range strings.Split(out.String(), "\n") {
		if raw, ok := strings.CutPrefix(line, "WEBHOOK_CHILD "); ok {
			if err := json.Unmarshal([]byte(raw), &reply); err != nil {
				t.Fatal(err)
			}
		}
	}
	if reply.PID == 0 || reply.PID == os.Getpid() || reply.Failure != "" {
		t.Fatal("child result", reply)
	}
	t.Logf("independent child pid=%d schema=%s operation=%s found=%v state=%s exit=%d", reply.PID, reply.Schema, reply.ID, reply.Found, reply.State, expected)
	return reply
}

func TestPostgreSQLWebhookIndependentProcessesAndRemoteCount(t *testing.T) {
	f := newWebhookFixture(t)
	a, oa := f.child(t, "enqueue", "")
	b, ob := f.child(t, "enqueue", "")
	ra := waitWebhookChild(t, a, oa, 0)
	rb := waitWebhookChild(t, b, ob, 0)
	if ra.PID == rb.PID || ra.ID != rb.ID || ra.Schema != rb.Schema {
		t.Fatal("processes did not share one stable operation")
	}
	f.success(t)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("X-NewPress-Operation") != ra.ID {
			t.Error("operation identity lost")
		}
		w.WriteHeader(202)
	}))
	defer remote.Close()
	a, oa = f.child(t, "send", remote.URL)
	b, ob = f.child(t, "send", remote.URL)
	ra = waitWebhookChild(t, a, oa, 0)
	rb = waitWebhookChild(t, b, ob, 0)
	if ra.PID == rb.PID || ra.Found == rb.Found || calls.Load() != 1 || f.items(t)[0].State != "http_confirmed" {
		t.Fatal("duplicate process dispatch", ra, rb, calls.Load())
	}
	f.assertNoClaim(t)
	t.Log("simulated remote accepted exactly 1 HTTP request; later dispatch made 0 requests")
}

func TestPostgreSQLWebhookLostResponseAndCrashNeverResend(t *testing.T) {
	f := newWebhookFixture(t)
	for _, slot := range []string{"first", "second", "third"} {
		f.accept(t, slot)
	}
	f.success(t)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/lost" {
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = c.Close()
			return
		}
		w.WriteHeader(204)
	}))
	defer remote.Close()
	a, oa := f.child(t, "send", remote.URL+"/lost")
	lost := waitWebhookChild(t, a, oa, 0)
	if lost.State != "transport_unconfirmed" {
		t.Fatal(lost)
	}
	a, oa = f.child(t, "send-crash", remote.URL)
	waitWebhookChild(t, a, oa, 24)
	a, oa = f.child(t, "claim-crash", remote.URL)
	waitWebhookChild(t, a, oa, 23)
	if calls.Load() != 2 {
		t.Fatal("remote before recovery", calls.Load())
	}
	// Let actual DB-issued attempt deadlines expire, without editing timestamps.
	select {
	case <-time.After(plugindispatch.AttemptDuration + 200*time.Millisecond):
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	f.assertNoClaim(t)
	for _, i := range f.items(t) {
		if i.State != "uncertain" {
			t.Fatal("lost send replayable", i.State)
		}
	}
	a, oa = f.child(t, "send", remote.URL)
	retry := waitWebhookChild(t, a, oa, 0)
	if retry.Found || calls.Load() != 2 {
		t.Fatal("restarted worker resent", retry, calls.Load())
	}
	t.Log("remote count 2: one lost response, one response received before exit; pre-send crash adds 0; restart adds 0")
}
