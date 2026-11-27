package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
)

func TestSnippetSummaryPaginationAPIAndPage(t *testing.T) {
	_, handler, r, cookie, principal := newAuthenticatedContentServer(t)
	a := snippetAccess(principal)
	s, err := r.SaveSnippet(t.Context(), 0, content.Mutation{}, content.SnippetInput{Title: "v1", ContentSource: "<p>old-only-body</p>", Visibility: content.SnippetShared}, a)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 30; i++ {
		s, err = r.SaveSnippet(t.Context(), s.ID, content.Mutation{ExpectedVersion: s.Version}, content.SnippetInput{Title: fmt.Sprint("v", i), ContentSource: fmt.Sprintf("<p>current-body-%d</p>", i), Visibility: content.SnippetShared}, a)
		if err != nil {
			t.Fatal(err)
		}
	}
	url := "https://example.test/api/v1/admin/snippets/1"
	w := serveContentAPI(handler, cookie, http.MethodGet, url+"?view=summary", "", nil)
	var result content.SnippetPage
	decodeAPIResponse(t, w, &result)
	if w.Code != 200 || len(result.History) != 24 || result.NextBefore != 7 || result.Current.Version != 30 || strings.Contains(w.Body.String(), "old-only-body") || w.Header().Get("ETag") != snippetETag(s) {
		t.Fatal(w.Code, result, w.Body.String())
	}
	w = serveContentAPI(handler, cookie, http.MethodGet, url+"?view=summary&before=7", "", nil)
	decodeAPIResponse(t, w, &result)
	if w.Code != 200 || len(result.History) != 6 || result.History[5].Version != 1 || strings.Contains(w.Body.String(), "old-only-body") {
		t.Fatal(w.Code, result)
	}
	for _, query := range []string{"?before=7", "?view=other", "?view=summary&before=0", "?view=summary&before=1&before=2"} {
		w = serveContentAPI(handler, cookie, http.MethodGet, url+query, "", nil)
		if w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	w = serveContentAPI(handler, cookie, http.MethodGet, url, "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "old-only-body") {
		t.Fatal("legacy history changed", w.Code)
	}
	w = serveContentAPI(handler, cookie, http.MethodGet, "https://example.test/admin/snippets/?edit=1", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "更早记录") || !strings.Contains(w.Body.String(), "before=7") || strings.Contains(w.Body.String(), "old-only-body") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = serveContentAPI(handler, cookie, http.MethodGet, "https://example.test/admin/snippets/?edit=1&before=7", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "最新记录") || !strings.Contains(w.Body.String(), "current-body-30") {
		t.Fatal(w.Code, w.Body.String())
	}
}
