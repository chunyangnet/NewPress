package database

import (
	"bytes"

	"newpress.local/newpress/internal/packagefs"
)

// Content metadata and snapshots must keep JSON numbers exact across reads and
// later writes. Decoding into interface fields through float64 loses that fact.
func decodeContentJSON[T any](payload []byte) (T, error) {
	return packagefs.DecodeJSONWithNumbers[T](bytes.NewReader(payload), int64(len(payload)), "stored content JSON")
}
