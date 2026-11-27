package content

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStructuredContentSurvivesFileAutosaveRevisionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	repo, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	source := `<h2>第一章</h2><aside data-callout="warning"><p>先备份</p></aside><pre><code class="language-go">fmt.Println(&quot;&lt;script&gt;&quot;)</code></pre><table><thead><tr><th scope="col">项目</th></tr></thead><tbody><tr><td>记录</td></tr></tbody></table>`
	input := PostInput{AuthorID: 1, Title: "结构化正文", Slug: "structured", ContentSource: source}
	post, err := repo.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	revisions, _ := repo.ListRevisions(t.Context(), post.ID, 10)
	if len(revisions) != 1 {
		t.Fatal("missing revision")
	}
	snapshot := post
	snapshot.ContentSource += `<p>尚未正式保存</p>`
	snapshot.ContentHTML = sanitizeContent(snapshot.ContentSource)
	_, err = repo.SaveAutosave(t.Context(), Autosave{PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	repo, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.GetAutosave(t.Context(), post.ID, "user:1")
	if err != nil || recovered.Snapshot.ContentSource != snapshot.ContentSource || recovered.Snapshot.ContentHTML != snapshot.ContentHTML {
		t.Fatal("autosave changed", err)
	}
	loaded, err := repo.GetByID(t.Context(), post.ID)
	if err != nil || loaded.ContentSource != source || !strings.Contains(loaded.ContentHTML, `data-callout="warning"`) || !strings.Contains(loaded.ContentHTML, `class="language-go"`) {
		t.Fatal("structured content changed", loaded.ContentHTML, err)
	}
	input.ContentSource = "<p>替换</p>"
	updated, err := repo.Update(t.Context(), post.ID, Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := repo.RestoreRevision(t.Context(), post.ID, revisions[0].ID, Mutation{ExpectedVersion: updated.Version})
	if err != nil || restored.ContentSource != source || restored.ContentHTML != loaded.ContentHTML {
		t.Fatal("revision lost block content", err)
	}
}
