package navigation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeMenuAndItemsRejectUnsafeBoundaries(t *testing.T) {
	menu, fields := NormalizeMenuInput(MenuInput{
		Name: "  中文 主菜单  ", Slug: "", Description: "  站点入口  ",
		Locations: []string{LocationMobile, LocationPrimary, LocationPrimary},
	})
	if len(fields) != 0 || menu.Slug != "中文-主菜单" || menu.Description != "站点入口" || len(menu.Locations) != 2 || menu.Locations[0] != LocationPrimary {
		t.Fatalf("normalized menu = %#v, fields=%#v", menu, fields)
	}

	items, fields := NormalizeItemInputs([]ItemInput{
		{ID: -1, Type: TypeCustom, Label: "外部文档", URL: "https://docs.example.test/guide", Target: TargetBlank, Rel: "ugc unknown", Icon: "globe-2", Accent: "#1a2b3c", Presentation: PresentationPanel},
		{ID: -2, ParentID: -1, Type: TypePage, ObjectID: 7, Label: "关于", URL: "/pages/about", Presentation: PresentationStandard},
	})
	if len(fields) != 0 || items[0].Rel != "ugc noopener noreferrer" || items[0].Accent != "#1A2B3C" || items[1].Target != TargetSelf {
		t.Fatalf("normalized items = %#v, fields=%#v", items, fields)
	}

	unsafe := []string{"javascript:alert(1)", "//evil.test/path", "https://user:pass@example.test/", "/safe\\escape"}
	for _, value := range unsafe {
		if err := ValidateURL(value); err == nil {
			t.Fatalf("ValidateURL(%q) accepted unsafe value", value)
		}
	}
	if err := ValidateURL("mailto:owner@example.test"); err != nil {
		t.Fatalf("valid mailto rejected: %v", err)
	}

	_, fields = NormalizeItemInputs([]ItemInput{
		{ID: -1, Label: "root", URL: "/", Presentation: PresentationStandard},
		{ID: -2, ParentID: -1, Label: "two", URL: "/two", Presentation: PresentationStandard},
		{ID: -3, ParentID: -2, Label: "three", URL: "/three", Presentation: PresentationStandard},
		{ID: -4, ParentID: -3, Label: "four", URL: "/four", Presentation: PresentationStandard},
	})
	if fields["items.3.parent_id"] == "" {
		t.Fatalf("fourth level was not rejected: %#v", fields)
	}

	_, fields = NormalizeItemInputs([]ItemInput{
		{ID: -1, ParentID: -2, Label: "one", URL: "/one"},
		{ID: -2, ParentID: -1, Label: "two", URL: "/two"},
	})
	if fields["items.0.parent_id"] == "" && fields["items.1.parent_id"] == "" {
		t.Fatalf("cycle was not rejected: %#v", fields)
	}
}

func TestDecodeItemInputsRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	valid := `[{"id":-1,"parent_id":0,"type":"custom","object_id":0,"label":"首页","url":"/","description":"","target":"self","rel":"","icon":"house","accent":"#16A89A","presentation":"standard"}]`
	items, err := DecodeItemInputs(valid)
	if err != nil || len(items) != 1 || items[0].Label != "首页" {
		t.Fatalf("valid navigation JSON = %#v, %v", items, err)
	}
	for name, payload := range map[string]string{
		"unknown field":   strings.Replace(valid, `"presentation":"standard"`, `"presentation":"standard","html":"<b>unsafe</b>"`, 1),
		"trailing object": valid + `{}`,
		"trailing scalar": valid + ` true`,
	} {
		if _, err := DecodeItemInputs(payload); err == nil {
			t.Fatalf("%s payload was accepted", name)
		}
	}
}

func TestFileStoreLifecyclePersistenceHierarchyAndConflicts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "navigation.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	menu, err := store.Create(ctx, MenuInput{
		Name: "主导航", Slug: "main", Description: "测试菜单",
		Locations: []string{LocationPrimary, LocationMobile},
	}, "owner")
	if err != nil || menu.ID != 1 || menu.Version != 1 {
		t.Fatalf("create menu = %#v, %v", menu, err)
	}
	if _, err := store.Create(ctx, MenuInput{Name: "重复别名", Slug: "MAIN"}, "owner"); !errors.Is(err, ErrSlugConflict) {
		t.Fatalf("slug conflict = %v", err)
	}
	if _, err := store.Create(ctx, MenuInput{Name: "位置冲突", Slug: "other", Locations: []string{LocationMobile}}, "owner"); !errors.Is(err, ErrLocationConflict) {
		t.Fatalf("location conflict = %v", err)
	}

	menu, err = store.ReplaceItems(ctx, menu.ID, Mutation{ExpectedVersion: menu.Version, Actor: "owner"}, []ItemInput{
		{ID: -10, Label: "首页", URL: "/", Icon: "house", Accent: "#16a89a"},
		{ID: -20, Label: "发现", URL: "/explore/", Presentation: PresentationDropdown},
		{ID: -21, ParentID: -20, Type: TypeCategory, ObjectID: 3, Label: "工程", URL: "/categories/engineering/"},
	})
	if err != nil || menu.Version != 2 || len(menu.Items) != 3 || menu.Items[0].ID <= 0 || menu.Items[2].ParentID != menu.Items[1].ID {
		t.Fatalf("replace items = %#v, %v", menu, err)
	}
	flat := Flatten(menu)
	if len(flat) != 3 || flat[2].Depth != 1 || flat[2].Item.Label != "工程" {
		t.Fatalf("flattened menu = %#v", flat)
	}
	if _, err := store.ReplaceItems(ctx, menu.ID, Mutation{ExpectedVersion: 1}, nil); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale item replace = %v", err)
	}

	menu, err = store.Update(ctx, menu.ID, Mutation{ExpectedVersion: menu.Version, Actor: "owner"}, MenuInput{
		Name: "主导航（更新）", Slug: "main", Description: "已更新", Locations: []string{LocationPrimary, LocationFooter},
	})
	if err != nil || menu.Version != 3 || stringSliceContains(menu.Locations, LocationMobile) {
		t.Fatalf("update menu = %#v, %v", menu, err)
	}
	if _, err := store.GetLocation(ctx, LocationFooter); err != nil {
		t.Fatalf("footer location unavailable: %v", err)
	}

	reloaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reloaded.Get(ctx, menu.ID)
	if err != nil || stored.Version != menu.Version || len(stored.Items) != 3 || stored.Items[2].ParentID != stored.Items[1].ID {
		t.Fatalf("reloaded menu = %#v, %v", stored, err)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		t.Fatalf("navigation file mode = %v, %v", info.Mode(), err)
	}
	snapshot, err := reloaded.Snapshot(ctx)
	if err != nil || snapshot.MenuCount != 1 || snapshot.ItemCount != 3 || snapshot.Locations[LocationFooter] != menu.ID {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	if err := reloaded.Delete(ctx, menu.ID, Mutation{ExpectedVersion: menu.Version, Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.Get(ctx, menu.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted menu lookup = %v", err)
	}
}

func TestFileStoreRejectsDuplicateAndDamagedDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navigation.json")
	duplicate := `{"schema":2,"schema":2,"kind":"newpress/navigation","next_menu_id":1,"next_item_id":1,"updated_at":"2026-08-11T00:00:00Z","menus":[]}`
	if err := os.WriteFile(path, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path, false); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
		t.Fatalf("duplicate document error = %v", err)
	}

	damaged := `{"schema":2,"kind":"newpress/navigation","next_menu_id":2,"next_item_id":2,"updated_at":"2026-08-11T00:00:00Z","menus":[{"id":1,"version":1,"name":"bad","slug":"bad","locations":[],"items":[{"id":1,"menu_id":1,"parent_id":99,"position":0,"type":"custom","label":"bad","url":"/","target":"self","presentation":"standard","created_at":"2026-08-11T00:00:00Z","updated_at":"2026-08-11T00:00:00Z"}],"created_at":"2026-08-11T00:00:00Z","updated_at":"2026-08-11T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path, false); err == nil || !strings.Contains(err.Error(), "failed validation") {
		t.Fatalf("damaged hierarchy error = %v", err)
	}
}

func TestNormalizeRegistryInputsRejectsCrossMenuConflicts(t *testing.T) {
	inputs := []RegistryMenuInput{
		{Menu: MenuInput{Name: "主导航", Slug: "main", Locations: []string{LocationPrimary}}},
		{Menu: MenuInput{Name: "重复", Slug: "MAIN", Locations: []string{LocationPrimary}}},
	}
	_, fields := NormalizeRegistryInputs(inputs)
	if fields["menus.1.slug"] == "" || fields["menus.1.locations"] == "" {
		t.Fatalf("registry conflict fields = %#v", fields)
	}

	tooMany := make([]RegistryMenuInput, MaxMenus+1)
	if _, fields := NormalizeRegistryInputs(tooMany); fields["menus"] == "" {
		t.Fatalf("menu limit fields = %#v", fields)
	}
}

func TestFileStoreReplaceAllIsAtomicAndRevisionProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "navigation.json")
	store, err := NewFileStore(path, true)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision := SnapshotRevision(before)
	inputs := []RegistryMenuInput{
		{
			Menu: MenuInput{Name: "迁移主导航", Slug: "portable-main", Description: "完整替换", Locations: []string{LocationPrimary, LocationMobile}},
			Items: []ItemInput{
				{ID: -1, Type: TypeCustom, Label: "首页", URL: "/", Icon: "house", Accent: "#16A89A", Presentation: PresentationDropdown},
				{ID: -2, ParentID: -1, Type: TypeCategory, ObjectID: 42, Label: "工程", URL: "/categories/engineering/", Presentation: PresentationStandard},
			},
		},
		{
			Menu:  MenuInput{Name: "页脚", Slug: "portable-footer", Locations: []string{LocationFooter}},
			Items: []ItemInput{{ID: -1, Type: TypeCustom, Label: "帮助", URL: "/pages/help", Presentation: PresentationStandard}},
		},
	}
	after, err := store.ReplaceAll(context.Background(), RegistryMutation{ExpectedRevision: beforeRevision, Actor: "owner"}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if after.MenuCount != 2 || after.ItemCount != 3 || SnapshotRevision(after) == beforeRevision {
		t.Fatalf("replacement snapshot = %#v", after)
	}
	main, err := store.GetLocation(context.Background(), LocationPrimary)
	if err != nil || main.Slug != "portable-main" || len(main.Items) != 2 || main.Items[1].ParentID != main.Items[0].ID || main.Items[0].ID <= 0 {
		t.Fatalf("replacement main menu = %#v, %v", main, err)
	}
	if _, err := store.GetLocation(context.Background(), LocationUtility); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed utility location = %v", err)
	}

	staleResult, err := store.ReplaceAll(context.Background(), RegistryMutation{ExpectedRevision: beforeRevision}, nil)
	if !errors.Is(err, ErrSnapshotConflict) || SnapshotRevision(staleResult) != SnapshotRevision(after) {
		t.Fatalf("stale replacement = %#v, %v", staleResult, err)
	}
	invalid := []RegistryMenuInput{
		{Menu: MenuInput{Name: "一", Slug: "one", Locations: []string{LocationPrimary}}},
		{Menu: MenuInput{Name: "二", Slug: "two", Locations: []string{LocationPrimary}}},
	}
	if _, err := store.ReplaceAll(context.Background(), RegistryMutation{ExpectedRevision: SnapshotRevision(after)}, invalid); err == nil || len(FieldErrors(err)) == 0 {
		t.Fatalf("invalid replacement error = %v", err)
	}
	unchanged, _ := store.Snapshot(context.Background())
	if SnapshotRevision(unchanged) != SnapshotRevision(after) {
		t.Fatalf("invalid replacement mutated registry: %#v", unchanged)
	}

	reloaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	reloadedSnapshot, err := reloaded.Snapshot(context.Background())
	if err != nil || SnapshotRevision(reloadedSnapshot) != SnapshotRevision(after) {
		t.Fatalf("reloaded replacement = %#v, %v", reloadedSnapshot, err)
	}
}

func TestSeedDemoProvidesPrimaryUtilityAndMobileLocations(t *testing.T) {
	store, err := NewFileStore("", true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || snapshot.MenuCount != 2 || snapshot.ItemCount != 8 {
		t.Fatalf("seed snapshot = %#v, %v", snapshot, err)
	}
	for _, location := range []string{LocationPrimary, LocationUtility, LocationMobile} {
		menu, err := store.GetLocation(context.Background(), location)
		if err != nil || len(menu.Items) == 0 {
			t.Fatalf("seed location %s = %#v, %v", location, menu, err)
		}
	}
}
