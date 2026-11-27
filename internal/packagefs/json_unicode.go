package packagefs

import (
	"errors"
	"unicode/utf8"
)

// encoding/json replaces invalid UTF-8 and unpaired UTF-16 surrogates with
// U+FFFD. Reject those lossy inputs before decoding keys, values or raw messages.
// The token decoder remains responsible for the rest of the JSON grammar.
func validateJSONUnicode(payload []byte) error {
	if !utf8.Valid(payload) {
		return errors.New("JSON document is not valid UTF-8")
	}
	insideString := false
	for index := 0; index < len(payload); index++ {
		if payload[index] == '"' {
			insideString = !insideString
			continue
		}
		if !insideString || payload[index] != '\\' {
			continue
		}
		if index+1 >= len(payload) || payload[index+1] != 'u' {
			// Skip the escaped byte so an escaped quote cannot close the string
			// and a doubled backslash cannot start a Unicode escape.
			index++
			continue
		}
		unit, ok := jsonUTF16Escape(payload[index:])
		if !ok {
			// A malformed escape is already invalid JSON; let the decoder report it.
			return nil
		}
		switch {
		case unit >= 0xdc00 && unit <= 0xdfff:
			return errors.New("JSON string contains an unpaired UTF-16 surrogate")
		case unit >= 0xd800 && unit <= 0xdbff:
			low, ok := jsonUTF16Escape(payload[index+6:])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return errors.New("JSON string contains an unpaired UTF-16 surrogate")
			}
			index += 6
		}
		index += 5
	}
	return nil
}

func jsonUTF16Escape(payload []byte) (uint16, bool) {
	if len(payload) < 6 || payload[0] != '\\' || payload[1] != 'u' {
		return 0, false
	}
	var unit uint16
	for _, digit := range payload[2:6] {
		unit <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			unit |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			unit |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			unit |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return unit, true
}
