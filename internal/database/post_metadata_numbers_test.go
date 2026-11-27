package database

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"newpress.local/newpress/internal/content"
)

type metadataJSONBRow []byte

func (row metadataJSONBRow) Scan(destinations ...any) error {
	// Use the installed pgx JSONB codec, without requiring a database server.
	return pgtype.NewMap().Scan(pgtype.JSONBOID, pgtype.TextFormatCode, row, destinations[len(destinations)-1])
}

func TestPostMetadataScanPreservesJSONNumbers(t *testing.T) {
	for _, number := range []string{"9007199254740993", "9223372036854775807", "18446744073709551615", "1234567890.1234567890123456789", "1e400", "1e-400"} {
		t.Run(number, func(t *testing.T) {
			payload := []byte(`{"number":` + number + `}`)
			post, err := scanPost(metadataJSONBRow(payload))
			if err != nil {
				t.Fatalf("metadata scan rejected valid JSON number: %v", err)
			}
			got, err := json.Marshal(post.Meta)
			if err != nil || !bytes.Equal(got, payload) {
				t.Errorf("metadata number was rounded: got=%s want=%s err=%v", got, payload, err)
			}
		})
	}
}

func TestContentSnapshotDecoderPreservesJSONNumbers(t *testing.T) {
	for _, number := range []string{"9007199254740993", "9223372036854775807", "18446744073709551615", "0.1234567890123456789", "1e400", "1e-400"} {
		t.Run(number, func(t *testing.T) {
			payload := []byte(`{"id":1,"meta":{"number":` + number + `}}`)
			post, err := decodeContentJSON[content.Post](payload)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(post.Meta)
			want := []byte(`{"number":` + number + `}`)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("snapshot number was rounded: got=%s want=%s err=%v", got, want, err)
			}
		})
	}
}
