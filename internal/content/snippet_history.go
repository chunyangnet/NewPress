package content

// SnippetHistoryValidator checks the same complete history invariants as
// ValidateSnippetRecord without retaining earlier revision bodies. Call Add in
// ascending version order and Complete only after the source was fully read.
// Withdrawal versions may leave gaps in the body revision sequence.
type SnippetHistoryValidator struct {
	snippet              Snippet
	count                int
	previous             int64
	lastTitle            string
	sharingBoundaryFound bool
	err                  error
}

func NewSnippetHistoryValidator(s Snippet) (*SnippetHistoryValidator, error) {
	if err := ValidateSnippetMetadata(s); err != nil {
		return nil, err
	}
	return &SnippetHistoryValidator{snippet: s, sharingBoundaryFound: s.Visibility != SnippetShared}, nil
}

func (v *SnippetHistoryValidator) Add(revision SnippetRevision) error {
	if v.err != nil {
		return v.err
	}
	if v.count >= MaxSnippetRevisions || revision.Version <= v.previous || ValidateSnippetRevision(v.snippet, revision) != nil {
		v.err = ErrSnippetCorrupt
		return v.err
	}
	v.count++
	v.previous, v.lastTitle = revision.Version, revision.Title
	v.sharingBoundaryFound = v.sharingBoundaryFound || revision.Version == v.snippet.SharedSince
	return nil
}

func (v *SnippetHistoryValidator) Complete() error {
	if v.err != nil {
		return v.err
	}
	if v.count == 0 || v.previous != v.snippet.CurrentVersion || v.lastTitle != v.snippet.Title || !v.sharingBoundaryFound {
		return ErrSnippetCorrupt
	}
	return nil
}
