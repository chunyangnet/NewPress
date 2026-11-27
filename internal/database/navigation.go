package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/navigation"
)

type NavigationRepository struct {
	pool *pgxpool.Pool
}

func NewNavigationRepository(pool *pgxpool.Pool) *NavigationRepository {
	return &NavigationRepository{pool: pool}
}

func (repository *NavigationRepository) Ready(ctx context.Context) error {
	if repository == nil || repository.pool == nil {
		return errors.New("navigation repository is nil")
	}
	return repository.pool.Ping(ctx)
}

func (repository *NavigationRepository) List(ctx context.Context) ([]navigation.Menu, error) {
	if repository == nil || repository.pool == nil {
		return nil, errors.New("navigation repository is nil")
	}
	return loadNavigationMenus(ctx, repository.pool)
}

func (repository *NavigationRepository) Get(ctx context.Context, id int64) (navigation.Menu, error) {
	if repository == nil || repository.pool == nil || id <= 0 {
		return navigation.Menu{}, navigation.ErrNotFound
	}
	return loadNavigationMenu(ctx, repository.pool, id)
}

func (repository *NavigationRepository) GetLocation(ctx context.Context, location string) (navigation.Menu, error) {
	location = strings.ToLower(strings.TrimSpace(location))
	if repository == nil || repository.pool == nil || !navigation.ValidLocation(location) {
		return navigation.Menu{}, navigation.ErrNotFound
	}
	var id int64
	if err := repository.pool.QueryRow(ctx, `SELECT menu_id FROM np_navigation_locations WHERE location=$1`, location).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return navigation.Menu{}, navigation.ErrNotFound
	} else if err != nil {
		return navigation.Menu{}, fmt.Errorf("resolve navigation location: %w", err)
	}
	return loadNavigationMenu(ctx, repository.pool, id)
}

func (repository *NavigationRepository) Create(ctx context.Context, input navigation.MenuInput, actor string) (navigation.Menu, error) {
	if repository == nil || repository.pool == nil {
		return navigation.Menu{}, errors.New("navigation repository is nil")
	}
	normalized, fields := navigation.NormalizeMenuInput(input)
	if len(fields) > 0 {
		return navigation.Menu{}, &navigation.ValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return navigation.Menu{}, fmt.Errorf("begin create navigation menu: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-navigation-menus',0))`); err != nil {
		return navigation.Menu{}, fmt.Errorf("lock navigation registry: %w", err)
	}
	menu, err := createNavigationInTransaction(ctx, tx, normalized, actor, time.Now().UTC())
	if err != nil {
		return navigation.Menu{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return navigation.Menu{}, fmt.Errorf("commit navigation menu: %w", err)
	}
	return menu, nil
}

// The caller holds the navigation registry lock and owns the transaction.
func createNavigationInTransaction(ctx context.Context, tx pgx.Tx, normalized navigation.MenuInput, actor string, now time.Time) (navigation.Menu, error) {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM np_navigation_menus`).Scan(&count); err != nil {
		return navigation.Menu{}, fmt.Errorf("count navigation menus: %w", err)
	}
	if count >= navigation.MaxMenus {
		return navigation.Menu{}, &navigation.ValidationError{Fields: map[string]string{"name": fmt.Sprintf("最多只能创建 %d 个菜单", navigation.MaxMenus)}}
	}
	menu, err := scanNavigationMenu(tx.QueryRow(ctx, `INSERT INTO np_navigation_menus(name,slug,description,version,created_at,updated_at)
		VALUES ($1,$2,$3,1,$4,$4) RETURNING `+navigationMenuColumns,
		normalized.Name, normalized.Slug, normalized.Description, now))
	if err != nil {
		return navigation.Menu{}, translateNavigationError(err)
	}
	if err := replaceNavigationLocations(ctx, tx, menu.ID, normalized.Locations, now); err != nil {
		return navigation.Menu{}, err
	}
	menu.Locations = append([]string(nil), normalized.Locations...)
	menu.Items = []navigation.Item{}
	if err := recordNavigationAudit(ctx, tx, actor, "navigation.menu.create", menu.ID, map[string]any{"slug": menu.Slug, "locations": menu.Locations}); err != nil {
		return navigation.Menu{}, err
	}
	return menu, nil
}

func (repository *NavigationRepository) Update(ctx context.Context, id int64, mutation navigation.Mutation, input navigation.MenuInput) (navigation.Menu, error) {
	if repository == nil || repository.pool == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return navigation.Menu{}, navigation.ErrInvalidInput
	}
	normalized, fields := navigation.NormalizeMenuInput(input)
	if len(fields) > 0 {
		return navigation.Menu{}, &navigation.ValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return navigation.Menu{}, fmt.Errorf("begin update navigation menu: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := lockNavigationMenu(ctx, tx, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	if current.Version != mutation.ExpectedVersion {
		return navigation.Menu{}, navigation.ErrVersionConflict
	}
	now := time.Now().UTC()
	menu, err := scanNavigationMenu(tx.QueryRow(ctx, `UPDATE np_navigation_menus SET
		name=$2,slug=$3,description=$4,version=version+1,updated_at=$5
		WHERE id=$1 AND version=$6 RETURNING `+navigationMenuColumns,
		id, normalized.Name, normalized.Slug, normalized.Description, now, mutation.ExpectedVersion))
	if err != nil {
		return navigation.Menu{}, translateNavigationError(err)
	}
	if err := replaceNavigationLocations(ctx, tx, id, normalized.Locations, now); err != nil {
		return navigation.Menu{}, err
	}
	menu.Locations = append([]string(nil), normalized.Locations...)
	menu.Items, err = loadNavigationItems(ctx, tx, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	if err := recordNavigationAudit(ctx, tx, mutation.Actor, "navigation.menu.update", id, map[string]any{
		"from_version": current.Version, "version": menu.Version, "slug": menu.Slug, "locations": menu.Locations,
	}); err != nil {
		return navigation.Menu{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return navigation.Menu{}, fmt.Errorf("commit navigation update: %w", err)
	}
	return menu, nil
}

func (repository *NavigationRepository) ReplaceItems(ctx context.Context, id int64, mutation navigation.Mutation, inputs []navigation.ItemInput) (navigation.Menu, error) {
	if repository == nil || repository.pool == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return navigation.Menu{}, navigation.ErrInvalidInput
	}
	normalized, fields := navigation.NormalizeItemInputs(inputs)
	if len(fields) > 0 {
		return navigation.Menu{}, &navigation.ValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return navigation.Menu{}, fmt.Errorf("begin replace navigation items: %w", err)
	}
	defer tx.Rollback(ctx)
	current, err := lockNavigationMenu(ctx, tx, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	if current.Version != mutation.ExpectedVersion {
		return navigation.Menu{}, navigation.ErrVersionConflict
	}
	existingItems, err := loadNavigationItems(ctx, tx, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	existing := make(map[int64]navigation.Item, len(existingItems))
	for _, item := range existingItems {
		existing[item.ID] = item
	}
	for index, input := range normalized {
		if input.ID > 0 {
			if _, exists := existing[input.ID]; !exists {
				return navigation.Menu{}, &navigation.ValidationError{Fields: map[string]string{fmt.Sprintf("items.%d.id", index): "菜单项目不属于当前菜单"}}
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE np_navigation_items SET parent_id=NULL WHERE menu_id=$1`, id); err != nil {
		return navigation.Menu{}, fmt.Errorf("detach navigation hierarchy: %w", err)
	}
	now := time.Now().UTC()
	identities := make(map[int64]int64, len(normalized))
	wanted := make(map[int64]struct{}, len(normalized))
	for position, input := range normalized {
		objectID := nullablePositiveID(input.ObjectID)
		if input.ID > 0 {
			if _, err := tx.Exec(ctx, `UPDATE np_navigation_items SET
				position=$3,item_type=$4,object_id=$5,label=$6,url=$7,description=$8,target=$9,rel=$10,icon=$11,accent=$12,presentation=$13,updated_at=$14,badge=$15
				WHERE id=$1 AND menu_id=$2`, input.ID, id, position, input.Type, objectID, input.Label, input.URL,
				input.Description, input.Target, input.Rel, input.Icon, input.Accent, input.Presentation, now, input.Badge); err != nil {
				return navigation.Menu{}, fmt.Errorf("update navigation item: %w", err)
			}
			identities[input.ID] = input.ID
			wanted[input.ID] = struct{}{}
			continue
		}
		var itemID int64
		if err := tx.QueryRow(ctx, `INSERT INTO np_navigation_items(
			menu_id,parent_id,position,item_type,object_id,label,url,description,target,rel,icon,accent,presentation,created_at,updated_at,badge
		) VALUES ($1,NULL,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,$14) RETURNING id`,
			id, position, input.Type, objectID, input.Label, input.URL, input.Description, input.Target,
			input.Rel, input.Icon, input.Accent, input.Presentation, now, input.Badge).Scan(&itemID); err != nil {
			return navigation.Menu{}, fmt.Errorf("insert navigation item: %w", err)
		}
		identities[input.ID] = itemID
		wanted[itemID] = struct{}{}
	}
	for itemID := range existing {
		if _, keep := wanted[itemID]; keep {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM np_navigation_items WHERE id=$1 AND menu_id=$2`, itemID, id); err != nil {
			return navigation.Menu{}, fmt.Errorf("delete removed navigation item: %w", err)
		}
	}
	for _, input := range normalized {
		if input.ParentID == 0 {
			continue
		}
		itemID := identities[input.ID]
		parentID := identities[input.ParentID]
		if _, err := tx.Exec(ctx, `UPDATE np_navigation_items SET parent_id=$3 WHERE id=$1 AND menu_id=$2`, itemID, id, parentID); err != nil {
			return navigation.Menu{}, fmt.Errorf("restore navigation hierarchy: %w", err)
		}
	}
	menu, err := scanNavigationMenu(tx.QueryRow(ctx, `UPDATE np_navigation_menus SET version=version+1,updated_at=$2
		WHERE id=$1 AND version=$3 RETURNING `+navigationMenuColumns, id, now, mutation.ExpectedVersion))
	if err != nil {
		return navigation.Menu{}, translateNavigationError(err)
	}
	menu.Locations, err = loadNavigationLocations(ctx, tx, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	menu.Items, err = loadNavigationItems(ctx, tx, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	if err := recordNavigationAudit(ctx, tx, mutation.Actor, "navigation.items.replace", id, map[string]any{
		"from_version": current.Version, "version": menu.Version, "item_count": len(menu.Items),
	}); err != nil {
		return navigation.Menu{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return navigation.Menu{}, fmt.Errorf("commit navigation items: %w", err)
	}
	return menu, nil
}

func (repository *NavigationRepository) ReplaceAll(ctx context.Context, mutation navigation.RegistryMutation, inputs []navigation.RegistryMenuInput) (navigation.Snapshot, error) {
	if repository == nil || repository.pool == nil || strings.TrimSpace(mutation.ExpectedRevision) == "" {
		return navigation.Snapshot{}, navigation.ErrInvalidInput
	}
	normalized, fields := navigation.NormalizeRegistryInputs(inputs)
	if len(fields) > 0 {
		return navigation.Snapshot{}, &navigation.ValidationError{Fields: fields}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return navigation.Snapshot{}, fmt.Errorf("begin replace navigation registry: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('newpress-navigation-menus',0))`); err != nil {
		return navigation.Snapshot{}, fmt.Errorf("lock navigation registry: %w", err)
	}
	if err := lockNavigationRegistry(ctx, tx); err != nil {
		return navigation.Snapshot{}, err
	}
	currentMenus, err := loadNavigationMenus(ctx, tx)
	if err != nil {
		return navigation.Snapshot{}, err
	}
	current := navigationSnapshotFromMenus(currentMenus)
	if navigation.SnapshotRevision(current) != mutation.ExpectedRevision {
		return current, navigation.ErrSnapshotConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM np_navigation_menus`); err != nil {
		return current, fmt.Errorf("clear navigation registry: %w", err)
	}
	now := time.Now().UTC()
	for _, input := range normalized {
		menu, err := scanNavigationMenu(tx.QueryRow(ctx, `INSERT INTO np_navigation_menus(name,slug,description,version,created_at,updated_at)
			VALUES ($1,$2,$3,1,$4,$4) RETURNING `+navigationMenuColumns,
			input.Menu.Name, input.Menu.Slug, input.Menu.Description, now))
		if err != nil {
			return current, translateNavigationError(err)
		}
		if err := replaceNavigationLocations(ctx, tx, menu.ID, input.Menu.Locations, now); err != nil {
			return current, err
		}
		identities := make(map[int64]int64, len(input.Items))
		for position, item := range input.Items {
			var itemID int64
			if err := tx.QueryRow(ctx, `INSERT INTO np_navigation_items(
				menu_id,parent_id,position,item_type,object_id,label,url,description,target,rel,icon,accent,presentation,created_at,updated_at,badge
			) VALUES ($1,NULL,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13,$14) RETURNING id`,
				menu.ID, position, item.Type, nullablePositiveID(item.ObjectID), item.Label, item.URL,
				item.Description, item.Target, item.Rel, item.Icon, item.Accent, item.Presentation, now, item.Badge).Scan(&itemID); err != nil {
				return current, fmt.Errorf("insert imported navigation item: %w", err)
			}
			identities[item.ID] = itemID
		}
		for _, item := range input.Items {
			if item.ParentID == 0 {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE np_navigation_items SET parent_id=$3 WHERE id=$1 AND menu_id=$2`,
				identities[item.ID], menu.ID, identities[item.ParentID]); err != nil {
				return current, fmt.Errorf("restore imported navigation hierarchy: %w", err)
			}
		}
	}
	resultMenus, err := loadNavigationMenus(ctx, tx)
	if err != nil {
		return current, err
	}
	result := navigationSnapshotFromMenus(resultMenus)
	if err := recordNavigationRegistryAudit(ctx, tx, mutation.Actor, map[string]any{
		"from_revision":   mutation.ExpectedRevision,
		"revision":        navigation.SnapshotRevision(result),
		"from_menu_count": current.MenuCount,
		"from_item_count": current.ItemCount,
		"menu_count":      result.MenuCount,
		"item_count":      result.ItemCount,
	}); err != nil {
		return current, err
	}
	if err := tx.Commit(ctx); err != nil {
		return current, fmt.Errorf("commit navigation registry replacement: %w", err)
	}
	return result, nil
}

func (repository *NavigationRepository) Delete(ctx context.Context, id int64, mutation navigation.Mutation) error {
	if repository == nil || repository.pool == nil || id <= 0 || mutation.ExpectedVersion <= 0 {
		return navigation.ErrInvalidInput
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete navigation menu: %w", err)
	}
	defer tx.Rollback(ctx)
	menu, err := lockNavigationMenu(ctx, tx, id)
	if err != nil {
		return err
	}
	if menu.Version != mutation.ExpectedVersion {
		return navigation.ErrVersionConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM np_navigation_menus WHERE id=$1 AND version=$2`, id, mutation.ExpectedVersion); err != nil {
		return fmt.Errorf("delete navigation menu: %w", err)
	}
	if err := recordNavigationAudit(ctx, tx, mutation.Actor, "navigation.menu.delete", id, map[string]any{
		"slug": menu.Slug, "version": menu.Version,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit navigation deletion: %w", err)
	}
	return nil
}

func (repository *NavigationRepository) Snapshot(ctx context.Context) (navigation.Snapshot, error) {
	menus, err := repository.List(ctx)
	if err != nil {
		return navigation.Snapshot{}, err
	}
	return navigationSnapshotFromMenus(menus), nil
}

func navigationSnapshotFromMenus(menus []navigation.Menu) navigation.Snapshot {
	locations := make(map[string]int64)
	itemCount := 0
	var updatedAt time.Time
	for _, menu := range menus {
		itemCount += len(menu.Items)
		if menu.UpdatedAt.After(updatedAt) {
			updatedAt = menu.UpdatedAt
		}
		for _, location := range menu.Locations {
			locations[location] = menu.ID
		}
	}
	return navigation.Snapshot{
		Menus: menus, Locations: locations, MenuCount: len(menus), ItemCount: itemCount, UpdatedAt: updatedAt,
	}
}

type navigationQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadNavigationMenus(ctx context.Context, querier navigationQuerier) ([]navigation.Menu, error) {
	rows, err := querier.Query(ctx, `SELECT `+navigationMenuColumns+` FROM np_navigation_menus ORDER BY lower(name),id`)
	if err != nil {
		return nil, fmt.Errorf("list navigation menus: %w", err)
	}
	menus := make([]navigation.Menu, 0)
	for rows.Next() {
		menu, scanErr := scanNavigationMenu(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan navigation menu: %w", scanErr)
		}
		menus = append(menus, menu)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate navigation menus: %w", err)
	}
	rows.Close()
	byID := make(map[int64]*navigation.Menu, len(menus))
	for index := range menus {
		menus[index].Items = []navigation.Item{}
		byID[menus[index].ID] = &menus[index]
	}
	locationRows, err := querier.Query(ctx, `SELECT location,menu_id FROM np_navigation_locations ORDER BY location`)
	if err != nil {
		return nil, fmt.Errorf("list navigation locations: %w", err)
	}
	for locationRows.Next() {
		var location string
		var menuID int64
		if err := locationRows.Scan(&location, &menuID); err != nil {
			locationRows.Close()
			return nil, fmt.Errorf("scan navigation location: %w", err)
		}
		if menu := byID[menuID]; menu != nil {
			menu.Locations = append(menu.Locations, location)
		}
	}
	if err := locationRows.Err(); err != nil {
		locationRows.Close()
		return nil, fmt.Errorf("iterate navigation locations: %w", err)
	}
	locationRows.Close()
	itemRows, err := querier.Query(ctx, `SELECT `+navigationItemColumns+` FROM np_navigation_items ORDER BY menu_id,position,id`)
	if err != nil {
		return nil, fmt.Errorf("list navigation items: %w", err)
	}
	for itemRows.Next() {
		item, scanErr := scanNavigationItem(itemRows)
		if scanErr != nil {
			itemRows.Close()
			return nil, fmt.Errorf("scan navigation item: %w", scanErr)
		}
		if menu := byID[item.MenuID]; menu != nil {
			menu.Items = append(menu.Items, item)
		}
	}
	if err := itemRows.Err(); err != nil {
		itemRows.Close()
		return nil, fmt.Errorf("iterate navigation items: %w", err)
	}
	itemRows.Close()
	return menus, nil
}

func loadNavigationMenu(ctx context.Context, querier navigationQuerier, id int64) (navigation.Menu, error) {
	menu, err := scanNavigationMenu(querier.QueryRow(ctx, `SELECT `+navigationMenuColumns+` FROM np_navigation_menus WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return navigation.Menu{}, navigation.ErrNotFound
	}
	if err != nil {
		return navigation.Menu{}, fmt.Errorf("get navigation menu: %w", err)
	}
	menu.Locations, err = loadNavigationLocations(ctx, querier, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	menu.Items, err = loadNavigationItems(ctx, querier, id)
	if err != nil {
		return navigation.Menu{}, err
	}
	return menu, nil
}

func loadNavigationLocations(ctx context.Context, querier navigationQuerier, menuID int64) ([]string, error) {
	rows, err := querier.Query(ctx, `SELECT location FROM np_navigation_locations WHERE menu_id=$1 ORDER BY location`, menuID)
	if err != nil {
		return nil, fmt.Errorf("load navigation locations: %w", err)
	}
	defer rows.Close()
	locations := make([]string, 0, 4)
	for rows.Next() {
		var location string
		if err := rows.Scan(&location); err != nil {
			return nil, fmt.Errorf("scan navigation location: %w", err)
		}
		locations = append(locations, location)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate navigation locations: %w", err)
	}
	// Keep the domain-defined display order instead of database collation order.
	ordered := make([]string, 0, len(locations))
	for _, candidate := range navigation.Locations() {
		for _, location := range locations {
			if candidate == location {
				ordered = append(ordered, location)
			}
		}
	}
	return ordered, nil
}

func loadNavigationItems(ctx context.Context, querier navigationQuerier, menuID int64) ([]navigation.Item, error) {
	rows, err := querier.Query(ctx, `SELECT `+navigationItemColumns+` FROM np_navigation_items WHERE menu_id=$1 ORDER BY position,id`, menuID)
	if err != nil {
		return nil, fmt.Errorf("load navigation items: %w", err)
	}
	defer rows.Close()
	items := make([]navigation.Item, 0)
	for rows.Next() {
		item, scanErr := scanNavigationItem(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan navigation item: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate navigation items: %w", err)
	}
	return items, nil
}

func lockNavigationMenu(ctx context.Context, tx pgx.Tx, id int64) (navigation.Menu, error) {
	menu, err := scanNavigationMenu(tx.QueryRow(ctx, `SELECT `+navigationMenuColumns+` FROM np_navigation_menus WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return navigation.Menu{}, navigation.ErrNotFound
	}
	if err != nil {
		return navigation.Menu{}, fmt.Errorf("lock navigation menu: %w", err)
	}
	return menu, nil
}

func lockNavigationRegistry(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT id FROM np_navigation_menus ORDER BY id FOR UPDATE`)
	if err != nil {
		return fmt.Errorf("lock navigation registry rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan locked navigation menu: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate locked navigation menus: %w", err)
	}
	return nil
}

func replaceNavigationLocations(ctx context.Context, tx pgx.Tx, menuID int64, locations []string, now time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM np_navigation_locations WHERE menu_id=$1`, menuID); err != nil {
		return fmt.Errorf("clear navigation locations: %w", err)
	}
	for _, location := range locations {
		if _, err := tx.Exec(ctx, `INSERT INTO np_navigation_locations(location,menu_id,updated_at) VALUES ($1,$2,$3)`, location, menuID, now); err != nil {
			return translateNavigationError(err)
		}
	}
	return nil
}

func recordNavigationAudit(ctx context.Context, tx pgx.Tx, actor, action string, menuID int64, metadata map[string]any) error {
	payload, _ := json.Marshal(metadata)
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
		VALUES ($1,$2,'navigation_menu',$3,$4)`, strings.TrimSpace(actor), action, fmt.Sprint(menuID), payload); err != nil {
		return fmt.Errorf("record navigation audit: %w", err)
	}
	return nil
}

func recordNavigationRegistryAudit(ctx context.Context, tx pgx.Tx, actor string, metadata map[string]any) error {
	payload, _ := json.Marshal(metadata)
	if _, err := tx.Exec(ctx, `INSERT INTO np_audit_events(actor_label,action,object_type,object_id,metadata)
		VALUES ($1,'navigation.registry.replace','navigation_registry','all',$2)`, strings.TrimSpace(actor), payload); err != nil {
		return fmt.Errorf("record navigation registry audit: %w", err)
	}
	return nil
}

func translateNavigationError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		constraint := strings.ToLower(postgresError.ConstraintName)
		switch {
		case strings.Contains(constraint, "navigation_menus_slug"):
			return navigation.ErrSlugConflict
		case strings.Contains(constraint, "navigation_locations"):
			return navigation.ErrLocationConflict
		}
	}
	return err
}

const navigationMenuColumns = `id,version,name,slug,description,created_at,updated_at`

const navigationItemColumns = `
	id,menu_id,COALESCE(parent_id,0),position,item_type,COALESCE(object_id,0),label,url,
	description,target,rel,icon,accent,presentation,created_at,updated_at,badge`

func scanNavigationMenu(row scanner) (navigation.Menu, error) {
	var menu navigation.Menu
	if err := row.Scan(&menu.ID, &menu.Version, &menu.Name, &menu.Slug, &menu.Description, &menu.CreatedAt, &menu.UpdatedAt); err != nil {
		return navigation.Menu{}, err
	}
	menu.CreatedAt = menu.CreatedAt.UTC()
	menu.UpdatedAt = menu.UpdatedAt.UTC()
	return menu, nil
}

func scanNavigationItem(row scanner) (navigation.Item, error) {
	var item navigation.Item
	if err := row.Scan(
		&item.ID, &item.MenuID, &item.ParentID, &item.Position, &item.Type, &item.ObjectID,
		&item.Label, &item.URL, &item.Description, &item.Target, &item.Rel, &item.Icon,
		&item.Accent, &item.Presentation, &item.CreatedAt, &item.UpdatedAt, &item.Badge,
	); err != nil {
		return navigation.Item{}, err
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item, nil
}

var _ navigation.Store = (*NavigationRepository)(nil)
