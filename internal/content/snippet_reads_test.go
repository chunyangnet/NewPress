package content

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/contenthtml"
)

func TestSnippetPagesAndIndexedImpactsAcrossStores(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			var r Repository = NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			if kind == "file" {
				var err error
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			a := snippetManager()
			s, err := r.SaveSnippet(t.Context(), 0, Mutation{}, SnippetInput{"private", "<p>never visible history</p>", SnippetPrivate}, a)
			if err != nil {
				t.Fatal(err)
			}
			for i := 2; i <= 53; i++ {
				s, err = r.SaveSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, SnippetInput{fmt.Sprint("version ", i), fmt.Sprintf("<p>body %d</p>", i), SnippetShared}, a)
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "file" {
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			reader := SnippetAccess{UserID: 7, Identity: "user:7", Actor: "reader", Read: true}
			before := int64(0)
			seen := []int64{}
			for {
				page, e := r.GetSnippetPage(t.Context(), s.ID, reader, before, 24)
				if e != nil {
					t.Fatal(e)
				}
				if len(page.History) > 24 || page.Current.Version != 53 || page.Current.ContentSource != "<p>body 53</p>" {
					t.Fatal(page)
				}
				raw, _ := json.Marshal(page)
				if strings.Contains(string(raw), "never visible") {
					t.Fatal("private history leaked")
				}
				for _, item := range page.History {
					seen = append(seen, item.Version)
				}
				if page.NextBefore == 0 {
					break
				}
				if before > 0 && page.NextBefore >= before {
					t.Fatal("nondecreasing cursor")
				}
				before = page.NextBefore
			}
			if len(seen) != 52 || seen[0] != 53 || seen[51] != 2 {
				t.Fatal(seen)
			}
			old, e := r.GetSnippetPage(t.Context(), s.ID, a, 2, 24)
			if e != nil || len(old.History) != 1 || old.History[0].Version != 1 {
				t.Fatal(old, e)
			}
			for i, source := range []string{
				contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 2}) + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 53}),
				`<ASIDE DATA-NP-SNIPPET='1' DATA-NP-SNIPPET-VERSION='3'>discard</ASIDE>`,
				`<aside data-np-snippet="bad">` + contenthtml.SnippetShell(contenthtml.SnippetKey{ID: s.ID, Version: 2}) + `</aside>`,
			} {
				if _, e = r.Create(t.Context(), PostInput{Title: fmt.Sprint("post ", i), Slug: fmt.Sprint("post-", i), AuthorID: 7, ContentSource: source}); e != nil {
					t.Fatal(e)
				}
			}
			assertImpacts := func() {
				t.Helper()
				posts := []Post{}
				for id := int64(1); id <= 3; id++ {
					p, e := r.GetByID(t.Context(), id)
					if errors.Is(e, ErrNotFound) {
						continue
					}
					if e != nil {
						t.Fatal(e)
					}
					posts = append(posts, p)
				}
				want := []SnippetImpact{}
				for _, p := range posts {
					v := SnippetPostImpact(p, s.ID, s.CurrentVersion)
					if v.References > 0 {
						want = append(want, v)
					}
				}
				got, e := r.SnippetImpacts(t.Context(), s.ID, a, 100, 0)
				if e != nil || !reflect.DeepEqual(got.Items, want) {
					t.Fatal(got, want, e)
				}
			}
			assertImpacts()
			p, e := r.GetByID(t.Context(), 1)
			if e != nil {
				t.Fatal(e)
			}
			p, e = r.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, PostInput{Title: p.Title, AuthorID: p.AuthorID, ContentSource: "<p>no refs</p>"})
			if e != nil {
				t.Fatal(e)
			}
			assertImpacts()
			revs, e := r.ListRevisions(t.Context(), p.ID, 10)
			if e != nil || len(revs) != 2 {
				t.Fatal(revs, e)
			}
			p, e = r.RestoreRevision(t.Context(), p.ID, revs[len(revs)-1].ID, Mutation{ExpectedVersion: p.Version})
			if e != nil {
				t.Fatal(e)
			}
			assertImpacts()
			p, e = r.ChangeStatus(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, StatusTrash)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = r.BulkChangeStatus(t.Context(), BulkStatusChange{Delete: true, Status: StatusTrash, Items: []BulkMutation{{ID: p.ID, ExpectedVersion: p.Version}}}); e != nil {
				t.Fatal(e)
			}
			assertImpacts()
			if kind == "file" {
				r, err = NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				assertImpacts()
			}
			if _, e = r.GetSnippetPage(t.Context(), s.ID, a, -1, 24); e == nil {
				t.Fatal("negative cursor accepted")
			}
			s, e = r.WithdrawSnippet(t.Context(), s.ID, Mutation{ExpectedVersion: s.Version}, a)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = r.GetSnippetPage(t.Context(), s.ID, reader, 0, 24); !errors.Is(e, ErrNotFound) {
				t.Fatal(e)
			}
		})
	}
}
