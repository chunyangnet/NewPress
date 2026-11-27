package wechat

import (
	"path/filepath"
	"reflect"
	"testing"

	"newpress.local/newpress/internal/content"
)

func metadataArticle(t *testing.T) Collected {
	t.Helper()
	a, err := ParseArticle("https://mp.weixin.qq.com/s/metadata", `<meta property="og:title" content="元数据核对"><meta name="author" content="原作者"><meta name="description" content="原摘要"><meta property="og:image" content="https://mmbiz.qpic.cn/original-cover"><div id="js_content"><p>不变的正文</p><img data-src="https://mmbiz.qpic.cn/body-image" alt="正文图片"></div>`)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCollectMetadataChangesPreserveImportedPost(t *testing.T) {
	for _, kind := range []string{"memory", "file"} {
		t.Run(kind, func(t *testing.T) {
			a := metadataArticle(t)
			var repo content.Repository = content.NewMemoryRepository(false)
			path := filepath.Join(t.TempDir(), "content.json")
			var err error
			if kind == "file" {
				repo, err = content.NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			batch := Batch([]Collected{a}, map[string]string{a.Cover: "/media/cover.jpg", a.Images[0]: "/media/body.jpg"}, 1, "本站作者", "owner")
			plan, err := repo.PreviewImport(t.Context(), batch)
			if err != nil {
				t.Fatal(err)
			}
			created, err := repo.ApplyImport(t.Context(), batch, plan.Revision)
			if err != nil {
				t.Fatal(err)
			}
			id := created.Items[0].PostID
			post, err := repo.GetByID(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			post, err = repo.Update(t.Context(), id, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Slug: post.Slug, Title: "人工标题", Excerpt: "人工摘要", CoverURL: "/media/manual.jpg", ContentSource: "<p>人工正文</p>", AuthorID: 1})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				repo, err = content.NewFileRepository(path, false)
				if err != nil {
					t.Fatal(err)
				}
			}
			post, err = repo.GetByID(t.Context(), id)
			if err != nil || post.Title != "人工标题" || post.Excerpt != "人工摘要" || post.CoverURL != "/media/manual.jpg" || post.ContentSource != "<p>人工正文</p>" || post.Version != 2 {
				t.Fatal("manual edit did not survive reopen", post, err)
			}
			for _, field := range []string{"unchanged", "excerpt", "cover", "remove-excerpt", "remove-cover", "presentation"} {
				t.Run(field, func(t *testing.T) {
					changed := a
					want := "changed"
					switch field {
					case "excerpt":
						changed.Excerpt = "只有摘要更新"
					case "cover":
						changed.Cover = "https://mmbiz.qpic.cn/new-cover"
					case "remove-excerpt":
						changed.Excerpt = ""
					case "remove-cover":
						changed.Cover = ""
					case "presentation":
						changed.Preview = "显示文字变化"
						changed.Warnings = []string{"新提示"}
						want = "skip"
					default:
						want = "skip"
					}
					fresh := Batch([]Collected{changed}, nil, 2, "另一位管理员", "another")
					preview, err := repo.PreviewImport(t.Context(), fresh)
					if err != nil || preview.New != 0 || preview.Existing != 1 || preview.Items[0].Action != want {
						t.Fatalf("%s: expected %s, got %#v, %v", field, want, preview, err)
					}
					result, err := repo.ApplyImport(t.Context(), fresh, preview.Revision)
					if err != nil || result.Items[0].Action != want {
						t.Fatal(result, err)
					}
					current, err := repo.GetByID(t.Context(), id)
					if err != nil || !reflect.DeepEqual(current, post) {
						t.Fatal("recollection modified local post", current, err)
					}
				})
			}
		})
	}
}

func TestCollectRequiresMetadataFingerprint(t *testing.T) {
	a := metadataArticle(t)
	file := filepath.Join(t.TempDir(), "content.json")
	repo, err := content.NewFileRepository(file, false)
	if err != nil {
		t.Fatal(err)
	}
	current := Batch([]Collected{a}, nil, 1, "本站作者", "owner")
	incomplete := current
	incomplete.Items = append([]content.ImportEntry(nil), current.Items...)
	incomplete.Items[0].MetadataDigest = ""
	if _, err = repo.PreviewImport(t.Context(), incomplete); len(content.FieldErrors(err)) == 0 {
		t.Fatal("missing fingerprint accepted in preview", err)
	}
	if _, err = repo.ApplyImport(t.Context(), incomplete, "invalid"); len(content.FieldErrors(err)) == 0 {
		t.Fatal("missing fingerprint accepted on apply", err)
	}
	plan, err := repo.PreviewImport(t.Context(), current)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.ApplyImport(t.Context(), current, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	repo, err = content.NewFileRepository(file, false)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.GetByID(t.Context(), created.Items[0].PostID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = repo.PreviewImport(t.Context(), current)
	if err != nil || plan.New != 0 || plan.Existing != 1 || plan.Items[0].Action != "skip" {
		t.Fatal("stored fingerprint did not survive reopen", plan, err)
	}
	if _, err = repo.ApplyImport(t.Context(), current, plan.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetByID(t.Context(), before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("recollection modified post", after, err)
	}
}
