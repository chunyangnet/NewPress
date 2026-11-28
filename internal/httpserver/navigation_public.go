package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/module"
	"newpress.local/newpress/internal/navigation"
)

type navigationView struct {
	Explore                []navigationItemView
	ExploreConfigured      bool
	ExploreTitle           string
	ExploreDescription     string
	HasBadges              bool
	Primary                []navigationItemView
	Utility                []navigationItemView
	Mobile                 []navigationItemView
	MobileDock             []navigationItemView
	MobileAction           []navigationItemView
	Footer                 []navigationItemView
	PrimaryConfigured      bool
	UtilityConfigured      bool
	MobileConfigured       bool
	MobileActionConfigured bool
	FooterConfigured       bool
	CSSURL                 string
}

type navigationItemView struct {
	ID           int64
	Label        string
	URL          string
	Description  string
	Rel          string
	Icon         string
	Presentation string
	Badge        navigation.Badge
	NewWindow    bool
	Active       bool
	Current      bool
	Children     []navigationItemView
}

const navigationReadTimeout = 350 * time.Millisecond

// A discovery page has already loaded these categories for its filter. Reuse
// that request's result (including an empty slice) for navigation alias matching.
type navigationCategoriesContextKey struct{}

func (s *Server) publicNavigation(path string) navigationView {
	return s.publicNavigationContext(context.Background(), path)
}

func (s *Server) publicNavigationContext(parent context.Context, path string) navigationView {
	if s == nil || s.navigation == nil || parent.Err() != nil {
		return navigationView{}
	}
	ctx, cancel := context.WithTimeout(parent, navigationReadTimeout)
	defer cancel()
	snapshot, err := s.navigation.Snapshot(ctx)
	if err != nil || ctx.Err() != nil {
		return navigationView{}
	}
	menus := make(map[int64]navigation.Menu, len(snapshot.Menus))
	for _, menu := range snapshot.Menus {
		menus[menu.ID] = s.publicNavigationMenu(menu)
	}
	menuFor := func(location string) (navigation.Menu, bool) {
		id := snapshot.Locations[location]
		menu, ok := menus[id]
		return menu, ok && id > 0
	}
	// Resolve saved category names and custom slugs to the same identity, only
	// on filtered discovery requests. Stored URLs remain untouched.
	aliases := map[string]string{}
	if navigationNeedsCategoryAliases(path, snapshot.Locations, menus) {
		terms, loaded := ctx.Value(navigationCategoriesContextKey{}).([]content.Term)
		if !loaded && s.repository != nil && ctx.Err() == nil {
			result, err := s.repository.ListTerms(ctx, content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500})
			if err == nil {
				terms = result.Items
			}
		}
		if ctx.Err() != nil {
			return navigationView{}
		}
		for _, term := range terms {
			aliases[strings.ToLower(term.Name)] = term.Slug
			aliases[term.Slug] = term.Slug
		}
	}
	primaryMenu, primaryConfigured := menuFor(navigation.LocationPrimary)
	utilityMenu, utilityConfigured := menuFor(navigation.LocationUtility)
	mobileMenu, mobileConfigured := menuFor(navigation.LocationMobile)
	mobileActionMenu, mobileActionConfigured := menuFor(navigation.LocationMobileAction)
	footerMenu, footerConfigured := menuFor(navigation.LocationFooter)
	exploreMenu, exploreConfigured := menuFor(navigation.LocationExplore)
	view := navigationView{
		Explore: buildNavigationTree(exploreMenu, path, aliases), ExploreConfigured: exploreConfigured,
		ExploreTitle: exploreMenu.Name, ExploreDescription: exploreMenu.Description,
		Primary: buildNavigationTree(primaryMenu, path, aliases), Utility: rootNavigationItems(utilityMenu, path, 12, aliases),
		Mobile: rootNavigationItems(mobileMenu, path, 0, aliases), Footer: buildNavigationTree(footerMenu, path, aliases),
		MobileAction: rootNavigationItems(mobileActionMenu, path, 1, aliases), MobileActionConfigured: mobileActionConfigured,
		PrimaryConfigured: primaryConfigured, UtilityConfigured: utilityConfigured,
		MobileConfigured: mobileConfigured, FooterConfigured: footerConfigured,
		CSSURL: "/navigation.css?v=" + navigationSnapshotRevision(snapshot),
	}
	if !exploreConfigured {
		view.Explore, _ = exploreFallbackItems(view.Primary)
	}
	if !mobileConfigured && primaryConfigured {
		view.Mobile = rootNavigationItems(primaryMenu, path, 0, aliases)
	}
	if len(view.MobileAction) > 0 {
		current, err := url.Parse(path)
		view.MobileAction[0].Active = err == nil && mobileNavigationMatch(view.MobileAction[0].URL, current) > 0
	}
	for _, menu := range menus {
		if len(menu.Locations) == 0 {
			continue
		}
		for _, item := range menu.Items {
			if item.Badge.Text != "" {
				view.HasBadges = true
				break
			}
		}
	}
	if ctx.Err() != nil {
		return navigationView{}
	}
	return view
}

func navigationNeedsCategoryAliases(currentPath string, locations map[string]int64, menus map[int64]navigation.Menu) bool {
	current, err := url.Parse(currentPath)
	if err != nil || current.Path != "/explore/" || current.Query().Get("category") == "" {
		return false
	}
	// Unassigned menus and filtered private subtrees cannot contribute a
	// current link. Match the URL rules used by navigationMatch below.
	for _, id := range locations {
		for _, item := range menus[id].Items {
			target, err := url.Parse(item.URL)
			if err == nil && !target.IsAbs() && target.Host == "" && target.Fragment == "" && path.Clean(target.Path) == "/explore" && target.Query().Get("category") != "" {
				return true
			}
		}
	}
	return false
}

// exploreFallbackItems uses the first published root pointing at the directory.
// Keep this shared by the public projection and the editor's source explanation.
func exploreFallbackItems(items []navigationItemView) ([]navigationItemView, bool) {
	for _, item := range items {
		target, err := url.Parse(item.URL)
		if err == nil && target.Host == "" && !target.IsAbs() && target.Path == "/explore/" && target.RawQuery == "" && target.Fragment == "" {
			return item.Children, true
		}
	}
	return nil, false
}

// Public menus are shared across sessions and may contain links saved before
// launch. Keep operational links in the authenticated account menu, and omit
// retired demos without rewriting the administrator's stored configuration.
func (s *Server) publicNavigationMenu(menu navigation.Menu) navigation.Menu {
	menu.Items = append([]navigation.Item(nil), menu.Items...)
	for i := range menu.Items {
		item := &menu.Items[i]
		if item.Label == "星球" && item.URL == "/community/" {
			item.Label = "圈子"
		}
		if item.Label == "问答" && item.URL == "/community/#questions" {
			item.URL = "/questions/"
		}
	}
	publicURL, _ := url.Parse(s.config.PublicURL)
	blocked := make(map[int64]bool)
	for _, item := range menu.Items {
		target, err := url.Parse(item.URL)
		if err != nil {
			blocked[item.ID] = true
			continue
		}
		if target.Host != "" && (publicURL == nil || !strings.EqualFold(target.Host, publicURL.Host)) {
			continue
		}
		targetPath := path.Clean("/" + strings.TrimLeft(target.Path, "/"))
		if targetPath == "/tools" || targetPath == "/tools/device-mockup" {
			blocked[item.ID] = true
		}
		if !s.moduleEnabled(module.Owner(targetPath)) {
			blocked[item.ID] = true
		}
		for _, prefix := range []string{"/components", "/market", "/admin", "/health", "/api"} {
			if targetPath == prefix || strings.HasPrefix(targetPath, prefix+"/") {
				blocked[item.ID] = true
				break
			}
		}
	}
	// Input is not necessarily in parent-first order. Exclude each blocked
	// subtree before building or limiting any public location.
	for depth := 0; depth < navigation.MaxDepth; depth++ {
		for _, item := range menu.Items {
			if blocked[item.ParentID] {
				blocked[item.ID] = true
			}
		}
	}
	items := make([]navigation.Item, 0, len(menu.Items))
	for _, item := range menu.Items {
		if !blocked[item.ID] {
			items = append(items, item)
		}
	}
	menu.Items = items
	return menu
}

func buildNavigationTree(menu navigation.Menu, currentPath string, aliases ...map[string]string) []navigationItemView {
	if menu.ID <= 0 || len(menu.Items) == 0 {
		return nil
	}
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
	var build func(int64, int) []navigationItemView
	build = func(parentID int64, depth int) []navigationItemView {
		if depth >= navigation.MaxDepth {
			return nil
		}
		result := make([]navigationItemView, 0, len(children[parentID]))
		for _, item := range children[parentID] {
			view := navigationItemView{
				ID: item.ID, Label: item.Label, URL: item.URL, Description: item.Description,
				Rel: item.Rel, Icon: item.Icon, Presentation: item.Presentation, Badge: item.Badge,
				NewWindow: item.Target == navigation.TargetBlank,
			}
			view.Children = build(item.ID, depth+1)
			result = append(result, view)
		}
		return result
	}
	items := build(0, 0)
	var best *navigationItemView
	score := 0
	var selectCurrent func([]navigationItemView)
	selectCurrent = func(items []navigationItemView) {
		for i := range items {
			if match := navigationMatch(items[i].URL, currentPath, aliases...); match > score {
				best, score = &items[i], match
			}
			selectCurrent(items[i].Children)
		}
	}
	selectCurrent(items)
	if best != nil {
		best.Current = true
	}
	var markAncestors func([]navigationItemView) bool
	markAncestors = func(items []navigationItemView) bool {
		active := false
		for i := range items {
			childActive := markAncestors(items[i].Children)
			items[i].Active = items[i].Current || childActive
			active = active || items[i].Active
		}
		return active
	}
	markAncestors(items)
	return items
}

func rootNavigationItems(menu navigation.Menu, currentPath string, limit int, aliases ...map[string]string) []navigationItemView {
	items := buildNavigationTree(menu, currentPath, aliases...)
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	for index := range items {
		items[index].Children = nil
	}
	return items
}

func navigationURLActive(rawURL, currentPath string) bool {
	return navigationMatch(rawURL, currentPath) > 0
}

// Query parameters on the menu link constrain the destination; additional
// result pagination and display parameters do not change its identity.
func navigationMatch(rawURL, currentURL string, aliases ...map[string]string) int {
	target, err := url.Parse(rawURL)
	current, currentErr := url.Parse(currentURL)
	if err != nil || currentErr != nil || target.IsAbs() || target.Host != "" || target.Path == "" || target.Fragment != "" {
		return 0
	}
	targetPath, currentPath := path.Clean(target.Path), path.Clean(current.Path)
	exact := targetPath == currentPath
	if !exact && !(targetPath != "/" && strings.HasSuffix(target.Path, "/") && strings.HasPrefix(currentPath, targetPath+"/")) {
		return 0
	}
	query, err := url.ParseQuery(target.RawQuery)
	values, currentErr := url.ParseQuery(current.RawQuery)
	if err != nil || currentErr != nil {
		return 0
	}
	for key, expected := range query {
		actual := append([]string(nil), values[key]...)
		expected = append([]string(nil), expected...)
		if len(actual) != len(expected) {
			return 0
		}
		sort.Strings(actual)
		sort.Strings(expected)
		for i := range expected {
			if key == "category" && targetPath == "/explore" && len(aliases) > 0 {
				if canonical, ok := aliases[0][strings.ToLower(actual[i])]; ok {
					actual[i] = canonical
				}
				if canonical, ok := aliases[0][strings.ToLower(expected[i])]; ok {
					expected[i] = canonical
				}
			}
			if actual[i] != expected[i] {
				return 0
			}
		}
	}
	score := len(targetPath)*100 + len(query)
	if exact {
		score += 100000
	}
	return score
}

func (s *Server) navigationCSS(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/navigation.css" {
		http.NotFound(writer, request)
		return
	}
	if request.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	writer.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(request.Context(), navigationReadTimeout)
	defer cancel()
	snapshot, err := s.navigation.Snapshot(ctx)
	if request.Context().Err() != nil {
		panic(http.ErrAbortHandler)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		http.Error(writer, "navigation stylesheet unavailable", http.StatusServiceUnavailable)
		return
	}
	revision := navigationSnapshotRevision(snapshot)
	etag := `"navigation-` + revision + `"`
	writer.Header().Set("Content-Type", "text/css; charset=utf-8")
	writer.Header().Set("ETag", etag)
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.URL.Query().Get("v") == revision {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		writer.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	}
	if etagMatches(request.Header.Get("If-None-Match"), etag) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	var css strings.Builder
	css.WriteString("/* NewPress navigation accents: generated from validated values. */\n")
	for _, menu := range snapshot.Menus {
		for _, item := range menu.Items {
			if item.Accent != "" {
				fmt.Fprintf(&css, ".nav-item-%d{--menu-accent:%s}\n", item.ID, item.Accent)
			}
			if variables := item.Badge.CSSVariables(); variables != "" {
				fmt.Fprintf(&css, ".nav-item-%d{ %s }\n", item.ID, variables)
			}
		}
	}
	_, _ = writer.Write([]byte(css.String()))
}

func navigationSnapshotRevision(snapshot navigation.Snapshot) string {
	return navigation.SnapshotRevision(snapshot)
}

// mobileDockItems projects four distinct destinations without changing the saved
// menu. A flattened dock has one current destination, not the ancestor states
// inherited by the primary menu's flyout tree.
func mobileDockItems(items []navigationItemView, currentPath string, community bool) []navigationItemView {
	dock := make([]navigationItemView, 0, 4)
	fallback := []navigationItemView{{Label: "首页", URL: "/", Icon: "house"}, {Label: "发现", URL: "/explore/", Icon: "compass"}}
	if community {
		fallback = append(fallback, navigationItemView{Label: "圈子", URL: "/community/", Icon: "users"})
	}
	fallback = append(fallback, navigationItemView{Label: "我的", URL: "/account/", Icon: "user-round"}, navigationItemView{Label: "专题", URL: "/series/", Icon: "bookmark"})
	seen := make(map[string]bool, 4)
	for _, item := range append(append([]navigationItemView(nil), items...), fallback...) {
		if len(dock) == 4 {
			break
		}
		if item.URL != "" && !seen[item.URL] {
			seen[item.URL] = true
			item.Active = false
			item.Current = false
			dock = append(dock, item)
		}
	}
	current, err := url.Parse(currentPath)
	if err != nil || current.Path == "" {
		return dock
	}
	best, score := -1, 0
	for i, item := range dock {
		if match := mobileNavigationMatch(item.URL, current); match > score {
			best, score = i, match
		}
	}
	if best >= 0 {
		dock[best].Active = true
		dock[best].Current = true
	}
	return dock
}

func mobileNavigationMatch(target string, current *url.URL) int {
	return navigationMatch(target, current.String())
}
