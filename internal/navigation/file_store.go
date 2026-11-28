package navigation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/packagefs"
)

const (
	fileSchema   = 2
	fileKind     = "newpress/navigation"
	maxFileBytes = 8 << 20
)

type fileDocument struct {
	Schema     int               `json:"schema"`
	Kind       string            `json:"kind"`
	NextMenuID int64             `json:"next_menu_id"`
	NextItemID int64             `json:"next_item_id"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Menus      []Menu            `json:"menus"`
	Creations  []CreationReceipt `json:"creations,omitempty"`
}

type FileStore struct {
	mu           fileRWMutex
	path         string
	audit        *audit.Ledger
	auditFault   error
	persistFault error
	creations    []CreationReceipt
	menus        map[int64]Menu
	nextMenuID   int64
	nextItemID   int64
	updatedAt    time.Time
}

func NewFileStore(path string, seedDemo bool, ledgers ...*audit.Ledger) (*FileStore, error) {
	var ledger *audit.Ledger
	if len(ledgers) > 0 {
		ledger = ledgers[0]
	}
	store := &FileStore{path: path, audit: ledger, menus: make(map[int64]Menu), nextMenuID: 1, nextItemID: 1}
	if path == "" {
		if seedDemo {
			store.seedDemoLocked(time.Now().UTC())
		}
		return store, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve navigation store path: %w", err)
	}
	store.path = absolute
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(absolute), 0o750); err != nil {
		return nil, fmt.Errorf("create navigation store directory: %w", err)
	}
	document, err := loadFileDocument(absolute)
	if errors.Is(err, os.ErrNotExist) {
		if seedDemo {
			store.seedDemoLocked(time.Now().UTC())
			event := audit.Event{Action: "navigation.seeded", ObjectType: "navigation_registry", ObjectID: "all", Actor: "system", Metadata: map[string]any{
				"menu_count": len(store.menus), "item_count": store.nextItemID - 1,
			}}
			if err := store.persistLocked(context.Background(), store.menus, store.nextMenuID, store.nextItemID, store.updatedAt, &event); err != nil {
				return nil, err
			}
		}
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := validateFileDocument(document); err != nil {
		return nil, fmt.Errorf("stored navigation is invalid: %w", err)
	}
	store.nextMenuID = document.NextMenuID
	store.nextItemID = document.NextItemID
	store.updatedAt = document.UpdatedAt.UTC()
	store.creations = append([]CreationReceipt(nil), document.Creations...)
	for _, menu := range document.Menus {
		store.menus[menu.ID] = cloneMenu(menu)
	}
	return store, nil
}

func (store *FileStore) Ready(ctx context.Context) error {
	if store == nil {
		return errors.New("navigation store is nil")
	}
	if err := store.mu.RLockContext(ctx); err != nil {
		return err
	}
	defer store.mu.RUnlock()
	if store.persistFault != nil {
		return fmt.Errorf("navigation store write outcome is uncertain; reload required: %w", store.persistFault)
	}
	if store.path == "" {
		return ctx.Err()
	}
	if store.auditFault != nil {
		return fmt.Errorf("navigation store audit failure: %w", store.auditFault)
	}
	if store.audit != nil {
		if err := store.audit.Ready(); err != nil {
			return fmt.Errorf("navigation store audit is not ready: %w", err)
		}
	}
	info, err := packagefs.LstatNoSymlink(filepath.Dir(store.path))
	if err != nil {
		return fmt.Errorf("inspect navigation store directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("navigation store parent is not a directory")
	}
	return ctx.Err()
}

func (store *FileStore) List(ctx context.Context) ([]Menu, error) {
	if store == nil {
		return nil, errors.New("navigation store is nil")
	}
	if err := store.mu.RLockContext(ctx); err != nil {
		return nil, err
	}
	defer store.mu.RUnlock()
	menus := store.menusLocked()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return menus, nil
}

func (store *FileStore) Get(ctx context.Context, id int64) (Menu, error) {
	if store == nil || id <= 0 {
		return Menu{}, ErrNotFound
	}
	if err := store.mu.RLockContext(ctx); err != nil {
		return Menu{}, err
	}
	defer store.mu.RUnlock()
	menu, ok := store.menus[id]
	if !ok {
		return Menu{}, ErrNotFound
	}
	menu = cloneMenu(menu)
	if err := ctx.Err(); err != nil {
		return Menu{}, err
	}
	return menu, nil
}

func (store *FileStore) GetLocation(ctx context.Context, location string) (Menu, error) {
	location = strings.ToLower(strings.TrimSpace(location))
	if store == nil || !ValidLocation(location) {
		return Menu{}, ErrNotFound
	}
	if err := store.mu.RLockContext(ctx); err != nil {
		return Menu{}, err
	}
	defer store.mu.RUnlock()
	for _, menu := range store.menus {
		if stringSliceContains(menu.Locations, location) {
			menu = cloneMenu(menu)
			if err := ctx.Err(); err != nil {
				return Menu{}, err
			}
			return menu, nil
		}
	}
	return Menu{}, ErrNotFound
}

func (store *FileStore) Create(ctx context.Context, input MenuInput, actor string) (Menu, error) {
	if store == nil {
		return Menu{}, errors.New("navigation store is nil")
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return Menu{}, err
	}
	defer store.mu.Unlock()
	return store.createLocked(ctx, input, actor, nil)
}

func (store *FileStore) createLocked(ctx context.Context, input MenuInput, actor string, receipt *CreationReceipt) (Menu, error) {
	if err := ctx.Err(); err != nil {
		return Menu{}, err
	}
	normalized, fields := NormalizeMenuInput(input)
	if len(fields) > 0 {
		return Menu{}, &ValidationError{Fields: fields}
	}
	if len(store.menus) >= MaxMenus {
		return Menu{}, &ValidationError{Fields: map[string]string{"name": fmt.Sprintf("最多只能创建 %d 个菜单", MaxMenus)}}
	}
	if err := store.checkMenuConflictsLocked(0, normalized); err != nil {
		return Menu{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	menu := Menu{
		ID: store.nextMenuID, Version: 1, Name: normalized.Name, Slug: normalized.Slug,
		Description: normalized.Description, Locations: append([]string(nil), normalized.Locations...),
		Items: []Item{}, CreatedAt: now, UpdatedAt: now,
	}
	candidate := cloneMenuMap(store.menus)
	candidate[menu.ID] = menu
	event := audit.Event{Action: "navigation.menu.create", ObjectType: "navigation_menu", ObjectID: fmt.Sprint(menu.ID), Actor: auditActor(actor), Metadata: map[string]any{
		"version": menu.Version, "location_count": len(menu.Locations), "item_count": len(menu.Items),
	}}
	receipts := store.creations
	if receipt != nil {
		receipt.MenuID, receipt.CreatedAt = menu.ID, now
		receipts = append(append([]CreationReceipt(nil), receipts...), *receipt)
	}
	if err := store.persistStateLocked(ctx, candidate, store.nextMenuID+1, store.nextItemID, now, receipts, &event); err != nil {
		return Menu{}, err
	}
	store.menus = candidate
	store.nextMenuID++
	store.updatedAt = now
	store.creations = receipts
	return cloneMenu(menu), nil
}

func (store *FileStore) Update(ctx context.Context, id int64, mutation Mutation, input MenuInput) (Menu, error) {
	if store == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return Menu{}, ErrInvalidInput
	}
	normalized, fields := NormalizeMenuInput(input)
	if len(fields) > 0 {
		return Menu{}, &ValidationError{Fields: fields}
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return Menu{}, err
	}
	defer store.mu.Unlock()
	menu, ok := store.menus[id]
	if !ok {
		return Menu{}, ErrNotFound
	}
	if menu.Version != mutation.ExpectedVersion {
		return Menu{}, ErrVersionConflict
	}
	if err := store.checkMenuConflictsLocked(id, normalized); err != nil {
		return Menu{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	menu.Name = normalized.Name
	menu.Slug = normalized.Slug
	menu.Description = normalized.Description
	menu.Locations = append([]string(nil), normalized.Locations...)
	menu.Version++
	menu.UpdatedAt = now
	candidate := cloneMenuMap(store.menus)
	candidate[id] = cloneMenu(menu)
	event := audit.Event{Action: "navigation.menu.update", ObjectType: "navigation_menu", ObjectID: fmt.Sprint(id), Actor: auditActor(mutation.Actor), Metadata: map[string]any{
		"from_version": mutation.ExpectedVersion, "version": menu.Version, "location_count": len(menu.Locations), "item_count": len(menu.Items),
	}}
	if err := store.persistLocked(ctx, candidate, store.nextMenuID, store.nextItemID, now, &event); err != nil {
		return Menu{}, err
	}
	store.menus = candidate
	store.updatedAt = now
	return cloneMenu(menu), nil
}

func (store *FileStore) ReplaceItems(ctx context.Context, id int64, mutation Mutation, inputs []ItemInput) (Menu, error) {
	if store == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return Menu{}, ErrInvalidInput
	}
	normalized, fields := NormalizeItemInputs(inputs)
	if len(fields) > 0 {
		return Menu{}, &ValidationError{Fields: fields}
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return Menu{}, err
	}
	defer store.mu.Unlock()
	menu, ok := store.menus[id]
	if !ok {
		return Menu{}, ErrNotFound
	}
	if menu.Version != mutation.ExpectedVersion {
		return Menu{}, ErrVersionConflict
	}
	existing := make(map[int64]Item, len(menu.Items))
	for _, item := range menu.Items {
		existing[item.ID] = item
	}
	for index, item := range normalized {
		if item.ID > 0 {
			if _, exists := existing[item.ID]; !exists {
				return Menu{}, &ValidationError{Fields: map[string]string{fmt.Sprintf("items.%d.id", index): "菜单项目不属于当前菜单"}}
			}
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	nextItemID := store.nextItemID
	identities := make(map[int64]int64, len(normalized))
	for _, input := range normalized {
		if input.ID > 0 {
			identities[input.ID] = input.ID
			continue
		}
		identities[input.ID] = nextItemID
		nextItemID++
	}
	items := make([]Item, 0, len(normalized))
	for position, input := range normalized {
		itemID := identities[input.ID]
		createdAt := now
		if current, exists := existing[itemID]; exists {
			createdAt = current.CreatedAt
		}
		parentID := int64(0)
		if input.ParentID != 0 {
			parentID = identities[input.ParentID]
		}
		items = append(items, Item{
			ID: itemID, MenuID: id, ParentID: parentID, Position: position,
			Type: input.Type, ObjectID: input.ObjectID, Label: input.Label, URL: input.URL,
			Description: input.Description, Target: input.Target, Rel: input.Rel, Icon: input.Icon,
			Accent: input.Accent, Presentation: input.Presentation, Badge: input.Badge, CreatedAt: createdAt, UpdatedAt: now,
		})
	}
	menu.Items = items
	menu.Version++
	menu.UpdatedAt = now
	candidate := cloneMenuMap(store.menus)
	candidate[id] = cloneMenu(menu)
	event := audit.Event{Action: "navigation.items.replace", ObjectType: "navigation_menu", ObjectID: fmt.Sprint(id), Actor: auditActor(mutation.Actor), Metadata: map[string]any{
		"from_version": mutation.ExpectedVersion, "version": menu.Version, "previous_item_count": len(existing), "item_count": len(items),
	}}
	if err := store.persistLocked(ctx, candidate, store.nextMenuID, nextItemID, now, &event); err != nil {
		return Menu{}, err
	}
	store.menus = candidate
	store.nextItemID = nextItemID
	store.updatedAt = now
	return cloneMenu(menu), nil
}

func (store *FileStore) Delete(ctx context.Context, id int64, mutation Mutation) error {
	if store == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return ErrInvalidInput
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return err
	}
	defer store.mu.Unlock()
	menu, ok := store.menus[id]
	if !ok {
		return ErrNotFound
	}
	if menu.Version != mutation.ExpectedVersion {
		return ErrVersionConflict
	}
	candidate := cloneMenuMap(store.menus)
	delete(candidate, id)
	now := time.Now().UTC().Truncate(time.Second)
	event := audit.Event{Action: "navigation.menu.delete", ObjectType: "navigation_menu", ObjectID: fmt.Sprint(id), Actor: auditActor(mutation.Actor), Metadata: map[string]any{
		"version": mutation.ExpectedVersion, "item_count": len(menu.Items), "location_count": len(menu.Locations),
	}}
	if err := store.persistLocked(ctx, candidate, store.nextMenuID, store.nextItemID, now, &event); err != nil {
		return err
	}
	store.menus = candidate
	store.updatedAt = now
	return nil
}

func (store *FileStore) Snapshot(ctx context.Context) (Snapshot, error) {
	if store == nil {
		return Snapshot{}, errors.New("navigation store is nil")
	}
	if err := store.mu.RLockContext(ctx); err != nil {
		return Snapshot{}, err
	}
	defer store.mu.RUnlock()
	menus := store.menusLocked()
	snapshot := snapshotFromMenus(menus, store.updatedAt)
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (store *FileStore) ReplaceAll(ctx context.Context, mutation RegistryMutation, inputs []RegistryMenuInput) (Snapshot, error) {
	if store == nil || strings.TrimSpace(mutation.ExpectedRevision) == "" {
		return Snapshot{}, ErrInvalidInput
	}
	normalized, fields := NormalizeRegistryInputs(inputs)
	if len(fields) > 0 {
		return Snapshot{}, &ValidationError{Fields: fields}
	}
	if err := store.mu.LockContext(ctx); err != nil {
		return Snapshot{}, err
	}
	defer store.mu.Unlock()
	current := snapshotFromMenus(store.menusLocked(), store.updatedAt)
	if SnapshotRevision(current) != mutation.ExpectedRevision {
		return current, ErrSnapshotConflict
	}
	now := time.Now().UTC().Truncate(time.Second)
	nextMenuID := store.nextMenuID
	nextItemID := store.nextItemID
	candidate := make(map[int64]Menu, len(normalized))
	for _, input := range normalized {
		menuID := nextMenuID
		nextMenuID++
		identities := make(map[int64]int64, len(input.Items))
		for _, item := range input.Items {
			identities[item.ID] = nextItemID
			nextItemID++
		}
		items := make([]Item, 0, len(input.Items))
		for position, item := range input.Items {
			parentID := int64(0)
			if item.ParentID != 0 {
				parentID = identities[item.ParentID]
			}
			items = append(items, Item{
				ID: identities[item.ID], MenuID: menuID, ParentID: parentID, Position: position,
				Type: item.Type, ObjectID: item.ObjectID, Label: item.Label, URL: item.URL,
				Description: item.Description, Target: item.Target, Rel: item.Rel, Icon: item.Icon,
				Accent: item.Accent, Presentation: item.Presentation, Badge: item.Badge, CreatedAt: now, UpdatedAt: now,
			})
		}
		candidate[menuID] = Menu{
			ID: menuID, Version: 1, Name: input.Menu.Name, Slug: input.Menu.Slug,
			Description: input.Menu.Description, Locations: append([]string(nil), input.Menu.Locations...),
			Items: items, CreatedAt: now, UpdatedAt: now,
		}
	}
	itemCount := 0
	for _, menu := range candidate {
		itemCount += len(menu.Items)
	}
	event := audit.Event{Action: "navigation.registry.replace", ObjectType: "navigation_registry", ObjectID: "all", Actor: auditActor(mutation.Actor), Metadata: map[string]any{
		"previous_menu_count": current.MenuCount, "previous_item_count": current.ItemCount, "menu_count": len(candidate), "item_count": itemCount,
	}}
	if err := store.persistLocked(ctx, candidate, nextMenuID, nextItemID, now, &event); err != nil {
		return current, err
	}
	store.menus = candidate
	store.nextMenuID = nextMenuID
	store.nextItemID = nextItemID
	store.updatedAt = now
	return snapshotFromMenus(store.menusLocked(), store.updatedAt), nil
}

func (store *FileStore) checkMenuConflictsLocked(editingID int64, input MenuInput) error {
	for id, menu := range store.menus {
		if id == editingID {
			continue
		}
		if strings.EqualFold(menu.Slug, input.Slug) {
			return ErrSlugConflict
		}
		for _, location := range input.Locations {
			if stringSliceContains(menu.Locations, location) {
				return fmt.Errorf("%w: %s", ErrLocationConflict, location)
			}
		}
	}
	return nil
}

func (store *FileStore) menusLocked() []Menu {
	menus := make([]Menu, 0, len(store.menus))
	for _, menu := range store.menus {
		menus = append(menus, cloneMenu(menu))
	}
	SortMenus(menus)
	return menus
}

func (store *FileStore) seedDemoLocked(now time.Time) {
	now = now.UTC().Truncate(time.Second)
	primary := Menu{
		ID: 1, Version: 1, Name: "站点主导航", Slug: "primary-navigation",
		Description: "桌面主导航与移动底栏共用的核心入口。", Locations: []string{LocationPrimary, LocationMobile},
		CreatedAt: now, UpdatedAt: now,
	}
	items := []Item{
		{ID: 1, Label: "首页", URL: "/", Type: TypeCustom, Icon: "house", Accent: "#2563EB", Presentation: PresentationStandard},
		{ID: 2, Label: "发现", URL: "/explore/", Type: TypeCustom, Icon: "compass", Accent: "#4E7CF6", Presentation: PresentationDropdown},
		{ID: 3, ParentID: 2, Label: "项目日志", URL: "/explore/?category=项目日志", Type: TypeCustom, Icon: "newspaper", Presentation: PresentationStandard},
		{ID: 4, ParentID: 2, Label: "主题设计", URL: "/explore/?category=主题设计", Type: TypeCustom, Icon: "diamond", Presentation: PresentationStandard},
		{ID: 5, ParentID: 2, Label: "扩展架构", URL: "/explore/?category=扩展架构", Type: TypeCustom, Icon: "code-2", Presentation: PresentationStandard},
		{ID: 6, Label: "圈子", URL: "/community/", Type: TypeCustom, Icon: "users", Accent: "#8D6BE8", Presentation: PresentationStandard},
	}
	for index := range items {
		items[index].MenuID = primary.ID
		items[index].Position = index
		items[index].Target = TargetSelf
		items[index].CreatedAt = now
		items[index].UpdatedAt = now
	}
	primary.Items = items
	utility := Menu{
		ID: 2, Version: 1, Name: "顶部快捷入口", Slug: "utility-navigation",
		Description: "品牌区旁的紧凑业务入口。", Locations: []string{LocationUtility},
		CreatedAt: now, UpdatedAt: now,
		Items: []Item{
			{ID: 9, MenuID: 2, Position: 0, Type: TypeCustom, Label: "圈子", URL: "/community/", Target: TargetSelf, Presentation: PresentationStandard, CreatedAt: now, UpdatedAt: now},
			{ID: 11, MenuID: 2, Position: 2, Type: TypeCustom, Label: "问答", URL: "/questions/", Target: TargetSelf, Presentation: PresentationStandard, CreatedAt: now, UpdatedAt: now},
		},
	}
	store.menus[primary.ID] = primary
	store.menus[utility.ID] = utility
	store.nextMenuID = 3
	store.nextItemID = 13
	store.updatedAt = now
}

func (store *FileStore) persistLocked(ctx context.Context, menus map[int64]Menu, nextMenuID, nextItemID int64, updatedAt time.Time, event *audit.Event) error {
	return store.persistStateLocked(ctx, menus, nextMenuID, nextItemID, updatedAt, store.creations, event)
}

func (store *FileStore) persistStateLocked(ctx context.Context, menus map[int64]Menu, nextMenuID, nextItemID int64, updatedAt time.Time, receipts []CreationReceipt, event *audit.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store.persistFault != nil {
		return store.persistFault
	}
	if store.path == "" {
		return nil
	}
	document := fileDocument{
		Schema: fileSchema, Kind: fileKind, NextMenuID: nextMenuID, NextItemID: nextItemID,
		UpdatedAt: updatedAt, Menus: make([]Menu, 0, len(menus)), Creations: receipts,
	}
	for _, menu := range menus {
		document.Menus = append(document.Menus, cloneMenu(menu))
	}
	SortMenus(document.Menus)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(true)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("encode navigation document: %w", err)
	}
	if encoded.Len() > maxFileBytes {
		return errors.New("navigation document exceeds size limit")
	}
	// Cancellation is safe before durable work begins. Once the audit/file
	// transaction starts, finish it and publish the matching memory state;
	// reporting cancellation after a successful commit would hide that write.
	if err := ctx.Err(); err != nil {
		return err
	}
	payload := encoded.Bytes()
	write := func() error { return writeFileBytes(store.path, payload) }
	if store.audit == nil || event == nil {
		err := write()
		if err != nil {
			// Replacement may have succeeded before permissions/sync failed.
			// Never overwrite that durable state using stale in-memory facts.
			store.persistFault = err
		}
		return err
	}
	if _, err := store.audit.ApplyFile(audit.FactNavigation, *event, payload, store.path, write); err != nil {
		store.auditFault = err
		store.persistFault = err
		return err
	}
	return nil
}

func writeFileBytes(path string, payload []byte) error {
	directory := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(directory, 0o750); err != nil {
		return fmt.Errorf("inspect navigation store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".newpress-navigation-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary navigation document: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary navigation document: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary navigation document: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary navigation document: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary navigation document: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	if err := packagefs.ChmodRegularNoSymlink(path, 0o600); err != nil {
		return fmt.Errorf("protect navigation document: %w", err)
	}
	if runtime.GOOS != "windows" {
		if handle, err := packagefs.OpenDirectoryNoSymlink(directory); err == nil {
			_ = handle.Sync()
			_ = handle.Close()
		}
	}
	return nil
}

func auditActor(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || len([]rune(value)) > 160 || containsControl(value) {
		return "system"
	}
	return value
}

func loadFileDocument(path string) (fileDocument, error) {
	file, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		file, _, err = packagefs.OpenRegularNoSymlink(path + ".bak")
	}
	if err != nil {
		return fileDocument{}, err
	}
	defer file.Close()
	payload, err := io.ReadAll(&io.LimitedReader{R: file, N: maxFileBytes + 1})
	if err != nil {
		return fileDocument{}, fmt.Errorf("read navigation document: %w", err)
	}
	if len(payload) == 0 || len(payload) > maxFileBytes {
		return fileDocument{}, errors.New("navigation document size is invalid")
	}
	if err := packagefs.ValidateUniqueJSONFor(payload, &fileDocument{}); err != nil {
		return fileDocument{}, fmt.Errorf("validate navigation document JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document fileDocument
	if err := decoder.Decode(&document); err != nil {
		return fileDocument{}, fmt.Errorf("decode navigation document: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fileDocument{}, err
	}
	return document, nil
}

func validateFileDocument(document fileDocument) error {
	if document.Schema != fileSchema || document.Kind != fileKind || document.NextMenuID <= 0 || document.NextItemID <= 0 || document.UpdatedAt.IsZero() {
		return errors.New("unsupported navigation document metadata")
	}
	if err := validateCreationReceipts(document.Creations, document.NextMenuID); err != nil {
		return err
	}
	if len(document.Menus) > MaxMenus {
		return errors.New("stored navigation exceeds menu limit")
	}
	menuIDs := make(map[int64]struct{}, len(document.Menus))
	itemIDs := make(map[int64]struct{})
	slugs := make(map[string]struct{}, len(document.Menus))
	locations := make(map[string]int64)
	for _, menu := range document.Menus {
		if menu.ID <= 0 || menu.ID >= document.NextMenuID || menu.Version <= 0 || menu.CreatedAt.IsZero() || menu.UpdatedAt.IsZero() {
			return errors.New("invalid stored menu metadata")
		}
		if _, exists := menuIDs[menu.ID]; exists {
			return fmt.Errorf("duplicate stored menu id %d", menu.ID)
		}
		menuIDs[menu.ID] = struct{}{}
		normalized, fields := NormalizeMenuInput(MenuInput{Name: menu.Name, Slug: menu.Slug, Description: menu.Description, Locations: menu.Locations})
		if len(fields) > 0 || normalized.Name != menu.Name || normalized.Slug != menu.Slug || normalized.Description != menu.Description || !equalStrings(normalized.Locations, menu.Locations) {
			return fmt.Errorf("stored menu %d failed validation", menu.ID)
		}
		if _, exists := slugs[strings.ToLower(menu.Slug)]; exists {
			return fmt.Errorf("duplicate stored menu slug %q", menu.Slug)
		}
		slugs[strings.ToLower(menu.Slug)] = struct{}{}
		for _, location := range menu.Locations {
			if owner := locations[location]; owner != 0 {
				return fmt.Errorf("stored location %s belongs to menus %d and %d", location, owner, menu.ID)
			}
			locations[location] = menu.ID
		}
		inputs := make([]ItemInput, 0, len(menu.Items))
		for _, item := range menu.Items {
			if item.ID <= 0 || item.ID >= document.NextItemID || item.MenuID != menu.ID || item.Position < 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
				return fmt.Errorf("invalid stored item metadata in menu %d", menu.ID)
			}
			if _, exists := itemIDs[item.ID]; exists {
				return fmt.Errorf("duplicate stored item id %d", item.ID)
			}
			itemIDs[item.ID] = struct{}{}
			inputs = append(inputs, ItemInput{
				ID: item.ID, ParentID: item.ParentID, Type: item.Type, ObjectID: item.ObjectID,
				Label: item.Label, URL: item.URL, Description: item.Description, Target: item.Target,
				Rel: item.Rel, Icon: item.Icon, Accent: item.Accent, Presentation: item.Presentation, Badge: item.Badge,
			})
		}
		normalizedItems, itemFields := NormalizeItemInputs(inputs)
		if len(itemFields) > 0 || len(normalizedItems) != len(menu.Items) {
			return fmt.Errorf("stored items in menu %d failed validation", menu.ID)
		}
		for index, item := range menu.Items {
			if !itemInputMatches(normalizedItems[index], item) {
				return fmt.Errorf("stored item %d is not canonical", item.ID)
			}
		}
	}
	return nil
}

func snapshotFromMenus(menus []Menu, updatedAt time.Time) Snapshot {
	locations := make(map[string]int64)
	itemCount := 0
	for _, menu := range menus {
		itemCount += len(menu.Items)
		for _, location := range menu.Locations {
			locations[location] = menu.ID
		}
	}
	return Snapshot{Menus: menus, Locations: locations, MenuCount: len(menus), ItemCount: itemCount, UpdatedAt: updatedAt}
}

func cloneMenu(menu Menu) Menu {
	menu.Locations = append([]string(nil), menu.Locations...)
	menu.Items = append([]Item(nil), menu.Items...)
	return menu
}

func cloneMenuMap(source map[int64]Menu) map[int64]Menu {
	result := make(map[int64]Menu, len(source))
	for id, menu := range source {
		result[id] = cloneMenu(menu)
	}
	return result
}

func stringSliceContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func itemInputMatches(input ItemInput, item Item) bool {
	return input.ID == item.ID && input.ParentID == item.ParentID && input.Type == item.Type && input.ObjectID == item.ObjectID &&
		input.Label == item.Label && input.URL == item.URL && input.Description == item.Description && input.Target == item.Target &&
		input.Rel == item.Rel && input.Icon == item.Icon && input.Accent == item.Accent && input.Presentation == item.Presentation && input.Badge == item.Badge
}

func replaceFile(temporaryPath, target string) error {
	return packagefs.ReplaceRegularFileNoSymlink(temporaryPath, target)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("unexpected trailing JSON token %v", token)
	}
	return nil
}

var _ Store = (*FileStore)(nil)
