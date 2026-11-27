package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/navigation"
)

func TestNavigationJSONAPILifecycleAndPublicProjection(t *testing.T) {
	_, handler, store, cookie, principal := newNavigationHTTPTestServer(t)
	collectionURL := "https://example.test/api/v1/admin/navigation/menus"

	unauthenticated := serveContentAPI(handler, nil, http.MethodGet, collectionURL, "", nil)
	assertAPIError(t, unauthenticated, http.StatusUnauthorized, "auth.authentication_required")

	empty := serveContentAPI(handler, cookie, http.MethodGet, collectionURL, "", nil)
	if empty.Code != http.StatusOK || empty.Header().Get("ETag") == "" || empty.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("empty navigation collection = %d %#v %s", empty.Code, empty.Header(), empty.Body.String())
	}
	var emptyBody adminNavigationMenusAPIResponse
	decodeAPIResponse(t, empty, &emptyBody)
	if emptyBody.MenuCount != 0 || emptyBody.ItemCount != 0 || len(emptyBody.Menus) != 0 || len(emptyBody.LocationSlots) != len(navigation.Locations()) || emptyBody.Limits.Depth != navigation.MaxDepth {
		t.Fatalf("empty navigation body = %#v", emptyBody)
	}

	createPayload := `{"name":"站点主导航","slug":"primary","description":"供网页与客户端共享","locations":["primary","mobile"]}`
	createHeaders := authenticatedAPIHeaders(principal)
	createHeaders["Idempotency-Key"] = "navigation-create-2026"
	createdResponse := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, createPayload, createHeaders)
	if createdResponse.Code != http.StatusCreated || createdResponse.Header().Get("Location") == "" || createdResponse.Header().Get("ETag") == "" {
		t.Fatalf("create navigation API = %d %#v %s", createdResponse.Code, createdResponse.Header(), createdResponse.Body.String())
	}
	var created adminNavigationMenuAPIResponse
	decodeAPIResponse(t, createdResponse, &created)
	if created.ID <= 0 || created.Version != 1 || created.ItemCount != 0 || len(created.Items) != 0 || created.ETag != createdResponse.Header().Get("ETag") {
		t.Fatalf("created navigation = %#v", created)
	}

	replayed := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, createPayload, createHeaders)
	if replayed.Code != http.StatusCreated || replayed.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("navigation idempotent replay = %d %#v %s", replayed.Code, replayed.Header(), replayed.Body.String())
	}
	var replayedBody adminNavigationMenuAPIResponse
	decodeAPIResponse(t, replayed, &replayedBody)
	if replayedBody.ID != created.ID {
		t.Fatalf("replayed navigation ID = %d, want %d", replayedBody.ID, created.ID)
	}
	idempotencyConflict := serveContentAPI(handler, cookie, http.MethodPost, collectionURL,
		`{"name":"站点主导航","slug":"primary","description":"不同请求体","locations":["primary","mobile"]}`, createHeaders)
	assertAPIError(t, idempotencyConflict, http.StatusConflict, "request.idempotency_conflict")

	list := serveContentAPI(handler, cookie, http.MethodGet, collectionURL, "", nil)
	if list.Code != http.StatusOK || list.Header().Get("ETag") == empty.Header().Get("ETag") {
		t.Fatalf("navigation list = %d %#v %s", list.Code, list.Header(), list.Body.String())
	}
	var listBody adminNavigationMenusAPIResponse
	decodeAPIResponse(t, list, &listBody)
	if listBody.MenuCount != 1 || listBody.ItemCount != 0 || listBody.Locations[navigation.LocationPrimary] != created.ID || listBody.ETag != list.Header().Get("ETag") {
		t.Fatalf("navigation list body = %#v", listBody)
	}
	conditionalList := serveContentAPI(handler, cookie, http.MethodGet, collectionURL, "", map[string]string{"If-None-Match": list.Header().Get("ETag")})
	if conditionalList.Code != http.StatusNotModified || conditionalList.Body.Len() != 0 {
		t.Fatalf("conditional navigation list = %d %s", conditionalList.Code, conditionalList.Body.String())
	}

	itemURL := collectionURL + "/" + strconv.FormatInt(created.ID, 10)
	item := serveContentAPI(handler, cookie, http.MethodGet, itemURL, "", nil)
	if item.Code != http.StatusOK || item.Header().Get("ETag") != created.ETag {
		t.Fatalf("navigation item = %d %#v %s", item.Code, item.Header(), item.Body.String())
	}

	items := []navigation.ItemInput{
		{ID: -1, Type: navigation.TypeCustom, Label: "首页", URL: "/", Icon: "house", Accent: "#16A89A", Presentation: navigation.PresentationStandard},
		{ID: -2, Type: navigation.TypeCustom, Label: "<script>alert(1)</script>", URL: "/explore/", Icon: "compass", Presentation: navigation.PresentationDropdown},
		{ID: -3, ParentID: -2, Type: navigation.TypeCategory, ObjectID: 7, Label: "工程", URL: "/categories/engineering/", Icon: "tags", Presentation: navigation.PresentationStandard},
		{ID: -4, ParentID: -3, Type: navigation.TypeCustom, Label: "外部指南", URL: "https://docs.example.test/guide", Target: navigation.TargetBlank, Rel: "ugc", Icon: "book-open", Presentation: navigation.PresentationStandard},
	}
	encodedItems, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	itemHeaders := authenticatedAPIHeaders(principal)
	itemHeaders["If-Match"] = created.ETag
	itemsResponse := serveContentAPI(handler, cookie, http.MethodPut, itemURL+"/items", string(encodedItems), itemHeaders)
	if itemsResponse.Code != http.StatusOK || itemsResponse.Header().Get("ETag") == created.ETag {
		t.Fatalf("replace navigation items = %d %#v %s", itemsResponse.Code, itemsResponse.Header(), itemsResponse.Body.String())
	}
	var withItems adminNavigationMenuAPIResponse
	decodeAPIResponse(t, itemsResponse, &withItems)
	if withItems.Version != 2 || withItems.ItemCount != 4 || withItems.Items[3].Rel != "ugc noopener noreferrer" || withItems.Items[3].Target != navigation.TargetBlank {
		t.Fatalf("navigation items response = %#v", withItems)
	}
	strictItemHeaders := authenticatedAPIHeaders(principal)
	strictItemHeaders["If-Match"] = withItems.ETag
	duplicateNested := serveContentAPI(handler, cookie, http.MethodPut, itemURL+"/items",
		`{"items":[{"id":-1,"parent_id":0,"type":"custom","object_id":0,"label":"one","label":"two","url":"/","description":"","target":"self","rel":"","icon":"","accent":"","presentation":"standard"}]}`, strictItemHeaders)
	assertAPIError(t, duplicateNested, http.StatusBadRequest, "request.invalid_json")
	nullItems := serveContentAPI(handler, cookie, http.MethodPut, itemURL+"/items", `{"items":null}`, strictItemHeaders)
	assertAPIError(t, nullItems, http.StatusBadRequest, "request.invalid_json")

	publicURL := "https://example.test/api/v1/navigation/primary"
	publicResponse := serveContentAPI(handler, nil, http.MethodGet, publicURL, "", nil)
	if publicResponse.Code != http.StatusOK || publicResponse.Header().Get("ETag") == "" || publicResponse.Header().Get("Last-Modified") == "" || !strings.Contains(publicResponse.Header().Get("Cache-Control"), "must-revalidate") {
		t.Fatalf("public navigation = %d %#v %s", publicResponse.Code, publicResponse.Header(), publicResponse.Body.String())
	}
	var publicBody publicNavigationMenuAPIResponse
	decodeAPIResponse(t, publicResponse, &publicBody)
	if publicBody.Location != navigation.LocationPrimary || publicBody.ID != created.ID || len(publicBody.Items) != 2 || len(publicBody.Items[1].Children) != 1 ||
		len(publicBody.Items[1].Children[0].Children) != 1 || publicBody.Items[1].Children[0].Children[0].Rel != "ugc noopener noreferrer" {
		t.Fatalf("public navigation body = %#v", publicBody)
	}
	conditionalPublic := serveContentAPI(handler, nil, http.MethodGet, publicURL, "", map[string]string{"If-None-Match": publicResponse.Header().Get("ETag")})
	if conditionalPublic.Code != http.StatusNotModified || conditionalPublic.Body.Len() != 0 {
		t.Fatalf("conditional public navigation = %d %s", conditionalPublic.Code, conditionalPublic.Body.String())
	}

	sourcesURL := "https://example.test/api/v1/admin/navigation/sources"
	sources := serveContentAPI(handler, cookie, http.MethodGet, sourcesURL, "", nil)
	if sources.Code != http.StatusOK || sources.Header().Get("ETag") == "" {
		t.Fatalf("navigation sources = %d %#v %s", sources.Code, sources.Header(), sources.Body.String())
	}
	var sourcesBody adminNavigationSourcesAPIResponse
	decodeAPIResponse(t, sources, &sourcesBody)
	if sourcesBody.Count < 6 || len(sourcesBody.Groups) < 4 || sourcesBody.Groups[0].ID != "builtins" {
		t.Fatalf("navigation sources body = %#v", sourcesBody)
	}
	conditionalSources := serveContentAPI(handler, cookie, http.MethodGet, sourcesURL, "", map[string]string{"If-None-Match": sources.Header().Get("ETag")})
	if conditionalSources.Code != http.StatusNotModified {
		t.Fatalf("conditional sources = %d %s", conditionalSources.Code, conditionalSources.Body.String())
	}

	conflictPayload := `{"name":"冲突菜单","slug":"conflict","description":"","locations":["primary"]}`
	conflict := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, conflictPayload, authenticatedAPIHeaders(principal))
	assertAPIError(t, conflict, http.StatusConflict, "navigation.location_conflict")

	unsafeHeaders := authenticatedAPIHeaders(principal)
	unsafeHeaders["If-Match"] = withItems.ETag
	unsafePayload := `{"items":[{"id":-1,"parent_id":0,"type":"custom","object_id":0,"label":"bad","url":"javascript:alert(1)","description":"","target":"self","rel":"","icon":"","accent":"#4E7CF6","presentation":"standard"}]}`
	unsafe := serveContentAPI(handler, cookie, http.MethodPut, itemURL+"/items", unsafePayload, unsafeHeaders)
	assertAPIError(t, unsafe, http.StatusUnprocessableEntity, "navigation.validation_failed")
	unchanged, err := store.Get(t.Context(), created.ID)
	if err != nil || unchanged.Version != withItems.Version || len(unchanged.Items) != 4 {
		t.Fatalf("unsafe API write changed menu = %#v, %v", unchanged, err)
	}

	staleHeaders := authenticatedAPIHeaders(principal)
	staleHeaders["If-Match"] = created.ETag
	stale := serveContentAPI(handler, cookie, http.MethodPatch, itemURL, `{"description":"过期写入"}`, staleHeaders)
	assertAPIError(t, stale, http.StatusPreconditionFailed, "navigation.precondition_failed")
	if stale.Header().Get("ETag") != withItems.ETag {
		t.Fatalf("stale navigation current etag = %q, want %q", stale.Header().Get("ETag"), withItems.ETag)
	}

	patchHeaders := authenticatedAPIHeaders(principal)
	patchHeaders["If-Match"] = withItems.ETag
	patchedResponse := serveContentAPI(handler, cookie, http.MethodPatch, itemURL, `{"name":"主导航 2026","description":"统一网页与客户端入口"}`, patchHeaders)
	if patchedResponse.Code != http.StatusOK {
		t.Fatalf("patch navigation = %d %s", patchedResponse.Code, patchedResponse.Body.String())
	}
	var patched adminNavigationMenuAPIResponse
	decodeAPIResponse(t, patchedResponse, &patched)
	if patched.Version != 3 || patched.Name != "主导航 2026" || patched.ETag == withItems.ETag {
		t.Fatalf("patched navigation = %#v", patched)
	}

	missingPrecondition := serveContentAPI(handler, cookie, http.MethodDelete, itemURL, "", authenticatedAPIHeaders(principal))
	assertAPIError(t, missingPrecondition, http.StatusPreconditionRequired, "navigation.precondition_required")
	deleteHeaders := authenticatedAPIHeaders(principal)
	deleteHeaders["If-Match"] = patched.ETag
	deleted := serveContentAPI(handler, cookie, http.MethodDelete, itemURL, "", deleteHeaders)
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete navigation = %d %s", deleted.Code, deleted.Body.String())
	}
	unassigned := serveContentAPI(handler, nil, http.MethodGet, publicURL, "", nil)
	assertAPIError(t, unassigned, http.StatusNotFound, "navigation.location_unassigned")
}

func TestNavigationJSONAPIStrictSecurityAndCapabilityBoundaries(t *testing.T) {
	server, handler, _, cookie, principal := newNavigationHTTPTestServer(t)
	collectionURL := "https://example.test/api/v1/admin/navigation/menus"
	payload := `{"name":"边界菜单","slug":"boundary","description":"","locations":[]}`

	crossSiteHeaders := authenticatedAPIHeaders(principal)
	crossSiteHeaders["Origin"] = "https://attacker.test"
	crossSiteHeaders["Sec-Fetch-Site"] = "cross-site"
	crossSite := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, payload, crossSiteHeaders)
	assertAPIError(t, crossSite, http.StatusForbidden, "auth.cross_site_request_rejected")

	missingCSRFHeaders := authenticatedAPIHeaders(principal)
	delete(missingCSRFHeaders, "X-CSRF-Token")
	missingCSRF := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, payload, missingCSRFHeaders)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "auth.csrf_validation_failed")

	wrongTypeHeaders := authenticatedAPIHeaders(principal)
	wrongTypeHeaders["Content-Type"] = "text/plain"
	wrongType := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, payload, wrongTypeHeaders)
	assertAPIError(t, wrongType, http.StatusUnsupportedMediaType, "request.json_content_type_required")

	unknownField := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, `{"name":"A","slug":"a","description":"","locations":[],"unexpected":true}`, authenticatedAPIHeaders(principal))
	assertAPIError(t, unknownField, http.StatusBadRequest, "request.invalid_json")
	duplicateField := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, `{"name":"A","name":"B","slug":"a","description":"","locations":[]}`, authenticatedAPIHeaders(principal))
	assertAPIError(t, duplicateField, http.StatusBadRequest, "request.invalid_json")

	oversized := `{"name":"A","slug":"a","description":"` + strings.Repeat("x", adminNavigationAPIRequestMaxBytes) + `","locations":[]}`
	oversizedResponse := serveContentAPI(handler, cookie, http.MethodPost, collectionURL, oversized, authenticatedAPIHeaders(principal))
	assertAPIError(t, oversizedResponse, http.StatusRequestEntityTooLarge, "request.body_too_large")

	badQuery := serveContentAPI(handler, cookie, http.MethodGet, collectionURL+"?unknown=1", "", nil)
	assertAPIError(t, badQuery, http.StatusBadRequest, "request.invalid_query")
	method := serveContentAPI(handler, cookie, http.MethodPut, collectionURL, "", nil)
	assertAPIError(t, method, http.StatusMethodNotAllowed, "request.method_not_allowed")
	if method.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("navigation method Allow = %q", method.Header().Get("Allow"))
	}
	publicMethod := serveContentAPI(handler, nil, http.MethodPost, "https://example.test/api/v1/navigation/primary", "", nil)
	assertAPIError(t, publicMethod, http.StatusMethodNotAllowed, "request.method_not_allowed")
	if publicMethod.Header().Get("Allow") != "GET" {
		t.Fatalf("public navigation method Allow = %q", publicMethod.Header().Get("Allow"))
	}

	_, persistentHandler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, editorCookie, _ := createAndLoginTestUser(t, manager, owner, "navigation-editor", "navigation-editor@example.test", "导航编辑", "editor")
	for label, candidate := range map[string]*http.Cookie{"editor": editorCookie, "administrator": ownerCookie} {
		response := serveContentAPI(persistentHandler, candidate, http.MethodGet, collectionURL, "", nil)
		if label == "editor" && response.Code != http.StatusForbidden {
			t.Fatalf("editor navigation API = %d %s", response.Code, response.Body.String())
		}
		if label == "administrator" && response.Code != http.StatusOK {
			t.Fatalf("administrator navigation API = %d %s", response.Code, response.Body.String())
		}
	}

	unconfigured, err := auth.New(auth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	server.auth = unconfigured
	loopbackReadRequest := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/admin/navigation/menus", nil)
	loopbackReadRequest.RemoteAddr = "127.0.0.1:41234"
	loopbackRead := httptest.NewRecorder()
	handler.ServeHTTP(loopbackRead, loopbackReadRequest)
	if loopbackRead.Code != http.StatusOK {
		t.Fatalf("loopback navigation API read = %d %s", loopbackRead.Code, loopbackRead.Body.String())
	}
	loopbackWriteRequest := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/admin/navigation/menus", strings.NewReader(payload))
	loopbackWriteRequest.RemoteAddr = "[::1]:41234"
	loopbackWriteRequest.Header.Set("Content-Type", "application/json")
	loopbackWrite := httptest.NewRecorder()
	handler.ServeHTTP(loopbackWrite, loopbackWriteRequest)
	assertAPIError(t, loopbackWrite, http.StatusForbidden, "auth.write_disabled")

	externalReadRequest := httptest.NewRequest(http.MethodGet, "http://example.test/api/v1/admin/navigation/menus", nil)
	externalReadRequest.RemoteAddr = "198.51.100.25:41234"
	externalRead := httptest.NewRecorder()
	handler.ServeHTTP(externalRead, externalReadRequest)
	assertAPIError(t, externalRead, http.StatusNotFound, "request.not_found")

	invalidLocation := serveContentAPI(handler, nil, http.MethodGet, "http://localhost/api/v1/navigation/unknown", "", nil)
	assertAPIError(t, invalidLocation, http.StatusNotFound, "navigation.location_not_found")
}
