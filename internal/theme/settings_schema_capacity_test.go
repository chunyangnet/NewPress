package theme

import (
	"fmt"
	"testing"
)

func TestSettingsSchemaFieldCapacity(t *testing.T) {
	schema := settingSchema{Type: "object", Properties: map[string]settingProperty{}}
	for i := 0; i < maxThemeSettingFields; i++ {
		schema.Properties[fmt.Sprintf("option_%d", i)] = settingProperty{Type: "boolean", Default: false}
	}
	if err := validateSettingSchema(&schema); err != nil {
		t.Fatalf("bounded full-feature theme schema: %v", err)
	}
	schema.Properties["overflow"] = settingProperty{Type: "boolean", Default: false}
	if err := validateSettingSchema(&schema); err == nil {
		t.Fatal("oversized schema accepted")
	}
}
