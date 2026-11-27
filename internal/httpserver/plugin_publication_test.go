package httpserver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/plugin"
)

type pluginPublicationRepository struct {
	content.Repository
	post content.Post
}

func (r pluginPublicationRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	if err := ctx.Err(); err != nil {
		return content.Post{}, err
	}
	if id != r.post.ID {
		return content.Post{}, content.ErrNotFound
	}
	return r.post, nil
}

func TestPluginContentVisibilityMatchesPublicationTime(t *testing.T) {
	root := t.TempDir()
	manager := plugin.NewManager(root)
	installDirectorySample(t, manager, nil)
	manifest, configuration, _, err := manager.Configuration("content-age")
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.WritePrivate(filepath.Join(root, "content-age", ".official.json"), plugin.Release{Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	configuration.Grants = manifest.Capabilities
	if err := manager.Configure("content-age", configuration); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetEnabled("content-age", true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, tc := range []struct {
		name, kind, status string
		at                 time.Time
		visible            bool
	}{
		{"published", content.TypePost, content.StatusPublish, now.Add(-time.Hour), true},
		{"scheduled-due", content.TypePost, content.StatusFuture, now.Add(-time.Hour), true},
		{"scheduled-not-due", content.TypePost, content.StatusFuture, now.Add(time.Hour), false},
		{"publish-before-time", content.TypePost, content.StatusPublish, now.Add(time.Hour), false},
		{"missing-publication-time", content.TypePost, content.StatusPublish, time.Time{}, false},
		{"private", content.TypePost, content.StatusPrivate, now.Add(-time.Hour), false},
		{"draft", content.TypePost, content.StatusDraft, now.Add(-time.Hour), false},
		{"page", content.TypePage, content.StatusPublish, now.Add(-time.Hour), false},
		{"flash", content.TypeFlash, content.StatusPublish, now.Add(-time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			post := content.Post{ID: 1, Type: tc.kind, Status: tc.status, Title: "定时发布文章", Slug: "scheduled-plugin", Excerpt: "公开摘要", PublishedAt: tc.at, UpdatedAt: now.Add(-200 * 24 * time.Hour)}
			server := &Server{plugins: manager, repository: pluginPublicationRepository{post: post}}
			value, err := server.pluginReadContent(t.Context(), post.ID)
			if tc.visible {
				if err != nil || value == nil {
					t.Errorf("public article unavailable to plugin: value=%v err=%v", value, err)
				} else {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					var summary struct {
						ID int64 `json:"id"`
					}
					if err := json.Unmarshal(raw, &summary); err != nil || summary.ID != post.ID {
						t.Errorf("wrong public summary: %s %v", raw, err)
					}
				}
			} else if err == nil || value != nil {
				t.Errorf("nonpublic content reached plugin: value=%v err=%v", value, err)
			}
			fragments := server.pluginRender(t.Context(), post)
			if tc.visible && (len(fragments) != 1 || fragments[0].Title != "留意内容时效") {
				t.Errorf("public article lost its WASM fragment: %+v", fragments)
			} else if !tc.visible && len(fragments) != 0 {
				t.Errorf("nonpublic article invoked render hook: %+v", fragments)
			}
		})
	}
}
