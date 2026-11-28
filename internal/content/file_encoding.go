package content

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/trace"
	"sort"
)

var errContentFileCapacity = errors.New("content store exceeds 128 MiB; use PostgreSQL for a larger site")

// Keep the byte-returning form for callers that need an encoded document.
// Repository commits use writeContentState and never assemble this buffer.
func encodeContentDocument(state *MemoryRepository) ([]byte, error) {
	var buffer bytes.Buffer
	if err := encodeContentState(&buffer, state); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeContentState(ctx context.Context, path string, state *MemoryRepository) ([sha256.Size]byte, error) {
	defer trace.StartRegion(ctx, "content.file.persist").End()
	var digest [sha256.Size]byte
	hash := sha256.New()
	err := replaceContentFile(ctx, path, func(file io.Writer) error {
		if trace.IsEnabled() {
			file = contentFileTraceWriter{ctx: ctx, writer: file}
		}
		buffer := bufio.NewWriterSize(io.MultiWriter(file, hash), 256<<10)
		region := trace.StartRegion(ctx, "content.file.encode")
		err := encodeContentState(buffer, state)
		region.End()
		if err != nil {
			return err
		}
		region = trace.StartRegion(ctx, "content.file.flush")
		err = buffer.Flush()
		region.End()
		return err
	})
	if err != nil {
		return digest, err
	}
	hash.Sum(digest[:0])
	return digest, nil
}

func encodeContentState(writer io.Writer, state *MemoryRepository) error {
	history := contentHistoryForEncoding(state)
	size, err := writeContentJSONWithHistory(writer, contentDocumentMetadata(state), maxContentFileBytes, &history)
	if err != nil {
		if errors.Is(err, errContentFileCapacity) {
			return err
		}
		return fmt.Errorf("encode content store: %w", err)
	}
	withdrawalReserve := 0
	for _, snippet := range state.snippets {
		if snippet.Visibility != SnippetWithdrawn {
			withdrawalReserve += 64
		}
	}
	evidenceReserve, err := snippetFileEvidenceBudget(state.snippetQuarantines)
	if err != nil {
		return err
	}
	if len(state.snippetQuarantines) != 0 && state.contentSchema < 3 {
		return ErrSnippetFileMaintenanceEvidence
	}
	if size+withdrawalReserve+evidenceReserve > maxContentFileBytes {
		return errContentFileCapacity
	}
	return nil
}

// Serialize the existing document in its declared field order. Only one array
// element is handed to json.Encoder at a time, so its shared encoder pool does
// not retain a buffer the size of the entire content store. A single unusually
// large record can still require a large buffer.
func writeContentJSON(writer io.Writer, document contentDocument, limit int) (int, error) {
	return writeContentJSONWithHistory(writer, document, limit, nil)
}

func writeContentJSONWithHistory(writer io.Writer, document contentDocument, limit int, history *contentEncodingHistory) (int, error) {
	e := contentJSONEncoder{writer: writer, limit: limit}
	e.valueEncoder = json.NewEncoder(&e)
	e.text("{")
	e.field("schema", document.Schema)
	e.field("kind", document.Kind)
	e.field("next_id", document.NextID)
	e.field("next_term_id", document.NextTermID)
	e.field("next_revision_id", document.NextRevisionID)
	contentJSONArray(&e, "posts", document.Posts, false)
	contentJSONArray(&e, "terms", document.Terms, false)
	if history == nil {
		contentJSONArray(&e, "revisions", document.Revisions, false)
		contentJSONArray(&e, "autosaves", document.Autosaves, false)
	} else {
		history.write(&e)
	}
	contentJSONArray(&e, "activities", document.Activities, true)
	contentJSONArray(&e, "series", document.Series, true)
	if document.NextSeriesID != 0 {
		e.field("next_series_id", document.NextSeriesID)
	}
	contentJSONArray(&e, "redirects", document.Redirects, true)
	if document.NextRedirectID != 0 {
		e.field("next_redirect_id", document.NextRedirectID)
	}
	contentJSONArray(&e, "imports", document.Imports, true)
	contentJSONArray(&e, "submissions", document.Submissions, true)
	contentJSONSnippets(&e, document.Snippets)
	contentJSONArray(&e, "snippet_receipts", document.SnippetReceipts, true)
	if document.NextSnippetID != 0 {
		e.field("next_snippet_id", document.NextSnippetID)
	}
	contentJSONArray(&e, "snippet_quarantines", document.SnippetQuarantines, true)
	e.text("}\n")
	return e.size, e.err
}

type contentJSONEncoder struct {
	writer       io.Writer
	limit        int
	size         int
	fields       int
	err          error
	valueEncoder *json.Encoder
	valueTail    [1]byte
	hasValueTail bool
}

func (e *contentJSONEncoder) text(text string) {
	if e.err != nil {
		return
	}
	if len(text) > e.limit-e.size {
		e.err = errContentFileCapacity
		return
	}
	var n int
	n, e.err = io.WriteString(e.writer, text)
	e.size += n
	if n != len(text) && e.err == nil {
		e.err = io.ErrShortWrite
	}
}

func (e *contentJSONEncoder) value(value any) {
	if e.err != nil {
		return
	}
	if err := e.valueEncoder.Encode(value); err != nil {
		e.err = err
		return
	}
	// Encode appends one newline. Remove only that final byte to preserve the
	// existing compact document, including its single final newline.
	if !e.hasValueTail || e.valueTail[0] != '\n' {
		e.err = errors.New("JSON value encoding did not end with a newline")
		return
	}
	e.hasValueTail = false
}

// Write accepts the value encoder's output without making a separate copy of
// each record. Retaining its final byte works even if Encode writes in chunks;
// the next chunk releases the previous tail, and value drops the last newline.
// Document punctuation uses text and never passes through this adapter.
func (e *contentJSONEncoder) Write(data []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	if len(data) == 0 {
		return 0, nil
	}
	if e.hasValueTail {
		if _, err := e.writeValueBytes(e.valueTail[:]); err != nil {
			return 0, err
		}
	}
	n, err := e.writeValueBytes(data[:len(data)-1])
	if err != nil {
		return n, err
	}
	e.valueTail[0], e.hasValueTail = data[len(data)-1], true
	return len(data), nil
}

func (e *contentJSONEncoder) writeValueBytes(data []byte) (int, error) {
	if len(data) > e.limit-e.size {
		e.err = errContentFileCapacity
		return 0, e.err
	}
	if len(data) == 0 {
		return 0, nil
	}
	n, err := e.writer.Write(data)
	e.size += n
	e.err = err
	if n != len(data) && e.err == nil {
		e.err = io.ErrShortWrite
	}
	return n, e.err
}

func (e *contentJSONEncoder) key(name string) {
	if e.fields != 0 {
		e.text(",")
	}
	e.fields++
	// Field names above are fixed ASCII identifiers, never user input.
	e.text("\"" + name + "\":")
}

func (e *contentJSONEncoder) field(name string, value any) {
	e.key(name)
	e.value(value)
}

func contentJSONArray[T any](e *contentJSONEncoder, name string, rows []T, omitEmpty bool) {
	if e.err != nil || omitEmpty && len(rows) == 0 {
		return
	}
	e.key(name)
	if rows == nil {
		e.text("null")
		return
	}
	e.text("[")
	for i := range rows {
		if e.err != nil {
			return
		}
		if i != 0 {
			e.text(",")
		}
		e.value(&rows[i])
	}
	e.text("]")
}

func contentDocumentMetadata(state *MemoryRepository) contentDocument {
	document := contentDocument{
		Schema: state.contentSchema, Kind: "newpress.content", NextID: state.nextID,
		NextTermID: state.nextTermID, NextRevisionID: state.nextRevisionID,
		Posts: state.posts, Terms: state.terms, Revisions: []Revision{}, Autosaves: []Autosave{},
		Activities: state.activities,
		Series:     state.series, NextSeriesID: state.nextSeriesID,
		Redirects: state.redirects, NextRedirectID: state.nextRedirectID,
		Imports:     state.imports,
		Submissions: state.submissions,
		Snippets:    state.snippets, SnippetReceipts: state.snippetReceipts,
		SnippetQuarantines: state.snippetQuarantines,
	}
	if state.contentSchema >= 2 {
		document.NextSnippetID = state.nextSnippetID
	}
	return document
}

func contentDocumentForEncoding(state *MemoryRepository) contentDocument {
	document := contentDocumentMetadata(state)
	for _, revisions := range state.revisions {
		document.Revisions = append(document.Revisions, revisions...)
	}
	sort.Slice(document.Revisions, func(i, j int) bool { return document.Revisions[i].ID < document.Revisions[j].ID })
	for _, owners := range state.autosaves {
		for _, autosave := range owners {
			if autosave.ExpiresAt.After(state.now()) {
				document.Autosaves = append(document.Autosaves, autosave)
			}
		}
	}
	sort.Slice(document.Autosaves, func(i, j int) bool {
		left, right := document.Autosaves[i], document.Autosaves[j]
		if left.PostID == right.PostID {
			return left.OwnerKey < right.OwnerKey
		}
		return left.PostID < right.PostID
	})
	return document
}
