package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"

	"newpress.local/newpress/internal/navigation"
)

func TestNavigationCustomJSONRejectsAliasesBeforeMutation(t *testing.T) {
	for _, body := range []string{
		`[{"label":"first","LABEL":"second","url":"/"}]`,
		`[{"label":"safe","url":"/","badge":{"text":"first","TEXT":"second"}}]`,
	} {
		t.Run(body, func(t *testing.T) {
			value := navigationItemInputsAPIValue{Value: []navigation.ItemInput{{Label: "unchanged"}}}
			if err := json.Unmarshal([]byte(body), &value); err == nil {
				t.Error("custom array decoder accepted field aliases")
			}
			if value.Present || !reflect.DeepEqual(value.Value, []navigation.ItemInput{{Label: "unchanged"}}) {
				t.Error("rejected array changed the destination")
			}
		})
	}
}

func TestNavigationJSONAndFormRejectInvalidItemsWithoutWrites(t *testing.T) {
	_, handler, store, cookie, principal := newNavigationHTTPTestServer(t)
	menu, err := store.Create(t.Context(), navigation.MenuInput{Name: "菜单", Slug: "integrity", Locations: []string{navigation.LocationPrimary}}, principal.Username)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, items string }{
		{"case alias", `[{"id":-1,"label":"first","LABEL":"second","url":"/"}]`},
		{"nested badge alias", `[{"id":-1,"label":"safe","url":"/","badge":{"text":"one","TEXT":"two"}}]`},
		{"invalid UTF8", "[{\"id\":-1,\"label\":\"private-\xff\",\"url\":\"/\"}]"},
		{"high surrogate", `[{"id":-1,"label":"private-\ud800","url":"/"}]`},
	} {
		for _, route := range []string{"API", "form"} {
			t.Run(test.name+"/"+route, func(t *testing.T) {
				before, err := store.Get(t.Context(), menu.ID)
				if err != nil {
					t.Fatal(err)
				}
				if route == "API" {
					headers := authenticatedAPIHeaders(principal)
					headers["If-Match"] = navigationMenuAPIETag(before)
					response := serveContentAPI(handler, cookie, http.MethodPut, fmt.Sprintf("https://example.test/api/v1/admin/navigation/menus/%d/items", menu.ID), `{"items":`+test.items+`}`, headers)
					if response.Code != http.StatusBadRequest {
						t.Errorf("invalid navigation API input accepted: %d %s", response.Code, response.Body.String())
					}
				} else {
					response := serveNavigationForm(handler, cookie, url.Values{
						"csrf_token": {principal.CSRFToken}, "action": {"save_items"}, "id": {strconv.FormatInt(menu.ID, 10)},
						"version": {strconv.FormatInt(before.Version, 10)}, "items_json": {test.items},
					}, "https://example.test", "application/x-www-form-urlencoded")
					if response.Code != http.StatusUnprocessableEntity {
						t.Errorf("invalid navigation form input accepted: %d", response.Code)
					}
				}
				after, err := store.Get(t.Context(), menu.ID)
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Errorf("invalid items changed navigation state: %v", err)
				}
			})
		}
	}
}
