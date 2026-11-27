package plugin

import (
	"testing"
)

func TestAIProMaxIsRegisteredBundledPlugin(t *testing.T) {
	manifest := AIProMaxManifest()
	if err := ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if !IsBundledSlug(AIProMax) || !isBuiltin(manifest) {
		t.Fatal("AI Pro Max must resolve only to the reviewed host implementation")
	}
	if manifest.Runtime != "builtin/ai-pro-max-v1" || manifest.NewPress != manifest.Runtime {
		t.Fatalf("unexpected runtime binding: %+v", manifest)
	}
	for _, candidate := range BundledManifests() {
		if candidate.Slug == AIProMax {
			return
		}
	}
	t.Fatal("AI Pro Max is missing from bundled plugin discovery")
}
