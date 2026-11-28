package content

import "slices"

// These checks run under the file repository's lock, after verifying every
// persisted byte. They do not mutate the published state or authorize new work.
func (r *MemoryRepository) confirmSnippetWithdrawal(id int64, m Mutation, a SnippetAccess) (Snippet, bool, error) {
	if CheckSnippetAccess(a, true) != nil || !a.Manage {
		return Snippet{}, false, ErrSnippetPermission
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	current, _, err := r.snippetLocked(id)
	if err != nil {
		return Snippet{}, false, err
	}
	// Reuse the normal permission/version checks, including the version limit.
	// A new withdrawal still uses the real clock in the detached candidate.
	next, err := WithdrawSnippetRecord(current, m, a, current.UpdatedAt)
	if err != nil {
		return Snippet{}, false, err
	}
	return next.Snippet, current.Visibility == SnippetWithdrawn && r.contentSchema >= 2, nil
}

func (r *MemoryRepository) confirmSnippetUpdate(u SnippetUpdate, a SnippetAccess) (SnippetResult, bool, error) {
	if err := CheckSnippetAccess(a, true); err != nil {
		return SnippetResult{}, false, err
	}
	u, err := NormalizeSnippetUpdate(u, true)
	if err != nil {
		return SnippetResult{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	fingerprint := SnippetUpdateFingerprint(u, a)
	for _, receipt := range r.snippetReceipts {
		if receipt.Identity == a.Identity && receipt.RequestKey == u.RequestKey {
			if receipt.Fingerprint != fingerprint {
				return SnippetResult{}, false, ErrSnippetReplay
			}
			receipt.Items = slices.Clone(receipt.Items)
			return SnippetResult{Receipt: receipt, Replayed: true}, r.contentSchema >= 2, nil
		}
	}
	return SnippetResult{}, false, nil
}
