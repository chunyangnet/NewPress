package content

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSnippetHistoryValidationRetainsAllRecordRules(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	base := SnippetRecord{Snippet: Snippet{ID: 1, Version: 7, CurrentVersion: 7, SharedSince: 3, Visibility: SnippetShared, Title: "current", CreatedAt: now, UpdatedAt: now.Add(time.Hour)}}
	for _, version := range []int64{1, 3, 7} {
		title := "old"
		if version == 7 {
			title = "current"
		}
		base.Revisions = append(base.Revisions, SnippetRevision{Version: version, Title: title, ContentSource: "<p>body</p>", ContentHTML: "<p>body</p>", Actor: "editor", CreatedAt: now.Add(time.Duration(version) * time.Minute)})
	}
	cases := []struct {
		name   string
		change func(*SnippetRecord)
		valid  bool
	}{
		{"withdrawal gaps", func(*SnippetRecord) {}, true},
		{"withdrawn metadata newer than body", func(s *SnippetRecord) { s.Version = 8; s.Visibility = SnippetWithdrawn; s.SharedSince = 0 }, true},
		{"empty history", func(s *SnippetRecord) { s.Revisions = nil }, false},
		{"missing current", func(s *SnippetRecord) { s.Revisions = s.Revisions[:2] }, false},
		{"missing sharing boundary", func(s *SnippetRecord) { s.SharedSince = 2 }, false},
		{"old source is not normalized", func(s *SnippetRecord) { s.Revisions[0].ContentSource = " <p>body</p>" }, false},
		{"old rendered body changed", func(s *SnippetRecord) { s.Revisions[0].ContentHTML = "<p>tampered</p>" }, false},
		{"old dynamic markup", func(s *SnippetRecord) {
			s.Revisions[0].ContentSource = `<div data-np-snippet="1" data-np-version="1"></div>`
		}, false},
		{"old actor invalid UTF-8", func(s *SnippetRecord) { s.Revisions[0].Actor = "editor\xff" }, false},
		{"old timestamp invalid", func(s *SnippetRecord) { s.Revisions[0].CreatedAt = now.Add(-time.Second) }, false},
		{"duplicate version", func(s *SnippetRecord) { s.Revisions[1].Version = 1 }, false},
		{"out of order", func(s *SnippetRecord) { s.Revisions[0], s.Revisions[1] = s.Revisions[1], s.Revisions[0] }, false},
		{"latest title mismatch", func(s *SnippetRecord) { s.Revisions[2].Title = "other" }, false},
		{"metadata invalid", func(s *SnippetRecord) { s.Visibility = "unknown" }, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s := base
			s.Revisions = slices.Clone(base.Revisions)
			test.change(&s)
			validator, streamErr := NewSnippetHistoryValidator(s.Snippet)
			if streamErr == nil {
				for _, rev := range s.Revisions {
					if err := validator.Add(rev); err != nil {
						streamErr = err
					}
				}
				endErr := validator.Complete()
				if streamErr != nil && endErr == nil {
					t.Fatal("history error was forgotten")
				}
				streamErr = endErr
			}
			for _, err := range []error{streamErr, ValidateSnippetRecord(s)} {
				if test.valid && err != nil || !test.valid && !errors.Is(err, ErrSnippetCorrupt) {
					t.Fatalf("valid=%v error=%v", test.valid, err)
				}
			}
		})
	}
}

func TestSnippetHistoryCountLimitAndStickyFailure(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	s := Snippet{ID: 1, Version: MaxSnippetRevisions, CurrentVersion: MaxSnippetRevisions, Visibility: SnippetPrivate, Title: "body", CreatedAt: now, UpdatedAt: now}
	v, err := NewSnippetHistoryValidator(s)
	if err != nil {
		t.Fatal(err)
	}
	rev := SnippetRevision{Title: s.Title, ContentSource: "<p>x</p>", ContentHTML: "<p>x</p>", Actor: "editor", CreatedAt: now}
	for i := int64(1); i <= MaxSnippetRevisions; i++ {
		rev.Version = i
		if err := v.Add(rev); err != nil {
			t.Fatal(i, err)
		}
	}
	if err = v.Complete(); err != nil {
		t.Fatal(err)
	}
	rev.Version++
	if !errors.Is(v.Add(rev), ErrSnippetCorrupt) || !errors.Is(v.Complete(), ErrSnippetCorrupt) {
		t.Fatal("oversized history accepted")
	}
	// Even if a caller continues consuming the source, a failed row is fatal.
	v, _ = NewSnippetHistoryValidator(s)
	rev.Version, rev.ContentHTML = 1, strings.Repeat("x", 20)
	if !errors.Is(v.Add(rev), ErrSnippetCorrupt) {
		t.Fatal("invalid first body accepted")
	}
	rev.Version, rev.ContentHTML = s.CurrentVersion, "<p>x</p>"
	if !errors.Is(v.Add(rev), ErrSnippetCorrupt) || !errors.Is(v.Complete(), ErrSnippetCorrupt) {
		t.Fatal("later body cleared earlier corruption")
	}
}
