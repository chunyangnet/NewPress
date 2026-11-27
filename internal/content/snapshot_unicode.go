package content

import "newpress.local/newpress/internal/packagefs"

// A recovery point may contain incomplete editorial fields. Check only its
// representation here, without normalizing or requiring publishable content.
func validSnapshotJSON(snapshot Post) bool {
	return packagefs.ValidateJSONEncoding(snapshot) == nil
}
