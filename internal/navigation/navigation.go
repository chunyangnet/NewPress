// Package navigation owns reusable site menus and their theme locations.
// It deliberately stores only presentation-safe link metadata: credentials,
// executable markup and arbitrary HTML never cross this boundary.
package navigation

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	LocationExplore      = "explore"
	LocationPrimary      = "primary"
	LocationUtility      = "utility"
	LocationMobile       = "mobile"
	LocationMobileAction = "mobile_action"
	LocationFooter       = "footer"

	TypeCustom   = "custom"
	TypePost     = "post"
	TypePage     = "page"
	TypeCategory = "category"
	TypeTag      = "tag"

	TargetSelf  = "self"
	TargetBlank = "blank"

	PresentationStandard = "standard"
	PresentationDropdown = "dropdown"
	PresentationPanel    = "panel"
	PresentationCards    = "cards"

	MaxMenus            = 20
	MaxItemsPerMenu     = 80
	MaxMenuNameRunes    = 80
	MaxMenuSlugBytes    = 96
	MaxDescriptionRunes = 240
	MaxItemLabelRunes   = 80
	MaxItemDescription  = 180
	MaxURLBytes         = 2048
	MaxDepth            = 3
)

var (
	ErrNotFound         = errors.New("navigation menu not found")
	ErrVersionConflict  = errors.New("navigation menu version conflict")
	ErrSlugConflict     = errors.New("navigation menu slug conflict")
	ErrLocationConflict = errors.New("navigation location is already assigned")
	ErrInvalidInput     = errors.New("invalid navigation input")

	colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	iconPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
)

type Menu struct {
	ID          int64     `json:"id"`
	Version     int64     `json:"version"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description string    `json:"description,omitempty"`
	Locations   []string  `json:"locations,omitempty"`
	Items       []Item    `json:"items"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Item struct {
	ID           int64     `json:"id"`
	MenuID       int64     `json:"menu_id"`
	ParentID     int64     `json:"parent_id,omitempty"`
	Position     int       `json:"position"`
	Type         string    `json:"type"`
	ObjectID     int64     `json:"object_id,omitempty"`
	Label        string    `json:"label"`
	URL          string    `json:"url"`
	Description  string    `json:"description,omitempty"`
	Target       string    `json:"target"`
	Rel          string    `json:"rel,omitempty"`
	Icon         string    `json:"icon,omitempty"`
	Accent       string    `json:"accent,omitempty"`
	Presentation string    `json:"presentation"`
	Badge        Badge     `json:"badge,omitzero"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type MenuInput struct {
	Name        string
	Slug        string
	Description string
	Locations   []string
}

// ItemInput accepts negative IDs as request-local identities for newly added
// items. Stores replace those temporary values with durable positive IDs while
// preserving parent references in one atomic mutation.
type ItemInput struct {
	ID           int64  `json:"id"`
	ParentID     int64  `json:"parent_id"`
	Type         string `json:"type"`
	ObjectID     int64  `json:"object_id"`
	Label        string `json:"label"`
	URL          string `json:"url"`
	Description  string `json:"description"`
	Target       string `json:"target"`
	Rel          string `json:"rel"`
	Icon         string `json:"icon"`
	Accent       string `json:"accent"`
	Presentation string `json:"presentation"`
	Badge        Badge  `json:"badge,omitzero"`
}

type Mutation struct {
	ExpectedVersion int64
	Actor           string
}

type Snapshot struct {
	Menus     []Menu           `json:"menus"`
	Locations map[string]int64 `json:"locations"`
	MenuCount int              `json:"menu_count"`
	ItemCount int              `json:"item_count"`
	UpdatedAt time.Time        `json:"updated_at,omitempty"`
}

type FlatItem struct {
	Item  Item
	Depth int
}

type Store interface {
	CreateOnce(context.Context, Creation) (CreationResult, error)
	FindCreation(context.Context, string, string) (CreationResult, error)
	List(context.Context) ([]Menu, error)
	Get(context.Context, int64) (Menu, error)
	GetLocation(context.Context, string) (Menu, error)
	Create(context.Context, MenuInput, string) (Menu, error)
	Update(context.Context, int64, Mutation, MenuInput) (Menu, error)
	ReplaceItems(context.Context, int64, Mutation, []ItemInput) (Menu, error)
	ReplaceAll(context.Context, RegistryMutation, []RegistryMenuInput) (Snapshot, error)
	Delete(context.Context, int64, Mutation) error
	Snapshot(context.Context) (Snapshot, error)
	Ready(context.Context) error
}

type ValidationError struct {
	Fields map[string]string
}

func (err *ValidationError) Error() string { return "navigation validation failed" }

func FieldErrors(err error) map[string]string {
	var validation *ValidationError
	if errors.As(err, &validation) {
		result := make(map[string]string, len(validation.Fields))
		for key, value := range validation.Fields {
			result[key] = value
		}
		return result
	}
	return nil
}

func Locations() []string {
	return []string{LocationPrimary, LocationUtility, LocationMobile, LocationMobileAction, LocationFooter, LocationExplore}
}

func LocationLabel(location string) string {
	switch location {
	case LocationExplore:
		return "发现页导航"
	case LocationPrimary:
		return "桌面主导航"
	case LocationUtility:
		return "顶部快捷入口"
	case LocationMobile:
		return "移动底部导航"
	case LocationMobileAction:
		return "移动底栏中央按钮"
	case LocationFooter:
		return "页脚导航"
	default:
		return location
	}
}

func ValidLocation(value string) bool {
	switch value {
	case LocationPrimary, LocationUtility, LocationMobile, LocationMobileAction, LocationFooter, LocationExplore:
		return true
	default:
		return false
	}
}

func ValidItemType(value string) bool {
	switch value {
	case TypeCustom, TypePost, TypePage, TypeCategory, TypeTag:
		return true
	default:
		return false
	}
}

func ValidPresentation(value string) bool {
	switch value {
	case PresentationStandard, PresentationDropdown, PresentationPanel, PresentationCards:
		return true
	default:
		return false
	}
}

func NormalizeMenuInput(input MenuInput) (MenuInput, map[string]string) {
	fields := make(map[string]string)
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		fields["name"] = "菜单名称不能为空"
	} else if utf8.RuneCountInString(input.Name) > MaxMenuNameRunes || containsControl(input.Name) {
		fields["name"] = fmt.Sprintf("菜单名称不能超过 %d 个字符或包含控制字符", MaxMenuNameRunes)
	}
	input.Slug = NormalizeSlug(input.Slug)
	if input.Slug == "" {
		input.Slug = NormalizeSlug(input.Name)
	}
	if !ValidSlug(input.Slug) {
		fields["slug"] = fmt.Sprintf("别名只能包含字母、数字和连字符，且不能超过 %d 字节", MaxMenuSlugBytes)
	}
	input.Description = strings.TrimSpace(input.Description)
	if utf8.RuneCountInString(input.Description) > MaxDescriptionRunes || containsControlExceptWhitespace(input.Description) {
		fields["description"] = fmt.Sprintf("说明不能超过 %d 个字符或包含控制字符", MaxDescriptionRunes)
	}
	seen := make(map[string]struct{}, len(input.Locations))
	locations := make([]string, 0, len(input.Locations))
	for _, value := range input.Locations {
		value = strings.ToLower(strings.TrimSpace(value))
		if !ValidLocation(value) {
			fields["locations"] = "菜单位置不受支持"
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		locations = append(locations, value)
	}
	sort.Slice(locations, func(i, j int) bool { return locationOrder(locations[i]) < locationOrder(locations[j]) })
	input.Locations = locations
	return input, fields
}

func NormalizeItemInputs(inputs []ItemInput) ([]ItemInput, map[string]string) {
	fields := make(map[string]string)
	if len(inputs) > MaxItemsPerMenu {
		fields["items"] = fmt.Sprintf("每个菜单最多包含 %d 个项目", MaxItemsPerMenu)
		return nil, fields
	}
	items := make([]ItemInput, len(inputs))
	ids := make(map[int64]int, len(inputs))
	for index, input := range inputs {
		prefix := fmt.Sprintf("items.%d", index)
		if input.ID == 0 {
			// Give non-JavaScript and API callers a deterministic request-local ID.
			input.ID = -int64(index + 1)
		}
		if _, exists := ids[input.ID]; exists {
			fields[prefix+".id"] = "菜单项目编号重复"
		} else {
			ids[input.ID] = index
		}
		input.Type = strings.ToLower(strings.TrimSpace(input.Type))
		if input.Type == "" {
			input.Type = TypeCustom
		}
		if !ValidItemType(input.Type) {
			fields[prefix+".type"] = "菜单项目类型不受支持"
		}
		if input.Type == TypeCustom {
			input.ObjectID = 0
		} else if input.ObjectID <= 0 {
			fields[prefix+".object_id"] = "内容或分类编号不正确"
		}
		input.Label = strings.TrimSpace(input.Label)
		if input.Label == "" {
			fields[prefix+".label"] = "菜单文字不能为空"
		} else if utf8.RuneCountInString(input.Label) > MaxItemLabelRunes || containsControl(input.Label) {
			fields[prefix+".label"] = fmt.Sprintf("菜单文字不能超过 %d 个字符或包含控制字符", MaxItemLabelRunes)
		}
		input.URL = strings.TrimSpace(input.URL)
		if err := ValidateURL(input.URL); err != nil {
			fields[prefix+".url"] = err.Error()
		}
		input.Description = strings.TrimSpace(input.Description)
		if utf8.RuneCountInString(input.Description) > MaxItemDescription || containsControlExceptWhitespace(input.Description) {
			fields[prefix+".description"] = fmt.Sprintf("项目说明不能超过 %d 个字符或包含控制字符", MaxItemDescription)
		}
		input.Target = strings.ToLower(strings.TrimSpace(input.Target))
		if input.Target == "" {
			input.Target = TargetSelf
		}
		if input.Target != TargetSelf && input.Target != TargetBlank {
			fields[prefix+".target"] = "打开方式不受支持"
		}
		input.Rel = normalizeRel(input.Rel, input.Target == TargetBlank)
		input.Icon = strings.ToLower(strings.TrimSpace(input.Icon))
		if input.Icon != "" && !iconPattern.MatchString(input.Icon) {
			fields[prefix+".icon"] = "图标名称只能包含小写字母、数字和连字符"
		}
		input.Accent = strings.ToUpper(strings.TrimSpace(input.Accent))
		if input.Accent != "" && !colorPattern.MatchString(input.Accent) {
			fields[prefix+".accent"] = "强调色必须是六位十六进制颜色"
		}
		input.Presentation = strings.ToLower(strings.TrimSpace(input.Presentation))
		if input.Presentation == "" {
			input.Presentation = PresentationStandard
		}
		if !ValidPresentation(input.Presentation) {
			fields[prefix+".presentation"] = "展开样式不受支持"
		}
		var badgeFields map[string]string
		input.Badge, badgeFields = NormalizeBadge(input.Badge)
		for key, message := range badgeFields {
			fields[prefix+".badge."+key] = message
		}
		items[index] = input
	}
	for index, item := range items {
		prefix := fmt.Sprintf("items.%d", index)
		if item.ParentID == item.ID {
			fields[prefix+".parent_id"] = "菜单项目不能以自身为父级"
			continue
		}
		if item.ParentID != 0 {
			if _, exists := ids[item.ParentID]; !exists {
				fields[prefix+".parent_id"] = "父级菜单项目不存在"
			}
		} else if item.Presentation == "" {
			items[index].Presentation = PresentationStandard
		}
	}
	if len(fields) == 0 {
		for index, item := range items {
			depth, cycle := inputDepth(item.ID, items, ids)
			if cycle {
				fields[fmt.Sprintf("items.%d.parent_id", index)] = "菜单层级形成循环"
				continue
			}
			if depth >= MaxDepth {
				fields[fmt.Sprintf("items.%d.parent_id", index)] = fmt.Sprintf("菜单最多支持 %d 层", MaxDepth)
			}
			if depth > 0 && item.Presentation != PresentationStandard {
				fields[fmt.Sprintf("items.%d.presentation", index)] = "只有顶层项目可以选择展开样式"
			}
		}
	}
	return items, fields
}

func NormalizeSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	result.Grow(min(len(value), MaxMenuSlugBytes))
	separator := false
	for _, character := range value {
		switch {
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			if separator && result.Len() > 0 && result.Len() < MaxMenuSlugBytes {
				result.WriteByte('-')
			}
			separator = false
			if result.Len()+utf8.RuneLen(character) <= MaxMenuSlugBytes {
				result.WriteRune(character)
			}
		case character == '-' || character == '_' || unicode.IsSpace(character):
			separator = result.Len() > 0
		}
		if result.Len() >= MaxMenuSlugBytes {
			break
		}
	}
	return strings.Trim(result.String(), "-")
}

func ValidSlug(value string) bool {
	if len(value) < 1 || len(value) > MaxMenuSlugBytes || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") || strings.Contains(value, "--") {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' {
			continue
		}
		return false
	}
	return true
}

func ValidateURL(value string) error {
	if value == "" {
		return errors.New("链接地址不能为空")
	}
	if len(value) > MaxURLBytes || containsControl(value) || strings.Contains(value, "\\") {
		return fmt.Errorf("链接地址不能超过 %d 字节或包含控制字符", MaxURLBytes)
	}
	if strings.HasPrefix(value, "/") {
		if strings.HasPrefix(value, "//") {
			return errors.New("链接不能使用协议相对地址")
		}
		parsed, err := url.ParseRequestURI(value)
		if err != nil || parsed.IsAbs() || parsed.Host != "" {
			return errors.New("站内链接格式不正确")
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return errors.New("链接必须是站内绝对路径或完整 HTTP(S)/mailto/tel 地址")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Host == "" || parsed.User != nil {
			return errors.New("外部链接必须包含主机且不能携带凭据")
		}
	case "mailto":
		if parsed.Opaque == "" || strings.Contains(parsed.Opaque, "?") {
			return errors.New("邮件链接格式不正确")
		}
	case "tel":
		if parsed.Opaque == "" {
			return errors.New("电话链接格式不正确")
		}
	default:
		return errors.New("链接协议不受支持")
	}
	return nil
}

func Flatten(menu Menu) []FlatItem {
	children := make(map[int64][]Item, len(menu.Items))
	for _, item := range menu.Items {
		children[item.ParentID] = append(children[item.ParentID], item)
	}
	for parentID := range children {
		sort.SliceStable(children[parentID], func(i, j int) bool {
			if children[parentID][i].Position == children[parentID][j].Position {
				return children[parentID][i].ID < children[parentID][j].ID
			}
			return children[parentID][i].Position < children[parentID][j].Position
		})
	}
	result := make([]FlatItem, 0, len(menu.Items))
	var walk func(int64, int)
	walk = func(parentID int64, depth int) {
		for _, item := range children[parentID] {
			result = append(result, FlatItem{Item: item, Depth: depth})
			walk(item.ID, depth+1)
		}
	}
	walk(0, 0)
	return result
}

func SortMenus(items []Menu) {
	sort.SliceStable(items, func(i, j int) bool {
		if strings.EqualFold(items[i].Name, items[j].Name) {
			return items[i].ID < items[j].ID
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
}

func inputDepth(id int64, items []ItemInput, index map[int64]int) (int, bool) {
	seen := map[int64]struct{}{id: {}}
	depth := 0
	current := items[index[id]].ParentID
	for current != 0 {
		if _, exists := seen[current]; exists {
			return depth, true
		}
		seen[current] = struct{}{}
		position, exists := index[current]
		if !exists {
			return depth, false
		}
		depth++
		current = items[position].ParentID
	}
	return depth, false
}

func normalizeRel(value string, externalTarget bool) string {
	allowed := map[string]bool{"nofollow": true, "noopener": true, "noreferrer": true, "sponsored": true, "ugc": true}
	seen := make(map[string]bool)
	for _, token := range strings.Fields(strings.ToLower(value)) {
		if allowed[token] {
			seen[token] = true
		}
	}
	if externalTarget {
		seen["noopener"] = true
		seen["noreferrer"] = true
	}
	order := []string{"nofollow", "ugc", "sponsored", "noopener", "noreferrer"}
	result := make([]string, 0, len(seen))
	for _, token := range order {
		if seen[token] {
			result = append(result, token)
		}
	}
	return strings.Join(result, " ")
}

func locationOrder(value string) int {
	for index, location := range Locations() {
		if location == value {
			return index
		}
	}
	return len(Locations())
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func containsControlExceptWhitespace(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}
