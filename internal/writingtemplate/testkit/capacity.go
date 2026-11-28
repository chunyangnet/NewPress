// Package testkit constructs bounded writing-template fixtures for persistence,
// recovery and read measurements. Only tests import this package. The additional
// histories are constructed records, not a claim about successful API writes.
package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/writingtemplate"
)

type CapacityFixture struct {
	Raw       []byte
	Create    writingtemplate.Command
	ID        string
	Version   int64
	Templates int
	Revisions int
}

func Capacity(t testing.TB, shape string) CapacityFixture {
	t.Helper()
	count, versions := 1, 1
	switch shape {
	case "small":
	case "revisions":
		count, versions = 100, 20
	case "bytes":
		count, versions = 100, 10
	default:
		t.Fatalf("unknown capacity shape %q", shape)
	}
	ctx := context.Background()
	store := &writingtemplate.MemoryStorage{}
	s, err := writingtemplate.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	d := writingtemplate.Definition{BasePrompt: "article", Title: "容量核对模板", Instruction: "保留材料中的事实与来源。"}
	create := writingtemplate.Command{Action: "save", Definition: &d, Actor: "user:capacity", RequestKey: fmt.Sprintf("%064x", 1)}
	first, err := s.Apply(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Schema    int                        `json:"schema"`
		Kind      string                     `json:"kind"`
		Templates []writingtemplate.Template `json:"templates"`
		Receipts  json.RawMessage            `json:"receipts"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	base := doc.Templates[0].Revisions[0]
	base.At = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	doc.Templates = nil
	for n := 0; n < count; n++ {
		item := writingtemplate.Template{ID: fmt.Sprintf("%032x", n+1)}
		if n == 0 {
			item.ID = first.ID
		}
		for v := 1; v <= versions; v++ {
			next := base
			next.Version = int64(v)
			item.Revisions = append(item.Revisions, next)
		}
		doc.Templates = append(doc.Templates, item)
	}
	marshal := func() []byte {
		t.Helper()
		result, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if shape == "bytes" {
		// Keep the actual create definition and receipt intact. Fill all other
		// revisions with Chinese text, leaving less than 3 KiB of encoded space.
		padding := (writingtemplate.MaxLibraryBytes - len(marshal())) / (3 * (count*versions - 1))
		for n := range doc.Templates {
			for v := range doc.Templates[n].Revisions {
				if n != 0 || v != 0 {
					doc.Templates[n].Revisions[v].Definition.Instruction += strings.Repeat("写", padding)
				}
			}
		}
	}
	raw = marshal()
	if err = writingtemplate.ValidateSnapshot(raw); err != nil {
		t.Fatal(err)
	}
	if shape == "bytes" && (len(raw) > writingtemplate.MaxLibraryBytes || len(raw) < writingtemplate.MaxLibraryBytes-4096) {
		t.Fatalf("byte fixture is not near capacity: %d", len(raw))
	}
	return CapacityFixture{Raw: raw, Create: create, ID: first.ID, Version: int64(versions), Templates: count, Revisions: count * versions}
}
