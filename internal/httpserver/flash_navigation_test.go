package httpserver

import (
	"newpress.local/newpress/internal/auth"
	"testing"
)

func TestFlashAdminNavigation(t *testing.T) {
	view := adminView{Current: "flash", Principal: auth.Principal{Capabilities: []string{auth.CapabilityAll}}}
	groups := view.Navigation()
	found := false
	for _, group := range groups {
		for i, item := range group.Items {
			if item.ID != "flash" {
				continue
			}
			found = true
			if group.ID != "content" || !group.Active || !item.Active || item.URL != "/admin/flash/" || i == 0 || group.Items[i-1].ID != "content" {
				t.Fatalf("invalid flash navigation: %#v", group)
			}
		}
	}
	if !found {
		t.Fatal("missing flash entry")
	}
	view.Principal.Capabilities = []string{auth.CapabilityContentRead}
	for _, group := range view.Navigation() {
		for _, item := range group.Items {
			if item.ID == "flash" {
				t.Fatal("flash requires content:write")
			}
		}
	}
}
