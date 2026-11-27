package backup

import (
	"path/filepath"
	"reflect"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

func TestBackupRestoresWeChatMetadataAndLegacyReview(t *testing.T) {
	fixture := fixtureSite(t, false)
	posts, err := content.NewFileRepository(filepath.Join(fixture.cfg.DataDir, "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	articles := []wechat.Collected{}
	for _, key := range []string{"modern", "legacy"} {
		a, err := wechat.ParseArticle("https://mp.weixin.qq.com/s/"+key, `<meta property="og:title" content="备份核对"><meta name="description" content="原摘要"><meta property="og:image" content="https://mmbiz.qpic.cn/source-cover"><div id="js_content"><p>原正文</p></div>`)
		if err != nil {
			t.Fatal(err)
		}
		articles = append(articles, a)
	}
	batch := wechat.Batch(articles, nil, 0, "NewPress", "backup")
	plan, err := posts.PreviewImport(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	created, err := posts.ApplyImport(t.Context(), batch, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range created.Items {
		p, err := posts.GetByID(t.Context(), row.PostID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = posts.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, content.PostInput{Type: p.Type, Status: p.Status, Slug: p.Slug, Title: "本站人工标题", Excerpt: "本站人工摘要", ContentSource: "<p>本站人工正文</p>"}); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(fixture.parent, "metadata.zip")
	if _, err = Create(t.Context(), fixture.cfg, archive); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archive, filepath.Join(fixture.parent, "restored"))
	if err != nil || !restored.ApplicationValidated {
		t.Fatal(restored, err)
	}
	posts, err = content.NewFileRepository(filepath.Join(restored.Environment["NEWPRESS_DATA_DIR"], "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	fresh := wechat.Batch(articles, nil, 0, "NewPress", "backup")
	plan, err = posts.PreviewImport(t.Context(), fresh)
	if err != nil || plan.Items[0].Action != "skip" || plan.Items[1].Action != "skip" {
		t.Fatal("restore lost comparison history", plan, err)
	}
	before := []content.Post{}
	for _, row := range created.Items {
		p, err := posts.GetByID(t.Context(), row.PostID)
		if err != nil || p.Title != "本站人工标题" || p.Excerpt != "本站人工摘要" || p.Version != 2 {
			t.Fatal(p, err)
		}
		before = append(before, p)
	}
	articles[0].Excerpt = "只修改来源摘要"
	articles[1].Cover = "https://mmbiz.qpic.cn/other-cover"
	fresh = wechat.Batch(articles, nil, 0, "NewPress", "backup")
	plan, err = posts.PreviewImport(t.Context(), fresh)
	if err != nil || plan.Items[0].Action != "changed" || plan.Items[1].Action != "changed" || plan.New != 0 || plan.Existing != 2 {
		t.Fatal(plan, err)
	}
	if _, err = posts.ApplyImport(t.Context(), fresh, plan.Revision); err != nil {
		t.Fatal(err)
	}
	for _, p := range before {
		after, err := posts.GetByID(t.Context(), p.ID)
		if err != nil || !reflect.DeepEqual(p, after) {
			t.Fatal("replay changed restored post", after, err)
		}
	}
}
