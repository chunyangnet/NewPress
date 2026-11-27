package content

import "unicode/utf8"

// ValidateRevisionActor rejects lossy audit text before a mutation can advance
// the content version or invalidate recovery points. Empty actors retain the
// existing caller-specific fallback behavior.
func ValidateRevisionActor(actor string) error {
	if !utf8.ValidString(actor) {
		return &ValidationError{Fields: map[string]string{"actor": "操作者名称必须使用有效的 UTF-8 编码"}}
	}
	return nil
}

// Validate each original string before normalization or JSON encoding can
// discard malformed bytes. Fields must not be concatenated: incomplete UTF-8
// sequences in separate fields could otherwise combine into a valid sequence.
func validateUTF8Fields(fields map[string]string, values map[string]string) bool {
	valid := true
	for name, value := range values {
		if !utf8.ValidString(value) {
			fields[name] = "内容必须使用有效的 UTF-8 编码"
			valid = false
		}
	}
	return valid
}
