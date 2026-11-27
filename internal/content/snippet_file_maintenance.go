package content

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

func snippetFilePosts(s snippetFileSnapshot) map[int64]Post {
	posts := make(map[int64]Post, len(s.doc.Posts))
	for _, p := range s.doc.Posts {
		posts[p.ID] = p
	}
	return posts
}

func validateSnippetFileLinks(records []SnippetRecord, receipts []SnippetReceipt) error {
	byID := make(map[int64]SnippetRecord, len(records))
	versions := make(map[int64]map[int64]bool, len(records))
	for _, s := range records {
		byID[s.ID] = s
		versions[s.ID] = map[int64]bool{}
		for _, v := range s.Revisions {
			versions[s.ID][v.Version] = true
		}
	}
	for _, r := range receipts {
		s, exists := byID[r.SnippetID]
		if !exists || !versions[r.SnippetID][r.TargetVersion] || r.SnippetVersion > s.Version || r.TargetVersion > s.CurrentVersion || r.CreatedAt.Before(s.CreatedAt) {
			return ErrSnippetCorrupt
		}
	}
	return nil
}

func reviewSnippetFileBundle(s snippetFileSnapshot, b snippetFileBundle) SnippetFileMaintenanceReview {
	report := SnippetFileMaintenanceReview{Backend: "file", Scope: b.Target.scope(), SnippetID: b.Target.SnippetID, Reference: b.reference(), SnapshotSHA256: fileSnippetDigest(s.raw), Healthy: true, Faults: []string{}}
	fault := func(code string) {
		report.Healthy = false
		if !slices.Contains(report.Faults, code) {
			report.Faults = append(report.Faults, code)
		}
	}
	if b.Target.EditorialKey != "" {
		r, err := packagefs.DecodeJSON[SubmissionReceipt](bytes.NewReader(b.Editorial), 16384, "file editorial receipt")
		if err != nil || ValidateSubmissionReceipts([]SubmissionReceipt{r}, snippetFilePosts(s)) != nil {
			fault("invalid_editorial_receipt")
		}
		return report
	}
	var record SnippetRecord
	err := json.Unmarshal(b.Snippet, &record)
	if err != nil || len(b.Snippet) == 0 || ValidateSnippetRecord(record) != nil {
		fault("invalid_snippet_record")
	}
	if s.doc.NextSnippetID <= b.Target.SnippetID || s.doc.NextSnippetID >= math.MaxInt64 {
		fault("invalid_next_snippet_id")
	}
	var receipts []SnippetReceipt
	for _, raw := range b.Receipts {
		r, err := packagefs.DecodeJSON[SnippetReceipt](bytes.NewReader(raw), 16384, "file snippet receipt")
		if err != nil {
			fault("invalid_update_receipt")
			continue
		}
		receipts = append(receipts, r)
	}
	if len(b.Snippet) == 0 || ValidateSnippetState([]SnippetRecord{record}, receipts, max(b.Target.SnippetID+1, s.doc.NextSnippetID)) != nil || validateSnippetFileLinks([]SnippetRecord{record}, receipts) != nil {
		fault("invalid_snippet_history_or_receipts")
	}
	return report
}

// Callers hold the directory lock. These operations only read the named primary;
// no missing-primary recovery, site initialization or fallback to .bak occurs.
func ReviewSnippetFileMaintenance(ctx context.Context, path string, target SnippetFileTarget) (SnippetFileMaintenanceReview, error) {
	s, err := readSnippetFileSnapshot(ctx, path)
	if err != nil {
		return SnippetFileMaintenanceReview{}, err
	}
	b, err := s.bundle(target)
	if err != nil {
		return SnippetFileMaintenanceReview{}, err
	}
	return reviewSnippetFileBundle(s, b), ctx.Err()
}

func ExportSnippetFileRepair(ctx context.Context, path string, target SnippetFileTarget) ([]byte, error) {
	s, err := readSnippetFileSnapshot(ctx, path)
	if err != nil {
		return nil, err
	}
	// File mode already requires a fully valid snapshot to open; exporting a
	// trusted repair candidate applies that same complete validation boundary.
	if _, err = decodeContentDocument(s.raw); err != nil {
		return nil, err
	}
	b, err := s.bundle(target)
	if err != nil {
		return nil, err
	}
	if !reviewSnippetFileBundle(s, b).Healthy {
		return nil, ErrSnippetFileMaintenanceIncomplete
	}
	doc := SnippetFileRepairDocument{Schema: 1, Kind: "newpress.snippet-file-repair"}
	if len(b.Editorial) > 0 {
		doc.Editorial = &SubmissionReceipt{}
		err = json.Unmarshal(b.Editorial, doc.Editorial)
	} else {
		doc.Snippet = &SnippetRecord{}
		err = json.Unmarshal(b.Snippet, doc.Snippet)
		for _, raw := range b.Receipts {
			var r SnippetReceipt
			if e := json.Unmarshal(raw, &r); e != nil {
				return nil, e
			}
			doc.Receipts = append(doc.Receipts, r)
		}
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxSnippetFileRepairBytes {
		return nil, ErrSnippetCapacity
	}
	return raw, ctx.Err()
}

func ListSnippetFileQuarantines(ctx context.Context, path string, query SnippetInspectionQuery) (SnippetFileQuarantinePage, error) {
	if query.Validate() != nil {
		return SnippetFileQuarantinePage{}, ErrSnippetFileMaintenanceInput
	}
	s, err := readSnippetFileSnapshot(ctx, path)
	if err != nil {
		return SnippetFileQuarantinePage{}, err
	}
	page := SnippetFileQuarantinePage{Backend: "file", SnapshotSHA256: fileSnippetDigest(s.raw), Items: []SnippetFileQuarantineSummary{}, Total: len(s.doc.SnippetQuarantines), NextOffset: -1}
	for i, q := range s.doc.SnippetQuarantines {
		if q.Repair == nil {
			page.Unresolved++
		}
		if i >= query.Offset && len(page.Items) < query.Limit {
			page.Items = append(page.Items, q.summary())
		}
	}
	if query.Offset+len(page.Items) < page.Total {
		page.NextOffset = query.Offset + len(page.Items)
	}
	return page, ctx.Err()
}

func prepareSnippetFileWrite(s snippetFileSnapshot, all []SnippetFileQuarantine) ([]byte, error) {
	reserve, err := snippetFileEvidenceBudget(all)
	if err != nil {
		return nil, err
	}
	s.fields["schema"] = json.RawMessage("3")
	if s.doc.Schema == 1 {
		// Schema 1 has no snippet counter. Upgrading an editorial-only file
		// initializes it without changing any existing content identity.
		s.fields["next_snippet_id"] = json.RawMessage("1")
	}
	s.fields["snippet_quarantines"], err = json.Marshal(all)
	if err != nil {
		return nil, err
	}
	// Maintenance may add evidence beside other still-shared snippets. Retain
	// the same withdrawal headroom as normal writes, using the candidate rows
	// because a repair may have just replaced or restored one of them.
	var snippets []json.RawMessage
	if value := s.fields["snippets"]; len(value) != 0 {
		if err := json.Unmarshal(value, &snippets); err != nil {
			return nil, ErrSnippetFileMaintenanceEvidence
		}
	}
	withdrawalReserve := 0
	for _, raw := range snippets {
		fields, err := snippetFileObject(raw, fileSnippetFields)
		if err != nil {
			return nil, err
		}
		visibility, valid := snippetFileField[string](fields, "visibility")
		if !valid || visibility != string(SnippetWithdrawn) {
			withdrawalReserve += 64
		}
	}
	raw := append(encodeSnippetFileFields(s.fields), '\n')
	if len(raw)+reserve+withdrawalReserve > maxContentFileBytes {
		return nil, ErrSnippetCapacity
	}
	return raw, nil
}

func QuarantineSnippetFile(ctx context.Context, path string, request SnippetFileCaptureRequest, now time.Time) (SnippetFileQuarantineSummary, error) {
	if !request.valid() || now.IsZero() {
		return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceInput
	}
	s, err := readSnippetFileSnapshot(ctx, path)
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	for _, q := range s.doc.SnippetQuarantines {
		if q.ID == request.id() {
			if q.Capture != request {
				return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceConflict
			}
			return q.summary(), ctx.Err()
		}
		if q.Repair == nil && q.Capture.Target == request.Target {
			return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceConflict
		}
	}
	b, err := s.bundle(request.Target)
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	if b.reference() != request.Reference {
		return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceConflict
	}
	if reviewSnippetFileBundle(s, b).Healthy {
		return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceInput
	}
	q := SnippetFileQuarantine{ID: request.id(), Capture: request, CapturedAt: now.UTC(), Original: b}
	raw, err := prepareSnippetFileWrite(s, append(s.doc.SnippetQuarantines, q))
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	if err = writeSnippetFileMaintenance(ctx, path, s.raw, raw); err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	return q.summary(), nil
}

func preserveEditorialFileFacts(original []byte, replacement SubmissionReceipt) error {
	m, err := snippetFileObject(original, fileEditorialFields)
	if err != nil {
		return err
	}
	if v, ok := snippetFileField[string](m, "fingerprint"); ok && validSubmissionDigest(v) && v != replacement.Fingerprint {
		return ErrSnippetFileMaintenanceConflict
	}
	if v, ok := snippetFileField[int64](m, "post_id"); ok && v > 0 && v != replacement.PostID {
		return ErrSnippetFileMaintenanceConflict
	}
	if v, ok := snippetFileField[int64](m, "version"); ok && v > 0 && v != replacement.Version {
		return ErrSnippetFileMaintenanceConflict
	}
	if v, ok := snippetFileField[string](m, "action"); ok && ValidSubmissionAction(v) && v != replacement.Action {
		return ErrSnippetFileMaintenanceConflict
	}
	if v, ok := snippetFileField[time.Time](m, "created_at"); ok && !v.IsZero() && !v.Equal(replacement.CreatedAt) {
		return ErrSnippetFileMaintenanceConflict
	}
	if v, ok := snippetFileField[bool](m, "deleted"); ok && v != replacement.Deleted {
		return ErrSnippetFileMaintenanceConflict
	}
	return nil
}

func preserveUpdateFileFacts(original []byte, replacement SnippetReceipt) error {
	m, err := snippetFileObject(original, fileSnippetReceiptFields)
	if err != nil {
		return err
	}
	if v, ok := snippetFileField[string](m, "fingerprint"); ok && validSubmissionDigest(v) && v != replacement.Fingerprint {
		return ErrSnippetFileMaintenanceConflict
	}
	for key, want := range map[string]int64{"snippet_version": replacement.SnippetVersion, "target_version": replacement.TargetVersion} {
		if v, ok := snippetFileField[int64](m, key); ok && v > 0 && v != want {
			return ErrSnippetFileMaintenanceConflict
		}
	}
	if v, ok := snippetFileField[time.Time](m, "created_at"); ok && !v.IsZero() && !v.Equal(replacement.CreatedAt) {
		return ErrSnippetFileMaintenanceConflict
	}
	if v, ok := snippetFileField[[]SnippetSelection](m, "items"); ok && len(v) > 0 && len(v) <= MaxBulkMutations {
		valid, previous := true, int64(0)
		for _, item := range v {
			if item.ID <= previous || item.Version <= 0 {
				valid = false
			}
			previous = item.ID
		}
		if valid && !reflect.DeepEqual(v, replacement.Items) {
			return ErrSnippetFileMaintenanceConflict
		}
	}
	return nil
}

func buildSnippetFileRepair(s snippetFileSnapshot, b snippetFileBundle, doc SnippetFileRepairDocument, now time.Time) (snippetFileBundle, int64, error) {
	result := snippetFileBundle{Target: b.Target}
	if b.Target.EditorialKey != "" {
		if doc.Editorial == nil || doc.Snippet != nil || doc.Editorial.Key != b.Target.EditorialKey || ValidateSubmissionReceipts([]SubmissionReceipt{*doc.Editorial}, snippetFilePosts(s)) != nil {
			return result, 0, ErrSnippetFileMaintenanceInput
		}
		if err := preserveEditorialFileFacts(b.Editorial, *doc.Editorial); err != nil {
			return result, 0, err
		}
		raw, err := json.Marshal(doc.Editorial)
		result.Editorial = raw
		return result, 0, err
	}
	if doc.Snippet == nil || doc.Editorial != nil || doc.Snippet.ID != b.Target.SnippetID || ValidateSnippetRecord(*doc.Snippet) != nil ||
		s.doc.NextSnippetID <= b.Target.SnippetID || s.doc.NextSnippetID >= math.MaxInt64 {
		return result, 0, ErrSnippetFileMaintenanceInput
	}
	candidate := *doc.Snippet
	if ValidateSnippetState([]SnippetRecord{candidate}, doc.Receipts, s.doc.NextSnippetID) != nil || validateSnippetFileLinks([]SnippetRecord{candidate}, doc.Receipts) != nil {
		return result, 0, ErrSnippetFileMaintenanceInput
	}
	maxVersion := candidate.Version
	versions := map[int64]SnippetRevision{}
	for _, v := range candidate.Revisions {
		versions[v.Version] = v
	}
	retained := map[int64]json.RawMessage{}
	if len(b.Snippet) != 0 {
		fields, rows, err := snippetFileHistory(b.Snippet)
		if err != nil {
			return result, 0, err
		}
		for _, field := range []string{"version", "current_version", "shared_since"} {
			if version, ok := snippetFileField[int64](fields, field); ok {
				maxVersion = max(maxVersion, version)
			}
		}
		if created, ok := snippetFileField[time.Time](fields, "created_at"); ok && !created.IsZero() && !created.Equal(candidate.CreatedAt) {
			return result, 0, ErrSnippetFileMaintenanceConflict
		}
		if updated, ok := snippetFileField[time.Time](fields, "updated_at"); ok && updated.After(candidate.UpdatedAt) {
			candidate.UpdatedAt = updated
		}
		metadata := map[string]json.RawMessage{}
		for k, v := range fields {
			if k != "revisions" {
				metadata[k] = v
			}
		}
		var oldMeta Snippet
		_ = json.Unmarshal(encodeSnippetFileFields(metadata), &oldMeta)
		for _, raw := range rows {
			m, _ := snippetFileObject(raw, fileSnippetRevisionFields)
			version, _ := snippetFileField[int64](m, "version")
			want, found := versions[version]
			if !found {
				return result, 0, ErrSnippetFileMaintenanceConflict
			}
			old, err := packagefs.DecodeJSON[SnippetRevision](bytes.NewReader(raw), 2<<20, "original file snippet revision")
			meta := oldMeta
			if ValidateSnippetMetadata(meta) != nil {
				meta = Snippet{ID: b.Target.SnippetID, Version: old.Version, CurrentVersion: old.Version, Visibility: SnippetPrivate, Title: old.Title, CreatedAt: old.CreatedAt, UpdatedAt: old.CreatedAt}
			}
			independent := Snippet{ID: b.Target.SnippetID, Version: old.Version, CurrentVersion: old.Version, Visibility: SnippetPrivate, Title: old.Title, CreatedAt: old.CreatedAt, UpdatedAt: old.CreatedAt}
			validHistory := ValidateSnippetMetadata(meta) == nil && ValidateSnippetRevision(meta, old) == nil || ValidateSnippetMetadata(independent) == nil && ValidateSnippetRevision(independent, old) == nil
			if err == nil && validHistory && !reflect.DeepEqual(old, want) {
				return result, 0, ErrSnippetFileMaintenanceConflict
			}
			if err == nil && reflect.DeepEqual(old, want) {
				retained[version] = raw
			}
		}
	}
	byKey := map[string]SnippetReceipt{}
	for _, r := range doc.Receipts {
		byKey[r.Identity+"\x00"+r.RequestKey] = r
	}
	for _, raw := range b.Receipts {
		_, key, _ := snippetFileReceiptIdentity(raw)
		want, exists := byKey[key]
		if !exists {
			return result, 0, ErrSnippetFileMaintenanceConflict
		}
		if err := preserveUpdateFileFacts(raw, want); err != nil {
			return result, 0, err
		}
		old, err := packagefs.DecodeJSON[SnippetReceipt](bytes.NewReader(raw), 16384, "original file snippet receipt")
		if err == nil && ValidateSnippetReceipt(old) == nil {
			if !reflect.DeepEqual(old, want) {
				return result, 0, ErrSnippetFileMaintenanceConflict
			}
			maxVersion = max(maxVersion, old.SnippetVersion)
		}
		encoded, e := json.Marshal(want)
		if e != nil {
			return result, 0, e
		}
		if err == nil && reflect.DeepEqual(old, want) {
			encoded = raw
		}
		result.Receipts = append(result.Receipts, encoded)
		delete(byKey, key)
	}
	// Additional independently verified receipts are appended deterministically.
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		raw, err := json.Marshal(byKey[key])
		if err != nil {
			return result, 0, err
		}
		result.Receipts = append(result.Receipts, raw)
	}
	if maxVersion >= math.MaxInt64-1 {
		return result, 0, ErrSnippetCapacity
	}
	candidate.Version, candidate.Visibility, candidate.SharedSince = maxVersion+1, SnippetWithdrawn, 0
	if now.After(candidate.UpdatedAt) {
		candidate.UpdatedAt = now
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return result, 0, err
	}
	fields, err := snippetFileObject(raw, fileSnippetFields)
	if err != nil {
		return result, 0, err
	}
	rows := make([]json.RawMessage, 0, len(candidate.Revisions))
	for _, v := range candidate.Revisions {
		encoded := retained[v.Version]
		if encoded == nil {
			encoded, err = json.Marshal(v)
			if err != nil {
				return result, 0, err
			}
		}
		rows = append(rows, encoded)
	}
	fields["revisions"] = encodeSnippetFileArray(rows)
	result.Snippet = encodeSnippetFileFields(fields)
	return result, candidate.Version, result.validate()
}

func replaceSnippetFileBundle(s *snippetFileSnapshot, b snippetFileBundle) {
	if b.Target.EditorialKey != "" {
		rows := s.doc.Submissions
		for i, raw := range rows {
			key, _ := snippetFileEditorialIdentity(raw)
			if key == b.Target.EditorialKey {
				rows[i] = b.Editorial
			}
		}
		s.fields["submissions"] = encodeSnippetFileArray(rows)
		return
	}
	found := false
	rows := s.doc.Snippets
	for i, raw := range rows {
		id, _ := snippetFileID(raw)
		if id == b.Target.SnippetID {
			rows[i] = b.Snippet
			found = true
		}
	}
	if !found {
		rows = append(rows, b.Snippet)
	}
	s.fields["snippets"] = encodeSnippetFileArray(rows)
	byKey := map[string][]byte{}
	for _, raw := range b.Receipts {
		_, key, _ := snippetFileReceiptIdentity(raw)
		byKey[key] = raw
	}
	receipts := s.doc.SnippetReceipts
	for i, raw := range receipts {
		_, key, _ := snippetFileReceiptIdentity(raw)
		if replacement, ok := byKey[key]; ok {
			receipts[i] = replacement
			delete(byKey, key)
		}
	}
	for _, raw := range b.Receipts {
		_, key, _ := snippetFileReceiptIdentity(raw)
		if _, ok := byKey[key]; ok {
			receipts = append(receipts, raw)
		}
	}
	if len(receipts) > 0 || s.fields["snippet_receipts"] != nil {
		s.fields["snippet_receipts"] = encodeSnippetFileArray(receipts)
	}
}

func RepairSnippetFile(ctx context.Context, path string, request SnippetFileRepairRequest, now time.Time) (SnippetFileQuarantineSummary, error) {
	if !request.valid() || now.IsZero() {
		return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceInput
	}
	doc, err := decodeSnippetFileRepair(request.Replacement)
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	s, err := readSnippetFileSnapshot(ctx, path)
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	selected, unresolved := -1, 0
	for i, q := range s.doc.SnippetQuarantines {
		if q.Repair == nil {
			unresolved++
		}
		if q.ID == request.ID {
			selected = i
		}
	}
	if selected < 0 {
		return SnippetFileQuarantineSummary{}, ErrNotFound
	}
	q := s.doc.SnippetQuarantines[selected]
	if old := q.Repair; old != nil {
		if old.Operator != request.Operator || old.Reason != request.Reason || old.ReplacementSHA256 != request.ReplacementSHA256 {
			return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceConflict
		}
		return q.summary(), ctx.Err()
	}
	before, err := s.bundle(q.Capture.Target)
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	if before.reference() != q.Capture.Reference {
		return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceConflict
	}
	after, version, err := buildSnippetFileRepair(s, before, doc, now.UTC())
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	if !reviewSnippetFileBundle(s, after).Healthy {
		return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceInput
	}
	replaceSnippetFileBundle(&s, after)
	at := now.UTC()
	if at.Before(q.CapturedAt) {
		at = q.CapturedAt
	}
	q.Repair = &SnippetFileRepairAudit{Operator: request.Operator, Reason: request.Reason, At: at, ReplacementSHA256: request.ReplacementSHA256, ResultReference: after.reference(), SnippetVersion: version}
	s.doc.SnippetQuarantines[selected] = q
	raw, err := prepareSnippetFileWrite(s, s.doc.SnippetQuarantines)
	if err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	if unresolved == 1 {
		if _, err = decodeContentDocument(raw); err != nil {
			return SnippetFileQuarantineSummary{}, ErrSnippetFileMaintenanceIncomplete
		}
	} else if _, err = decodeSnippetFileSnapshot(raw); err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	if err = writeSnippetFileMaintenance(ctx, path, s.raw, raw); err != nil {
		return SnippetFileQuarantineSummary{}, err
	}
	return q.summary(), nil
}
