package content

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicSearchStaticTextAndNormalization(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{`<p>中<em>文</em>完整正文</p><p>A&nbsp;&amp; B</p>`, "中文完整正文 a & b"},
		{`<p data-x="private-attribute">public<!--secret-comment--> text</p><script>private-script</script><style>private-style</style><template>private-template</template>`, "public text"},
		{`<p>one<br>two</p><table><tr><td>甲</td><td>乙</td></tr></table>`, "one two 甲 乙"},
		{`<code>&lt;script&gt;literal&lt;/script&gt;</code>`, "<script>literal</script>"},
		{"<p>İ Σ K É ß</p><p>A\u3000\u00a0B</p>", "i σ k é ß a b"},
	} {
		if got := PublicSearchBody(tc.source); got != tc.want {
			t.Fatalf("static text %q: got %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestPublicSearchFullBodyRankingAndFileReopen(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "content.json")
			var repository Repository = NewMemoryRepository(false)
			if mode == "file" {
				file, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				repository = file
			}
			stamp := time.Now().Add(-time.Hour)
			var ids []int64
			for i, input := range []PostInput{
				{Title: "ordinary", ContentSource: "<p>needle 深<em>度</em>正文 100%_literal\\path</p>"},
				{Title: "needle title", ContentSource: "<p>other text</p>"},
				{Title: "excerpt match", Excerpt: "needle"},
				{Title: "tag match", TagNames: []string{"needle"}},
				{Title: "author match", AuthorName: "needle"},
			} {
				input.Type, input.Status, input.Slug = TypePost, StatusPublish, fmt.Sprintf("full-search-%d", i)
				input.PublishedAt = stamp.Add(time.Duration(i) * time.Minute)
				if input.Excerpt == "" {
					input.Excerpt = "explicit unrelated summary"
				}
				post, err := repository.Create(t.Context(), input)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, post.ID)
			}
			for _, status := range []string{StatusDraft, StatusPrivate, StatusPending, StatusTrash, StatusFuture} {
				_, err := repository.Create(t.Context(), PostInput{Title: "needle hidden", Status: status, Slug: "hidden-" + status, ContentSource: "<p>private-unique-token</p>", PublishedAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "file" {
				file, err := NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
				repository = file
			}
			want := []int64{ids[1], ids[2], ids[3], ids[4], ids[0]}
			for offset, id := range want {
				page, err := repository.SearchPublished(t.Context(), PublishedQuery{Search: "NEEDLE", Offset: offset, Limit: 1})
				if err != nil || page.Total != len(want) || len(page.Items) != 1 || page.Items[0].ID != id {
					t.Fatalf("ranked page %d: %#v, %v", offset, page, err)
				}
			}
			for _, query := range []string{"深度正文", "100%_literal\\path"} {
				page, err := repository.SearchPublished(t.Context(), PublishedQuery{Search: query})
				if err != nil || page.Total != 1 || page.Items[0].ID != ids[0] {
					t.Fatal(query, page, err)
				}
			}
			page, err := repository.SearchPublished(t.Context(), PublishedQuery{Search: "private-unique-token"})
			if err != nil || page.Total != 0 {
				t.Fatal("private body leaked", page, err)
			}
			before, err := repository.GetByID(t.Context(), ids[0])
			if err != nil {
				t.Fatal(err)
			}
			revisions, err := repository.ListRevisions(t.Context(), ids[0], 10)
			if err != nil {
				t.Fatal(err)
			}
			updated, err := repository.Update(t.Context(), ids[0], Mutation{ExpectedVersion: before.Version, Actor: "editor"}, PostInput{Title: before.Title, Slug: before.Slug, Status: before.Status, Excerpt: before.Excerpt, ContentSource: "<p>replacement-body</p>", Category: before.Category, PublishedAt: before.PublishedAt})
			if err != nil {
				t.Fatal(err)
			}
			page, err = repository.SearchPublished(t.Context(), PublishedQuery{Search: "深度正文"})
			if err != nil || page.Total != 0 {
				t.Fatal("stale body", page, err)
			}
			if _, err := repository.RestoreRevision(t.Context(), ids[0], revisions[0].ID, Mutation{ExpectedVersion: updated.Version, Actor: "editor"}); err != nil {
				t.Fatal(err)
			}
			page, err = repository.SearchPublished(t.Context(), PublishedQuery{Search: "深度正文"})
			if err != nil || page.Total != 1 {
				t.Fatal("restored body", page, err)
			}
		})
	}
}

func TestPublicSearchDoesNotCrossFieldsOrRewardRepetition(t *testing.T) {
	post := Post{Title: "alpha", Excerpt: "beta", ContentHTML: strings.Repeat("<p>needle</p>", 1000)}
	if PublicSearchScore(post, "alpha beta") != 0 || PublicSearchScore(post, "needle") != 1 {
		t.Fatal("field boundaries or body weighting changed")
	}
	post.Title = "needle"
	if PublicSearchScore(post, "needle") != 65 {
		t.Fatal("title weight changed")
	}
}
