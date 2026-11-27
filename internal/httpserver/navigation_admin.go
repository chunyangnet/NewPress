package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
)

type adminExploreSource struct {
	Title, Detail, MenuName, EditURL string
}

func (s *Server) adminExploreSource(snapshot navigation.Snapshot) adminExploreSource {
	result := adminExploreSource{Title: "尚无发现页入口", Detail: "未绑定专属菜单，主导航也没有可复用的发现页子项。"}
	for _, menu := range snapshot.Menus {
		if menu.ID == snapshot.Locations[navigation.LocationExplore] {
			result.MenuName, result.EditURL = menu.Name, navigationAdminURL(menu.ID, "")
			result.Title, result.Detail = "使用发现页专属菜单", "菜单名称和说明将公开显示为发现页标题和介绍。"
			if len(menu.Items) == 0 {
				result.Title, result.Detail = "发现页专属菜单为空", "保持空白，不会自动复用主导航；访客仍可搜索或浏览文章。"
			} else if len(s.publicNavigationMenu(menu).Items) == 0 {
				result.Title, result.Detail = "专属菜单暂无可公开入口", "已保存项目因模块或公开链接规则被过滤，不会回退到主导航。"
			}
			return result
		}
	}
	for _, menu := range snapshot.Menus {
		if menu.ID != snapshot.Locations[navigation.LocationPrimary] {
			continue
		}
		items, found := exploreFallbackItems(buildNavigationTree(s.publicNavigationMenu(menu), "/explore/"))
		if found {
			result.MenuName, result.EditURL = menu.Name, navigationAdminURL(menu.ID, "")
			if len(items) > 0 {
				result.Title, result.Detail = "复用主导航子项", "尚未绑定专属菜单，当前使用主导航中发现页入口的子项。"
			} else {
				result.Detail = "主导航的发现页入口没有可公开的子项。"
				raw, _ := exploreFallbackItems(buildNavigationTree(menu, "/explore/"))
				if len(raw) > 0 {
					result.Detail = "主导航子项已保存，但被模块或公开链接规则过滤。"
				}
			}
		}
	}
	return result
}

type adminNavigationView struct {
	ExploreSource adminExploreSource
	ExploreEditor bool
	Menus         []adminNavigationMenuOption
	Selected      navigation.Menu
	HasSelected   bool
	FlatItems     []adminNavigationItemRow
	ItemsJSON     string
	CreateForm    adminNavigationMenuForm
	EditForm      adminNavigationMenuForm
	Locations     []adminNavigationLocationOption
	SourceGroups  []adminNavigationSourceGroup
	Icons         []adminNavigationOption
	MenuCount     int
	ItemCount     int
	AssignedCount int
	CanWrite      bool
	Notice        adminNotice
	FieldErrors   map[string]string
	FormMode      string
}

type adminNavigationMenuOption struct {
	Menu          navigation.Menu
	URL           string
	Active        bool
	LocationLabel string
}

type adminNavigationItemRow struct {
	Item        navigation.Item
	Depth       int
	DepthClass  string
	TypeLabel   string
	AccentValue string
}

type adminNavigationMenuForm struct {
	Mode        string
	ID          int64
	Version     int64
	Name        string
	Slug        string
	Description string
	Locations   []string
}

type adminNavigationLocationOption struct {
	Value         string
	Label         string
	Detail        string
	Checked       bool
	CreateChecked bool
	OccupiedBy    string
}

type adminNavigationSourceGroup struct {
	ID      string
	Label   string
	Count   int
	Sources []adminNavigationSource
}

type adminNavigationSource struct {
	Type        string
	ObjectID    int64
	Label       string
	URL         string
	Description string
	Icon        string
}

type adminNavigationOption struct {
	Value string
	Label string
}

func (s *Server) adminNavigation(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/navigation/" {
		s.renderError(writer, http.StatusNotFound, "导航编排页面没有找到")
		return
	}
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return
	}
	query, ok := strictQueryShape(request.URL.Query(), map[string]bool{"menu": true, "notice": true, "location": true})
	if !ok {
		s.renderError(writer, http.StatusBadRequest, "导航编排参数不正确")
		return
	}
	menuID := int64(0)
	if location := query.Get("location"); query.Has("location") && (!navigation.ValidLocation(location) || query.Has("menu")) {
		s.renderError(writer, http.StatusBadRequest, "导航位置不正确")
		return
	}
	if raw := strings.TrimSpace(query.Get("menu")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			s.renderError(writer, http.StatusBadRequest, "菜单编号不正确")
			return
		}
		menuID = value
	}
	s.renderAdminNavigation(writer, request, http.StatusOK, principal, readOnly, menuID,
		adminNavigationMenuForm{}, "", nil, navigationNoticeFromQuery(query.Get("notice")))
}

func (s *Server) adminNavigationSubmit(writer http.ResponseWriter, request *http.Request) {
	principal, form, ok := s.requireNavigationWrite(writer, request)
	if !ok {
		return
	}
	action := strings.TrimSpace(form.Get("action"))
	if invalid := invalidNavigationForm(form, action); invalid != "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_navigation_form_shape", "field": invalid})
		return
	}
	id, version, identityFields := parseNavigationIdentity(form)
	if len(identityFields) > 0 {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"error": "invalid_navigation_identity", "fields": identityFields})
		return
	}
	actor := principal.Username
	if actor == "" {
		actor = principal.Label()
	}
	switch action {
	case "create":
		if id != 0 || version != 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unexpected_navigation_identity"})
			return
		}
		menuForm := parseNavigationMenuForm(form)
		menuForm.Mode = "create"
		created, err := s.navigation.Create(request.Context(), menuForm.input(), actor)
		if err != nil {
			s.handleNavigationMutationError(writer, request, principal, action, menuForm, "", err)
			return
		}
		http.Redirect(writer, request, navigationAdminURL(created.ID, "created"), http.StatusSeeOther)
	case "update":
		if id <= 0 || version <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "navigation_identity_required"})
			return
		}
		menuForm := parseNavigationMenuForm(form)
		menuForm.Mode, menuForm.ID, menuForm.Version = "update", id, version
		updated, err := s.navigation.Update(request.Context(), id, navigation.Mutation{ExpectedVersion: version, Actor: actor}, menuForm.input())
		if err != nil {
			s.handleNavigationMutationError(writer, request, principal, action, menuForm, "", err)
			return
		}
		http.Redirect(writer, request, navigationAdminURL(updated.ID, "updated"), http.StatusSeeOther)
	case "save_items":
		if id <= 0 || version <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "navigation_identity_required"})
			return
		}
		payload := form.Get("items_json")
		items, err := navigation.DecodeItemInputs(payload)
		if err != nil {
			s.renderAdminNavigation(writer, request, http.StatusUnprocessableEntity, principal, false, id,
				adminNavigationMenuForm{}, payload, map[string]string{"items": "菜单项目数据不是受支持的严格 JSON"},
				adminNotice{Message: "导航层级数据无法解析，请刷新后重试", Tone: "error"})
			return
		}
		updated, err := s.navigation.ReplaceItems(request.Context(), id, navigation.Mutation{ExpectedVersion: version, Actor: actor}, items)
		if err != nil {
			s.handleNavigationMutationError(writer, request, principal, action,
				adminNavigationMenuForm{Mode: "items", ID: id, Version: version}, payload, err)
			return
		}
		http.Redirect(writer, request, navigationAdminURL(updated.ID, "items-saved"), http.StatusSeeOther)
	case "delete":
		if id <= 0 || version <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "navigation_identity_required"})
			return
		}
		if err := s.navigation.Delete(request.Context(), id, navigation.Mutation{ExpectedVersion: version, Actor: actor}); err != nil {
			s.handleNavigationMutationError(writer, request, principal, action,
				adminNavigationMenuForm{Mode: "delete", ID: id, Version: version}, "", err)
			return
		}
		http.Redirect(writer, request, navigationAdminURL(0, "deleted"), http.StatusSeeOther)
	default:
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "unknown_navigation_action"})
	}
}

func (s *Server) renderAdminNavigation(writer http.ResponseWriter, request *http.Request, status int, principal auth.Principal, readOnly bool,
	selectedID int64, formOverride adminNavigationMenuForm, itemsJSON string, fieldErrors map[string]string, notice adminNotice,
) {
	snapshot, err := s.navigation.Snapshot(request.Context())
	if err != nil {
		slog.Error("load navigation snapshot", "error", err)
		s.renderError(writer, http.StatusInternalServerError, "导航仓库暂时不可用")
		return
	}
	if request.Method == http.MethodGet {
		if location := request.URL.Query().Get("location"); navigation.ValidLocation(location) {
			selectedID = snapshot.Locations[location]
			if selectedID == 0 {
				formOverride = adminNavigationMenuForm{Mode: "create", Name: navigation.LocationLabel(location), Locations: []string{location}}
			}
		}
	}
	if selectedID == 0 && formOverride.Mode != "create" && len(snapshot.Menus) > 0 {
		selectedID = snapshot.Menus[0].ID
	}
	selected := navigation.Menu{}
	menuNames := make(map[int64]string, len(snapshot.Menus))
	menuOptions := make([]adminNavigationMenuOption, 0, len(snapshot.Menus))
	for _, menu := range snapshot.Menus {
		menuNames[menu.ID] = menu.Name
		active := menu.ID == selectedID
		if active {
			selected = menu
		}
		labels := make([]string, 0, len(menu.Locations))
		for _, location := range menu.Locations {
			labels = append(labels, navigation.LocationLabel(location))
		}
		locationLabel := strings.Join(labels, " · ")
		if locationLabel == "" {
			locationLabel = "尚未分配位置"
		}
		menuOptions = append(menuOptions, adminNavigationMenuOption{
			Menu: menu, URL: navigationAdminURL(menu.ID, ""), Active: active, LocationLabel: locationLabel,
		})
	}
	if selectedID > 0 && selected.ID == 0 {
		s.renderError(writer, http.StatusNotFound, "菜单没有找到")
		return
	}
	editForm := navigationMenuFormFromMenu(selected)
	createForm := adminNavigationMenuForm{}
	if formOverride.Mode == "update" {
		editForm = formOverride
	} else if formOverride.Mode == "create" {
		createForm = formOverride
	}
	// Keep a rejected badge edit visible so the administrator can correct it.
	// Only reuse a tree whose identities, links and hierarchy validate separately.
	editorMenu := selected
	if itemsJSON != "" && formOverride.Mode == "items" {
		if attempted, err := navigation.DecodeItemInputs(itemsJSON); err == nil {
			structure := append([]navigation.ItemInput(nil), attempted...)
			for i := range structure {
				structure[i].Badge = navigation.Badge{}
			}
			if normalized, fields := navigation.NormalizeItemInputs(structure); len(fields) == 0 {
				editorMenu.Items = make([]navigation.Item, len(normalized))
				for i, input := range normalized {
					editorMenu.Items[i] = navigation.Item{ID: input.ID, MenuID: selected.ID, ParentID: input.ParentID, Position: i,
						Type: input.Type, ObjectID: input.ObjectID, Label: input.Label, URL: input.URL, Description: input.Description,
						Target: input.Target, Rel: input.Rel, Icon: input.Icon, Accent: input.Accent, Presentation: input.Presentation, Badge: attempted[i].Badge}
				}
			}
		}
	}
	flatItems := make([]adminNavigationItemRow, 0, len(editorMenu.Items))
	for _, flat := range navigation.Flatten(editorMenu) {
		accent := flat.Item.Accent
		if accent == "" {
			accent = "#4E7CF6"
		}
		flatItems = append(flatItems, adminNavigationItemRow{
			Item: flat.Item, Depth: flat.Depth, DepthClass: "depth-" + strconv.Itoa(flat.Depth),
			TypeLabel: navigationItemTypeLabel(flat.Item.Type), AccentValue: accent,
		})
	}
	if itemsJSON == "" {
		itemsJSON = marshalNavigationItems(selected)
	}
	locations := make([]adminNavigationLocationOption, 0, len(navigation.Locations()))
	for _, location := range navigation.Locations() {
		ownerID := snapshot.Locations[location]
		option := adminNavigationLocationOption{
			Value: location, Label: navigation.LocationLabel(location), Detail: navigationLocationDetail(location),
			Checked: stringListContains(editForm.Locations, location), CreateChecked: stringListContains(createForm.Locations, location),
		}
		if ownerID > 0 && ownerID != selected.ID {
			option.OccupiedBy = menuNames[ownerID]
		}
		locations = append(locations, option)
	}
	sources, sourceErr := s.navigationSources(request)
	if sourceErr != nil {
		slog.Warn("load navigation sources", "error", sourceErr)
	}
	assignedCount := len(snapshot.Locations)
	data := s.baseViewFor(writer, "导航编排", "管理菜单、链接和显示位置，修改后可在站点各处使用", "/admin/navigation/")
	data.BodyClass = "is-admin is-admin-navigation"
	data.ActiveNav = "account"
	data.IsAdmin = true
	data.Admin = s.adminView("navigation", principal, readOnly)
	data.Admin.FieldErrors = fieldErrors
	data.Admin.Notice = notice.Message
	data.Admin.NoticeTone = notice.Tone
	data.MenuAdmin = adminNavigationView{
		ExploreSource: s.adminExploreSource(snapshot),
		ExploreEditor: stringListContains(editForm.Locations, navigation.LocationExplore) || stringListContains(createForm.Locations, navigation.LocationExplore),
		Menus:         menuOptions, Selected: selected, HasSelected: selected.ID > 0, FlatItems: flatItems,
		ItemsJSON: itemsJSON, CreateForm: createForm, EditForm: editForm, Locations: locations,
		SourceGroups: sources, Icons: navigationIconsForMenu(editorMenu), MenuCount: snapshot.MenuCount,
		ItemCount: snapshot.ItemCount, AssignedCount: assignedCount,
		CanWrite: !readOnly && s.auth.Configured() && principal.Can(auth.CapabilityNavigationManage),
		Notice:   notice, FieldErrors: fieldErrors,
		FormMode: formOverride.Mode,
	}
	s.render(writer, status, "admin-navigation", data)
}

func (s *Server) navigationSources(request *http.Request) ([]adminNavigationSourceGroup, error) {
	builtins := []adminNavigationSource{
		{Type: navigation.TypeCustom, Label: "首页", URL: "/", Description: "站点门户", Icon: "house"},
		{Type: navigation.TypeCustom, Label: "快报", URL: "/flash/", Description: "短资讯时间线", Icon: "newspaper"},
		{Type: navigation.TypeCustom, Label: "我的快报", URL: "/flash/submit/", Description: "投稿与审核进度", Icon: "pen-line"},
		{Type: navigation.TypeCustom, Label: "发现", URL: "/explore/", Description: "站点导航与分组入口", Icon: "compass"},
		{Type: navigation.TypeCustom, Label: "浏览全部文章", URL: "/explore/?sort=updated", Description: "文章搜索、分类与排序", Icon: "library"},
		{Type: navigation.TypeCustom, Label: "商城", URL: "/shop/", Description: "商品、拼团与购买方案", Icon: "shopping-bag"},
		{Type: navigation.TypeCustom, Label: "商城订单", URL: "/shop/orders/", Description: "付款与交付记录", Icon: "shopping-cart"},
		{Type: navigation.TypeCustom, Label: "问答", URL: "/questions/", Description: "全站问题与解答", Icon: "circle-help"},
		{Type: navigation.TypeCustom, Label: "圈子", URL: "/community/", Description: "公开内容与社区讨论", Icon: "users"},
		{Type: navigation.TypeCustom, Label: "我的", URL: "/account/", Description: "个人账号与设置", Icon: "user-round"},
		{Type: navigation.TypeCustom, Label: "我的收藏", URL: "/account/?view=bookmark", Description: "已收藏的内容", Icon: "bookmark"},
		{Type: navigation.TypeCustom, Label: "专题", URL: "/series/", Description: "按专题浏览内容", Icon: "bookmark"},
	}
	builtins = slices.DeleteFunc(builtins, func(item adminNavigationSource) bool { return !s.moduleURLAvailable(item.URL) })
	groups := []adminNavigationSourceGroup{{ID: "builtins", Label: "系统入口", Count: len(builtins), Sources: builtins}}
	if s.moduleEnabled("links") && s.linkPages != nil && s.pageAvailable("links") {
		pages, err := s.linkPages.List(request.Context())
		if err != nil {
			return groups, err
		}
		sources := []adminNavigationSource{}
		for _, page := range pages {
			if page.Settings.Enabled {
				sources = append(sources, adminNavigationSource{Type: navigation.TypeCustom, Label: page.Settings.Name, URL: "/links/" + page.Slug + "/", Description: page.Settings.Description, Icon: "link"})
			}
		}
		groups = append(groups, adminNavigationSourceGroup{ID: "link-pages", Label: "友链功能页面", Count: len(sources), Sources: sources})
	}
	if s.moduleEnabled("feedback") && s.feedback != nil && s.pageAvailable("feedback") {
		boards, err := s.feedback.List(request.Context())
		if err != nil {
			return groups, err
		}
		sources := []adminNavigationSource{}
		for _, board := range boards {
			if board.Settings.Enabled {
				sources = append(sources, adminNavigationSource{Type: navigation.TypeCustom, Label: board.Settings.Name, URL: feedbackURL(board.Slug), Description: board.Settings.Description, Icon: "message-circle"})
			}
		}
		groups = append(groups, adminNavigationSourceGroup{ID: "features", Label: "功能页面", Count: len(sources), Sources: sources})
	}
	for _, postType := range []string{content.TypePage, content.TypePost} {
		result, err := s.repository.ListAdmin(request.Context(), content.AdminQuery{Type: postType, Status: content.StatusPublish, Limit: 100})
		if err != nil {
			return groups, err
		}
		sources := make([]adminNavigationSource, 0, len(result.Items))
		for _, post := range result.Items {
			icon := "newspaper"
			if post.Type == content.TypePage {
				icon = "panels-top-left"
			}
			sources = append(sources, adminNavigationSource{
				Type: post.Type, ObjectID: post.ID, Label: post.Title, URL: s.contentPath(post),
				Description: truncateRunes(post.Excerpt, 72), Icon: icon,
			})
		}
		label := "已发布文章"
		if postType == content.TypePage {
			label = "公开页面"
		}
		groups = append(groups, adminNavigationSourceGroup{ID: postType, Label: label, Count: result.Total, Sources: sources})
	}
	terms, err := s.repository.ListTerms(request.Context(), content.TermQuery{Limit: 500})
	if err != nil {
		return groups, err
	}
	for _, taxonomy := range []string{content.TaxonomyCategory, content.TaxonomyTag} {
		sources := make([]adminNavigationSource, 0)
		for _, term := range terms.Items {
			if term.Taxonomy != taxonomy {
				continue
			}
			prefix := "/categories/"
			typeName := navigation.TypeCategory
			icon := "tags"
			if taxonomy == content.TaxonomyTag {
				prefix = "/tags/"
				typeName = navigation.TypeTag
				icon = "hash"
			}
			sources = append(sources, adminNavigationSource{
				Type: typeName, ObjectID: term.ID, Label: term.Name, URL: prefix + url.PathEscape(term.Slug) + "/",
				Description: fmt.Sprintf("%d 篇公开内容", term.Count), Icon: icon,
			})
		}
		label := "分类目录"
		if taxonomy == content.TaxonomyTag {
			label = "文章标签"
		}
		groups = append(groups, adminNavigationSourceGroup{ID: taxonomy, Label: label, Count: len(sources), Sources: sources})
	}
	return groups, nil
}

func (s *Server) requireNavigationWrite(writer http.ResponseWriter, request *http.Request) (auth.Principal, url.Values, bool) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return auth.Principal{}, nil, false
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return auth.Principal{}, nil, false
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return auth.Principal{}, nil, false
	}
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "form_content_type_required"})
		return auth.Principal{}, nil, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 256<<10)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "navigation_form_too_large"})
			return auth.Principal{}, nil, false
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_navigation_form"})
		return auth.Principal{}, nil, false
	}
	form := request.PostForm
	if len(form["csrf_token"]) != 1 || !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return auth.Principal{}, nil, false
	}
	return principal, form, true
}

func invalidNavigationForm(form url.Values, action string) string {
	allowed := map[string]bool{"csrf_token": true, "action": true, "id": true, "version": true}
	switch action {
	case "create", "update":
		allowed["name"], allowed["slug"], allowed["description"], allowed["locations"] = true, true, true, true
	case "save_items":
		allowed["items_json"] = true
	case "delete":
	default:
		return "action"
	}
	for key, values := range form {
		if !allowed[key] || len(values) == 0 {
			return key
		}
		if key == "locations" {
			if len(values) > len(navigation.Locations()) {
				return key
			}
			continue
		}
		if len(values) != 1 {
			return key
		}
	}
	return ""
}

func parseNavigationIdentity(form url.Values) (int64, int64, map[string]string) {
	fields := make(map[string]string)
	parse := func(key string) int64 {
		raw := strings.TrimSpace(form.Get(key))
		if raw == "" {
			return 0
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			fields[key] = "编号或版本不正确"
			return 0
		}
		return value
	}
	return parse("id"), parse("version"), fields
}

func parseNavigationMenuForm(form url.Values) adminNavigationMenuForm {
	return adminNavigationMenuForm{
		Name: form.Get("name"), Slug: form.Get("slug"), Description: form.Get("description"),
		Locations: append([]string(nil), form["locations"]...),
	}
}

func (form adminNavigationMenuForm) input() navigation.MenuInput {
	return navigation.MenuInput{Name: form.Name, Slug: form.Slug, Description: form.Description, Locations: form.Locations}
}

func navigationMenuFormFromMenu(menu navigation.Menu) adminNavigationMenuForm {
	return adminNavigationMenuForm{
		ID: menu.ID, Version: menu.Version, Name: menu.Name, Slug: menu.Slug,
		Description: menu.Description, Locations: append([]string(nil), menu.Locations...),
	}
}

func (s *Server) handleNavigationMutationError(writer http.ResponseWriter, request *http.Request, principal auth.Principal,
	action string, form adminNavigationMenuForm, itemsJSON string, err error,
) {
	if errors.Is(err, navigation.ErrNotFound) {
		s.renderError(writer, http.StatusNotFound, "菜单没有找到")
		return
	}
	status := http.StatusUnprocessableEntity
	fields := navigation.FieldErrors(err)
	notice := adminNotice{Message: "导航配置没有通过服务端校验", Tone: "error"}
	switch {
	case errors.Is(err, navigation.ErrSlugConflict):
		status = http.StatusConflict
		fields = map[string]string{"slug": "已有菜单使用这个别名"}
		notice.Message = "菜单别名冲突，请更换后重试"
	case errors.Is(err, navigation.ErrLocationConflict):
		status = http.StatusConflict
		fields = map[string]string{"locations": "至少一个位置已分配给其他菜单"}
		notice.Message = "菜单位置不能同时分配给多个菜单"
	case errors.Is(err, navigation.ErrVersionConflict):
		status = http.StatusConflict
		notice.Message = "菜单刚刚被其他请求修改，请检查最新版本后再保存"
	case len(fields) == 0:
		slog.Error("mutate navigation", "action", action, "menu_id", form.ID, "error", err)
		s.renderError(writer, http.StatusInternalServerError, "导航配置暂时无法保存")
		return
	}
	selectedID := form.ID
	if selectedID > 0 {
		if current, loadErr := s.navigation.Get(request.Context(), selectedID); loadErr == nil {
			form.Version = current.Version
		}
	}
	s.renderAdminNavigation(writer, request, status, principal, false, selectedID, form, itemsJSON, fields, notice)
}

func marshalNavigationItems(menu navigation.Menu) string {
	items := make([]navigation.ItemInput, 0, len(menu.Items))
	for _, flat := range navigation.Flatten(menu) {
		item := flat.Item
		accent := item.Accent
		if accent == "" {
			accent = "#4E7CF6"
		}
		items = append(items, navigation.ItemInput{
			ID: item.ID, ParentID: item.ParentID, Type: item.Type, ObjectID: item.ObjectID,
			Label: item.Label, URL: item.URL, Description: item.Description, Target: item.Target,
			Rel: item.Rel, Icon: item.Icon, Accent: accent, Presentation: item.Presentation, Badge: item.Badge,
		})
	}
	payload, _ := json.Marshal(items)
	return string(payload)
}

func navigationAdminURL(menuID int64, notice string) string {
	query := url.Values{}
	if menuID > 0 {
		query.Set("menu", strconv.FormatInt(menuID, 10))
	}
	if notice != "" {
		query.Set("notice", notice)
	}
	if len(query) == 0 {
		return "/admin/navigation/"
	}
	return "/admin/navigation/?" + query.Encode()
}

func navigationNoticeFromQuery(value string) adminNotice {
	switch value {
	case "created":
		return adminNotice{Message: "菜单已创建，可以从内容库添加入口并分配主题位置", Tone: "success"}
	case "updated":
		return adminNotice{Message: "菜单名称、说明与位置分配已原子更新", Tone: "success"}
	case "items-saved":
		return adminNotice{Message: "菜单层级、链接与展示方式已发布到公开主题", Tone: "success"}
	case "deleted":
		return adminNotice{Message: "菜单及其项目已删除，公开主题会安全回落到默认导航", Tone: "success"}
	default:
		return adminNotice{}
	}
}

func navigationLocationDetail(value string) string {
	switch value {
	case navigation.LocationExplore:
		return "发现页分组入口：带子项的顶层为分组，无子项为独立入口"
	case navigation.LocationPrimary:
		return "桌面头部，支持三层与四种展开方式"
	case navigation.LocationUtility:
		return "品牌区旁的紧凑文字入口"
	case navigation.LocationMobile:
		return "前四个不同链接的顶层项目，不足时补齐常用入口"
	case navigation.LocationMobileAction:
		return "首个有效顶层项目作为中央按钮；空菜单隐藏按钮，未分配时显示快捷操作"
	case navigation.LocationFooter:
		return "顶层为栏目，第二层为页脚链接"
	default:
		return "主题声明的位置"
	}
}

func navigationItemTypeLabel(value string) string {
	switch value {
	case navigation.TypePost:
		return "文章"
	case navigation.TypePage:
		return "页面"
	case navigation.TypeCategory:
		return "分类"
	case navigation.TypeTag:
		return "标签"
	default:
		return "自定义"
	}
}

func navigationIconOptions() []adminNavigationOption {
	return []adminNavigationOption{
		{Value: "", Label: "无图标"}, {Value: "house", Label: "首页"}, {Value: "compass", Label: "发现"},
		{Value: "user-round", Label: "我的"}, {Value: "bookmark", Label: "收藏"}, {Value: "plus", Label: "加号"}, {Value: "pen-line", Label: "写作"},
		{Value: "newspaper", Label: "内容"}, {Value: "panels-top-left", Label: "页面/组件"}, {Value: "users", Label: "社区"},
		{Value: "shopping-bag", Label: "商城"}, {Value: "tags", Label: "分类标签"}, {Value: "hash", Label: "话题"},
		{Value: "diamond", Label: "精选"}, {Value: "code-2", Label: "开发"}, {Value: "gauge", Label: "控制台"},
		{Value: "globe-2", Label: "外部链接"}, {Value: "circle-help", Label: "帮助"}, {Value: "message-circle", Label: "讨论"},
		{Value: "link", Label: "链接"}, {Value: "search", Label: "搜索"}, {Value: "library", Label: "资料库"},
		{Value: "image", Label: "图片"}, {Value: "play", Label: "视频"}, {Value: "file-archive", Label: "资源下载"},
		{Value: "star", Label: "推荐"}, {Value: "heart", Label: "喜欢"}, {Value: "award", Label: "成就"},
		{Value: "orbit", Label: "圈子"}, {Value: "message-square", Label: "留言"}, {Value: "bell", Label: "通知"},
		{Value: "megaphone", Label: "公告"}, {Value: "send", Label: "发送"}, {Value: "calendar-check", Label: "签到"},
		{Value: "coffee", Label: "生活"}, {Value: "store", Label: "店铺"}, {Value: "shopping-cart", Label: "购物车"},
		{Value: "coins", Label: "积分"}, {Value: "sparkles", Label: "灵感"}, {Value: "bot", Label: "AI 助手"},
		{Value: "rocket", Label: "开始"}, {Value: "zap", Label: "快捷操作"}, {Value: "sliders-horizontal", Label: "设置"},
		{Value: "shield-check", Label: "安全"}, {Value: "badge-check", Label: "认证"}, {Value: "key-round", Label: "密钥"},
		{Value: "rss", Label: "订阅"}, {Value: "inbox", Label: "收件箱"}, {Value: "clock-3", Label: "时间"},
		{Value: "route", Label: "路线"},
	}
}

// Imported menus may use a theme's own sprite names. Retain these options so
// opening and saving the visual editor never silently clears an existing icon.
func navigationIconsForMenu(menu navigation.Menu) []adminNavigationOption {
	options := navigationIconOptions()
	seen := make(map[string]bool, len(options))
	for _, option := range options {
		seen[option.Value] = true
	}
	for _, item := range menu.Items {
		if !seen[item.Icon] {
			options = append(options, adminNavigationOption{Value: item.Icon, Label: "自定义 · " + item.Icon})
			seen[item.Icon] = true
		}
	}
	return options
}

func stringListContains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func truncateRunes(value string, maximum int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum]) + "…"
}
