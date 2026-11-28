package content

import "encoding/json"

// Count the standard encoding without retaining Marshal's result copy. Each
// value still uses the standard encoder's internal buffer; all history is
// visited, and no cached size can hide a changed record.
type snippetJSONSizeWriter struct{ size int }

func (w *snippetJSONSizeWriter) Write(data []byte) (int, error) {
	w.size += len(data)
	return len(data), nil
}

func SnippetStorageSize(s SnippetRecord) int {
	// Fixed metadata reservation includes every possible withdrawal update.
	w := snippetJSONSizeWriter{size: 4096}
	encoder := json.NewEncoder(&w)
	for i := range s.Revisions {
		if err := encoder.Encode(&s.Revisions[i]); err != nil {
			// An unencodable record must never consume zero quota.
			return MaxSnippetStorageBytes + 1
		}
		w.size-- // Encode's trailing newline is not part of the stored value.
	}
	return w.size
}

func SnippetReceiptSize(r SnippetReceipt) int {
	var w snippetJSONSizeWriter
	if err := json.NewEncoder(&w).Encode(&r); err != nil {
		return MaxSnippetReceiptBytes + 1
	}
	return w.size - 1
}
