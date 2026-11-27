package navigation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

var ErrSnapshotConflict = fmt.Errorf("navigation snapshot revision conflict")

// RegistryMenuInput is a complete portable menu tree. Item IDs are request-local
// identities only; ReplaceAll always allocates new persistent IDs.
type RegistryMenuInput struct {
	Menu  MenuInput
	Items []ItemInput
}

type RegistryMutation struct {
	ExpectedRevision string
	Actor            string
}

// NormalizeRegistryInputs validates a whole navigation registry before any
// store mutation. It applies the same menu/item rules as the interactive editor
// and additionally enforces registry-wide slug and location uniqueness.
func NormalizeRegistryInputs(inputs []RegistryMenuInput) ([]RegistryMenuInput, map[string]string) {
	fields := make(map[string]string)
	if len(inputs) > MaxMenus {
		fields["menus"] = fmt.Sprintf("最多只能导入 %d 个菜单", MaxMenus)
		return nil, fields
	}
	normalized := make([]RegistryMenuInput, len(inputs))
	slugs := make(map[string]int, len(inputs))
	locations := make(map[string]int, len(Locations()))
	for index, input := range inputs {
		menu, menuFields := NormalizeMenuInput(input.Menu)
		for key, value := range menuFields {
			fields[fmt.Sprintf("menus.%d.%s", index, key)] = value
		}
		items, itemFields := NormalizeItemInputs(input.Items)
		for key, value := range itemFields {
			fields[fmt.Sprintf("menus.%d.%s", index, key)] = value
		}
		slugKey := strings.ToLower(menu.Slug)
		if previous, exists := slugs[slugKey]; exists && slugKey != "" {
			fields[fmt.Sprintf("menus.%d.slug", index)] = fmt.Sprintf("菜单别名与第 %d 个菜单重复", previous+1)
		} else if slugKey != "" {
			slugs[slugKey] = index
		}
		for _, location := range menu.Locations {
			if previous, exists := locations[location]; exists {
				fields[fmt.Sprintf("menus.%d.locations", index)] = fmt.Sprintf("位置已由第 %d 个菜单占用", previous+1)
				continue
			}
			locations[location] = index
		}
		normalized[index] = RegistryMenuInput{Menu: menu, Items: items}
	}
	if len(fields) > 0 {
		return normalized, fields
	}
	return normalized, nil
}

// SnapshotRevision is the opaque registry revision used by navigation CSS,
// management ETags, portable exports and whole-registry optimistic writes.
func SnapshotRevision(snapshot Snapshot) string {
	hash := sha256.New()
	for _, menu := range snapshot.Menus {
		fmt.Fprintf(hash, "%d:%d:%s|", menu.ID, menu.Version, menu.Slug)
		for _, location := range menu.Locations {
			fmt.Fprintf(hash, "%s,", location)
		}
		for _, item := range menu.Items {
			fmt.Fprintf(hash, "%d:%d:%d:%s:%s:%s|", item.ID, item.ParentID, item.Position, item.Accent, item.URL, item.Label)
		}
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}
