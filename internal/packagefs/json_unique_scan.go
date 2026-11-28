package packagefs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// This walk runs only after Unicode and json.Valid preflight. It does not
// interpret scalar values or replace the standard syntax/depth/type checks.
// Only object keys need decoding to detect duplicate names and target aliases.
type uniqueJSONScanner struct {
	payload []byte
	offset  int
}

func (s *uniqueJSONScanner) space() {
	for s.offset < len(s.payload) {
		switch s.payload[s.offset] {
		case ' ', '\t', '\r', '\n':
			s.offset++
		default:
			return
		}
	}
}

func (s *uniqueJSONScanner) skipString() {
	s.offset++ // opening quote
	for {
		switch s.payload[s.offset] {
		case '\\':
			s.offset += 2 // an escaped quote cannot end this string
		case '"':
			s.offset++
			return
		default:
			s.offset++
		}
	}
}

func (s *uniqueJSONScanner) key() (string, error) {
	start := s.offset
	s.skipString()
	raw := s.payload[start:s.offset]
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw[1 : len(raw)-1]), nil
	}
	var key string
	err := json.Unmarshal(raw, &key)
	return key, err
}

func (s *uniqueJSONScanner) value(target reflect.Type) error {
	s.space()
	switch s.payload[s.offset] {
	case '{':
		target = jsonDestinationType(target)
		s.offset++
		s.space()
		seen := make(map[string]struct{})
		fields := make(map[string]string)
		for s.payload[s.offset] != '}' {
			key, err := s.key()
			if err != nil {
				return err
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			child, field := jsonObjectField(target, key)
			if field != "" {
				if previous, exists := fields[field]; exists {
					return fmt.Errorf("duplicate JSON field %q (alias of %q)", key, previous)
				}
				fields[field] = key
			}
			s.space()
			s.offset++ // colon
			if err := s.value(child); err != nil {
				return err
			}
			s.space()
			if s.payload[s.offset] == ',' {
				s.offset++
				s.space()
			}
		}
		s.offset++
	case '[':
		target = jsonDestinationType(target)
		s.offset++
		s.space()
		for index := 0; s.payload[s.offset] != ']'; index++ {
			var child reflect.Type
			if target != nil && (target.Kind() == reflect.Slice || target.Kind() == reflect.Array && index < target.Len()) {
				child = target.Elem()
			}
			if err := s.value(child); err != nil {
				return err
			}
			s.space()
			if s.payload[s.offset] == ',' {
				s.offset++
				s.space()
			}
		}
		s.offset++
	case '"':
		s.skipString()
	default:
		// Numbers and literals were checked by json.Valid. Do not allocate
		// tokens for values that the duplicate-field check never inspects.
		for s.offset < len(s.payload) {
			switch s.payload[s.offset] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return nil
			default:
				s.offset++
			}
		}
	}
	return nil
}
