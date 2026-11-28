package aiworkbench

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestPreviewSourceCheckIsServerOnly(t *testing.T) {
	denied := errors.New("source changed")
	p := Preview{Task: "summary", Context: "confirmed snapshot"}
	bound := p.WithSourceCheck(func(context.Context) error { return denied })
	if err := bound.CheckSource(t.Context()); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if p.CheckSource(t.Context()) != nil || bound.WithSourceCheck(nil).CheckSource(t.Context()) != nil {
		t.Fatal("source check mutation leaked between values")
	}
	var previews Previews
	now := time.Now()
	stored, err := previews.Put(1, bound, now)
	if err != nil {
		t.Fatal(err)
	}
	taken, err := previews.Take(1, stored.Token, now)
	if err != nil || !errors.Is(taken.CheckSource(t.Context()), denied) {
		t.Fatal("preview lost its source check", err)
	}
	plainJSON, _ := json.Marshal(p)
	boundJSON, err := json.Marshal(bound)
	if err != nil || string(plainJSON) != string(boundJSON) {
		t.Fatal("source metadata leaked to client", err)
	}
	report := testReport(1, now)
	report.Source = &bound
	store, _ := NewStore("")
	if err := store.Save(report, now); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Get(1, report.ID, now)
	if err != nil || saved.Source.CheckSource(t.Context()) != nil || store.reports[0].Source.source != nil {
		t.Fatal("report retained live source dependency", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(p.CheckSource(ctx), context.Canceled) {
		t.Fatal("cancelled context ignored")
	}
}
