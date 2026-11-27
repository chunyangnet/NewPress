package navigation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"newpress.local/newpress/internal/packagefs"
)

const MaxItemsJSONBytes = 192 << 10

func DecodeItemInputs(payload string) ([]ItemInput, error) {
	if len(payload) == 0 || len(payload) > MaxItemsJSONBytes {
		return nil, errors.New("navigation item payload size is invalid")
	}
	encoded := []byte(payload)
	var items []ItemInput
	if err := packagefs.ValidateUniqueJSONFor(encoded, &items); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&items); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("unexpected trailing navigation JSON token")
	}
	return items, nil
}
