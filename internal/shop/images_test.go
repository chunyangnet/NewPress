package shop

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestProductImagesPersistAndRemainPublic(t *testing.T) {
	p := item("gallery", "physical", "direct")
	p.Image = "/media/cover.webp"
	p.Images = []string{"/media/side.webp", "https://example.test/detail.webp"}
	p.Description = "详细说明\n包含规格与使用方式。"
	p.DetailImages = []string{"/media/description.webp"}
	p.Variants = []Variant{{Key: "white", Title: "白色", Image: "/media/white.webp", Price: 10000, Stock: 5}}
	f := newFixture(t, p)
	f.restart(t)
	c, err := f.s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := PublicCatalog(c).Products[0]
	if !reflect.DeepEqual(got.Images, p.Images) || !reflect.DeepEqual(got.DetailImages, p.DetailImages) || got.Description != p.Description || got.Variants[0].Image != p.Variants[0].Image {
		t.Fatalf("product display data lost: %+v", got)
	}
	if got.Delivery != "" {
		t.Fatal("private delivery must stay private")
	}
}

func TestProductImageValidation(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "data:image/png;base64,AA", "http://example.test/image.png", "//example.test/image.png", `/\example.test/image.png`, "https://user:pass@example.test/image.png", "https://example.test/\nimage.png"} {
		for _, field := range []string{"cover", "gallery", "detail", "variant"} {
			t.Run(field+"/"+bad, func(t *testing.T) {
				p := item("gallery", "physical", "direct")
				switch field {
				case "cover":
					p.Image = bad
				case "gallery":
					p.Images = []string{bad}
				case "detail":
					p.DetailImages = []string{bad}
				case "variant":
					p.Variants = []Variant{{Key: "one", Title: "规格", Price: 100, Stock: 1, Image: bad}}
				}
				c := InitialState().Catalog
				c.Products = []Product{p}
				if ValidateCatalog(c) == nil {
					t.Fatal("invalid image URL accepted")
				}
			})
		}
	}
	c := InitialState().Catalog
	p := item("gallery", "physical", "direct")
	p.Image = "/cover.webp"
	for i := range 11 {
		p.Images = append(p.Images, fmt.Sprintf("/image-%d.webp", i))
	}
	c.Products = []Product{p}
	if err := ValidateCatalog(c); err != nil {
		t.Fatal("12 images including cover should be accepted", err)
	}
	c.Products[0].Images = append(c.Products[0].Images, "/image-12.webp")
	if ValidateCatalog(c) == nil {
		t.Fatal("gallery exceeding 12 images accepted")
	}
	c.Products[0] = p
	c.Products[0].Description = strings.Repeat("字", 16001)
	if ValidateCatalog(c) == nil {
		t.Fatal("oversized product description accepted")
	}
}
