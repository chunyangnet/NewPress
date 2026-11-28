package content

// A snippet's complete history can approach the snippet storage budget. Encode
// its metadata and revisions separately, keeping the existing flattened object
// and field order without handing that entire history to json.Encoder.
func contentJSONSnippets(e *contentJSONEncoder, records []SnippetRecord) {
	if e.err != nil || len(records) == 0 {
		return
	}
	e.key("snippets")
	e.text("[")
	for i := range records {
		if e.err != nil {
			return
		}
		if i != 0 {
			e.text(",")
		}
		e.snippet(&records[i])
	}
	e.text("]")
}

func (e *contentJSONEncoder) snippet(record *SnippetRecord) {
	if len(record.Revisions) <= 1 {
		// With no accumulated history, the existing single-value path already
		// has at most one revision's buffer and avoids extra metadata writes.
		e.value(record)
		return
	}
	parentFields := e.fields
	e.fields = 0
	e.text("{")
	e.field("id", record.ID)
	e.field("version", record.Version)
	e.field("current_version", record.CurrentVersion)
	e.field("shared_since", record.SharedSince)
	e.field("visibility", record.Visibility)
	e.field("title", record.Title)
	e.field("created_at", record.CreatedAt)
	e.field("updated_at", record.UpdatedAt)
	contentJSONArray(e, "revisions", record.Revisions, false)
	e.text("}")
	e.fields = parentFields
}
