package mediausage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
)

func newUsageTestService(t *testing.T, posts content.Repository) (*Service, media.Item) {
	t.Helper()
	library, err := media.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item, err := library.Upload(t.Context(), strings.NewReader("%PDF-1.7\n%%EOF\n"), media.UploadInput{Filename: "asset.pdf"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := site.NewManager("", site.DefaultSettings("Test", "Test"))
	if err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(filepath.Join("..", "..", "themes"), "chunnel", theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	menus, err := navigation.NewFileStore("", false)
	if err != nil {
		t.Fatal(err)
	}
	return New(library, posts, menus, settings, themes, "https://example.test"), item
}

func TestSeriesCoverReferencesAreProtectedAndRedacted(t *testing.T) {
	s, item := newUsageTestService(t, content.NewMemoryRepository(false))
	input := content.SeriesInput{Kind: content.SeriesKindTopic, Status: content.StatusDraft, Title: "未公开专题", Slug: "private-topic", CoverURL: item.PublicURL, PostIDs: []int64{}}
	series, err := s.Content().SaveSeries(t.Context(), 0, content.Mutation{}, input)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := s.Report(t.Context(), item, Access{ContentRead: true}, 0)
	if err != nil || hidden.Hidden != 1 || len(hidden.Items) != 0 {
		t.Fatal(hidden, err)
	}
	visible, err := s.Report(t.Context(), item, Access{Taxonomy: true}, 0)
	if err != nil || len(visible.Items) != 1 || visible.Items[0].Source != content.MediaSourceSeries {
		t.Fatal(visible, err)
	}
	if err = s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	if err = s.Content().DeleteSeries(t.Context(), series.ID, content.Mutation{ExpectedVersion: series.Version}); err != nil {
		t.Fatal(err)
	}
	if err = s.Trash(t.Context(), item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Content().SaveSeries(t.Context(), 0, content.Mutation{}, input); len(content.FieldErrors(err)) == 0 {
		t.Fatal("trashed cover reused", err)
	}
}

func TestReferenceReportPrivacyHistoryPaginationAndDeletion(t *testing.T) {
	s, item := newUsageTestService(t, content.NewMemoryRepository(false))
	posts := s.Content()
	input := content.PostInput{AuthorID: 7, Title: "Private page title", Type: content.TypePage, Slug: "private", Status: content.StatusPrivate, CoverURL: item.PublicURL, ContentSource: `<a href="` + item.PublicURL + `">attachment</a>`}
	post, err := posts.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.CoverURL, input.ContentSource = "", ""
	post, err = posts.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"user:7", "user:8"} {
		snapshot := post
		snapshot.Title, snapshot.CoverURL = "Secret autosave "+owner, item.PublicURL
		if _, err := posts.SaveAutosave(t.Context(), content.Autosave{PostID: post.ID, OwnerKey: owner, SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	access := Access{ContentRead: true, ContentAuthorID: 7, AutosaveOwner: "user:7"}
	report, err := s.Report(t.Context(), item, access, 0)
	if err != nil || report.Total != 3 || report.Hidden != 1 || report.Visible != 2 || len(report.Items) != 2 || report.Items[0].Source != content.MediaSourceRevision || report.Items[1].Title != "Secret autosave user:7" {
		t.Fatalf("report = %#v, %v", report, err)
	}
	if err := s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatalf("historical delete = %v", err)
	}
	other, err := s.Report(t.Context(), item, Access{ContentRead: true, ContentAuthorID: 99}, 0)
	if err != nil || len(other.Items) != 0 || other.Hidden != 3 {
		t.Fatalf("other author report = %#v %v", other, err)
	}
	if err := posts.DeleteAutosave(t.Context(), post.ID, "user:7"); err != nil {
		t.Fatal(err)
	}
	if err := posts.DeleteAutosave(t.Context(), post.ID, "user:8"); err != nil {
		t.Fatal(err)
	}
	if err := s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatalf("revision alone did not block delete: %v", err)
	}
	for i := 0; i < 25; i++ {
		if _, err := posts.Create(t.Context(), content.PostInput{Title: fmt.Sprintf("Reference %d", i), Slug: fmt.Sprintf("ref-%d", i), AuthorID: 7, Status: content.StatusTrash, CoverURL: item.PublicURL}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.Report(t.Context(), item, access, 0)
	if err != nil || first.Total != 51 || len(first.Items) != PageSize || !first.HasMore {
		t.Fatalf("first page = %#v %v", first, err)
	}
	last, err := s.Report(t.Context(), item, access, 40)
	if err != nil || last.Total != 51 || len(last.Items) != 11 || last.HasMore {
		t.Fatalf("last page = %#v %v", last, err)
	}
}

func TestSettingsNavigationAndMissingMediaWrites(t *testing.T) {
	s, item := newUsageTestService(t, content.NewMemoryRepository(false))
	settings := s.settings.Snapshot()
	candidate := settings.Settings
	candidate.Identity.IconURL = item.PublicURL
	if _, fields, err := s.settings.Save(settings.Revision, candidate); err != nil || len(fields) != 0 {
		t.Fatalf("save = %#v %v", fields, err)
	}
	if err := s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	report, err := s.Report(t.Context(), item, Access{Settings: true}, 0)
	if err != nil || report.Total != 1 || report.Items[0].Title != "站点图标" {
		t.Fatalf("site reference = %#v %v", report, err)
	}
	if _, err := s.settings.Reset(s.settings.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	values := s.themes.SettingsSnapshot().Values
	values["footer_note"] = item.PublicURL
	if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
		t.Fatalf("theme = %#v %v", fields, err)
	}
	if err := s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	if _, err := s.themes.ResetSettings(); err != nil {
		t.Fatal(err)
	}
	menus := s.Navigation()
	menu, err := menus.Create(t.Context(), navigation.MenuInput{Name: "Downloads", Slug: "downloads"}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	entries := []navigation.ItemInput{{ID: -1, Type: navigation.TypeCustom, Label: "File", URL: item.PublicURL}}
	menu, err = menus.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrInUse) {
		t.Fatal(err)
	}
	if _, err := menus.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: menu.Version}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Trash(t.Context(), item.ID, item.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.library.Open(item.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatal(err)
	}
	posts := s.Content()
	if _, err := posts.Create(t.Context(), content.PostInput{Title: "Invalid", Slug: "invalid", CoverURL: item.PublicURL}); content.FieldErrors(err)["cover_url"] == "" {
		t.Fatalf("stale cover save = %v", err)
	}
	if _, err := posts.Create(t.Context(), content.PostInput{Title: "Relative", Type: " page ", Slug: "nested/raw", ContentSource: `<img src="..` + item.PublicURL + `">`}); content.FieldErrors(err)["content_source"] == "" {
		t.Fatalf("unnormalized slug bypassed stale relative URL validation: %v", err)
	}
	post, err := posts.Create(t.Context(), content.PostInput{Title: "Valid", Slug: "valid"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := posts.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Title: "Stale body", Slug: "valid", ContentSource: `<img src="` + item.PublicURL + `">`}); content.FieldErrors(err)["content_source"] == "" {
		t.Fatalf("stale body = %v", err)
	}
	snapshot := post
	snapshot.CoverURL = item.PublicURL
	if _, err := posts.SaveAutosave(t.Context(), content.Autosave{PostID: post.ID, OwnerKey: "user:7", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour)}); content.FieldErrors(err)["cover_url"] == "" {
		t.Fatalf("stale autosave = %v", err)
	}
	if _, fields, err := s.settings.Save(s.settings.Snapshot().Revision, candidate); err != nil || fields["site_icon_url"] == "" {
		t.Fatalf("stale settings = %#v %v", fields, err)
	}
	if _, fields, err := s.themes.SaveSettings(values); err != nil || fields["footer_note"] == "" {
		t.Fatalf("stale theme = %#v %v", fields, err)
	}
	current, _ := menus.Get(t.Context(), menu.ID)
	if _, err := menus.ReplaceItems(t.Context(), menu.ID, navigation.Mutation{ExpectedVersion: current.Version}, entries); navigation.FieldErrors(err)["items"] == "" {
		t.Fatalf("stale menu = %v", err)
	}
	registry, _ := menus.Snapshot(t.Context())
	if _, err := menus.ReplaceAll(t.Context(), navigation.RegistryMutation{ExpectedRevision: navigation.SnapshotRevision(registry)}, []navigation.RegistryMenuInput{{Menu: navigation.MenuInput{Name: "Downloads", Slug: "downloads"}, Items: entries}}); navigation.FieldErrors(err)["items"] == "" {
		t.Fatalf("stale registry = %v", err)
	}
}

type failingSnapshots struct{ content.Repository }

func (f failingSnapshots) WalkMediaSnapshots(context.Context, func(content.MediaSnapshot) error) error {
	return errors.New("storage offline")
}

func TestFailedOrCanceledReferenceScanPreservesObject(t *testing.T) {
	s, item := newUsageTestService(t, failingSnapshots{content.NewMemoryRepository(false)})
	if err := s.Trash(t.Context(), item.ID, item.Version); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := s.library.Get(item.ID); err != nil {
		t.Fatal(err)
	}
	s.content = content.NewMemoryRepository(false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Trash(ctx, item.ID, item.Version); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := s.library.Get(item.ID); err != nil {
		t.Fatal(err)
	}
}

type pausedCreate struct {
	content.Repository
	entered chan struct{}
	proceed chan struct{}
}

func (r pausedCreate) Create(ctx context.Context, input content.PostInput) (content.Post, error) {
	close(r.entered)
	<-r.proceed
	return r.Repository.Create(ctx, input)
}

type pausedScan struct {
	content.Repository
	entered chan struct{}
	proceed chan struct{}
}

func (r pausedScan) WalkMediaSnapshots(ctx context.Context, visit func(content.MediaSnapshot) error) error {
	close(r.entered)
	<-r.proceed
	return r.Repository.WalkMediaSnapshots(ctx, visit)
}

func TestConcurrentSaveAndDeleteBothOrderings(t *testing.T) {
	for _, saveFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(saveFirst), func(t *testing.T) {
			s, item := newUsageTestService(t, content.NewMemoryRepository(false))
			input := content.PostInput{Title: "Concurrent", Slug: "concurrent", CoverURL: item.PublicURL}
			if !saveFirst {
				paused := pausedScan{Repository: s.content, entered: make(chan struct{}), proceed: make(chan struct{})}
				s.content = paused
				deleted := make(chan error, 1)
				go func() { deleted <- s.Trash(t.Context(), item.ID, item.Version) }()
				<-paused.entered
				saved := make(chan error, 1)
				go func() { _, err := s.Content().Create(t.Context(), input); saved <- err }()
				select {
				case err := <-saved:
					close(paused.proceed)
					t.Fatalf("save crossed pending deletion: %v", err)
				case <-time.After(25 * time.Millisecond):
				}
				close(paused.proceed)
				if err := <-deleted; err != nil {
					t.Fatal(err)
				}
				if err := <-saved; content.FieldErrors(err)["cover_url"] == "" {
					t.Fatalf("saved after deletion: %v", err)
				}
				return
			}
			paused := pausedCreate{Repository: s.content, entered: make(chan struct{}), proceed: make(chan struct{})}
			s.content = paused
			saved := make(chan error, 1)
			go func() { _, err := s.Content().Create(t.Context(), input); saved <- err }()
			<-paused.entered
			deleted := make(chan error, 1)
			go func() { deleted <- s.Trash(t.Context(), item.ID, item.Version) }()
			select {
			case err := <-deleted:
				close(paused.proceed)
				t.Fatalf("delete crossed pending save: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			close(paused.proceed)
			if err := <-saved; err != nil {
				t.Fatal(err)
			}
			if err := <-deleted; !errors.Is(err, ErrInUse) {
				t.Fatalf("delete = %v", err)
			}
		})
	}
}
