package httpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/navigation"
)

func TestAdminToolsNavigationImportPreviewApplyDetachAndReplace(t *testing.T) {
	server, handler, _, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	legacy, err := server.navigation.Create(t.Context(), navigation.MenuInput{
		Name: "旧导航", Slug: "legacy-navigation", Locations: []string{navigation.LocationPrimary},
	}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err = server.navigation.ReplaceItems(t.Context(), legacy.ID, navigation.Mutation{
		ExpectedVersion: legacy.Version, Actor: owner.Username,
	}, []navigation.ItemInput{{
		ID: -1, Type: navigation.TypeCustom, Label: "旧入口", URL: "/legacy/",
		Target: navigation.TargetSelf, Presentation: navigation.PresentationStandard,
	}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	payload := marshalNavigationImportDocument(t, validNavigationImportDocument())

	preview := serveNavigationImportPreview(handler, ownerCookie, url.Values{
		"csrf_token":    {owner.CSRFToken},
		"action":        {"preview"},
		"object_policy": {navigationImportDetachObjects},
	}, "portable-navigation.json", payload, "https://example.test")
	previewBody := preview.Body.String()
	if preview.Code != http.StatusOK || !strings.Contains(previewBody, "导入预览") ||
		!strings.Contains(previewBody, "跨站安全：解除对象 ID") ||
		!strings.Contains(previewBody, "legacy-navigation") ||
		!strings.Contains(previewBody, "portable-main") ||
		!strings.Contains(previewBody, "会把 2 个文章/页面/分类/标签入口转为自定义链接") {
		t.Fatalf("navigation import preview = %d %s", preview.Code, previewBody)
	}
	afterPreview, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if navigation.SnapshotRevision(afterPreview) != navigation.SnapshotRevision(before) || afterPreview.MenuCount != 1 {
		t.Fatalf("preview mutated navigation: before=%#v after=%#v", before, afterPreview)
	}

	apply := serveNavigationImportApply(handler, ownerCookie, url.Values{
		"csrf_token":        {owner.CSRFToken},
		"action":            {"apply"},
		"expected_revision": {navigation.SnapshotRevision(before)},
		"object_policy":     {navigationImportDetachObjects},
		"document":          {base64.RawURLEncoding.EncodeToString(payload)},
		"confirm":           {"replace"},
	}, "https://example.test")
	if apply.Code != http.StatusSeeOther || apply.Header().Get("Location") != "/admin/tools/?notice=navigation-imported" {
		t.Fatalf("navigation import apply = %d %#v %s", apply.Code, apply.Header(), apply.Body.String())
	}

	after, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.MenuCount != 2 || after.ItemCount != 5 || navigation.SnapshotRevision(after) == navigation.SnapshotRevision(before) {
		t.Fatalf("imported snapshot = %#v", after)
	}
	for _, menu := range after.Menus {
		if menu.ID == legacy.ID || menu.Slug == legacy.Slug {
			t.Fatalf("legacy menu survived full registry replacement: %#v", menu)
		}
	}
	main := menuBySlug(t, after, "portable-main")
	if !slices.Equal(main.Locations, []string{navigation.LocationPrimary, navigation.LocationMobile}) {
		t.Fatalf("imported primary locations = %#v", main.Locations)
	}
	flat := navigation.Flatten(main)
	if len(flat) != 4 || flat[0].Depth != 0 || flat[1].Depth != 1 || flat[2].Depth != 2 || flat[3].Depth != 0 {
		t.Fatalf("imported hierarchy = %#v", flat)
	}
	for _, index := range []int{0, 1} {
		if flat[index].Item.Type != navigation.TypeCustom || flat[index].Item.ObjectID != 0 {
			t.Fatalf("detached object item %d = %#v", index, flat[index].Item)
		}
	}
	quick := menuBySlug(t, after, "portable-quick")
	if !slices.Equal(quick.Locations, []string{navigation.LocationUtility, navigation.LocationFooter}) {
		t.Fatalf("imported quick locations = %#v", quick.Locations)
	}
	for location, slug := range map[string]string{
		navigation.LocationPrimary: "portable-main", navigation.LocationMobile: "portable-main",
		navigation.LocationUtility: "portable-quick", navigation.LocationFooter: "portable-quick",
	} {
		menu := menuBySlug(t, after, slug)
		if after.Locations[location] != menu.ID {
			t.Fatalf("location %s = %d, want menu %#v", location, after.Locations[location], menu)
		}
	}

	notice := serveCookieRequest(handler, http.MethodGet, "https://example.test/admin/tools/?notice=navigation-imported", ownerCookie, nil)
	if notice.Code != http.StatusOK || !strings.Contains(notice.Body.String(), "导航导入已通过快照并发检查并原子替换") {
		t.Fatalf("navigation import notice = %d %s", notice.Code, notice.Body.String())
	}
}

func TestAdminToolsNavigationImportPreserveObjectIdentity(t *testing.T) {
	server, handler, _, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	before, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	payload := marshalNavigationImportDocument(t, validNavigationImportDocument())
	preview := serveNavigationImportPreview(handler, ownerCookie, url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportPreserveObjects},
	}, "same-site.json", payload, "https://example.test")
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "同站恢复：保留对象 ID") ||
		!strings.Contains(preview.Body.String(), "仅当目标内容/分类仓库与导出来源相同时使用") {
		t.Fatalf("preserve preview = %d %s", preview.Code, preview.Body.String())
	}
	apply := serveNavigationImportApply(handler, ownerCookie, url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"apply"},
		"expected_revision": {navigation.SnapshotRevision(before)}, "object_policy": {navigationImportPreserveObjects},
		"document": {base64.RawURLEncoding.EncodeToString(payload)}, "confirm": {"replace"},
	}, "https://example.test")
	if apply.Code != http.StatusSeeOther {
		t.Fatalf("preserve apply = %d %s", apply.Code, apply.Body.String())
	}
	after, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	flat := navigation.Flatten(menuBySlug(t, after, "portable-main"))
	if len(flat) < 2 || flat[0].Item.Type != navigation.TypeCategory || flat[0].Item.ObjectID != 42 ||
		flat[1].Item.Type != navigation.TypePage || flat[1].Item.ObjectID != 77 {
		t.Fatalf("preserved object identity = %#v", flat)
	}
}

func TestAdminToolsNavigationImportSnapshotConflictReplansWithoutOverwrite(t *testing.T) {
	server, handler, _, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	initial, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	payload := marshalNavigationImportDocument(t, validNavigationImportDocument())
	preview := serveNavigationImportPreview(handler, ownerCookie, url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportDetachObjects},
	}, "conflict.json", payload, "https://example.test")
	if preview.Code != http.StatusOK {
		t.Fatalf("conflict preview = %d %s", preview.Code, preview.Body.String())
	}
	concurrent, err := server.navigation.Create(t.Context(), navigation.MenuInput{
		Name: "并发菜单", Slug: "concurrent-menu", Locations: []string{navigation.LocationFooter},
	}, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	latestRevision := navigation.SnapshotRevision(latest)
	response := serveNavigationImportApply(handler, ownerCookie, url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"apply"},
		"expected_revision": {navigation.SnapshotRevision(initial)}, "object_policy": {navigationImportDetachObjects},
		"document": {base64.RawURLEncoding.EncodeToString(payload)}, "confirm": {"replace"},
	}, "https://example.test")
	body := response.Body.String()
	if response.Code != http.StatusConflict || !strings.Contains(body, "预览后导航已被其他请求修改") ||
		!strings.Contains(body, "concurrent-menu") || !strings.Contains(body, `name="expected_revision" value="`+latestRevision+`"`) {
		t.Fatalf("navigation import conflict = %d %s", response.Code, body)
	}
	afterConflict, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterConflict.MenuCount != 1 || afterConflict.Menus[0].ID != concurrent.ID || navigation.SnapshotRevision(afterConflict) != latestRevision {
		t.Fatalf("conflict overwrote navigation = %#v", afterConflict)
	}

	retry := serveNavigationImportApply(handler, ownerCookie, url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"apply"}, "expected_revision": {latestRevision},
		"object_policy": {navigationImportDetachObjects}, "document": {base64.RawURLEncoding.EncodeToString(payload)},
		"confirm": {"replace"},
	}, "https://example.test")
	if retry.Code != http.StatusSeeOther {
		t.Fatalf("navigation import conflict retry = %d %s", retry.Code, retry.Body.String())
	}
	afterRetry, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterRetry.MenuCount != 2 {
		t.Fatalf("navigation import retry snapshot = %#v", afterRetry)
	}
	for _, menu := range afterRetry.Menus {
		if menu.Slug == concurrent.Slug {
			t.Fatalf("retry did not replace concurrent menu: %#v", afterRetry)
		}
	}
}

func TestAdminToolsNavigationImportStrictSecurityBoundary(t *testing.T) {
	server, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, editorCookie, editor := createAndLoginTestUser(t, manager, owner, "import-editor", "import-editor@example.test", "导入编辑", "editor")
	_, authorCookie, author := createAndLoginTestUser(t, manager, owner, "import-author", "import-author@example.test", "导入作者", "author")
	payload := marshalNavigationImportDocument(t, validNavigationImportDocument())
	validFields := url.Values{"csrf_token": {owner.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportDetachObjects}}

	unauthenticated := serveNavigationImportPreview(handler, nil, validFields, "navigation.json", payload, "https://example.test")
	if unauthenticated.Code != http.StatusSeeOther || !strings.HasPrefix(unauthenticated.Header().Get("Location"), "/admin/login/") {
		t.Fatalf("unauthenticated import = %d %#v", unauthenticated.Code, unauthenticated.Header())
	}
	for name, test := range map[string]struct {
		cookie    *http.Cookie
		principal auth.Principal
	}{"editor": {editorCookie, editor}, "author": {authorCookie, author}} {
		response := serveNavigationImportPreview(handler, test.cookie, url.Values{
			"csrf_token": {test.principal.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportDetachObjects},
		}, "navigation.json", payload, "https://example.test")
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s import capability = %d %s", name, response.Code, response.Body.String())
		}
	}
	crossSite := serveNavigationImportPreview(handler, ownerCookie, validFields, "navigation.json", payload, "https://attacker.test")
	if crossSite.Code != http.StatusForbidden || !strings.Contains(crossSite.Body.String(), "cross_site_request_rejected") {
		t.Fatalf("cross-site import = %d %s", crossSite.Code, crossSite.Body.String())
	}
	badCSRFFields := cloneURLValues(validFields)
	badCSRFFields.Set("csrf_token", "invalid")
	if response := serveNavigationImportPreview(handler, ownerCookie, badCSRFFields, "navigation.json", payload, "https://example.test"); response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "csrf_validation_failed") {
		t.Fatalf("bad csrf import = %d %s", response.Code, response.Body.String())
	}
	unknown := cloneURLValues(validFields)
	unknown.Set("unexpected", "true")
	if response := serveNavigationImportPreview(handler, ownerCookie, unknown, "navigation.json", payload, "https://example.test"); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown preview field = %d %s", response.Code, response.Body.String())
	}
	repeated := cloneURLValues(validFields)
	repeated["object_policy"] = []string{navigationImportDetachObjects, navigationImportPreserveObjects}
	if response := serveNavigationImportPreview(handler, ownerCookie, repeated, "navigation.json", payload, "https://example.test"); response.Code != http.StatusBadRequest {
		t.Fatalf("repeated preview field = %d %s", response.Code, response.Body.String())
	}
	invalidQueryRequest := newNavigationImportPreviewRequest(t, "https://example.test/admin/tools/navigation-import/?unexpected=1", ownerCookie, validFields, "navigation.json", payload, "https://example.test")
	invalidQuery := httptest.NewRecorder()
	handler.ServeHTTP(invalidQuery, invalidQueryRequest)
	if invalidQuery.Code != http.StatusBadRequest || !strings.Contains(invalidQuery.Body.String(), "request.invalid_query") {
		t.Fatalf("navigation import invalid query = %d %s", invalidQuery.Code, invalidQuery.Body.String())
	}

	wrongType := httptest.NewRequest(http.MethodPost, "https://example.test/admin/tools/navigation-import/", strings.NewReader(`{"schema":1}`))
	wrongType.AddCookie(ownerCookie)
	wrongType.Header.Set("Origin", "https://example.test")
	wrongType.Header.Set("Content-Type", "application/json")
	wrongTypeResponse := httptest.NewRecorder()
	handler.ServeHTTP(wrongTypeResponse, wrongType)
	if wrongTypeResponse.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong navigation import content type = %d %s", wrongTypeResponse.Code, wrongTypeResponse.Body.String())
	}

	fileTooLarge := serveNavigationImportPreview(handler, ownerCookie, validFields, "large.json", bytes.Repeat([]byte{'x'}, navigationImportFileMaxBytes+1), "https://example.test")
	if fileTooLarge.Code != http.StatusUnprocessableEntity || !strings.Contains(fileTooLarge.Body.String(), "不超过 256 KiB") {
		t.Fatalf("oversized navigation file = %d %s", fileTooLarge.Code, fileTooLarge.Body.String())
	}
	requestTooLarge := serveNavigationImportPreview(handler, ownerCookie, validFields, "huge.json", bytes.Repeat([]byte{'x'}, navigationImportFormMaxBytes), "https://example.test")
	if requestTooLarge.Code != http.StatusRequestEntityTooLarge || !strings.Contains(requestTooLarge.Body.String(), "超过 512 KiB") {
		t.Fatalf("oversized navigation request = %d %s", requestTooLarge.Code, requestTooLarge.Body.String())
	}

	current, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	validApply := url.Values{
		"csrf_token": {owner.CSRFToken}, "action": {"apply"}, "expected_revision": {navigation.SnapshotRevision(current)},
		"object_policy": {navigationImportDetachObjects}, "document": {base64.RawURLEncoding.EncodeToString(payload)}, "confirm": {"replace"},
	}
	for name, mutate := range map[string]func(url.Values){
		"unknown":      func(values url.Values) { values.Set("unexpected", "true") },
		"repeated":     func(values url.Values) { values["confirm"] = []string{"replace", "replace"} },
		"bad-csrf":     func(values url.Values) { values.Set("csrf_token", "invalid") },
		"bad-revision": func(values url.Values) { values.Set("expected_revision", "ABC") },
		"bad-document": func(values url.Values) { values.Set("document", "%%not-base64%%") },
	} {
		values := cloneURLValues(validApply)
		mutate(values)
		response := serveNavigationImportApply(handler, ownerCookie, values, "https://example.test")
		want := http.StatusBadRequest
		if name == "bad-csrf" {
			want = http.StatusForbidden
		}
		if response.Code != want {
			t.Fatalf("%s apply boundary = %d %s", name, response.Code, response.Body.String())
		}
	}
	oversizedApply := cloneURLValues(validApply)
	oversizedApply.Set("document", strings.Repeat("A", navigationImportFormMaxBytes))
	if response := serveNavigationImportApply(handler, ownerCookie, oversizedApply, "https://example.test"); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized apply request = %d %s", response.Code, response.Body.String())
	}

	unconfigured, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	server.auth = unconfigured
	loopback := newNavigationImportPreviewRequest(t, "http://localhost/admin/tools/navigation-import/", nil, validFields, "navigation.json", payload, "")
	loopback.RemoteAddr = "127.0.0.1:41234"
	loopbackResponse := httptest.NewRecorder()
	handler.ServeHTTP(loopbackResponse, loopback)
	if loopbackResponse.Code != http.StatusForbidden || !strings.Contains(loopbackResponse.Body.String(), "admin_credentials_not_configured") {
		t.Fatalf("unconfigured loopback import = %d %s", loopbackResponse.Code, loopbackResponse.Body.String())
	}
	external := newNavigationImportPreviewRequest(t, "http://example.test/admin/tools/navigation-import/", nil, validFields, "navigation.json", payload, "")
	external.RemoteAddr = "198.51.100.24:41234"
	externalResponse := httptest.NewRecorder()
	handler.ServeHTTP(externalResponse, external)
	if externalResponse.Code != http.StatusNotFound {
		t.Fatalf("unconfigured external import = %d %s", externalResponse.Code, externalResponse.Body.String())
	}
}

func TestAdminToolsNavigationImportRejectsMalformedDocumentsWithoutMutation(t *testing.T) {
	server, handler, _, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	before, err := server.navigation.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	basePayload := marshalNavigationImportDocument(t, validNavigationImportDocument())
	tests := map[string]func() []byte{
		"duplicate field": func() []byte {
			return []byte(strings.Replace(string(basePayload), `"schema":1`, `"schema":1,"schema":1`, 1))
		},
		"unknown field": func() []byte {
			return []byte(strings.Replace(string(basePayload), `"kind":`, `"unexpected":true,"kind":`, 1))
		},
		"trailing document": func() []byte { return append(append([]byte(nil), basePayload...), []byte(` {}`)...) },
		"null location slots": func() []byte {
			document := validNavigationImportDocument()
			document.LocationSlots = nil
			return marshalNavigationImportDocument(t, document)
		},
		"null menus": func() []byte {
			document := validNavigationImportDocument()
			document.Menus = nil
			return marshalNavigationImportDocument(t, document)
		},
		"null menu locations": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Locations = nil
			return marshalNavigationImportDocument(t, document)
		},
		"null menu items": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items = nil
			return marshalNavigationImportDocument(t, document)
		},
		"wrong schema": func() []byte {
			document := validNavigationImportDocument()
			document.Schema = 2
			return marshalNavigationImportDocument(t, document)
		},
		"wrong kind": func() []byte {
			document := validNavigationImportDocument()
			document.Kind = "newpress.navigation"
			return marshalNavigationImportDocument(t, document)
		},
		"missing parent": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items[1].ParentKey = "missing-parent"
			return marshalNavigationImportDocument(t, document)
		},
		"cycle": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items[0].ParentKey = "item-003"
			return marshalNavigationImportDocument(t, document)
		},
		"fourth level": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items = append(document.Menus[0].Items, navigationExportItem{
				Key: "item-005", ParentKey: "item-003", Position: 0, Type: navigation.TypeCustom,
				Label: "第四层", URL: "/fourth/", Target: navigation.TargetSelf, Presentation: navigation.PresentationStandard,
			})
			return marshalNavigationImportDocument(t, document)
		},
		"duplicate sibling position": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items[3].Position = 0
			return marshalNavigationImportDocument(t, document)
		},
		"duplicate location slot": func() []byte {
			document := validNavigationImportDocument()
			document.LocationSlots[3].Key = navigation.LocationPrimary
			return marshalNavigationImportDocument(t, document)
		},
		"duplicate menu location": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Locations = []string{navigation.LocationPrimary, navigation.LocationPrimary, navigation.LocationMobile}
			return marshalNavigationImportDocument(t, document)
		},
		"location contradiction": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Locations = []string{navigation.LocationPrimary}
			return marshalNavigationImportDocument(t, document)
		},
		"unsafe url": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items[0].URL = "javascript:alert(1)"
			return marshalNavigationImportDocument(t, document)
		},
		"unknown item type": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items[0].Type = "executable"
			return marshalNavigationImportDocument(t, document)
		},
		"missing object identity": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items[0].ObjectID = 0
			return marshalNavigationImportDocument(t, document)
		},
		"custom object identity": func() []byte {
			document := validNavigationImportDocument()
			document.Menus[0].Items[3].ObjectID = 999
			return marshalNavigationImportDocument(t, document)
		},
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			response := serveNavigationImportPreview(handler, ownerCookie, url.Values{
				"csrf_token": {owner.CSRFToken}, "action": {"preview"}, "object_policy": {navigationImportDetachObjects},
			}, name+".json", payload(), "https://example.test")
			if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), "导入预览") {
				t.Fatalf("malformed navigation document = %d %s", response.Code, response.Body.String())
			}
			after, snapshotErr := server.navigation.Snapshot(t.Context())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			if navigation.SnapshotRevision(after) != navigation.SnapshotRevision(before) || after.MenuCount != before.MenuCount || after.ItemCount != before.ItemCount {
				t.Fatalf("invalid preview mutated navigation: before=%#v after=%#v", before, after)
			}
		})
	}
}

func validNavigationImportDocument() navigationExportDocument {
	return navigationExportDocument{
		Schema: 1, Kind: "newpress.navigation_export",
		ExportedAt: time.Date(2026, time.August, 11, 8, 30, 0, 0, time.UTC),
		Source:     navigationExportSource{ThemeSlug: "chunnel", ThemeVersion: "0.14.0"},
		Revision:   "0123456789abcdef",
		LocationSlots: []navigationExportLocationSlot{
			{Key: navigation.LocationPrimary, Label: navigation.LocationLabel(navigation.LocationPrimary), MenuSlug: "portable-main"},
			{Key: navigation.LocationUtility, Label: navigation.LocationLabel(navigation.LocationUtility), MenuSlug: "portable-quick"},
			{Key: navigation.LocationMobile, Label: navigation.LocationLabel(navigation.LocationMobile), MenuSlug: "portable-main"},
			{Key: navigation.LocationFooter, Label: navigation.LocationLabel(navigation.LocationFooter), MenuSlug: "portable-quick"},
			{Key: navigation.LocationMobileAction, Label: navigation.LocationLabel(navigation.LocationMobileAction)},
			{Key: navigation.LocationExplore, Label: navigation.LocationLabel(navigation.LocationExplore)},
		},
		Menus: []navigationExportMenu{
			{
				Name: "迁移主导航", Slug: "portable-main", Description: "完整层级与对象策略测试",
				Locations: []string{navigation.LocationPrimary, navigation.LocationMobile},
				Items: []navigationExportItem{
					{Key: "item-001", Position: 0, Type: navigation.TypeCategory, ObjectID: 42, Label: "工程分类", URL: "/categories/engineering/", Target: navigation.TargetSelf, Icon: "grid", Accent: "#16A89A", Presentation: navigation.PresentationDropdown},
					{Key: "item-002", ParentKey: "item-001", Position: 0, Type: navigation.TypePage, ObjectID: 77, Label: "关于页面", URL: "/pages/about/", Target: navigation.TargetSelf, Presentation: navigation.PresentationStandard},
					{Key: "item-003", ParentKey: "item-002", Position: 0, Type: navigation.TypeCustom, Label: "第三层", URL: "/explore/", Target: navigation.TargetSelf, Presentation: navigation.PresentationStandard},
					{Key: "item-004", Position: 1, Type: navigation.TypeCustom, Label: "外部文档", URL: "https://docs.example.test/newpress", Target: navigation.TargetBlank, Rel: "noopener noreferrer", Presentation: navigation.PresentationStandard},
				},
			},
			{
				Name: "迁移快捷导航", Slug: "portable-quick", Locations: []string{navigation.LocationUtility, navigation.LocationFooter},
				Items: []navigationExportItem{{Key: "item-001", Position: 0, Type: navigation.TypeCustom, Label: "控制台", URL: "/admin/", Target: navigation.TargetSelf, Presentation: navigation.PresentationStandard}},
			},
		},
	}
}

func marshalNavigationImportDocument(t *testing.T, document navigationExportDocument) []byte {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func serveNavigationImportPreview(handler http.Handler, cookie *http.Cookie, fields url.Values, filename string, payload []byte, origin string) *httptest.ResponseRecorder {
	request := newNavigationImportPreviewRequest(nil, "https://example.test/admin/tools/navigation-import/", cookie, fields, filename, payload, origin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func newNavigationImportPreviewRequest(t *testing.T, target string, cookie *http.Cookie, fields url.Values, filename string, payload []byte, origin string) *http.Request {
	if t != nil {
		t.Helper()
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, values := range fields {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				panic(err)
			}
		}
	}
	part, err := writer.CreateFormFile("navigation_file", filename)
	if err != nil {
		panic(err)
	}
	if _, err := part.Write(payload); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	return request
}

func serveNavigationImportApply(handler http.Handler, cookie *http.Cookie, values url.Values, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "https://example.test/admin/tools/navigation-import/", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func menuBySlug(t *testing.T, snapshot navigation.Snapshot, slug string) navigation.Menu {
	t.Helper()
	for _, menu := range snapshot.Menus {
		if menu.Slug == slug {
			return menu
		}
	}
	t.Fatalf("navigation menu %q missing from %#v", slug, snapshot)
	return navigation.Menu{}
}
