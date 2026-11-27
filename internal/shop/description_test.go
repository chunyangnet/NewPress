package shop

import (
	"errors"
	"strings"
	"testing"
)

func TestDescriptionSourcePersistsWithoutLeakingIntoCatalog(t *testing.T) {
	p := item("details", "virtual", "direct")
	p.Description = "纯文本介绍"
	p.DescriptionSource = `<h2>使用说明</h2><p>支持内容块</p>`
	p.DescriptionAuthorID = 27
	f := newFixture(t, p)
	b, err := NewFileBackend(f.path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := (&Service{Backend: b}).Catalog(t.Context())
	if err != nil || c.Products[0].DescriptionSource != p.DescriptionSource || c.Products[0].DescriptionAuthorID != 27 {
		t.Fatal(c, err)
	}
	public := PublicCatalog(c)
	if public.Products[0].DescriptionSource != "" || public.Products[0].DescriptionAuthorID != 0 || c.Products[0].DescriptionSource == "" {
		t.Fatal("public catalog leaked source or mutated storage")
	}
	for _, source := range []string{strings.Repeat("x", MaxDescriptionBytes+1), "bad\x00source", string([]byte{0xff})} {
		c.Products[0].DescriptionSource = source
		if _, err := f.s.SaveCatalog(t.Context(), c); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid source accepted", err)
		}
	}
	c.Products[0].DescriptionSource = ""
	if _, err := f.s.SaveCatalog(t.Context(), c); err != nil {
		t.Fatal("plain-text description rejected", err)
	}
}

func TestDescriptionEditsDoNotInterruptExistingGroups(t *testing.T) {
	p := item("group-details", "virtual", "group")
	p.Group = &GroupPlan{Size: 2, Price: 8000, Hours: 24}
	p.Description = "原说明"
	p.DescriptionSource = "<p>商品介绍</p>"
	p.DescriptionAuthorID = 1
	f := newFixture(t, p)
	first := f.create(t, p.Key, 1, nil)
	if first.Snapshot.DescriptionSource != "" || first.Snapshot.DescriptionAuthorID != 0 {
		t.Fatal("editor source duplicated in order snapshot")
	}
	f.pay(t, first, 0)
	c, err := f.s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c.Products[0].Description = ""
	c.Products[0].DescriptionSource = "<h2>更新后的介绍</h2>"
	if _, err = f.s.SaveCatalog(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	second := f.create(t, p.Key, 2, func(in *Purchase) { in.GroupID = first.GroupID })
	if second.GroupID != first.GroupID {
		t.Fatal("description changed group terms")
	}
}
