package cdn

import (
	"slices"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
)

func TestContentEffectOutboxQueuesPublicPurge(t *testing.T) {
	for _, status := range []string{content.StatusDraft, content.StatusPrivate, content.StatusPublish} {
		t.Run(status, func(t *testing.T) {
			s, _, cfg := testService(t)
			cfg.AutoContent = true
			if _, err := s.Save(cfg, nil); err != nil {
				t.Fatal(err)
			}
			repo := content.NewMemoryRepository(false)
			post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: status, Slug: "plugin-changed", Title: "Plugin changed", ContentSource: "<p>Text</p>", AuthorID: 1})
			if err != nil {
				t.Fatal(err)
			}
			event := activity.New(activity.ContentUpdated, post.ID, post.ID, post.AuthorID, post.Version, post.Status, time.Now().UTC())
			for range 2 {
				if err = s.Observe(t.Context(), repo, event); err != nil {
					t.Fatal(err)
				}
			}
			state, err := s.Status()
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if status == content.StatusPublish {
				want = 1
			}
			if len(state.Jobs) != want {
				t.Fatal("public visibility or queued coalescing", len(state.Jobs), want)
			}
			if want == 1 && !slices.Contains(state.Jobs[0].Targets, "https://www.example.com"+content.PublicPath(post)) {
				t.Fatal("updated path missing", state.Jobs[0].Targets)
			}
		})
	}
}
