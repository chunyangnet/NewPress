package plugindispatch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/pluginjobs"
)

func testIntent(t *testing.T) Intent {
	t.Helper()
	r := Request{Authorization: plugineffects.Authorization{Claim: pluginjobs.Job{ID: strings.Repeat("a", 64)}, ManifestDigest: strings.Repeat("b", 64), ModuleDigest: strings.Repeat("c", 64)}, Slot: "notify", URL: "https://example.com/webhook?token=private", Body: json.RawMessage(`{"id":9007199254740993,"message":"<keep>"}`)}
	id, fp, body, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	i := Intent{Schema: 1, ID: id, JobID: r.Authorization.Claim.ID, SourceFingerprint: strings.Repeat("d", 64), Plugin: "test-webhook", Kind: "task.execute.v1", Epoch: strings.Repeat("e", 64), ActorID: 1, ManifestDigest: r.Authorization.ManifestDigest, ModuleDigest: r.Authorization.ModuleDigest, Slot: r.Slot, Fingerprint: fp, URL: r.URL, Body: body, Version: 1, State: "pending", CreatedAt: at, UpdatedAt: at}
	if err = Validate(i); err != nil {
		t.Fatal(err)
	}
	return i
}

func sendingIntent(t *testing.T) Intent {
	i := testIntent(t)
	i.State = "sending"
	i.Version = 2
	i.AttemptAt = i.CreatedAt
	i.AttemptUntil = i.AttemptAt.Add(AttemptDuration)
	i.AttemptToken = strings.Repeat("f", 64)
	return i
}

func TestWebhookIdentityBodyAndPermanentBudget(t *testing.T) {
	i := testIntent(t)
	if !strings.Contains(string(i.Body), "9007199254740993") || !strings.Contains(string(i.Body), `\u003c`) {
		t.Fatal("body precision or canonical escaping lost")
	}
	if TargetOrigin(i.URL) != "https://example.com" {
		t.Fatal("target leaked request path/query")
	}
	for _, body := range []string{`null`, `[]`, `{"x":1,"x":2}`, `{"x":1} {}`, `{"x":NaN}`, `{"x":"` + strings.Repeat("a", MaxBodyBytes) + `"}`} {
		if _, err := NormalizeBody([]byte(body)); err == nil {
			t.Fatal("accepted invalid body")
		}
	}
	for _, url := range []string{"http://example.com", "https://127.0.0.1", "https://user:pass@example.com", "https://example.com:444", "https://example.com/#fragment", "https://example.com./"} {
		if ValidURL(url) {
			t.Fatal("URL accepted", url)
		}
	}
	i = sendingIntent(t)
	i.State = "uncertain"
	i.ResultAt = i.UpdatedAt
	i.Version = 3
	i.Outcome = Outcome{Code: "transport_unconfirmed"}
	i.Review = &Review{Decision: "not_received", Reason: strings.Repeat("确", 500), ActorID: 9223372036854775807, At: i.UpdatedAt}
	i.State = "reviewed_not_received"
	i.Version = 4
	if err := Validate(i); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(i)
	if len(raw) > BudgetBytes(i) {
		t.Fatal("later review exhausted admission reservation")
	}
	i.Version = 2
	if Validate(i) == nil {
		t.Fatal("review accepted with wrong version")
	}
}

func TestWebhookStateRejectsContradictoryFacts(t *testing.T) {
	for _, change := range []func(*Intent){
		func(i *Intent) { i.State = "http_confirmed"; i.Version = 3 },
		func(i *Intent) { i.AttemptUntil = i.AttemptUntil.Add(time.Second) },
		func(i *Intent) { i.AttemptToken = "" },
		func(i *Intent) { i.Body = json.RawMessage(`{"different":true}`) },
		func(i *Intent) {
			i.Outcome = Outcome{Code: "http_2xx", HTTPStatus: 500, ResponseDigest: strings.Repeat("a", 64)}
		},
		func(i *Intent) { i.State = "pending"; i.Version = 1 },
	} {
		i := sendingIntent(t)
		change(&i)
		if Validate(i) == nil {
			t.Fatal("contradiction accepted", i.State)
		}
	}
}
