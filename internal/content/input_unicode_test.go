package content

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func unicodeWriteRepository(t *testing.T, mode string) (Repository, func() []byte, func() Repository) {
	t.Helper()
	if mode == "memory" {
		r := NewMemoryRepository(false)
		return r, func() []byte {
			body, err := encodeContentDocument(r)
			if err != nil {
				t.Fatal(err)
			}
			return body
		}, func() Repository { return r }
	}
	filename := filepath.Join(t.TempDir(), "content.json")
	r, err := NewFileRepository(filename, false)
	if err != nil {
		t.Fatal(err)
	}
	return r, func() []byte {
			body, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			return body
		}, func() Repository {
			reopened, err := NewFileRepository(filename, false)
			if err != nil {
				t.Fatal(err)
			}
			return reopened
		}
}

func TestPostWritesRejectMalformedUTF8BeforeMutation(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, operation := range []string{"create", "update"} {
			for _, encoding := range []struct{ name, bad string }{
				{"invalid byte", "\xff"}, {"truncated character", "\xe4\xb8"}, {"encoded surrogate", "\xed\xa0\x80"},
			} {
				for _, field := range []string{"title", "excerpt", "content_source", "cover_url", "category", "tags", "author_name", "actor", "ai_topics", "slug"} {
					t.Run(mode+"/"+operation+"/"+encoding.name+"/"+field, func(t *testing.T) {
						r, snapshot, reopen := unicodeWriteRepository(t, mode)
						input := PostInput{Title: "原文", Slug: "original", ContentSource: "<p>正文</p>"}
						var original Post
						if operation == "update" {
							var err error
							original, err = r.Create(t.Context(), input)
							if err != nil {
								t.Fatal(err)
							}
						}
						before := snapshot()
						value := "text-" + encoding.bad
						switch field {
						case "title":
							input.Title = value
						case "excerpt":
							input.Excerpt = value
						case "content_source":
							input.ContentSource = "<p>" + value + "</p>"
						case "cover_url":
							input.CoverURL = "/media/" + value + ".png"
						case "category":
							input.Category = value
						case "tags":
							input.TagNames = []string{"正常, " + value}
						case "author_name":
							input.AuthorName = value
						case "actor":
							input.Actor = value
						case "ai_topics":
							input.AITopics = value
						case "slug":
							input.Slug = value
						}
						var got Post
						var err error
						if operation == "create" {
							got, err = r.Create(t.Context(), input)
						} else {
							got, err = r.Update(t.Context(), original.ID, Mutation{ExpectedVersion: original.Version}, input)
						}
						if FieldErrors(err)[field] == "" {
							t.Errorf("malformed %s did not return a field error: %v", field, err)
						}
						if !bytes.Equal(before, snapshot()) {
							t.Error("rejected text changed content, terms or revisions")
						}
						if err == nil && mode == "file" {
							stored, loadErr := reopen().GetByID(t.Context(), got.ID)
							if loadErr != nil {
								t.Errorf("accepted input made storage unreadable: %v", loadErr)
							} else if got.Title != stored.Title || got.ContentSource != stored.ContentSource || got.Excerpt != stored.Excerpt || got.AuthorName != stored.AuthorName || got.Category != stored.Category || got.CoverURL != stored.CoverURL {
								t.Errorf("accepted text changes after reopen; title bytes %x -> %x", got.Title, stored.Title)
							}
						}
					})
				}
			}
		}
	}
}

func TestTaxonomyAndSeriesWritesRejectMalformedUTF8(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, operation := range []string{"create", "update"} {
			for _, field := range []string{"name", "description", "slug"} {
				t.Run(mode+"/term/"+operation+"/"+field, func(t *testing.T) {
					r, snapshot, _ := unicodeWriteRepository(t, mode)
					input := TermInput{Taxonomy: TaxonomyCategory, Name: "原分类", Slug: "original-term"}
					var original Term
					if operation == "update" {
						var err error
						original, err = r.CreateTerm(t.Context(), input)
						if err != nil {
							t.Fatal(err)
						}
					}
					before := snapshot()
					switch field {
					case "name":
						input.Name = "bad-\xff"
					case "description":
						input.Description = "bad-\xff"
					case "slug":
						input.Slug = "bad-\xff"
					}
					var err error
					if operation == "create" {
						_, err = r.CreateTerm(t.Context(), input)
					} else {
						_, err = r.UpdateTerm(t.Context(), original.ID, TermMutation{ExpectedVersion: original.Version}, input)
					}
					if FieldErrors(err)[field] == "" || !bytes.Equal(before, snapshot()) {
						t.Errorf("malformed term %s accepted or changed storage: %v", field, err)
					}
				})
			}
			for _, field := range []string{"title", "description", "slug", "cover_url"} {
				t.Run(mode+"/series/"+operation+"/"+field, func(t *testing.T) {
					r, snapshot, _ := unicodeWriteRepository(t, mode)
					input := SeriesInput{Kind: SeriesKindSequence, Status: StatusDraft, Title: "原系列", Slug: "original-series"}
					var original Series
					if operation == "update" {
						var err error
						original, err = r.SaveSeries(t.Context(), 0, Mutation{}, input)
						if err != nil {
							t.Fatal(err)
						}
					}
					before := snapshot()
					switch field {
					case "title":
						input.Title = "bad-\xff"
					case "description":
						input.Description = "bad-\xff"
					case "slug":
						input.Slug = "bad-\xff"
					case "cover_url":
						input.CoverURL = "/media/bad-\xff.png"
					}
					_, err := r.SaveSeries(t.Context(), original.ID, Mutation{ExpectedVersion: original.Version}, input)
					if FieldErrors(err)[field] == "" || !bytes.Equal(before, snapshot()) {
						t.Errorf("malformed series %s accepted or changed storage: %v", field, err)
					}
				})
			}
		}
	}
}

func TestFlashRejectsUTF8SequencesSplitAcrossFields(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			r, snapshot, _ := unicodeWriteRepository(t, mode)
			before := snapshot()
			_, err := r.Create(t.Context(), PostInput{Type: TypeFlash, Title: "快报", ContentSource: "正文", Flash: &FlashFields{SourceName: "\xc2", ReviewNote: "\xa9"}})
			fields := FieldErrors(err)
			if fields["source_name"] == "" || fields["review_note"] == "" {
				t.Errorf("two independently invalid fields formed one accepted UTF8 sequence: %v", err)
			}
			if !bytes.Equal(before, snapshot()) {
				t.Error("malformed flash metadata changed storage")
			}
		})
	}
}

func TestFlashUnicodeReportsTheActualField(t *testing.T) {
	for _, field := range []string{"source_name", "source_url", "review_note"} {
		t.Run(field, func(t *testing.T) {
			r, snapshot, _ := unicodeWriteRepository(t, "file")
			before := snapshot()
			flash := FlashFields{SourceName: "来源", SourceURL: "https://example.test/news", ReviewNote: "审核"}
			switch field {
			case "source_name":
				flash.SourceName += "\xff"
			case "source_url":
				flash.SourceURL += "\xff"
			case "review_note":
				flash.ReviewNote += "\xff"
			}
			_, err := r.Create(t.Context(), PostInput{Type: TypeFlash, Title: "快报", ContentSource: "正文", Flash: &flash})
			if fields := FieldErrors(err); len(fields) != 1 || fields[field] == "" {
				t.Errorf("encoding error did not identify the damaged field: %#v %v", fields, err)
			}
			if !bytes.Equal(before, snapshot()) {
				t.Error("malformed flash field changed storage")
			}
		})
	}
}

func TestEditorialUnicodePreservedAcrossStorage(t *testing.T) {
	for _, mode := range []string{"memory", "file"} {
		for _, text := range []string{"合法中文😀", "原文�", `字面\ud800`} {
			t.Run(mode+"/"+text, func(t *testing.T) {
				r, _, reopen := unicodeWriteRepository(t, mode)
				post, err := r.Create(t.Context(), PostInput{Title: text, Slug: "unicode", Excerpt: text, ContentSource: text, ContentHTML: "ignored-\xff", AuthorName: text, Actor: text, Category: text, TagNames: []string{text}, AITopics: text})
				if err != nil {
					t.Fatal(err)
				}
				term, err := r.CreateTerm(t.Context(), TermInput{Taxonomy: TaxonomyCategory, Slug: "unicode-term", Name: text, Description: text})
				if err != nil {
					t.Fatal(err)
				}
				series, err := r.SaveSeries(t.Context(), 0, Mutation{}, SeriesInput{Kind: SeriesKindTopic, Status: StatusPublish, Slug: "unicode-series", Title: text, Description: text, PostIDs: []int64{post.ID}})
				if err != nil {
					t.Fatal(err)
				}
				flash, err := r.Create(t.Context(), PostInput{Type: TypeFlash, Title: text, Slug: "unicode-flash", ContentSource: text, Flash: &FlashFields{SourceName: text, ReviewNote: text, SourceURL: "https://example.test/消息"}})
				if err != nil {
					t.Fatal(err)
				}
				loaded := reopen()
				got, err := loaded.GetByID(t.Context(), post.ID)
				if err != nil || got.Title != text || got.Excerpt != text || got.ContentSource != text || got.ContentHTML != post.ContentHTML || got.AuthorName != text || got.Category != text || len(got.Tags) != 1 || got.Tags[0].Name != text || got.Meta["ai_pro_max_topics"] != text {
					t.Errorf("valid Unicode changed after save/reopen: %#v %v", got, err)
				}
				gotTerm, err := loaded.GetTermByID(t.Context(), term.ID)
				if err != nil || gotTerm.Name != term.Name || gotTerm.Description != term.Description || gotTerm.Slug != term.Slug {
					t.Errorf("valid term changed: %#v %v", gotTerm, err)
				}
				gotSeries, err := loaded.GetSeries(t.Context(), series.ID)
				if err != nil || gotSeries.Title != series.Title || gotSeries.Description != series.Description || gotSeries.Slug != series.Slug {
					t.Errorf("valid series changed: %#v %v", gotSeries, err)
				}
				gotFlash, err := loaded.GetByID(t.Context(), flash.ID)
				if err != nil || FlashMetadata(gotFlash) != FlashMetadata(flash) || FlashMetadata(gotFlash).SourceName != text || FlashMetadata(gotFlash).ReviewNote != text {
					t.Errorf("valid flash metadata changed: %#v %v", gotFlash, err)
				}
				revisions, err := loaded.ListRevisions(t.Context(), post.ID, 10)
				if err != nil || len(revisions) != 1 || revisions[0].Actor != text {
					t.Errorf("valid revision actor changed: %#v %v", revisions, err)
				}
			})
		}
	}
}
