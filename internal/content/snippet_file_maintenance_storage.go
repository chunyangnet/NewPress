package content

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"reflect"
	"slices"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

// Shadow only the maintained collections so malformed fields remain available
// as original evidence. All other content fields use the normal strict decoder.
type snippetFileDocument struct {
	contentDocument
	Snippets        []json.RawMessage `json:"snippets,omitempty"`
	SnippetReceipts []json.RawMessage `json:"snippet_receipts,omitempty"`
	Submissions     []json.RawMessage `json:"submissions,omitempty"`
}

type snippetFileSnapshot struct {
	raw    []byte
	fields map[string]json.RawMessage
	doc    snippetFileDocument
}

func snippetFileObject(raw []byte, fields string) (map[string]json.RawMessage, error) {
	m, err := packagefs.DecodeJSON[map[string]json.RawMessage](bytes.NewReader(raw), maxContentFileBytes, "file snippet record")
	if err != nil || m == nil {
		return nil, ErrSnippetFileMaintenanceEvidence
	}
	for key := range m {
		if !slices.Contains(strings.Fields(fields), key) {
			return nil, ErrSnippetFileMaintenanceEvidence
		}
	}
	return m, nil
}

const fileSnippetFields = "id version current_version shared_since visibility title created_at updated_at revisions"
const fileSnippetRevisionFields = "version title content_source content_html actor created_at"
const fileSnippetReceiptFields = "identity request_key fingerprint snippet_id snippet_version target_version items created_at"
const fileEditorialFields = "deleted key fingerprint post_id version action created_at"

func snippetFileField[T any](fields map[string]json.RawMessage, name string) (T, bool) {
	var value T
	raw := fields[name]
	valid := len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && json.Unmarshal(raw, &value) == nil
	return value, valid
}

func snippetFileID(raw []byte) (int64, error) {
	m, err := snippetFileObject(raw, fileSnippetFields)
	id, ok := snippetFileField[int64](m, "id")
	if err != nil || !ok || id <= 0 || id >= math.MaxInt64 {
		return 0, ErrSnippetFileMaintenanceEvidence
	}
	return id, nil
}

func snippetFileReceiptIdentity(raw []byte) (int64, string, error) {
	m, err := snippetFileObject(raw, fileSnippetReceiptFields)
	id, validID := snippetFileField[int64](m, "snippet_id")
	identity, validIdentity := snippetFileField[string](m, "identity")
	key, validKey := snippetFileField[string](m, "request_key")
	if err != nil || !validID || id <= 0 || id >= math.MaxInt64 || !validIdentity || identity == "" || len(identity) > 160 || containsControl(identity) ||
		!validKey || !validSubmissionDigest(key) {
		return 0, "", ErrSnippetFileMaintenanceEvidence
	}
	return id, identity + "\x00" + key, nil
}

func snippetFileEditorialIdentity(raw []byte) (string, error) {
	m, err := snippetFileObject(raw, fileEditorialFields)
	key, ok := snippetFileField[string](m, "key")
	if err != nil || !ok || !validSubmissionDigest(key) {
		return "", ErrSnippetFileMaintenanceEvidence
	}
	return key, nil
}

func decodeSnippetFileSnapshot(raw []byte) (snippetFileSnapshot, error) {
	s := snippetFileSnapshot{raw: raw}
	var err error
	s.doc, err = packagefs.DecodeJSONWithNumbers[snippetFileDocument](bytes.NewReader(raw), maxContentFileBytes, "file snippet maintenance")
	if err != nil {
		return s, ErrSnippetFileMaintenanceEvidence
	}
	keys := []string{}
	typ := reflect.TypeFor[contentDocument]()
	for i := 0; i < typ.NumField(); i++ {
		keys = append(keys, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	s.fields, err = snippetFileObject(raw, strings.Join(keys, " "))
	if err != nil || s.doc.Kind != "newpress.content" || s.doc.Schema < 1 || s.doc.Schema > 3 ||
		(s.doc.Schema == 1 && (len(s.doc.Snippets)+len(s.doc.SnippetReceipts) != 0 || s.doc.NextSnippetID != 0)) ||
		(s.doc.Schema < 3 && len(s.doc.SnippetQuarantines) != 0) {
		return s, ErrSnippetFileMaintenanceEvidence
	}
	reserve, err := snippetFileEvidenceBudget(s.doc.SnippetQuarantines)
	if err != nil {
		return s, err
	}
	if len(raw)+reserve > maxContentFileBytes {
		return s, ErrSnippetCapacity
	}
	if len(s.doc.Snippets) > MaxSnippets || len(s.doc.SnippetReceipts) > MaxSnippetReceipts || len(s.doc.Submissions) > MaxSubmissionReceipts {
		return s, ErrSnippetCapacity
	}
	ids, receipts, submissions := map[int64]bool{}, map[string]bool{}, map[string]bool{}
	totalHistory, totalSnippetBytes, totalReceiptBytes := 0, 0, 0
	for _, raw := range s.doc.Snippets {
		id, e := snippetFileID(raw)
		if e != nil || ids[id] {
			return s, ErrSnippetFileMaintenanceEvidence
		}
		ids[id] = true
		_, rows, e := snippetFileHistory(raw)
		if e != nil {
			return s, e
		}
		totalHistory += len(rows)
		totalSnippetBytes += 4096
		for _, row := range rows {
			totalSnippetBytes += len(row)
		}
	}
	for _, raw := range s.doc.SnippetReceipts {
		_, key, e := snippetFileReceiptIdentity(raw)
		if e != nil || receipts[key] {
			return s, ErrSnippetFileMaintenanceEvidence
		}
		receipts[key] = true
		totalReceiptBytes += len(raw)
	}
	if totalHistory > MaxSnippetRevisions || totalSnippetBytes > MaxSnippetStorageBytes || totalReceiptBytes > MaxSnippetReceiptBytes {
		return s, ErrSnippetCapacity
	}
	for _, raw := range s.doc.Submissions {
		key, e := snippetFileEditorialIdentity(raw)
		if e != nil || submissions[key] {
			return s, ErrSnippetFileMaintenanceEvidence
		}
		submissions[key] = true
	}
	// A snippet-only command must never mask unrelated content corruption.
	canonical := make(map[string]json.RawMessage, len(s.fields))
	for key, value := range s.fields {
		canonical[key] = value
	}
	delete(canonical, "snippet_quarantines")
	canonical["schema"] = json.RawMessage("2")
	canonical["snippets"], canonical["snippet_receipts"], canonical["submissions"] = json.RawMessage("[]"), json.RawMessage("[]"), json.RawMessage("[]")
	canonical["next_snippet_id"] = json.RawMessage("1")
	if _, err = decodeContentDocument(encodeSnippetFileFields(canonical)); err != nil {
		return s, ErrSnippetFileMaintenanceEvidence
	}
	return s, nil
}

func readSnippetFileSnapshot(ctx context.Context, path string) (snippetFileSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return snippetFileSnapshot{}, err
	}
	raw, err := readContentFile(path)
	if err != nil {
		return snippetFileSnapshot{}, err
	}
	s, err := decodeSnippetFileSnapshot(raw)
	if err != nil {
		return s, err
	}
	return s, ctx.Err()
}

func (s snippetFileSnapshot) bundle(target SnippetFileTarget) (snippetFileBundle, error) {
	b := snippetFileBundle{Target: target}
	if !target.valid() {
		return b, ErrSnippetFileMaintenanceInput
	}
	if target.EditorialKey != "" {
		for _, raw := range s.doc.Submissions {
			key, _ := snippetFileEditorialIdentity(raw)
			if key == target.EditorialKey {
				b.Editorial = raw
				return b, b.validate()
			}
		}
	} else {
		for _, raw := range s.doc.Snippets {
			id, _ := snippetFileID(raw)
			if id == target.SnippetID {
				b.Snippet = raw
			}
		}
		for _, raw := range s.doc.SnippetReceipts {
			id, _, _ := snippetFileReceiptIdentity(raw)
			if id == target.SnippetID {
				b.Receipts = append(b.Receipts, raw)
			}
		}
		if len(b.Snippet)+len(b.Receipts) != 0 {
			return b, b.validate()
		}
	}
	return b, ErrNotFound
}

func (b snippetFileBundle) validate() error {
	if !b.Target.valid() || len(b.Receipts) > MaxSnippetReceipts {
		return ErrSnippetFileMaintenanceEvidence
	}
	if b.Target.EditorialKey != "" {
		key, err := snippetFileEditorialIdentity(b.Editorial)
		if err != nil || key != b.Target.EditorialKey || len(b.Snippet)+len(b.Receipts) != 0 || len(b.Editorial) > 16384 {
			return ErrSnippetFileMaintenanceEvidence
		}
		return nil
	}
	if len(b.Editorial) != 0 || len(b.Snippet)+len(b.Receipts) == 0 || len(b.Snippet) > MaxSnippetStorageBytes {
		return ErrSnippetFileMaintenanceEvidence
	}
	if len(b.Snippet) != 0 {
		id, err := snippetFileID(b.Snippet)
		if err != nil || id != b.Target.SnippetID {
			return ErrSnippetFileMaintenanceEvidence
		}
		if _, _, err = snippetFileHistory(b.Snippet); err != nil {
			return err
		}
	}
	seen, total := map[string]bool{}, 0
	for _, raw := range b.Receipts {
		id, key, err := snippetFileReceiptIdentity(raw)
		if err != nil || id != b.Target.SnippetID || seen[key] || len(raw) > 16384 {
			return ErrSnippetFileMaintenanceEvidence
		}
		seen[key] = true
		total += len(raw)
	}
	if total > MaxSnippetReceiptBytes {
		return ErrSnippetCapacity
	}
	return nil
}

func snippetFileHistory(raw []byte) (map[string]json.RawMessage, []json.RawMessage, error) {
	fields, err := snippetFileObject(raw, fileSnippetFields)
	if err != nil {
		return nil, nil, err
	}
	var rows []json.RawMessage
	if data := fields["revisions"]; len(data) != 0 && json.Unmarshal(data, &rows) != nil {
		return nil, nil, ErrSnippetFileMaintenanceEvidence
	}
	if len(rows) > MaxSnippetRevisions {
		return nil, nil, ErrSnippetCapacity
	}
	var previous int64
	for _, raw := range rows {
		m, e := snippetFileObject(raw, fileSnippetRevisionFields)
		v, ok := snippetFileField[int64](m, "version")
		if e != nil || !ok || v <= previous || v >= math.MaxInt64 || len(raw) > 2<<20 {
			return nil, nil, ErrSnippetFileMaintenanceEvidence
		}
		previous = v
	}
	return fields, rows, nil
}

// Preserve every original value's JSON bytes; json.Marshal(RawMessage) would
// compact and HTML-escape unrelated strings. Only object key order is rebuilt.
func encodeSnippetFileFields(fields map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out bytes.Buffer
	out.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(k)
		out.Write(name)
		out.WriteByte(':')
		out.Write(fields[k])
	}
	out.WriteByte('}')
	return out.Bytes()
}

func encodeSnippetFileArray(rows []json.RawMessage) []byte {
	var out bytes.Buffer
	out.WriteByte('[')
	for i, row := range rows {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(row)
	}
	out.WriteByte(']')
	return out.Bytes()
}

func writeSnippetFileMaintenance(ctx context.Context, path string, before, after []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := readContentFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return ErrSnippetFileMaintenanceConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeContentFile(path, after)
}
