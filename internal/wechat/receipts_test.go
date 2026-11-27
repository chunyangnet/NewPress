package wechat

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func receiptClient(body string) (*Client, *int) {
	c, calls := NewClient(), new(int)
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/cgi-bin/stable_token" {
			return reply(r, `{"access_token":"receipt-token","expires_in":7200}`), nil
		}
		*calls++
		return reply(r, body), nil
	})}
	return c, calls
}

func TestPublicationReceiptIdentifiers(t *testing.T) {
	for _, value := range []struct{ raw, want string }{
		{`"opaque-publish-id"`, "opaque-publish-id"},
		{`"900719925474099312345"`, "900719925474099312345"},
		{`900719925474099312345`, "900719925474099312345"},
	} {
		c, calls := receiptClient(`{"errcode":0,"publish_id":` + value.raw + `,"extra":{"future":true}}`)
		got, err := c.Publish(t.Context(), Defaults(), "draft")
		if err != nil || got != value.want || *calls != 1 {
			t.Fatal("valid identifier was changed", got, err, *calls)
		}
	}
	for name, raw := range map[string]string{
		"null": `null`, "boolean": `true`, "object": `{}`, "array": `[]`,
		"negative": `-12`, "fraction": `12.5`, "exponent": `12e3`,
		"empty": `""`, "spaces": `"  "`, "tab": `"remote\tid"`,
		"unicode space": `"remote\u00a0id"`, "surrogate": `"\ud800"`,
		"oversized": `"` + strings.Repeat("x", 129) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			c, calls := receiptClient(`{"publish_id":` + raw + `}`)
			got, err := c.Publish(t.Context(), Defaults(), "draft")
			var api *APIError
			if got != "" || !errors.As(err, &api) || !api.Uncertain || *calls != 1 {
				t.Fatalf("ambiguous write must require reconciliation: id=%q err=%v calls=%d", got, err, *calls)
			}
		})
	}
}

func TestWeChatAmbiguousResponseEnvelope(t *testing.T) {
	for name, body := range map[string]string{
		"null": `null`, "array": `[]`, "scalar": `"success"`,
		"null error":         `{"errcode":null,"media_id":"draft"}`,
		"string error":       `{"errcode":"0","media_id":"draft"}`,
		"duplicate error":    `{"errcode":40014,"errcode":0,"media_id":"draft"}`,
		"duplicate identity": `{"media_id":"first","media_id":"second"}`,
		"case alias":         `{"media_id":"first","MEDIA_ID":"second"}`,
		"case error":         `{"ERRCODE":40014,"media_id":"draft"}`,
		"nested duplicate":   `{"media_id":"draft","extra":{"x":1,"x":2}}`,
		"invalid UTF8":       "{\"media_id\":\"draft\xff\"}",
	} {
		t.Run(name, func(t *testing.T) {
			c, calls := receiptClient(body)
			got, err := c.Draft(t.Context(), Defaults(), Article{})
			var api *APIError
			if got != "" || !errors.As(err, &api) || !api.Uncertain || *calls != 1 {
				t.Fatalf("ambiguous envelope: id=%q err=%v calls=%d", got, err, *calls)
			}
		})
	}
	c, calls := receiptClient(`{"errcode":48001,"errmsg":"upstream credential must not be displayed"}`)
	_, err := c.Draft(t.Context(), Defaults(), Article{})
	var api *APIError
	if !errors.As(err, &api) || api.Uncertain || api.Code != 48001 || strings.Contains(err.Error(), "credential") || *calls != 1 {
		t.Fatal("explicit rejection must stay retryable and redacted", err, *calls)
	}
}

func TestDraftAndCoverReceiptIdentifiers(t *testing.T) {
	for _, id := range []string{"", " ", "draft\nid", "draft\tid", "draft\u00a0id", "\uFFFD", strings.Repeat("x", 129)} {
		raw, _ := json.Marshal(id)
		c, _ := receiptClient(`{"media_id":` + string(raw) + `}`)
		got, err := c.Draft(t.Context(), Defaults(), Article{})
		var api *APIError
		if got != "" || !errors.As(err, &api) || !api.Uncertain {
			t.Errorf("invalid draft ID %q accepted: %q, %v", id, got, err)
		}
		c, _ = receiptClient(`{"media_id":` + string(raw) + `}`)
		if got, err = c.Upload(t.Context(), Defaults(), []byte("fixture"), true); err == nil || got != "" {
			t.Errorf("invalid cover ID %q accepted: %q, %v", id, got, err)
		}
	}
}

func TestPublicationPollReceiptBinding(t *testing.T) {
	for name, body := range map[string]string{
		"missing identity":   `{"publish_status":0,"article_id":"article"}`,
		"null identity":      `{"publish_id":null,"publish_status":0,"article_id":"article"}`,
		"different identity": `{"publish_id":"other-task","publish_status":0,"article_id":"article"}`,
		"object identity":    `{"publish_id":{},"publish_status":0,"article_id":"article"}`,
		"unknown state":      `{"publish_id":"task","publish_status":7,"article_id":"article"}`,
		"negative state":     `{"publish_id":"task","publish_status":-1}`,
		"missing state":      `{"publish_id":"task"}`,
		"null state":         `{"publish_id":"task","publish_status":null}`,
		"missing article":    `{"publish_id":"task","publish_status":0}`,
		"blank article":      `{"publish_id":"task","publish_status":0,"article_id":" "}`,
		"control article":    `{"publish_id":"task","publish_status":0,"article_id":"article\n"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, calls := receiptClient(body)
			if _, err := c.Poll(t.Context(), Defaults(), "task"); err == nil || *calls != 1 {
				t.Fatalf("unverified poll result accepted: %v, calls=%d", err, *calls)
			}
		})
	}
	for status := 0; status <= 6; status++ {
		body, _ := json.Marshal(map[string]any{"publish_id": "900719925474099312345", "publish_status": status, "article_id": "article"})
		c, _ := receiptClient(string(body))
		got, err := c.Poll(t.Context(), Defaults(), "900719925474099312345")
		if err != nil || got.Status == nil || *got.Status != status {
			t.Fatal("documented status rejected", status, got, err)
		}
	}
	c, _ := receiptClient(`{"publish_id":900719925474099312345,"publish_status":1}`)
	if _, err := c.Poll(t.Context(), Defaults(), "900719925474099312345"); err != nil {
		t.Fatal("numeric compatibility lost precision", err)
	}
}

func TestAmbiguousReceiptRecoveryPersistsWithoutResending(t *testing.T) {
	for _, file := range []bool{false, true} {
		for _, stage := range []string{"draft", "publish"} {
			name := stage + "/memory"
			if file {
				name = stage + "/file"
			}
			t.Run(name, func(t *testing.T) {
				s, post := fixture(t)
				state, _ := s.Store.View(t.Context())
				state.Config.AutoPublish = true
				storePath := ""
				if file {
					storePath = filepath.Join(t.TempDir(), "wechat.json")
				}
				store, err := NewFileStore(storePath)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.Update(t.Context(), func(v *State) error { *v = state; return nil }); err != nil {
					t.Fatal(err)
				}
				s.Store = store
				calls := map[string]int{}
				pollBody := `{"publish_id":"wrong-task","publish_status":0,"article_id":"unrelated"}`
				s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					calls[r.URL.Path]++
					switch r.URL.Path {
					case "/cgi-bin/stable_token":
						return reply(r, `{"access_token":"token","expires_in":7200}`), nil
					case "/cgi-bin/draft/add":
						if stage == "draft" {
							return reply(r, `{"media_id":" \t"}`), nil
						}
						return reply(r, `{"media_id":"draft"}`), nil
					case "/cgi-bin/freepublish/submit":
						return reply(r, `{"publish_id":null}`), nil
					case "/cgi-bin/freepublish/get":
						var request map[string]string
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["publish_id"] != "verified-task" {
							t.Fatal("wrong query identity", request, err)
						}
						return reply(r, pollBody), nil
					default:
						return nil, errors.New("unexpected route")
					}
				})}
				if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
					t.Fatal(err)
				}
				if err = s.Tick(t.Context(), time.Now()); err != nil {
					t.Fatal(err)
				}
				job := stateJob(t, s)
				if job.Status != "uncertain" || job.Stage != stage || job.PublishID != "" {
					t.Fatal("ambiguous response adopted", job)
				}
				if file {
					s.Store, err = NewFileStore(storePath)
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, action := range []string{"retry", "resume", "publish", "draft_only", "cancel"} {
					if s.Action(t.Context(), job.ID, action, job.UpdatedAt) == nil {
						t.Fatal("uncertain task repeated", action)
					}
				}
				if err = s.Tick(t.Context(), time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if calls["/cgi-bin/draft/add"] != 1 || calls["/cgi-bin/freepublish/submit"] != map[string]int{"draft": 0, "publish": 1}[stage] {
					t.Fatal("write repeated", calls)
				}
				if stage == "draft" {
					if err = s.Resolve(t.Context(), job.ID, job.UpdatedAt, "verified-draft", ""); err != nil {
						t.Fatal(err)
					}
					if job = stateJob(t, s); job.Status != "draft" || job.MediaID != "verified-draft" {
						t.Fatal(job)
					}
					if err = s.Tick(t.Context(), time.Now().Add(time.Hour)); err != nil || calls["/cgi-bin/freepublish/submit"] != 0 {
						t.Fatal("resolved draft automatically published", err, calls)
					}
					return
				}
				if err = s.Resolve(t.Context(), job.ID, job.UpdatedAt, "", "verified-task"); err != nil {
					t.Fatal(err)
				}
				if err = s.Tick(t.Context(), time.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				job = stateJob(t, s)
				if job.Status != "publishing" || job.PublishID != "verified-task" || job.ArticleID != "" || job.Error == "" {
					t.Fatal("unrelated result adopted", job)
				}
				pollBody = `{"publish_id":"verified-task","publish_status":0,"article_id":"verified-article"}`
				if file {
					s.Store, err = NewFileStore(storePath)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err = s.Tick(t.Context(), time.Now().Add(2*time.Hour)); err != nil {
					t.Fatal(err)
				}
				job = stateJob(t, s)
				if job.Status != "published" || job.ArticleID != "verified-article" || job.Error != "" || calls["/cgi-bin/draft/add"] != 1 || calls["/cgi-bin/freepublish/submit"] != 1 {
					t.Fatal("reconciliation lost identity", job, calls)
				}
			})
		}
	}
}

func TestManualReconciliationRejectsInvalidIdentifiers(t *testing.T) {
	s, post := fixture(t)
	if _, err := s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Update(t.Context(), func(v *State) error { v.Jobs[0].Status = "uncertain"; v.Jobs[0].Stage = "publish"; return nil }); err != nil {
		t.Fatal(err)
	}
	job := stateJob(t, s)
	for _, id := range []string{"task\tID", "task\u00a0ID", "task\x1bID", "\uFFFD"} {
		if err := s.Resolve(t.Context(), job.ID, job.UpdatedAt, "", id); err == nil {
			t.Fatalf("invalid manual ID %q accepted", id)
		}
		if actual := stateJob(t, s); actual.Status != "uncertain" || actual.PublishID != "" || !actual.UpdatedAt.Equal(job.UpdatedAt) {
			t.Fatal("invalid reconciliation changed the task", actual)
		}
	}
}

func TestValidPublicationProgressClearsTransientFailure(t *testing.T) {
	s, post := fixture(t)
	if _, err := s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Update(t.Context(), func(state *State) error {
		job := &state.Jobs[0]
		job.Status, job.Stage, job.PublishID = "publishing", "poll", "original-task"
		job.Error = "previous response could not be verified"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Client, _ = receiptClient(`{"publish_id":"original-task","publish_status":1}`)
	if err := s.Tick(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	job := stateJob(t, s)
	if job.Status != "publishing" || job.PublishID != "original-task" || job.Error != "" {
		t.Fatal("confirmed progress retained obsolete failure", job)
	}
}
