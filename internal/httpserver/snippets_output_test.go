package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/theme"
)

type snippetOutputRepository struct {
	content.Repository
	calls   [][]contenthtml.SnippetKey
	bodies  map[contenthtml.SnippetKey]string
	failure error
}

func (r *snippetOutputRepository) ResolveSnippets(ctx context.Context, keys []contenthtml.SnippetKey) (map[contenthtml.SnippetKey]string, error) {
	r.calls = append(r.calls, slices.Clone(keys))
	if r.failure != nil || r.bodies != nil {
		return r.bodies, r.failure
	}
	return r.Repository.ResolveSnippets(ctx, keys)
}

func TestSnippetOutputBatchesAndBoundsAcrossBodies(t *testing.T) {
	key := contenthtml.SnippetKey{ID: 1, Version: 1}
	shell := contenthtml.SnippetShell(key)
	t.Run("occurrences", func(t *testing.T) {
		repo := &snippetOutputRepository{bodies: map[contenthtml.SnippetKey]string{key: "<p>visible</p>"}}
		s := &Server{repository: repo}
		first := "<h2>first</h2>" + strings.Repeat(shell, 150)
		last := strings.Repeat(shell, 150) + "<p>last</p>"
		got, found := s.expandPublicSnippetBodies(t.Context(), []string{first, last})
		if !found || len(repo.calls) != 1 || len(repo.calls[0]) != 1 || strings.Count(got[0], "visible") != 150 || strings.Count(got[1], "visible") != 50 {
			t.Fatal("response did not share lookup and instance limit", found, repo.calls, len(got[0]), len(got[1]))
		}
		if !strings.HasPrefix(got[0], "<h2>first</h2>") || !strings.HasSuffix(got[1], "<p>last</p>") || strings.Contains(strings.Join(got, ""), "data-np-snippet") {
			t.Fatal("normal body changed or unresolved shells survived")
		}
	})
	t.Run("bytes", func(t *testing.T) {
		body := "<p>" + strings.Repeat("s", content.MaxSnippetBodyBytes-7) + "</p>"
		repo := &snippetOutputRepository{bodies: map[contenthtml.SnippetKey]string{key: body}}
		s := &Server{repository: repo}
		sources := make([]string, 100)
		for i := range sources {
			sources[i] = fmt.Sprintf("<h2>%d</h2>", i) + shell
		}
		got, found := s.expandPublicSnippetBodies(t.Context(), sources)
		expanded, instances := 0, 0
		for i, value := range got {
			prefix := fmt.Sprintf("<h2>%d</h2>", i)
			if !strings.HasPrefix(value, prefix) {
				t.Fatal("ordinary content was truncated", i)
			}
			expanded += len(value) - len(prefix)
			instances += strings.Count(value, "<aside>")
		}
		if !found || expanded > maxExpandedSnippetsBytes || instances != 31 || len(repo.calls) != 1 {
			t.Fatal("aggregate expansion limit", expanded, instances, len(repo.calls))
		}
	})
	t.Run("distinct-versions", func(t *testing.T) {
		repo := &snippetOutputRepository{bodies: map[contenthtml.SnippetKey]string{}}
		sources := make([]string, 120)
		for i := range sources {
			k := contenthtml.SnippetKey{ID: 1, Version: int64(i + 1)}
			sources[i] = contenthtml.SnippetShell(k)
			repo.bodies[k] = fmt.Sprintf("<p>version-%d</p>", i+1)
		}
		s := &Server{repository: repo}
		got, found := s.expandPublicSnippetBodies(t.Context(), sources)
		if !found || len(repo.calls) != 1 || len(repo.calls[0]) != 100 {
			t.Fatal("lookup bound", repo.calls)
		}
		for i, value := range got {
			want := ""
			if i < 100 {
				want = fmt.Sprintf("<aside><p>version-%d</p></aside>", i+1)
			}
			if value != want {
				t.Fatalf("position %d: %q != %q", i, value, want)
			}
		}
	})
	t.Run("failure-and-malformed", func(t *testing.T) {
		repo := &snippetOutputRepository{bodies: map[contenthtml.SnippetKey]string{key: "<p>partial-must-not-leak</p>"}, failure: errors.New("unavailable")}
		s := &Server{repository: repo}
		sources := []string{"<p>keep</p>" + shell, `<aside data-np-snippet="bad">SECRET<aside>fallback</aside></aside>tail`, `<aside data-np-snippet-version="1">unclosed-secret`}
		got, found := s.expandPublicSnippetBodies(t.Context(), sources)
		if !found || !reflect.DeepEqual(got, []string{"<p>keep</p>", "tail", ""}) {
			t.Fatal(got, found)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		repo.calls = nil
		got, found = s.expandPublicSnippetBodies(ctx, []string{shell})
		if !found || got[0] != "" || len(repo.calls) != 0 {
			t.Fatal("cancelled read", got, repo.calls)
		}
	})
	t.Run("request-local-and-sanitized", func(t *testing.T) {
		repo := &snippetOutputRepository{bodies: map[contenthtml.SnippetKey]string{key: `<p onclick="bad()">safe</p><script>alert(1)</script>`}}
		s := &Server{repository: repo}
		first, _ := s.expandPublicSnippetBodies(t.Context(), []string{shell, shell})
		if first[0] != first[1] || strings.Contains(first[0], "script") || strings.Contains(first[0], "onclick") {
			t.Fatal(first)
		}
		repo.bodies = map[contenthtml.SnippetKey]string{}
		next, _ := s.expandPublicSnippetBodies(t.Context(), []string{shell})
		if next[0] != "" || len(repo.calls) != 2 {
			t.Fatal("body cached across requests", next, repo.calls)
		}
	})
}

func snippetOutputCollections(source string) pageView {
	p := content.Post{ID: 1, ContentSource: source, ContentHTML: source}
	items := []content.Post{p}
	return pageView{Post: p, Posts: items, Featured: items,
		Home:   homePageView{FocusPosts: items, Updated: items, SpotlightDesktop: items, SpotlightMobile: items, Recent: items},
		Series: seriesPageView{Chapters: items}, ReadingSeries: []seriesReadingView{{Previous: p, Next: p, Related: items}}}
}

func TestSnippetOutputCoversPublicPostCollectionsWithoutAliasing(t *testing.T) {
	key := contenthtml.SnippetKey{ID: 1, Version: 1}
	shell := contenthtml.SnippetShell(key)
	repo := &snippetOutputRepository{bodies: map[contenthtml.SnippetKey]string{key: "<p>shared-body</p>"}}
	s := &Server{repository: repo}
	original := snippetOutputCollections(shell)
	view := original
	if !s.prepareSnippets(t.Context(), &view) || len(repo.calls) != 1 {
		t.Fatal("no single collection batch")
	}
	if !reflect.DeepEqual(original, snippetOutputCollections(shell)) {
		t.Fatal("input aliases were mutated")
	}
	for _, project := range []any{publicPage(view), chunnelPage(view)} {
		seen := 0
		var walk func(reflect.Value)
		walk = func(value reflect.Value) {
			if value.Type() == reflect.TypeFor[presentation.Post]() {
				seen++
				if body := value.Interface().(presentation.Post).ContentHTML; body != "<aside><p>shared-body</p></aside>" {
					t.Fatal("unexpanded public post", body)
				}
				return
			}
			switch value.Kind() {
			case reflect.Struct:
				for i := 0; i < value.NumField(); i++ {
					if value.Field(i).CanInterface() {
						walk(value.Field(i))
					}
				}
			case reflect.Slice:
				for i := 0; i < value.Len(); i++ {
					walk(value.Index(i))
				}
			}
		}
		walk(reflect.ValueOf(project))
		if seen != 12 {
			t.Fatalf("public post field coverage changed: %d", seen)
		}
	}
	admin := original
	admin.IsAdmin = true
	repo.calls = nil
	if s.prepareSnippets(t.Context(), &admin) || len(repo.calls) != 0 || admin.Post.ContentHTML != shell {
		t.Fatal("admin edit source expanded")
	}
}

func TestSnippetPublicAPIBatchLimitsAndVisibilityRefresh(t *testing.T) {
	s, handler, repository, _, principal := newAuthenticatedContentServer(t)
	a := snippetAccess(principal)
	snippet, err := repository.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "batch", ContentSource: "<p>shared-output-value</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	shell := contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})
	for i := range 100 {
		if _, err = repository.Create(t.Context(), content.PostInput{Title: fmt.Sprintf("post-%03d", i), Slug: fmt.Sprintf("batch-%d", i), Status: content.StatusPublish, ContentSource: shell + shell + shell, Excerpt: "summary-only"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.API.PublicIncludeBody = true
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, settings); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	repo := &snippetOutputRepository{Repository: repository}
	s.repository = repo
	get := func(path string) *httptest.ResponseRecorder {
		return serveCookieRequest(handler, http.MethodGet, "https://example.test"+path, nil, nil)
	}
	response := get("/api/v1/posts?limit=100")
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || len(repo.calls) != 1 || strings.Count(response.Body.String(), "shared-output-value") != 200 {
		t.Fatal(response.Code, response.Header(), len(repo.calls), strings.Count(response.Body.String(), "shared-output-value"))
	}
	var payload struct {
		Items                     []publicPostResponse `json:"items"`
		Count, Total, Page, Pages int
		HasMore                   bool `json:"has_more"`
	}
	decodeAPIResponse(t, response, &payload)
	if len(payload.Items) != 100 || payload.Count != 100 || payload.Total != 100 || payload.HasMore {
		t.Fatal("pagination changed", payload.Count, payload.Total, payload.HasMore)
	}
	if _, err = repository.WithdrawSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, a); err != nil {
		t.Fatal(err)
	}
	response = get("/api/v1/posts?limit=100")
	if response.Code != 200 || strings.Contains(response.Body.String(), "shared-output-value") || len(repo.calls) != 2 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("withdrawal/caching", response.Code, len(repo.calls))
	}
	before := len(repo.calls)
	response = get("/feed.xml?limit=100")
	if response.Code != 200 || len(repo.calls) != before || strings.Contains(response.Body.String(), "data-np-snippet") || !strings.Contains(response.Body.String(), "summary-only") {
		t.Fatal("feed stopped being summary-only", response.Code)
	}
	snapshot = s.siteSettings.Snapshot()
	settings = snapshot.Settings
	settings.API.PublicIncludeBody = false
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, settings); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	response = get("/api/v1/posts?limit=100")
	if response.Code != 200 || len(repo.calls) != before || strings.Contains(response.Body.String(), "content_html") {
		t.Fatal("body-disabled API resolved snippets", response.Code, len(repo.calls))
	}
}

func TestPublicPostsEncodedResponseLimitPreservesWholeRecords(t *testing.T) {
	item := publicPostResponse{ID: 1, Title: "encoded", ContentHTML: strings.Repeat("<>&", 180000)}
	// Raw fields fit under 8 MiB; JSON HTML escaping pushes three records past it.
	if got, err := encodePublicPosts([]publicPostResponse{item, item}, 2, 1, 2, 1); err != nil || len(got) > theme.MaxRenderBytes || !json.Valid(got) {
		t.Fatal("valid page", len(got), err)
	}
	if got, err := encodePublicPosts([]publicPostResponse{item, item, item}, 3, 1, 3, 1); !errors.Is(err, theme.ErrRenderLimit) || got != nil {
		t.Fatal("encoded overflow returned partial JSON", len(got), err)
	}
	empty, err := encodePublicPosts(nil, 0, 1, 20, 1)
	if err != nil || !strings.Contains(string(empty), `"items":[]`) {
		t.Fatal("empty list shape", string(empty), err)
	}
}

func TestPublicPostsOversizeHTTPFailureIsAtomic(t *testing.T) {
	s, handler, repository, _, _ := newAuthenticatedContentServer(t)
	for i := range 5 {
		if _, err := repository.Create(t.Context(), content.PostInput{Title: fmt.Sprint("long-", i), Slug: fmt.Sprint("long-", i), Status: content.StatusPublish, ContentSource: "<p>" + strings.Repeat("x", 1800000) + "</p>"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := s.siteSettings.Snapshot()
	settings := snapshot.Settings
	settings.API.PublicIncludeBody = true
	if _, fields, err := s.siteSettings.Save(snapshot.Revision, settings); err != nil || len(fields) > 0 {
		t.Fatal(err, fields)
	}
	response := serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/posts?limit=5", nil, nil)
	if response.Code != 422 || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "content.response_too_large") || strings.Contains(response.Body.String(), "long-") {
		t.Fatal(response.Code, response.Header(), response.Body.String())
	}
	response = serveCookieRequest(handler, http.MethodGet, "https://example.test/api/v1/posts?limit=1", nil, nil)
	if response.Code != 200 || !json.Valid(response.Body.Bytes()) {
		t.Fatal("smaller page failed", response.Code)
	}
}

func TestSnippetThemeCollectionsRespectWithdrawalAndNoStore(t *testing.T) {
	s, _, repository, _, principal := newAuthenticatedContentServer(t)
	access := snippetAccess(principal)
	snippet, err := repository.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "theme snippet", ContentSource: "<p>theme-snippet-value</p>", Visibility: content.SnippetShared}, access)
	if err != nil {
		t.Fatal(err)
	}
	shell := contenthtml.SnippetShell(contenthtml.SnippetKey{ID: snippet.ID, Version: 1})
	s.themes = boundaryTheme(t, "independent", "body{color:red}", `{{safeHTML .Post.ContentHTML}}{{range .Posts}}{{safeHTML .ContentHTML}}{{end}}{{range .Featured}}{{safeHTML .ContentHTML}}{{end}}{{range .Home.FocusPosts}}{{safeHTML .ContentHTML}}{{end}}{{range .Home.Updated}}{{safeHTML .ContentHTML}}{{end}}{{range .Home.SpotlightDesktop}}{{safeHTML .ContentHTML}}{{end}}{{range .Home.SpotlightMobile}}{{safeHTML .ContentHTML}}{{end}}{{range .Home.Recent}}{{safeHTML .ContentHTML}}{{end}}{{range .Series.Chapters}}{{safeHTML .ContentHTML}}{{end}}{{range .ReadingSeries}}{{safeHTML .Previous.ContentHTML}}{{safeHTML .Next.ContentHTML}}{{range .Related}}{{safeHTML .ContentHTML}}{{end}}{{end}}`)
	view := snippetOutputCollections(shell)
	view.Site = s.baseView("home", "", "/").Site
	view.Now = time.Now()
	repo := &snippetOutputRepository{Repository: repository}
	s.repository = repo
	render := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.render(w, 200, "home", view)
		return w
	}
	response := render()
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || strings.Count(response.Body.String(), "theme-snippet-value") != 12 || len(repo.calls) != 1 {
		t.Fatal(response.Code, response.Header(), len(repo.calls), response.Body.String())
	}
	if _, err = repository.WithdrawSnippet(t.Context(), snippet.ID, content.Mutation{ExpectedVersion: snippet.Version}, access); err != nil {
		t.Fatal(err)
	}
	response = render()
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "theme-snippet-value") || len(repo.calls) != 2 {
		t.Fatal("stale theme body", response.Code, response.Header(), len(repo.calls))
	}
}

func TestPublicRenderedResponseIncludesInsertedAssetsInLimit(t *testing.T) {
	s, _, _, _, _ := newAuthenticatedContentServer(t)
	// Render exactly below the ceiling; select enhancement is added afterward.
	s.themes = boundaryTheme(t, "independent", "body{color:red}", `<html><head></head><body><select><option>choice</option></select>{{.Post.Title}}</body></html>`)
	view := s.baseView("bounded", "", "/")
	var plain theme.RenderBuffer
	if err := s.renderTheme(&plain, "home", view); err != nil {
		t.Fatal(err)
	}
	view.Post.Title = strings.Repeat("x", theme.MaxRenderBytes-plain.Len()-1)
	plain.Reset()
	if err := s.renderTheme(&plain, "home", view); err != nil || plain.Len() != theme.MaxRenderBytes-1 {
		t.Fatal("fixture must fit before asset insertion", plain.Len(), err)
	}
	w := httptest.NewRecorder()
	s.render(w, 200, "home", view)
	if w.Code != 500 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "<select") || w.Body.Len() > 10000 {
		t.Fatal("partial/oversize HTML committed", w.Code, w.Header(), w.Body.Len())
	}
}
