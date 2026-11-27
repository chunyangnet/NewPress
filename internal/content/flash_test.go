package content

import (
	"context"
	"testing"
	"time"
)

func TestFlashPersistenceAndQueryIsolation(t *testing.T) {
	path := t.TempDir() + "/content.json"
	repo, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	in := PostInput{Type: TypeFlash, Title: "测试快报", Slug: "flash-test", ContentSource: "快报正文", Status: StatusPublish, Flash: &FlashFields{SourceName: "原始来源", SourceURL: "https://example.test/news", Important: true}}
	p, err := repo.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !PublicAt(p, time.Now()) || PublicPath(p) != "/flash/flash-test/" {
		t.Fatalf("visibility/path: %#v", p)
	}
	plain, err := repo.SearchPublished(context.Background(), PublishedQuery{})
	if err != nil || plain.Total != 0 {
		t.Fatalf("flash leaked into article list: %#v %v", plain, err)
	}
	repo, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetPublishedFlashBySlug(context.Background(), p.Slug)
	if err != nil || loaded.ID != p.ID {
		t.Fatalf("exact flash lookup: %#v %v", loaded, err)
	}
	if _, err := repo.GetPublishedBySlug(context.Background(), p.Slug); err != ErrNotFound {
		t.Fatalf("article lookup accepted flash: %v", err)
	}
	result, err := repo.SearchPublished(context.Background(), PublishedQuery{Type: TypeFlash, Important: true})
	if err != nil || result.Total != 1 || FlashMetadata(result.Items[0]).SourceName != "原始来源" {
		t.Fatalf("reload: %#v %v", result, err)
	}
}

func TestFlashValidation(t *testing.T) {
	for _, source := range []string{"javascript:alert(1)", "//example.test", "https://user:pass@example.test"} {
		_, fields := NormalizePostInput(PostInput{Type: TypeFlash, Title: "快报", ContentSource: "正文", Flash: &FlashFields{SourceURL: source}}, time.Now())
		if fields["source_url"] == "" {
			t.Fatalf("accepted %q", source)
		}
	}
	_, fields := NormalizePostInput(PostInput{Type: TypeFlash, Title: "快报"}, time.Now())
	if fields["content_source"] == "" {
		t.Fatal("accepted empty flash")
	}
}
