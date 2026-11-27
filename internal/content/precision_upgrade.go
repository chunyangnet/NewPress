package content

import (
	"bytes"
	"encoding/json"
	"math"

	"newpress.local/newpress/internal/packagefs"
)

// TransformFileMetadata visits current posts and retained snapshots together.
// A changed current post advances its version to invalidate stale editors.
func TransformFileMetadata(body []byte, transform func(int64, map[string]any) (bool, error)) ([]byte, error) {
	if _, err := decodeContentDocument(body); err != nil {
		return nil, err
	}
	doc, err := packagefs.DecodeJSONWithNumbers[contentDocument](bytes.NewReader(body), maxContentFileBytes, "content precision upgrade")
	if err != nil {
		return nil, err
	}
	versions := map[int64]int64{}
	for i := range doc.Posts {
		p := &doc.Posts[i]
		changed, err := transform(p.ID, p.Meta)
		if err != nil {
			return nil, err
		}
		if changed {
			if p.Version == math.MaxInt64 {
				return nil, ErrVersionConflict
			}
			versions[p.ID] = p.Version
			p.Version++
		}
	}
	for i := range doc.Revisions {
		r := &doc.Revisions[i]
		if _, err = transform(r.PostID, r.Snapshot.Meta); err != nil {
			return nil, err
		}
	}
	for i := range doc.Autosaves {
		a := &doc.Autosaves[i]
		if _, err = transform(a.PostID, a.Snapshot.Meta); err != nil {
			return nil, err
		}
		if old, ok := versions[a.PostID]; ok && a.SourceVersion == old {
			a.SourceVersion++
			a.Snapshot.Version = a.SourceVersion
		}
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if _, err = decodeContentDocument(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}
