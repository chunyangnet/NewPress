package wechat

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

// A write response must have one unambiguous interpretation. In particular,
// encoding/json otherwise accepts null error codes and case-insensitive aliases.
// Unknown fields remain allowed so additive upstream changes are compatible.
func responseErrorCode(data []byte) (int, error) {
	data = bytes.TrimSpace(data)
	invalid := errors.New("微信返回的数据格式无效")
	if len(data) == 0 || data[0] != '{' || !utf8.Valid(data) || packagefs.ValidateUniqueJSON(data) != nil {
		return 0, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return 0, invalid
	}
	for key := range fields {
		for _, name := range []string{"errcode", "access_token", "expires_in", "media_id", "url", "publish_id", "publish_status", "article_id", "article_detail"} {
			if key != name && strings.EqualFold(key, name) {
				return 0, invalid
			}
		}
	}
	if raw, exists := fields["errcode"]; exists {
		var code *int
		if json.Unmarshal(raw, &code) != nil || code == nil {
			return 0, invalid
		}
		return *code, nil
	}
	return 0, nil
}

func validRemoteID(value string) bool {
	return len(value) <= 128 && validOpaqueID(value)
}

func validOpaqueID(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError
	})
}

// Submission returns an integer ID while status queries use strings. Accept
// either without converting through float64 or rounding a long task ID.
func publicationID(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return "", false
		}
	} else {
		for _, b := range raw {
			if b < '0' || b > '9' {
				return "", false
			}
		}
		value = string(raw)
	}
	return value, validRemoteID(value)
}
