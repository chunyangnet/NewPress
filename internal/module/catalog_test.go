package module

import "testing"

func TestCatalogIntegrityAndOwnership(t *testing.T) {
	if err := CheckCatalog(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"GET /api/v1/posts/{slug}/comments/receipt": "comments", "POST /api/v1/admin/contents": "content",
		"GET /api/v1/community/circles/{circle}": "community", "GET /admin/appearance/layout/{$}": "themes",
		"/admin/features/links/": "links", "/series-unrelated": "platform", "/api/v1/social/wallets/{currency}": "currency",
	} {
		if got := Owner(path); got != want {
			t.Errorf("%s: got %s want %s", path, got, want)
		}
	}
	copy := Catalog()
	copy[0].Paths[0] = "/changed"
	if Owner("/health/live") != "platform" || Catalog()[0].Paths[0] == "/changed" {
		t.Fatal("catalog aliases mutable data")
	}
}
func TestModuleConfigurationBoundary(t *testing.T) {
	if !Allowed(nil, "comments") || Allowed(map[string]bool{"comments": false}, "comments") || Allowed(nil, "plugin-jobs") {
		t.Fatal("module states")
	}
	for _, id := range []string{"unknown", "platform", "currency", "plugin-jobs"} {
		if len(Validate(map[string]bool{id: true})) == 0 {
			t.Fatalf("accepted non-switch %s", id)
		}
	}
	if len(Validate(map[string]bool{"comments": false, "series": true})) != 0 {
		t.Fatal("valid switches rejected")
	}
}
