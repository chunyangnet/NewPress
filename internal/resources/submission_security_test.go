package resources

import "testing"

func TestResourceDecodeRejectsAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{
		`{"items":[],"items":[]}`,
		`{"items":[],"Items":[]}`,
		`{"\u0069tems":[],"items":[]}`,
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("ambiguous resource configuration accepted: %s", raw)
		}
	}
}

func TestEmptySubmissionCannotAllocateGuestRecords(t *testing.T) {
	b, err := NewFileBackend("")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{Backend: b}
	r := example().Items[0]
	v := r.Variants[0]
	for _, input := range []struct{ fields, answers map[string]string }{
		{}, {fields: map[string]string{}}, {answers: map[string]string{}},
	} {
		if _, err := s.Submit(t.Context(), "g:attacker", 1, r, v, input.fields, input.answers, "2026-11-27"); err != ErrInvalid {
			t.Fatalf("non-actionable submission accepted: %v", err)
		}
	}
	state, err := b.Read(t.Context())
	if err != nil || len(state.Submissions) != 0 {
		t.Fatalf("empty writes persisted: %d, %v", len(state.Submissions), err)
	}
}
