package licensing

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedLicensesMatchMaintainedSources(t *testing.T) {
	for _, item := range []struct {
		path string
		text string
	}{
		{filepath.Join("..", "..", "LICENSE"), Apache20},
		{filepath.Join("..", "commentorigin", "flags", "LICENSE"), FlagIconsMIT},
	} {
		data, err := os.ReadFile(item.path)
		if err != nil || string(data) != item.text {
			t.Fatalf("embedded notice differs from %s: %v", item.path, err)
		}
	}
}
