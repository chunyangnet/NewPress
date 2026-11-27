package packagefs

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"unicode/utf8"
)

// ValidateJSONEncoding checks that encoding value produces one unambiguous JSON
// value without replacing malformed string bytes. It preserves custom JSON
// representations and binary slices, and follows the fields actually encoded.
func ValidateJSONEncoding(value any) error {
	_, err := MarshalUniqueJSON(value)
	return err
}

// MarshalUniqueJSON returns the exact encoding checked by ValidateJSONEncoding.
// Callers can persist or decode these bytes without invoking custom JSON
// marshalers again and accidentally storing a different representation.
func MarshalUniqueJSON(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateUniqueJSON(payload); err != nil {
		return nil, err
	}
	var encoded any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&encoded); err != nil {
		return nil, err
	}
	if !validEncodedStrings(reflect.ValueOf(value), encoded) {
		return nil, errors.New("JSON encoding would replace invalid UTF-8")
	}
	return payload, nil
}

func validEncodedStrings(value reflect.Value, encoded any) bool {
	if !value.IsValid() {
		return true
	}
	if (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) && value.IsNil() {
		return true
	}
	if value.Kind() == reflect.Interface {
		return validEncodedStrings(value.Elem(), encoded)
	}
	// Match the encoder's method choice, including addressable pointer methods.
	custom := value
	if value.Kind() != reflect.Pointer && value.CanAddr() && value.Addr().CanInterface() {
		address := value.Addr().Interface()
		_, jsonMethod := address.(json.Marshaler)
		_, textMethod := address.(encoding.TextMarshaler)
		if jsonMethod || textMethod {
			custom = value.Addr()
		}
	}
	if custom.CanInterface() {
		if _, ok := custom.Interface().(json.Marshaler); ok {
			return true // ValidateUniqueJSON already checked this output.
		}
		if marshaler, ok := custom.Interface().(encoding.TextMarshaler); ok {
			text, err := marshaler.MarshalText()
			return err == nil && utf8.Valid(text)
		}
	}
	switch value.Kind() {
	case reflect.Pointer:
		return validEncodedStrings(value.Elem(), encoded)
	case reflect.String:
		return utf8.ValidString(value.String())
	case reflect.Map:
		object, _ := encoded.(map[string]any)
		iter := value.MapRange()
		for iter.Next() {
			name, valid := encodedMapKey(iter.Key())
			if !valid || !validEncodedStrings(iter.Value(), object[name]) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		// []byte uses base64 unless its element has a custom marshaler.
		if _, binary := encoded.(string); binary && value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return true
		}
		array, _ := encoded.([]any)
		for index := 0; index < value.Len(); index++ {
			var child any
			if index < len(array) {
				child = array[index]
			}
			if !validEncodedStrings(value.Index(index), child) {
				return false
			}
		}
	case reflect.Struct:
		object, _ := encoded.(map[string]any)
		for _, field := range jsonFields(value.Type()).ordered {
			child, present := object[field.name]
			if !present {
				continue // Includes fields omitted by omitempty or omitzero.
			}
			fieldValue := value
			for _, index := range field.index {
				if fieldValue.Kind() == reflect.Pointer {
					if fieldValue.IsNil() {
						break
					}
					fieldValue = fieldValue.Elem()
				}
				fieldValue = fieldValue.Field(index)
			}
			if !validEncodedStrings(fieldValue, child) {
				return false
			}
		}
	}
	return true
}

func encodedMapKey(key reflect.Value) (string, bool) {
	if key.Kind() == reflect.String {
		return key.String(), utf8.ValidString(key.String())
	}
	if key.CanInterface() {
		if marshaler, ok := key.Interface().(encoding.TextMarshaler); ok {
			if key.Kind() == reflect.Pointer && key.IsNil() {
				return "", true
			}
			text, err := marshaler.MarshalText()
			return string(text), err == nil && utf8.Valid(text)
		}
	}
	switch key.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(key.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(key.Uint(), 10), true
	}
	return "", false // json.Marshal rejects unsupported map key types first.
}
