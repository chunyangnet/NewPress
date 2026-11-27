package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func example() Config {
	return Config{Items: []Resource{
		{ID: "bundle", Title: "资源", DailyLimit: 3, Variants: []Variant{
			{ID: "full", Name: "完整内容", Kind: "content", Body: `<p>私密内容</p><script>alert(1)</script>`, Groups: []Group{
				{Name: "付费并关注", Conditions: []Condition{{ID: "pay", Kind: "payment", Currency: "CNY", Amount: 990}, {ID: "follow", Kind: "follow"}}},
				{Name: "会员", Conditions: []Condition{{ID: "member", Kind: "membership"}}},
			}},
		}},
	}}
}

func TestAccessGroupsRequireEveryConditionAndAllowAlternatives(t *testing.T) {
	v := example().Items[0].Variants[0]
	for _, tc := range []struct{ pay, follow, member, want bool }{{false, false, false, false}, {true, false, false, false}, {false, true, false, false}, {true, true, false, true}, {false, false, true, true}} {
		ok, _ := Evaluate(v, func(c Condition) bool {
			switch c.Kind {
			case "payment":
				return tc.pay
			case "follow":
				return tc.follow
			case "membership":
				return tc.member
			}
			return false
		})
		if ok != tc.want {
			t.Fatal(tc, ok)
		}
	}
}
func TestConfigurationRejectsAmbiguityAndUnsafeDelivery(t *testing.T) {
	c, e := Normalize(example())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(c.Items[0].Variants[0].Body, "script") {
		t.Fatal("unsafe body")
	}
	for _, bad := range []string{`{"items":[],"unknown":1}`, `{"items":[]} {}`, `null`, `{"items":[{"id":"x"}]}`} {
		if _, e := Decode([]byte(bad)); e == nil {
			t.Fatal("accepted", bad)
		}
	}
	for _, url := range []string{"javascript:alert(1)", "//evil.test/x", "/\\evil.test/x", "https://name:password@example.test/x", "data:text/html,x"} {
		if SafeURL(url) {
			t.Fatal(url)
		}
	}
	for _, mutate := range []func(*Variant){func(v *Variant) { v.Groups = nil }, func(v *Variant) { v.Groups[0].Conditions = nil }, func(v *Variant) { v.Groups[0].Conditions[0].Kind = "unknown" }, func(v *Variant) { v.Groups[0].Conditions[1].ID = "pay" }, func(v *Variant) { v.Kind = "video"; v.MediaURL = "javascript:bad" }} {
		bad := example()
		mutate(&bad.Items[0].Variants[0])
		if _, e := Normalize(bad); e == nil {
			t.Fatal("unsafe policy accepted")
		}
	}
	raw, _ := json.Marshal(c)
	if _, e := Decode(raw); e != nil {
		t.Fatal(e)
	}
}
func TestDownloadQuotaAtomicRetryAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	backend, e := NewFileBackend(path)
	if e != nil {
		t.Fatal(e)
	}
	store := &Store{Backend: backend}
	r := example().Items[0]
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := store.Consume(context.Background(), Delivery{Actor: "u:1", PostID: 1, Resource: r.ID, Variant: "full", Link: "file", Request: fmt.Sprintf("request-%d", i), Day: "2026-11-21", CreatedAt: time.Now()}, r)
			if e == nil {
				success.Add(1)
			} else if e != ErrLimit {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 3 {
		t.Fatal(success.Load())
	}
	st, _ := backend.Read(t.Context())
	replay, e := store.Consume(t.Context(), st.Deliveries[0], r)
	if e != nil || !replay {
		t.Fatal(replay, e)
	}
	bad := st.Deliveries[0]
	bad.Link = "other"
	if _, e := store.Consume(t.Context(), bad, r); e != ErrInvalid {
		t.Fatal(e)
	}
	reopened, e := NewFileBackend(path)
	if e != nil {
		t.Fatal(e)
	}
	st, _ = reopened.Read(t.Context())
	if len(st.Deliveries) != 3 {
		t.Fatal(len(st.Deliveries))
	}
	next := st.Deliveries[0]
	next.Day = "2026-11-22"
	next.Request = "next-day"
	if _, e := (&Store{Backend: reopened}).Consume(t.Context(), next, r); e != nil {
		t.Fatal(e)
	}
}
func TestQuizFormValidationLimitsAndChangedQuestions(t *testing.T) {
	backend, _ := NewFileBackend("")
	store := &Store{Backend: backend}
	r := example().Items[0]
	v := r.Variants[0]
	v.Questions = []Question{{ID: "q", Title: "2 + 2", Answer: "4"}}
	v.PassPercent = 100
	v.AttemptsPerDay = 2
	v.Fields = []Field{{ID: "email", Label: "邮箱", Type: "email", Required: true}}
	if _, e := store.Submit(t.Context(), "u:1", 1, r, v, map[string]string{"email": "bad"}, nil, "2026-11-21"); e != ErrInvalid {
		t.Fatal(e)
	}
	for range 2 {
		result, e := store.Submit(t.Context(), "u:1", 1, r, v, nil, map[string]string{"q": "5"}, "2026-11-21")
		if e != nil || result.Passed {
			t.Fatal(result, e)
		}
	}
	if _, e := store.Submit(t.Context(), "u:1", 1, r, v, nil, map[string]string{"q": "4"}, "2026-11-21"); e != ErrLimit {
		t.Fatal(e)
	}
	result, e := store.Submit(t.Context(), "u:1", 1, r, v, map[string]string{"email": "reader@example.test"}, map[string]string{"q": "4"}, "2026-11-22")
	if e != nil || !result.Passed || !result.FormComplete {
		t.Fatal(result, e)
	}
	key := SubmissionKey("u:1", 1, r, v)
	v.Questions[0].Answer = "5"
	if SubmissionKey("u:1", 1, r, v) == key {
		t.Fatal("changed answer reused old grant")
	}
}
