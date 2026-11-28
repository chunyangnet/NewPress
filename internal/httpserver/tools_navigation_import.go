package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/navigation"
)

const (
	navigationImportFileMaxBytes = 256 << 10
	navigationImportFormMaxBytes = 512 << 10

	navigationImportDetachObjects   = "detach"
	navigationImportPreserveObjects = "preserve"
)

var (
	navigationImportRevisionPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
	navigationImportItemKeyPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

type adminNavigationImportView struct {
	HasPlan             bool
	FileName            string
	EncodedDocument     string
	ObjectPolicy        string
	ObjectPolicyLabel   string
	SourceThemeSlug     string
	SourceThemeVersion  string
	SourceRevision      string
	SourceExportedAt    string
	CurrentRevision     string
	Digest              string
	CurrentMenuCount    int
	CurrentItemCount    int
	ImportedMenuCount   int
	ImportedItemCount   int
	AssignedLocations   int
	AddCount            int
	UpdateCount         int
	DeleteCount         int
	UnchangedCount      int
	DetachedObjectCount int
	Changes             []adminNavigationImportChange
	Warnings            []string
}

type adminNavigationImportChange struct {
	Tone   string
	Action string
	Name   string
	Slug   string
	Detail string
}

type navigationImportValidationError struct {
	Message string
	Fields  map[string]string
}

func (err *navigationImportValidationError) Error() string { return err.Message }

func (s *Server) adminToolsNavigationImport(writer http.ResponseWriter, request *http.Request) {
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return
	}
	if !s.sameOriginRequest(request) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "cross_site_request_rejected"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, navigationImportFormMaxBytes)
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil {
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "navigation_import_content_type_required"})
		return
	}
	switch strings.ToLower(mediaType) {
	case "multipart/form-data":
		s.adminToolsNavigationImportPreview(writer, request, principal)
	case "application/x-www-form-urlencoded":
		s.adminToolsNavigationImportApply(writer, request, principal)
	default:
		writeJSON(writer, http.StatusUnsupportedMediaType, map[string]string{"error": "navigation_import_content_type_required"})
	}
}

func (s *Server) adminToolsNavigationImportPreview(writer http.ResponseWriter, request *http.Request, principal auth.Principal) {
	parseErr := request.ParseMultipartForm(navigationImportFileMaxBytes)
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	request, principal, ok := s.recheckNavigationImport(writer, request)
	if !ok {
		return
	}
	if parseErr != nil {
		status := http.StatusBadRequest
		message := "导航导入请求格式不正确"
		var maximum *http.MaxBytesError
		if errors.As(parseErr, &maximum) {
			status = http.StatusRequestEntityTooLarge
			message = "导航导入请求超过 512 KiB，未读取或执行任何内容"
		}
		s.renderAdminTools(writer, request, status, principal, false, adminNavigationImportView{}, adminNotice{Message: message, Tone: "error"})
		return
	}
	form := request.PostForm
	if !strictNavigationImportPreviewForm(form, request.MultipartForm.File) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_navigation_import_preview_shape"})
		return
	}
	if !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return
	}
	policy := form.Get("object_policy")
	fileHeader := request.MultipartForm.File["navigation_file"][0]
	if fileHeader.Size <= 0 || fileHeader.Size > navigationImportFileMaxBytes {
		s.renderAdminTools(writer, request, http.StatusUnprocessableEntity, principal, false, adminNavigationImportView{}, adminNotice{Message: "请选择不超过 256 KiB 的 NewPress 导航导出 JSON", Tone: "error"})
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		s.renderAdminTools(writer, request, http.StatusUnprocessableEntity, principal, false, adminNavigationImportView{}, adminNotice{Message: "无法读取导航导入文件", Tone: "error"})
		return
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, navigationImportFileMaxBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > navigationImportFileMaxBytes {
		s.renderAdminTools(writer, request, http.StatusUnprocessableEntity, principal, false, adminNavigationImportView{}, adminNotice{Message: "导航导入文件为空、不可读或超过 256 KiB", Tone: "error"})
		return
	}
	document, inputs, canonical, detached, err := prepareNavigationImport(payload, policy)
	if err != nil {
		s.renderNavigationImportValidationError(writer, request, principal, err)
		return
	}
	current, err := s.navigation.Snapshot(request.Context())
	request, principal, ok = s.recheckNavigationImport(writer, request)
	if !ok {
		return
	}
	if err != nil {
		slog.Error("snapshot navigation import preview", "error", err)
		s.renderAdminTools(writer, request, http.StatusServiceUnavailable, principal, false, adminNavigationImportView{}, adminNotice{Message: "当前导航快照不可用，无法生成安全差异预览", Tone: "error"})
		return
	}
	plan := s.navigationImportPlan(document, inputs, canonical, policy, current, safeImportFilename(fileHeader.Filename), detached, s.siteLocation(request.Context()))
	s.renderAdminTools(writer, request, http.StatusOK, principal, false, plan, adminNotice{Message: "导入文件已通过结构、层级、链接和位置校验；请核对差异后再原子替换", Tone: "success"})
}

func (s *Server) adminToolsNavigationImportApply(writer http.ResponseWriter, request *http.Request, principal auth.Principal) {
	parseErr := request.ParseForm()
	request, principal, ok := s.recheckNavigationImport(writer, request)
	if !ok {
		return
	}
	if parseErr != nil {
		status := http.StatusBadRequest
		message := "导航导入确认请求格式不正确"
		var maximum *http.MaxBytesError
		if errors.As(parseErr, &maximum) {
			status = http.StatusRequestEntityTooLarge
			message = "导航导入确认请求超过 512 KiB，未执行任何替换"
		}
		s.renderAdminTools(writer, request, status, principal, false, adminNavigationImportView{}, adminNotice{Message: message, Tone: "error"})
		return
	}
	form := request.PostForm
	if !strictNavigationImportApplyForm(form) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_navigation_import_apply_shape"})
		return
	}
	if !s.auth.CheckCSRF(principal, form.Get("csrf_token")) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "csrf_validation_failed"})
		return
	}
	expectedRevision := form.Get("expected_revision")
	if !navigationImportRevisionPattern.MatchString(expectedRevision) {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_navigation_import_revision"})
		return
	}
	encoded := form.Get("document")
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) == 0 || len(payload) > navigationImportFileMaxBytes {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_navigation_import_document"})
		return
	}
	policy := form.Get("object_policy")
	document, inputs, canonical, detached, err := prepareNavigationImport(payload, policy)
	if err != nil {
		s.renderNavigationImportValidationError(writer, request, principal, err)
		return
	}
	request, principal, ok = s.recheckNavigationImport(writer, request)
	if !ok {
		return
	}
	result, err := s.navigation.ReplaceAll(request.Context(), navigation.RegistryMutation{
		ExpectedRevision: expectedRevision,
		Actor:            principal.Username,
	}, inputs)
	if errors.Is(err, navigation.ErrSnapshotConflict) {
		plan := s.navigationImportPlan(document, inputs, canonical, policy, result, "已验证导航文档", detached, s.siteLocation(request.Context()))
		s.renderAdminTools(writer, request, http.StatusConflict, principal, false, plan, adminNotice{Message: "预览后导航已被其他请求修改；差异已按最新快照重算，请重新确认", Tone: "warning"})
		return
	}
	if err != nil {
		if fields := navigation.FieldErrors(err); len(fields) > 0 {
			plan := adminNavigationImportView{}
			if current, snapshotErr := s.navigation.Snapshot(request.Context()); snapshotErr == nil {
				plan = s.navigationImportPlan(document, inputs, canonical, policy, current, "已验证导航文档", detached, s.siteLocation(request.Context()))
			}
			s.renderAdminTools(writer, request, http.StatusUnprocessableEntity, principal, false, plan, adminNotice{Message: "导入内容未通过当前导航领域校验：" + firstNavigationImportFieldError(fields), Tone: "error"})
			return
		}
		slog.Error("replace navigation registry from import", "actor", principal.Username, "error", err)
		s.renderAdminTools(writer, request, http.StatusServiceUnavailable, principal, false, adminNavigationImportView{}, adminNotice{Message: "导航导入未提交，原导航保持不变", Tone: "error"})
		return
	}
	http.Redirect(writer, request, "/admin/tools/?notice=navigation-imported", http.StatusSeeOther)
}

// Keep the session chosen at the request boundary. Another cookie must not
// take over an import after the original session is revoked during body I/O.
func (s *Server) recheckNavigationImport(writer http.ResponseWriter, request *http.Request) (*http.Request, auth.Principal, bool) {
	previous, token, hadSession := s.currentAdminSession(request)
	state := requestSessionState{}
	if hadSession {
		principal, err := s.auth.ResolveSession(request.Context(), token)
		if err != nil && !errors.Is(err, auth.ErrSessionNotFound) {
			s.sessionStorageError(writer, request)
			return nil, auth.Principal{}, false
		}
		if err == nil {
			if principal.UserID != previous.UserID || principal.Username != previous.Username {
				s.renderError(writer, http.StatusForbidden, "当前登录身份已变化，请重新发起导航导入")
				return nil, auth.Principal{}, false
			}
			state = requestSessionState{principal: principal, token: token, ok: true}
		}
	}
	request = request.WithContext(context.WithValue(request.Context(), sessionStateKey{}, state))
	principal, readOnly, ok := s.requireAdminPageCapability(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return nil, auth.Principal{}, false
	}
	if readOnly || !s.auth.Configured() {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "admin_credentials_not_configured"})
		return nil, auth.Principal{}, false
	}
	return request, principal, true
}

func (s *Server) renderNavigationImportValidationError(writer http.ResponseWriter, request *http.Request, principal auth.Principal, err error) {
	message := "导航导出文档没有通过严格校验"
	var validation *navigationImportValidationError
	if errors.As(err, &validation) {
		message = validation.Message
		if len(validation.Fields) > 0 {
			message += "：" + firstNavigationImportFieldError(validation.Fields)
		}
	}
	s.renderAdminTools(writer, request, http.StatusUnprocessableEntity, principal, false, adminNavigationImportView{}, adminNotice{Message: message, Tone: "error"})
}

func strictNavigationImportPreviewForm(form map[string][]string, files map[string][]*multipart.FileHeader) bool {
	allowed := map[string]bool{"csrf_token": true, "action": true, "object_policy": true}
	if len(form) != len(allowed) || len(files) != 1 {
		return false
	}
	for key, values := range form {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	fileValues, ok := files["navigation_file"]
	return ok && len(fileValues) == 1 && form["action"][0] == "preview" && validNavigationImportObjectPolicy(form["object_policy"][0])
}

func strictNavigationImportApplyForm(form map[string][]string) bool {
	allowed := map[string]bool{
		"csrf_token": true, "action": true, "expected_revision": true,
		"document": true, "object_policy": true, "confirm": true,
	}
	if len(form) != len(allowed) {
		return false
	}
	for key, values := range form {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	return form["action"][0] == "apply" && form["confirm"][0] == "replace" && validNavigationImportObjectPolicy(form["object_policy"][0])
}

func validNavigationImportObjectPolicy(value string) bool {
	return value == navigationImportDetachObjects || value == navigationImportPreserveObjects
}

func prepareNavigationImport(payload []byte, objectPolicy string) (navigationExportDocument, []navigation.RegistryMenuInput, []byte, int, error) {
	var document navigationExportDocument
	if !validNavigationImportObjectPolicy(objectPolicy) || len(payload) == 0 || len(payload) > navigationImportFileMaxBytes {
		return document, nil, nil, 0, &navigationImportValidationError{Message: "导航导入策略或文件大小不正确"}
	}
	if err := validateUniqueJSONObject(payload, &document); err != nil {
		return document, nil, nil, 0, &navigationImportValidationError{Message: "导航 JSON 对象格式不正确，或包含重复字段、字段别名、无效 Unicode、尾随数据"}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return document, nil, nil, 0, &navigationImportValidationError{Message: "导航 JSON 字段未知、缺失或类型不正确"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return document, nil, nil, 0, &navigationImportValidationError{Message: "导航 JSON 只能包含一个文档"}
	}
	inputs, detached, err := navigationImportRegistryInputs(document, objectPolicy)
	if err != nil {
		return document, nil, nil, 0, err
	}
	canonical, err := json.Marshal(document)
	if err != nil || len(canonical) > navigationImportFileMaxBytes {
		return document, nil, nil, 0, &navigationImportValidationError{Message: "导航文档无法规范化或超过 256 KiB"}
	}
	return document, inputs, canonical, detached, nil
}

func navigationImportRegistryInputs(document navigationExportDocument, objectPolicy string) ([]navigation.RegistryMenuInput, int, error) {
	fields := make(map[string]string)
	if document.Schema != 1 {
		fields["schema"] = "只支持 schema 1"
	}
	if document.Kind != "newpress.navigation_export" {
		fields["kind"] = "文档 kind 必须是 newpress.navigation_export"
	}
	if document.ExportedAt.IsZero() {
		fields["exported_at"] = "缺少有效导出时间"
	}
	if !navigationImportRevisionPattern.MatchString(document.Revision) {
		fields["revision"] = "源导航修订必须是 16 位小写十六进制值"
	}
	if !safeNavigationImportText(document.Source.ThemeSlug, 96, false) || !navigation.ValidSlug(document.Source.ThemeSlug) {
		fields["source.theme_slug"] = "源主题别名不正确"
	}
	if !safeNavigationImportText(document.Source.ThemeVersion, 64, false) {
		fields["source.theme_version"] = "源主题版本不正确"
	}
	if document.LocationSlots == nil || len(document.LocationSlots) != len(navigation.Locations()) {
		fields["location_slots"] = "必须完整提供所有位置槽"
	}
	if document.Menus == nil {
		fields["menus"] = "menus 必须是数组，不能省略或为 null"
	}

	menuIndexes := make(map[string]int, len(document.Menus))
	for index, menu := range document.Menus {
		if menu.Locations == nil {
			fields[fmt.Sprintf("menus.%d.locations", index)] = "locations 必须是数组"
		} else {
			seenLocations := make(map[string]bool, len(menu.Locations))
			for locationIndex, location := range menu.Locations {
				canonical := strings.ToLower(strings.TrimSpace(location))
				field := fmt.Sprintf("menus.%d.locations.%d", index, locationIndex)
				if canonical != location || !navigation.ValidLocation(canonical) {
					fields[field] = "菜单位置未知或未规范化"
					continue
				}
				if seenLocations[canonical] {
					fields[field] = "菜单位置重复"
					continue
				}
				seenLocations[canonical] = true
			}
		}
		if menu.Items == nil {
			fields[fmt.Sprintf("menus.%d.items", index)] = "items 必须是数组"
		}
		slug := strings.TrimSpace(menu.Slug)
		if slug == "" || navigation.NormalizeSlug(slug) != slug || !navigation.ValidSlug(slug) {
			fields[fmt.Sprintf("menus.%d.slug", index)] = "菜单别名不是规范化安全 slug"
			continue
		}
		if previous, exists := menuIndexes[strings.ToLower(slug)]; exists {
			fields[fmt.Sprintf("menus.%d.slug", index)] = fmt.Sprintf("菜单别名与第 %d 个菜单重复", previous+1)
			continue
		}
		menuIndexes[strings.ToLower(slug)] = index
	}

	assigned := make(map[string][]string, len(document.Menus))
	seenSlots := make(map[string]bool, len(navigation.Locations()))
	for index, slot := range document.LocationSlots {
		key := strings.TrimSpace(slot.Key)
		if key != slot.Key || !navigation.ValidLocation(key) || seenSlots[key] {
			fields[fmt.Sprintf("location_slots.%d.key", index)] = "位置 key 未知、重复或未规范化"
			continue
		}
		seenSlots[key] = true
		if !safeNavigationImportText(slot.Label, 80, true) {
			fields[fmt.Sprintf("location_slots.%d.label", index)] = "位置标签包含控制字符或过长"
		}
		menuSlug := strings.TrimSpace(slot.MenuSlug)
		if menuSlug == "" {
			continue
		}
		if navigation.NormalizeSlug(menuSlug) != menuSlug || !navigation.ValidSlug(menuSlug) {
			fields[fmt.Sprintf("location_slots.%d.menu_slug", index)] = "位置菜单别名不正确"
			continue
		}
		if _, exists := menuIndexes[strings.ToLower(menuSlug)]; !exists {
			fields[fmt.Sprintf("location_slots.%d.menu_slug", index)] = "位置引用了文档中不存在的菜单"
			continue
		}
		assigned[strings.ToLower(menuSlug)] = append(assigned[strings.ToLower(menuSlug)], key)
	}
	for _, location := range navigation.Locations() {
		if !seenSlots[location] {
			fields["location_slots"] = "核心位置必须各出现一次"
		}
	}

	inputs := make([]navigation.RegistryMenuInput, 0, len(document.Menus))
	detached := 0
	for index, menu := range document.Menus {
		derivedLocations := assigned[strings.ToLower(menu.Slug)]
		normalizedMenu, menuFields := navigation.NormalizeMenuInput(navigation.MenuInput{
			Name: menu.Name, Slug: menu.Slug, Description: menu.Description, Locations: menu.Locations,
		})
		for key, value := range menuFields {
			fields[fmt.Sprintf("menus.%d.%s", index, key)] = value
		}
		sortNavigationLocations(derivedLocations)
		if !navigationImportEqualStrings(normalizedMenu.Locations, derivedLocations) {
			fields[fmt.Sprintf("menus.%d.locations", index)] = "菜单 locations 与位置槽不一致"
		}
		items, detachedCount, itemErr := navigationImportMenuItems(index, menu.Items, objectPolicy)
		if itemErr != nil {
			var validation *navigationImportValidationError
			if errors.As(itemErr, &validation) {
				for key, value := range validation.Fields {
					fields[key] = value
				}
				if len(validation.Fields) == 0 {
					fields[fmt.Sprintf("menus.%d.items", index)] = validation.Message
				}
			} else {
				fields[fmt.Sprintf("menus.%d.items", index)] = "导航项目无法校验"
			}
		} else {
			detached += detachedCount
		}
		normalizedMenu.Locations = append([]string(nil), derivedLocations...)
		inputs = append(inputs, navigation.RegistryMenuInput{Menu: normalizedMenu, Items: items})
	}
	if len(fields) > 0 {
		return nil, 0, &navigationImportValidationError{Message: "导航文档结构不符合导入格式", Fields: fields}
	}
	sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].Menu.Slug < inputs[j].Menu.Slug })
	normalized, registryFields := navigation.NormalizeRegistryInputs(inputs)
	if len(registryFields) > 0 {
		return nil, 0, &navigationImportValidationError{Message: "导航菜单、链接或层级未通过领域校验", Fields: registryFields}
	}
	return normalized, detached, nil
}

func navigationImportMenuItems(menuIndex int, items []navigationExportItem, objectPolicy string) ([]navigation.ItemInput, int, error) {
	fields := make(map[string]string)
	byKey := make(map[string]navigationExportItem, len(items))
	children := make(map[string][]navigationExportItem, len(items))
	positions := make(map[string]map[int]bool, len(items))
	for index, item := range items {
		prefix := fmt.Sprintf("menus.%d.items.%d", menuIndex, index)
		key := strings.TrimSpace(item.Key)
		if key != item.Key || !navigationImportItemKeyPattern.MatchString(key) {
			fields[prefix+".key"] = "项目 key 必须是 1–64 位小写字母、数字或连字符"
			continue
		}
		if _, exists := byKey[key]; exists {
			fields[prefix+".key"] = "项目 key 重复"
			continue
		}
		parentKey := strings.TrimSpace(item.ParentKey)
		if parentKey != item.ParentKey || (parentKey != "" && !navigationImportItemKeyPattern.MatchString(parentKey)) {
			fields[prefix+".parent_key"] = "parent_key 格式不正确"
		}
		if item.Position < 0 || item.Position >= navigation.MaxItemsPerMenu {
			fields[prefix+".position"] = "项目位置超出允许范围"
		}
		if item.Type != strings.ToLower(strings.TrimSpace(item.Type)) || !navigation.ValidItemType(item.Type) {
			fields[prefix+".type"] = "项目类型未知或未规范化"
		} else if item.Type == navigation.TypeCustom && item.ObjectID != 0 {
			fields[prefix+".object_id"] = "自定义链接不能携带对象 ID"
		} else if item.Type != navigation.TypeCustom && item.ObjectID <= 0 {
			fields[prefix+".object_id"] = "文章、页面、分类或标签入口必须携带正对象 ID"
		}
		byKey[key] = item
	}
	for index, item := range items {
		prefix := fmt.Sprintf("menus.%d.items.%d", menuIndex, index)
		if item.ParentKey == item.Key && item.Key != "" {
			fields[prefix+".parent_key"] = "项目不能以自身为父级"
		}
		if item.ParentKey != "" {
			if _, exists := byKey[item.ParentKey]; !exists {
				fields[prefix+".parent_key"] = "父级项目不存在"
			}
		}
		if positions[item.ParentKey] == nil {
			positions[item.ParentKey] = make(map[int]bool)
		}
		if positions[item.ParentKey][item.Position] {
			fields[prefix+".position"] = "同一父级下的位置重复"
		}
		positions[item.ParentKey][item.Position] = true
		children[item.ParentKey] = append(children[item.ParentKey], item)
	}
	for parent, siblings := range children {
		sort.SliceStable(siblings, func(i, j int) bool {
			if siblings[i].Position != siblings[j].Position {
				return siblings[i].Position < siblings[j].Position
			}
			return siblings[i].Key < siblings[j].Key
		})
		children[parent] = siblings
		for index, item := range siblings {
			if item.Position != index {
				fields[fmt.Sprintf("menus.%d.items.%s.position", menuIndex, item.Key)] = "同一父级下的位置必须从 0 连续排列"
			}
		}
	}
	if len(fields) > 0 {
		return nil, 0, &navigationImportValidationError{Message: "导航项目关系不正确", Fields: fields}
	}
	ordered := make([]navigationExportItem, 0, len(items))
	state := make(map[string]uint8, len(items))
	var walk func(string, int) bool
	walk = func(parent string, depth int) bool {
		for _, item := range children[parent] {
			if depth >= navigation.MaxDepth {
				return false
			}
			if state[item.Key] == 1 {
				return false
			}
			if state[item.Key] == 2 {
				continue
			}
			state[item.Key] = 1
			ordered = append(ordered, item)
			if !walk(item.Key, depth+1) {
				return false
			}
			state[item.Key] = 2
		}
		return true
	}
	if !walk("", 0) || len(ordered) != len(items) {
		return nil, 0, &navigationImportValidationError{Message: "导航项目形成循环、超过三层或没有根节点"}
	}
	identities := make(map[string]int64, len(ordered))
	for index, item := range ordered {
		identities[item.Key] = -int64(index + 1)
	}
	result := make([]navigation.ItemInput, 0, len(ordered))
	detached := 0
	for _, item := range ordered {
		itemType := item.Type
		objectID := item.ObjectID
		if objectPolicy == navigationImportDetachObjects && itemType != navigation.TypeCustom {
			itemType = navigation.TypeCustom
			objectID = 0
			detached++
		}
		result = append(result, navigation.ItemInput{
			ID: identities[item.Key], ParentID: identities[item.ParentKey], Type: itemType, ObjectID: objectID,
			Label: item.Label, URL: item.URL, Description: item.Description, Target: item.Target,
			Rel: item.Rel, Icon: item.Icon, Accent: item.Accent, Presentation: item.Presentation, Badge: item.Badge,
		})
	}
	return result, detached, nil
}

func (s *Server) navigationImportPlan(document navigationExportDocument, inputs []navigation.RegistryMenuInput, canonical []byte, objectPolicy string, current navigation.Snapshot, filename string, detached int, location *time.Location) adminNavigationImportView {
	plan := adminNavigationImportView{
		HasPlan: true, FileName: filename, EncodedDocument: base64.RawURLEncoding.EncodeToString(canonical),
		ObjectPolicy: objectPolicy, SourceThemeSlug: document.Source.ThemeSlug, SourceThemeVersion: document.Source.ThemeVersion,
		SourceRevision: document.Revision, SourceExportedAt: siteTime(location, document.ExportedAt, "2006-01-02 15:04:05 MST"),
		CurrentRevision: navigation.SnapshotRevision(current), CurrentMenuCount: current.MenuCount, CurrentItemCount: current.ItemCount,
		ImportedMenuCount: len(inputs), DetachedObjectCount: detached,
	}
	if objectPolicy == navigationImportDetachObjects {
		plan.ObjectPolicyLabel = "跨站安全：解除对象 ID"
	} else {
		plan.ObjectPolicyLabel = "同站恢复：保留对象 ID"
	}
	for _, input := range inputs {
		plan.ImportedItemCount += len(input.Items)
		plan.AssignedLocations += len(input.Menu.Locations)
	}
	digestInput := append(append([]byte(nil), canonical...), '\n')
	digestInput = append(digestInput, objectPolicy...)
	digest := sha256.Sum256(digestInput)
	plan.Digest = hex.EncodeToString(digest[:])
	currentMenus := make(map[string]navigation.Menu, len(current.Menus))
	for _, menu := range current.Menus {
		currentMenus[strings.ToLower(menu.Slug)] = menu
	}
	importedSlugs := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		slugKey := strings.ToLower(input.Menu.Slug)
		importedSlugs[slugKey] = true
		currentMenu, exists := currentMenus[slugKey]
		if !exists {
			plan.AddCount++
			plan.Changes = append(plan.Changes, adminNavigationImportChange{
				Tone: "add", Action: "新增", Name: input.Menu.Name, Slug: input.Menu.Slug,
				Detail: fmt.Sprintf("新增 %d 个项目 · %s", len(input.Items), navigationImportLocationsLabel(input.Menu.Locations)),
			})
			continue
		}
		if navigationRegistryMenuComparableFromCurrent(currentMenu) == navigationRegistryMenuComparableFromInput(input) {
			plan.UnchangedCount++
			plan.Changes = append(plan.Changes, adminNavigationImportChange{
				Tone: "same", Action: "保持", Name: input.Menu.Name, Slug: input.Menu.Slug,
				Detail: fmt.Sprintf("结构一致 · %d 个项目 · %s", len(input.Items), navigationImportLocationsLabel(input.Menu.Locations)),
			})
			continue
		}
		plan.UpdateCount++
		plan.Changes = append(plan.Changes, adminNavigationImportChange{
			Tone: "update", Action: "替换", Name: input.Menu.Name, Slug: input.Menu.Slug,
			Detail: fmt.Sprintf("%d → %d 个项目 · %s", len(currentMenu.Items), len(input.Items), navigationImportLocationsLabel(input.Menu.Locations)),
		})
	}
	for _, menu := range current.Menus {
		if importedSlugs[strings.ToLower(menu.Slug)] {
			continue
		}
		plan.DeleteCount++
		plan.Changes = append(plan.Changes, adminNavigationImportChange{
			Tone: "delete", Action: "删除", Name: menu.Name, Slug: menu.Slug,
			Detail: fmt.Sprintf("从目标站点移除 %d 个项目 · %s", len(menu.Items), navigationImportLocationsLabel(menu.Locations)),
		})
	}
	plan.Warnings = append(plan.Warnings, "应用会精确替换整个导航注册表；未出现在文档中的菜单会在同一事务中删除。")
	if objectPolicy == navigationImportDetachObjects && detached > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("跨站安全策略会把 %d 个文章/页面/分类/标签入口转为自定义链接，保留文字与 URL 但不沿用源站数字 ID。", detached))
	}
	if objectPolicy == navigationImportPreserveObjects {
		plan.Warnings = append(plan.Warnings, "同站恢复策略会保留 object_id；仅当目标内容/分类仓库与导出来源相同时使用。")
	}
	activeTheme := s.themes.Active()
	if document.Source.ThemeSlug != activeTheme.Slug {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("文档来自主题 %s，当前主题为 %s；核心位置可导入，但展示方式需要人工复核。", document.Source.ThemeSlug, activeTheme.Slug))
	}
	if document.Revision != plan.CurrentRevision {
		plan.Warnings = append(plan.Warnings, "源修订与目标当前修订不同；这对跨站迁移是正常情况，应用时仍以目标快照修订防止并发覆盖。")
	}
	if len(inputs) == 0 {
		plan.Warnings = append(plan.Warnings, "文档不包含任何菜单；确认后会清空全部自定义导航并让主题进入安全 fallback。")
	}
	return plan
}

type navigationRegistryComparableItem struct {
	Parent       int
	Type         string
	ObjectID     int64
	Label        string
	URL          string
	Description  string
	Target       string
	Rel          string
	Icon         string
	Accent       string
	Presentation string
	Badge        navigation.Badge
}

type navigationRegistryComparableMenu struct {
	Name        string
	Slug        string
	Description string
	Locations   string
	Items       string
}

func navigationRegistryMenuComparableFromCurrent(menu navigation.Menu) navigationRegistryComparableMenu {
	flat := navigation.Flatten(menu)
	indexes := make(map[int64]int, len(flat))
	for index, item := range flat {
		indexes[item.Item.ID] = index + 1
	}
	items := make([]navigationRegistryComparableItem, 0, len(flat))
	for _, item := range flat {
		items = append(items, navigationRegistryComparableItem{
			Parent: indexes[item.Item.ParentID], Type: item.Item.Type, ObjectID: item.Item.ObjectID,
			Label: item.Item.Label, URL: item.Item.URL, Description: item.Item.Description,
			Target: item.Item.Target, Rel: item.Item.Rel, Icon: item.Item.Icon,
			Accent: item.Item.Accent, Presentation: item.Item.Presentation, Badge: item.Item.Badge,
		})
	}
	encoded, _ := json.Marshal(items)
	return navigationRegistryComparableMenu{
		Name: menu.Name, Slug: menu.Slug, Description: menu.Description,
		Locations: strings.Join(menu.Locations, ","), Items: string(encoded),
	}
}

func navigationRegistryMenuComparableFromInput(input navigation.RegistryMenuInput) navigationRegistryComparableMenu {
	indexes := make(map[int64]int, len(input.Items))
	for index, item := range input.Items {
		indexes[item.ID] = index + 1
	}
	items := make([]navigationRegistryComparableItem, 0, len(input.Items))
	for _, item := range input.Items {
		items = append(items, navigationRegistryComparableItem{
			Parent: indexes[item.ParentID], Type: item.Type, ObjectID: item.ObjectID,
			Label: item.Label, URL: item.URL, Description: item.Description,
			Target: item.Target, Rel: item.Rel, Icon: item.Icon,
			Accent: item.Accent, Presentation: item.Presentation, Badge: item.Badge,
		})
	}
	encoded, _ := json.Marshal(items)
	return navigationRegistryComparableMenu{
		Name: input.Menu.Name, Slug: input.Menu.Slug, Description: input.Menu.Description,
		Locations: strings.Join(input.Menu.Locations, ","), Items: string(encoded),
	}
}

func navigationImportLocationsLabel(locations []string) string {
	if len(locations) == 0 {
		return "未分配位置"
	}
	labels := make([]string, 0, len(locations))
	for _, location := range locations {
		labels = append(labels, navigation.LocationLabel(location))
	}
	return strings.Join(labels, " / ")
}

func sortNavigationLocations(locations []string) {
	order := make(map[string]int, len(navigation.Locations()))
	for index, location := range navigation.Locations() {
		order[location] = index
	}
	sort.SliceStable(locations, func(i, j int) bool { return order[locations[i]] < order[locations[j]] })
}

func navigationImportEqualStrings(left, right []string) bool {
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

func safeNavigationImportText(value string, maximum int, allowEmpty bool) bool {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return false
	}
	if value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func safeImportFilename(value string) string {
	value = strings.TrimSpace(filepath.Base(value))
	if value == "" || value == "." {
		return "navigation-import.json"
	}
	runes := []rune(value)
	if len(runes) > 80 {
		value = string(runes[:80])
	}
	return value
}

func firstNavigationImportFieldError(fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "字段不正确"
	}
	return keys[0] + " — " + fields[keys[0]]
}
