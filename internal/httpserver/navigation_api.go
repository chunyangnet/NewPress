package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/packagefs"
)

const adminNavigationAPIRequestMaxBytes = 256 << 10

type adminNavigationMenuCreateAPIRequest struct {
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Description string   `json:"description"`
	Locations   []string `json:"locations"`
}

type adminNavigationMenuPatchAPIRequest struct {
	Name        apiOptionalString      `json:"name"`
	Slug        apiOptionalString      `json:"slug"`
	Description apiOptionalString      `json:"description"`
	Locations   apiOptionalStringSlice `json:"locations"`
}

func (request adminNavigationMenuPatchAPIRequest) Empty() bool {
	return !request.Name.Present && !request.Slug.Present && !request.Description.Present && !request.Locations.Present
}

type navigationItemInputsAPIValue struct {
	Present bool
	Value   []navigation.ItemInput
}

func (value *navigationItemInputsAPIValue) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("items must be a JSON array, not null")
	}
	var items []navigation.ItemInput
	if err := packagefs.ValidateUniqueJSONFor(data, &items); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&items); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("items must contain one JSON array")
	}
	if items == nil {
		items = []navigation.ItemInput{}
	}
	value.Present, value.Value = true, items
	return nil
}

type adminNavigationItemsAPIRequest struct {
	Items navigationItemInputsAPIValue `json:"items"`
}

type adminNavigationMenuAPIResponse struct {
	ID          int64             `json:"id"`
	Version     int64             `json:"version"`
	Name        string            `json:"name"`
	Slug        string            `json:"slug"`
	Description string            `json:"description"`
	Locations   []string          `json:"locations"`
	Items       []navigation.Item `json:"items"`
	ItemCount   int               `json:"item_count"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	ETag        string            `json:"etag"`
}

type adminNavigationLocationAPIResponse struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	MenuID      int64  `json:"menu_id,omitempty"`
}

type adminNavigationLimitsAPIResponse struct {
	Menus            int `json:"menus"`
	ItemsPerMenu     int `json:"items_per_menu"`
	Depth            int `json:"depth"`
	MenuNameRunes    int `json:"menu_name_runes"`
	DescriptionRunes int `json:"description_runes"`
	ItemLabelRunes   int `json:"item_label_runes"`
	ItemDescription  int `json:"item_description_runes"`
	URLBytes         int `json:"url_bytes"`
	RequestBodyBytes int `json:"request_body_bytes"`
}

type adminNavigationMenusAPIResponse struct {
	Revision      string                               `json:"revision"`
	ETag          string                               `json:"etag"`
	UpdatedAt     *time.Time                           `json:"updated_at,omitempty"`
	Menus         []adminNavigationMenuAPIResponse     `json:"menus"`
	Locations     map[string]int64                     `json:"locations"`
	LocationSlots []adminNavigationLocationAPIResponse `json:"location_slots"`
	MenuCount     int                                  `json:"menu_count"`
	ItemCount     int                                  `json:"item_count"`
	Limits        adminNavigationLimitsAPIResponse     `json:"limits"`
}

type adminNavigationSourceAPIResponse struct {
	Type        string `json:"type"`
	ObjectID    int64  `json:"object_id,omitempty"`
	Label       string `json:"label"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
}

type adminNavigationSourceGroupAPIResponse struct {
	ID      string                             `json:"id"`
	Label   string                             `json:"label"`
	Count   int                                `json:"count"`
	Sources []adminNavigationSourceAPIResponse `json:"sources"`
}

type adminNavigationSourcesAPIResponse struct {
	Groups []adminNavigationSourceGroupAPIResponse `json:"groups"`
	Count  int                                     `json:"count"`
	ETag   string                                  `json:"etag"`
}

type publicNavigationItemAPIResponse struct {
	ID           int64                             `json:"id"`
	ParentID     int64                             `json:"parent_id,omitempty"`
	Position     int                               `json:"position"`
	Type         string                            `json:"type"`
	ObjectID     int64                             `json:"object_id,omitempty"`
	Label        string                            `json:"label"`
	URL          string                            `json:"url"`
	Description  string                            `json:"description,omitempty"`
	Target       string                            `json:"target"`
	Rel          string                            `json:"rel,omitempty"`
	Icon         string                            `json:"icon,omitempty"`
	Accent       string                            `json:"accent,omitempty"`
	Presentation string                            `json:"presentation"`
	Badge        navigation.Badge                  `json:"badge,omitzero"`
	Children     []publicNavigationItemAPIResponse `json:"children"`
}

type publicNavigationMenuAPIResponse struct {
	Location      string                            `json:"location"`
	LocationLabel string                            `json:"location_label"`
	Revision      string                            `json:"revision"`
	ID            int64                             `json:"id"`
	Version       int64                             `json:"version"`
	Name          string                            `json:"name"`
	Slug          string                            `json:"slug"`
	Description   string                            `json:"description,omitempty"`
	UpdatedAt     time.Time                         `json:"updated_at"`
	ETag          string                            `json:"etag"`
	Items         []publicNavigationItemAPIResponse `json:"items"`
}

func (s *Server) adminNavigationMenusAPI(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityNavigationManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	snapshot, err := s.navigation.Snapshot(request.Context())
	if err != nil {
		slog.Error("list navigation API", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "navigation.unavailable", "导航配置暂时不可用", nil)
		return
	}
	response := adminNavigationSnapshotResponse(snapshot)
	setNavigationSnapshotAPIHeaders(writer, snapshot)
	if etagMatches(request.Header.Get("If-None-Match"), response.ETag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) adminNavigationMenuAPI(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityNavigationManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	menu, ok := s.loadNavigationMenuAPI(writer, request)
	if !ok {
		return
	}
	setNavigationMenuAPIHeaders(writer, menu)
	if etagMatches(request.Header.Get("If-None-Match"), navigationMenuAPIETag(menu)) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(writer, http.StatusOK, adminNavigationMenuResponse(menu))
}

func (s *Server) adminNavigationMenuCreateAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	idempotencyKey, err := strictIdempotencyKey(request)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.invalid_idempotency_key", "Idempotency-Key 必须是 8–128 位字母、数字或 . _ : -", nil)
		return
	}
	var payload adminNavigationMenuCreateAPIRequest
	body, ok := s.decodeStrictAPIJSONLimit(writer, request, &payload, adminNavigationAPIRequestMaxBytes, "256 KiB")
	if !ok {
		return
	}
	input := navigation.MenuInput{
		Name: payload.Name, Slug: payload.Slug, Description: payload.Description,
		Locations: append([]string(nil), payload.Locations...),
	}
	if idempotencyKey != "" {
		s.createNavigationAPISubmission(writer, request, principal, input, body, idempotencyKey)
		return
	}
	menu, err := s.navigation.Create(request.Context(), input, navigationAPIActor(principal))
	if err != nil {
		s.handleNavigationAPIError(writer, request, 0, err)
		return
	}
	s.writeNavigationMenuAPISuccess(writer, http.StatusCreated, menu, true)
}

func (s *Server) adminNavigationMenuUpdateAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	current, ok := s.loadNavigationMenuAPI(writer, request)
	if !ok || !s.requireNavigationAPIPrecondition(writer, request, current) {
		return
	}
	var payload adminNavigationMenuPatchAPIRequest
	if _, ok := s.decodeStrictAPIJSONLimit(writer, request, &payload, adminNavigationAPIRequestMaxBytes, "256 KiB"); !ok {
		return
	}
	if payload.Empty() {
		s.writeAPIError(writer, request, http.StatusBadRequest, "request.empty_patch", "PATCH 至少需要一个可写字段", nil)
		return
	}
	input := navigation.MenuInput{
		Name: current.Name, Slug: current.Slug, Description: current.Description,
		Locations: append([]string(nil), current.Locations...),
	}
	if payload.Name.Present {
		input.Name = payload.Name.Value
	}
	if payload.Slug.Present {
		input.Slug = payload.Slug.Value
	}
	if payload.Description.Present {
		input.Description = payload.Description.Value
	}
	if payload.Locations.Present {
		input.Locations = append([]string(nil), payload.Locations.Value...)
	}
	updated, err := s.navigation.Update(request.Context(), current.ID, navigation.Mutation{
		ExpectedVersion: current.Version, Actor: navigationAPIActor(principal),
	}, input)
	if err != nil {
		s.handleNavigationAPIError(writer, request, current.ID, err)
		return
	}
	s.writeNavigationMenuAPISuccess(writer, http.StatusOK, updated, false)
}

func (s *Server) adminNavigationMenuItemsAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	current, ok := s.loadNavigationMenuAPI(writer, request)
	if !ok || !s.requireNavigationAPIPrecondition(writer, request, current) {
		return
	}
	var payload adminNavigationItemsAPIRequest
	if _, ok := s.decodeStrictAPIJSONLimit(writer, request, &payload, adminNavigationAPIRequestMaxBytes, "256 KiB"); !ok {
		return
	}
	if !payload.Items.Present {
		s.writeAPIError(writer, request, http.StatusBadRequest, "navigation.items_required", "请求必须包含 items 数组", map[string]any{"field": "items"})
		return
	}
	updated, err := s.navigation.ReplaceItems(request.Context(), current.ID, navigation.Mutation{
		ExpectedVersion: current.Version, Actor: navigationAPIActor(principal),
	}, payload.Items.Value)
	if err != nil {
		s.handleNavigationAPIError(writer, request, current.ID, err)
		return
	}
	s.writeNavigationMenuAPISuccess(writer, http.StatusOK, updated, false)
}

func (s *Server) adminNavigationMenuDeleteAPI(writer http.ResponseWriter, request *http.Request) {
	principal, ok := s.requireAdminJSONWrite(writer, request, auth.CapabilityNavigationManage)
	if !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok || !s.requireEmptyAPIBody(writer, request) {
		return
	}
	current, ok := s.loadNavigationMenuAPI(writer, request)
	if !ok || !s.requireNavigationAPIPrecondition(writer, request, current) {
		return
	}
	if err := s.navigation.Delete(request.Context(), current.ID, navigation.Mutation{
		ExpectedVersion: current.Version, Actor: navigationAPIActor(principal),
	}); err != nil {
		s.handleNavigationAPIError(writer, request, current.ID, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminNavigationSourcesAPI(writer http.ResponseWriter, request *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityNavigationManage); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	groups, err := s.navigationSources(request)
	if err != nil {
		slog.Error("list navigation sources API", "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "navigation.sources_unavailable", "导航入口资源暂时不可用", nil)
		return
	}
	response := adminNavigationSourcesResponse(groups)
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", response.ETag)
	if etagMatches(request.Header.Get("If-None-Match"), response.ETag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) publicNavigationLocationAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	writer.Header().Set("Cache-Control", "no-store")
	if _, ok := s.strictAPIQuery(writer, request, nil); !ok {
		return
	}
	location := strings.ToLower(strings.TrimSpace(request.PathValue("location")))
	if !navigation.ValidLocation(location) {
		s.writeAPIError(writer, request, http.StatusNotFound, "navigation.location_not_found", "导航位置没有找到", nil)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), navigationReadTimeout)
	defer cancel()
	menu, err := s.navigation.GetLocation(ctx, location)
	if request.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	if ctx.Err() != nil {
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "navigation.unavailable", "导航配置暂时不可用", nil)
		return
	}
	if errors.Is(err, navigation.ErrNotFound) {
		s.writeAPIError(writer, request, http.StatusNotFound, "navigation.location_unassigned", "导航位置尚未分配菜单", map[string]any{"location": location})
		return
	}
	if err != nil {
		slog.Error("load public navigation API", "location", location, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "navigation.unavailable", "导航配置暂时不可用", nil)
		return
	}
	menu = s.publicNavigationMenu(menu)
	etag := navigationLocationAPIETag(location, menu)
	writer.Header().Set("ETag", etag)
	writer.Header().Set("Last-Modified", menu.UpdatedAt.UTC().Format(http.TimeFormat))
	cacheSeconds := s.siteSettings.Snapshot().Settings.Operations.PublicCacheSeconds
	if cacheSeconds > 0 {
		writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, stale-while-revalidate=%d", cacheSeconds, max(60, cacheSeconds*5)))
	} else {
		writer.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
	if etagMatches(request.Header.Get("If-None-Match"), etag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(publicNavigationMenuResponse(location, menu, etag))
}

func (s *Server) adminNavigationAPIMethodNotAllowed(allow string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if _, _, ok := s.requireAdminJSONReadCapability(writer, request, auth.CapabilityNavigationManage); !ok {
			return
		}
		writer.Header().Set("Allow", allow)
		s.writeAPIError(writer, request, http.StatusMethodNotAllowed, "request.method_not_allowed", "此资源不支持当前 HTTP 方法", map[string]any{"allow": allow})
	}
}

func (s *Server) publicNavigationAPIMethodNotAllowed(allow string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Allow", allow)
		s.writeAPIError(writer, request, http.StatusMethodNotAllowed, "request.method_not_allowed", "此资源不支持当前 HTTP 方法", map[string]any{"allow": allow})
	}
}

func (s *Server) loadNavigationMenuAPI(writer http.ResponseWriter, request *http.Request) (navigation.Menu, bool) {
	id, err := positivePathID(request.PathValue("id"))
	if err != nil {
		s.writeAPIError(writer, request, http.StatusNotFound, "navigation.menu_not_found", "菜单没有找到", nil)
		return navigation.Menu{}, false
	}
	menu, err := s.navigation.Get(request.Context(), id)
	if errors.Is(err, navigation.ErrNotFound) {
		s.writeAPIError(writer, request, http.StatusNotFound, "navigation.menu_not_found", "菜单没有找到", nil)
		return navigation.Menu{}, false
	}
	if err != nil {
		slog.Error("load navigation menu API", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "navigation.unavailable", "导航配置暂时不可用", nil)
		return navigation.Menu{}, false
	}
	return menu, true
}

func (s *Server) requireNavigationAPIPrecondition(writer http.ResponseWriter, request *http.Request, menu navigation.Menu) bool {
	values := request.Header.Values("If-Match")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		s.writeAPIError(writer, request, http.StatusPreconditionRequired, "navigation.precondition_required", "写入菜单必须携带当前 If-Match ETag", map[string]any{
			"current_etag": navigationMenuAPIETag(menu), "current_version": menu.Version,
		})
		return false
	}
	if !strongETagMatches(values[0], navigationMenuAPIETag(menu)) {
		s.writeNavigationPreconditionFailed(writer, request, menu)
		return false
	}
	return true
}

func (s *Server) writeNavigationPreconditionFailed(writer http.ResponseWriter, request *http.Request, menu navigation.Menu) {
	writer.Header().Set("ETag", navigationMenuAPIETag(menu))
	s.writeAPIError(writer, request, http.StatusPreconditionFailed, "navigation.precondition_failed", "菜单已被其他请求更新，请重新读取后合并", map[string]any{
		"current_etag": navigationMenuAPIETag(menu), "current_version": menu.Version,
	})
}

func (s *Server) handleNavigationAPIError(writer http.ResponseWriter, request *http.Request, id int64, err error) {
	switch {
	case errors.Is(err, navigation.ErrNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "navigation.menu_not_found", "菜单没有找到", nil)
	case errors.Is(err, navigation.ErrVersionConflict):
		current, loadErr := s.navigation.Get(request.Context(), id)
		if loadErr == nil {
			s.writeNavigationPreconditionFailed(writer, request, current)
			return
		}
		if errors.Is(loadErr, navigation.ErrNotFound) {
			s.writeAPIError(writer, request, http.StatusNotFound, "navigation.menu_not_found", "菜单没有找到", nil)
			return
		}
		slog.Error("load current navigation after API conflict", "id", id, "error", loadErr, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "navigation.unavailable", "无法读取当前菜单版本", nil)
	case errors.Is(err, navigation.ErrSlugConflict):
		s.writeAPIError(writer, request, http.StatusConflict, "navigation.slug_conflict", "已有菜单使用这个别名", map[string]any{
			"fields": map[string]string{"slug": "已有菜单使用这个别名"},
		})
	case errors.Is(err, navigation.ErrLocationConflict):
		s.writeAPIError(writer, request, http.StatusConflict, "navigation.location_conflict", "至少一个导航位置已分配给其他菜单", map[string]any{
			"fields": map[string]string{"locations": "导航位置不能同时分配给多个菜单"},
		})
	default:
		if fields := navigation.FieldErrors(err); len(fields) > 0 {
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "navigation.validation_failed", "导航配置没有通过服务端校验", map[string]any{"fields": fields})
			return
		}
		if errors.Is(err, navigation.ErrInvalidInput) {
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "navigation.validation_failed", "导航配置没有通过服务端校验", nil)
			return
		}
		slog.Error("mutate navigation API", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusInternalServerError, "navigation.update_failed", "导航配置暂时无法保存", nil)
	}
}

func (s *Server) writeNavigationMenuAPISuccess(writer http.ResponseWriter, status int, menu navigation.Menu, location bool) {
	setNavigationMenuAPIHeaders(writer, menu)
	if location {
		writer.Header().Set("Location", "/api/v1/admin/navigation/menus/"+strconv.FormatInt(menu.ID, 10))
	}
	writeJSON(writer, status, adminNavigationMenuResponse(menu))
}

func adminNavigationSnapshotResponse(snapshot navigation.Snapshot) adminNavigationMenusAPIResponse {
	revision := navigationSnapshotRevision(snapshot)
	menus := make([]adminNavigationMenuAPIResponse, 0, len(snapshot.Menus))
	for _, menu := range snapshot.Menus {
		menus = append(menus, adminNavigationMenuResponse(menu))
	}
	locations := make(map[string]int64, len(snapshot.Locations))
	for key, value := range snapshot.Locations {
		locations[key] = value
	}
	slots := make([]adminNavigationLocationAPIResponse, 0, len(navigation.Locations()))
	for _, location := range navigation.Locations() {
		slots = append(slots, adminNavigationLocationAPIResponse{
			Key: location, Label: navigation.LocationLabel(location), Description: navigationLocationDetail(location),
			MenuID: locations[location],
		})
	}
	var updatedAt *time.Time
	if !snapshot.UpdatedAt.IsZero() {
		value := snapshot.UpdatedAt.UTC()
		updatedAt = &value
	}
	return adminNavigationMenusAPIResponse{
		Revision: revision, ETag: navigationSnapshotAPIETag(snapshot), UpdatedAt: updatedAt,
		Menus: menus, Locations: locations, LocationSlots: slots,
		MenuCount: snapshot.MenuCount, ItemCount: snapshot.ItemCount,
		Limits: adminNavigationLimitsAPIResponse{
			Menus: navigation.MaxMenus, ItemsPerMenu: navigation.MaxItemsPerMenu, Depth: navigation.MaxDepth,
			MenuNameRunes: navigation.MaxMenuNameRunes, DescriptionRunes: navigation.MaxDescriptionRunes,
			ItemLabelRunes: navigation.MaxItemLabelRunes, ItemDescription: navigation.MaxItemDescription,
			URLBytes: navigation.MaxURLBytes, RequestBodyBytes: adminNavigationAPIRequestMaxBytes,
		},
	}
}

func adminNavigationMenuResponse(menu navigation.Menu) adminNavigationMenuAPIResponse {
	return adminNavigationMenuAPIResponse{
		ID: menu.ID, Version: menu.Version, Name: menu.Name, Slug: menu.Slug, Description: menu.Description,
		Locations: append([]string{}, menu.Locations...), Items: append([]navigation.Item{}, menu.Items...),
		ItemCount: len(menu.Items), CreatedAt: menu.CreatedAt, UpdatedAt: menu.UpdatedAt,
		ETag: navigationMenuAPIETag(menu),
	}
}

func adminNavigationSourcesResponse(groups []adminNavigationSourceGroup) adminNavigationSourcesAPIResponse {
	result := make([]adminNavigationSourceGroupAPIResponse, 0, len(groups))
	total := 0
	for _, group := range groups {
		sources := make([]adminNavigationSourceAPIResponse, 0, len(group.Sources))
		for _, source := range group.Sources {
			sources = append(sources, adminNavigationSourceAPIResponse{
				Type: source.Type, ObjectID: source.ObjectID, Label: source.Label, URL: source.URL,
				Description: source.Description, Icon: source.Icon,
			})
		}
		total += len(sources)
		result = append(result, adminNavigationSourceGroupAPIResponse{
			ID: group.ID, Label: group.Label, Count: group.Count, Sources: sources,
		})
	}
	encoded, _ := json.Marshal(result)
	digest := sha256.Sum256(encoded)
	etag := fmt.Sprintf(`"navigation-sources-%x"`, digest[:8])
	return adminNavigationSourcesAPIResponse{Groups: result, Count: total, ETag: etag}
}

func publicNavigationMenuResponse(location string, menu navigation.Menu, etag string) publicNavigationMenuAPIResponse {
	return publicNavigationMenuAPIResponse{
		Location: location, LocationLabel: navigation.LocationLabel(location), Revision: navigationLocationRevision(location, menu),
		ID: menu.ID, Version: menu.Version, Name: menu.Name, Slug: menu.Slug, Description: menu.Description,
		UpdatedAt: menu.UpdatedAt, ETag: etag, Items: publicNavigationItems(menu),
	}
}

func publicNavigationItems(menu navigation.Menu) []publicNavigationItemAPIResponse {
	children := make(map[int64][]navigation.Item, len(menu.Items))
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
	var build func(int64, int) []publicNavigationItemAPIResponse
	build = func(parentID int64, depth int) []publicNavigationItemAPIResponse {
		if depth >= navigation.MaxDepth {
			return []publicNavigationItemAPIResponse{}
		}
		result := make([]publicNavigationItemAPIResponse, 0, len(children[parentID]))
		for _, item := range children[parentID] {
			result = append(result, publicNavigationItemAPIResponse{
				ID: item.ID, ParentID: item.ParentID, Position: item.Position, Type: item.Type, ObjectID: item.ObjectID,
				Label: item.Label, URL: item.URL, Description: item.Description, Target: item.Target, Rel: item.Rel,
				Icon: item.Icon, Accent: item.Accent, Presentation: item.Presentation, Badge: item.Badge,
				Children: build(item.ID, depth+1),
			})
		}
		return result
	}
	return build(0, 0)
}

func setNavigationMenuAPIHeaders(writer http.ResponseWriter, menu navigation.Menu) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", navigationMenuAPIETag(menu))
	writer.Header().Set("Last-Modified", menu.UpdatedAt.UTC().Format(http.TimeFormat))
}

func setNavigationSnapshotAPIHeaders(writer http.ResponseWriter, snapshot navigation.Snapshot) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("ETag", navigationSnapshotAPIETag(snapshot))
	if !snapshot.UpdatedAt.IsZero() {
		writer.Header().Set("Last-Modified", snapshot.UpdatedAt.UTC().Format(http.TimeFormat))
	}
}

func navigationMenuAPIETag(menu navigation.Menu) string {
	return fmt.Sprintf(`"navigation-menu-%d-v%d"`, menu.ID, menu.Version)
}

func navigationSnapshotAPIETag(snapshot navigation.Snapshot) string {
	return `"navigation-snapshot-` + navigationSnapshotRevision(snapshot) + `"`
}

func navigationLocationRevision(location string, menu navigation.Menu) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s:%d:%d:%s|", location, menu.ID, menu.Version, menu.Slug)
	for _, item := range menu.Items {
		fmt.Fprintf(hash, "%d:%d:%d:%s:%s:%s:%s:%s:%s|", item.ID, item.ParentID, item.Position, item.Type, item.Label, item.URL, item.Target, item.Rel, item.Presentation)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)[:8])
}

func navigationLocationAPIETag(location string, menu navigation.Menu) string {
	return `"navigation-` + location + `-` + navigationLocationRevision(location, menu) + `"`
}

func navigationAPIActor(principal auth.Principal) string {
	if strings.TrimSpace(principal.Username) != "" {
		return principal.Username
	}
	return principal.Label()
}
