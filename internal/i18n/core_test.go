package i18n

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBundledLanguagePacksAreComplete(t *testing.T) {
	t.Parallel()
	starter, err := LoadDirectory(filepath.Join("..", "..", "themes", "starter", "locales"), DefaultLocale)
	if err != nil {
		t.Fatal(err)
	}
	for name, catalog := range map[string]*Catalog{"core": Core(), "starter": starter} {
		t.Run(name, func(t *testing.T) {
			if got := NewTranslator(DefaultLocale, catalog).Locales(); !slices.Equal(got, []string{"en", "ja", "zh-CN", "zh-Hant"}) {
				t.Fatalf("bundled locales = %v", got)
			}
			// Inspect each raw pack so fallback cannot hide a missing translation.
			baseline := catalog.packs[DefaultLocale]
			for locale, messages := range catalog.packs {
				if len(messages) != len(baseline) {
					t.Errorf("%s has %d keys, want %d", locale, len(messages), len(baseline))
				}
				for key, source := range baseline {
					translated, ok := messages[key]
					if !ok || strings.TrimSpace(translated) == "" {
						t.Errorf("%s lacks a translation for %s", locale, key)
						continue
					}
					want := placeholderPattern.FindAllString(source, -1)
					got := placeholderPattern.FindAllString(translated, -1)
					slices.Sort(want)
					slices.Sort(got)
					if !slices.Equal(got, want) {
						t.Errorf("%s/%s placeholders = %v, want %v", locale, key, got, want)
					}
				}
			}
		})
	}
}

func TestCoreTranslationsSupportedLocales(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		locale, home, comments string
	}{
		{"zh-CN", "首页", "评论（3）"},
		{"zh-Hant", "首頁", "留言（3）"},
		{"zh-TW", "首頁", "留言（3）"},
		{"zh-HK", "首頁", "留言（3）"},
		{"zh-MO", "首頁", "留言（3）"},
		{"zh-Hant-TW", "首頁", "留言（3）"},
		{"en", "Home", "Comments (3)"},
		{"en-US", "Home", "Comments (3)"},
		{"en-GB", "Home", "Comments (3)"},
		{"ja", "ホーム", "コメント（3件）"},
		{"ja-JP", "ホーム", "コメント（3件）"},
		{"de", "首页", "评论（3）"},
	} {
		t.Run(test.locale, func(t *testing.T) {
			translator := NewTranslator(test.locale, Core())
			if got, ok := translator.Lookup("navigation.home"); !ok || got != test.home {
				t.Errorf("home = %q, %v; want %q", got, ok, test.home)
			}
			if got, err := translator.Text("comments.title", map[string]any{"count": 3}); err != nil || got != test.comments {
				t.Errorf("comments = %q, %v; want %q", got, err, test.comments)
			}
		})
	}
}
