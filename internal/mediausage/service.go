// Package mediausage coordinates local media deletion with application writes
// that can create references. All content/navigation writes exposed by the
// application use its repositories; settings imports use the same guard.
package mediausage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
)

var (
	ErrInUse       = errors.New("media is referenced")
	ErrUnavailable = errors.New("media reference scan unavailable")
)

const (
	SourceSite       = "site_setting"
	SourceTheme      = "theme_setting"
	SourceNavigation = "navigation"
	SourceProfile    = "profile"
	SourceLink       = "link_logo"
	SourceShop       = "shop_image"
	SourceBadge      = "badge_image"
	PageSize         = 20
)

type Service struct {
	gate       sync.RWMutex
	library    *media.Manager
	content    content.Repository
	navigation navigation.Store
	settings   *site.Manager
	themes     *theme.Manager
	resolver   assetref.Resolver
	profiles   *auth.Manager
	features   func(context.Context, media.Item) ([]Reference, error)
}

// Access describes disclosure, never whether a reference blocks deletion.
// Autosave details remain owner-only even for an administrator.
type Access struct {
	ContentRead     bool
	SnippetsManage  bool
	ContentAuthorID int64
	AutosaveOwner   string
	Settings        bool
	Appearance      bool
	Navigation      bool
	Users           bool
	Taxonomy        bool
}

type Reference struct {
	Source      string   `json:"source"`
	Key         string   `json:"key,omitempty"`
	ID          int64    `json:"id,omitempty"`
	RevisionID  int64    `json:"revision_id,omitempty"`
	Version     int64    `json:"version,omitempty"`
	Title       string   `json:"title"`
	ContentType string   `json:"content_type,omitempty"`
	Status      string   `json:"status,omitempty"`
	Fields      []string `json:"fields"`
}

type Report struct {
	Items     []Reference    `json:"items"`
	Total     int            `json:"total"`
	Visible   int            `json:"visible"`
	Hidden    int            `json:"hidden"`
	Counts    map[string]int `json:"counts"`
	Offset    int            `json:"offset"`
	Limit     int            `json:"limit"`
	HasMore   bool           `json:"has_more"`
	CheckedAt time.Time      `json:"checked_at"`
}

func New(library *media.Manager, posts content.Repository, menus navigation.Store, settings *site.Manager, themes *theme.Manager, publicURL string) *Service {
	s := &Service{library: library, content: posts, navigation: menus, settings: settings, themes: themes, resolver: assetref.New(publicURL)}
	settings.SetSaveGuard(func(candidate site.Settings) (func(), map[string]string, error) {
		release, fields := s.hold(s.siteFields(candidate))
		return release, fields, nil
	})
	themes.SetSaveGuard(func(candidate map[string]any) (func(), map[string]string, error) {
		release, fields := s.hold(s.resolver.Values(candidate))
		return release, fields, nil
	})
	return s
}

func (s *Service) Content() content.Repository {
	return &contentRepository{Repository: s.content, usage: s}
}
func (s *Service) Navigation() navigation.Store {
	return &navigationStore{Store: s.navigation, usage: s}
}

// SetProfiles is called during construction, before serving any requests.
func (s *Service) SetProfiles(profiles *auth.Manager) { s.profiles = profiles }

// SetFeatures binds settings owned by optional application services before serving.
func (s *Service) SetFeatures(scan func(context.Context, media.Item) ([]Reference, error)) {
	s.features = scan
}

// HoldValues protects a settings write against concurrent trash/delete requests.
func (s *Service) HoldValues(values map[string]any) (func(), map[string]string) {
	return s.hold(s.resolver.Values(values))
}

// HoldAvatar prevents a deletion racing a profile write. Callers must release
// the lease after the persistent user update, including on failure.
func (s *Service) HoldAvatar(id int64) (func(), error) {
	s.gate.RLock()
	if id == 0 {
		return s.gate.RUnlock, nil
	}
	item, err := s.library.Get(id)
	if err != nil || item.Status() != media.StatusActive || item.Kind != media.KindImage {
		s.gate.RUnlock()
		return nil, media.ErrInvalidInput
	}
	return s.gate.RUnlock, nil
}

func (s *Service) hold(fields assetref.Fields) (func(), map[string]string) {
	s.gate.RLock()
	return s.gate.RUnlock, s.validate(fields)
}

// validate runs under a resource lease. A deletion that wins first causes a
// stale writer to fail validation; a writer that wins first blocks deletion.
func (s *Service) validate(fields assetref.Fields) map[string]string {
	invalid := make(map[string]string)
	checked := make(map[string]bool)
	for field, links := range fields {
		for _, link := range links {
			valid, exists := checked[link.Path]
			if !exists {
				item, err := s.library.Get(link.ID)
				valid = err == nil && item.Status() == media.StatusActive && item.PublicURL == link.Path
				checked[link.Path] = valid
			}
			if !valid {
				invalid[field] = "引用的媒体已删除、移入回收站或地址无效，请重新从媒体库选择"
			}
		}
	}
	return invalid
}

func (s *Service) siteFields(settings site.Settings) assetref.Fields {
	return s.resolver.Values(map[string]any{
		"site_icon_url":        settings.Identity.IconURL,
		"social_default_image": settings.Identity.SocialDefaultImage,
	})
}

func (s *Service) Report(ctx context.Context, item media.Item, access Access, offset int) (Report, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	return s.report(ctx, item, access, offset)
}

func (s *Service) report(ctx context.Context, item media.Item, access Access, offset int) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	report := Report{Items: []Reference{}, Counts: map[string]int{}, Offset: max(0, offset), Limit: PageSize}
	add := func(reference Reference, visible bool) {
		report.Total++
		report.Counts[reference.Source]++
		if !visible {
			report.Hidden++
			return
		}
		report.Visible++
		if report.Visible > report.Offset && len(report.Items) < report.Limit {
			report.Items = append(report.Items, reference)
		}
	}
	err := s.content.WalkMediaSnapshots(ctx, func(snapshot content.MediaSnapshot) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		fields := s.resolver.Content(snapshot.CoverURL, snapshot.ContentSource, snapshot.ContentHTML, contentPath(snapshot.Type, snapshot.Slug)).Matching(item.PublicURL)
		if len(fields) == 0 {
			return nil
		}
		visible := access.ContentRead && (access.ContentAuthorID == 0 || snapshot.AuthorID == access.ContentAuthorID)
		if snapshot.Source == content.MediaSourceSeries {
			visible = access.Taxonomy
		}
		if snapshot.Source == content.MediaSourceSnippet {
			visible = access.SnippetsManage
		}
		if snapshot.Source == content.MediaSourceAutosave {
			visible = visible && access.AutosaveOwner != "" && access.AutosaveOwner == snapshot.OwnerKey
		}
		add(Reference{
			Source: snapshot.Source, ID: snapshot.PostID, RevisionID: snapshot.RevisionID,
			Version: snapshot.Version, Title: snapshot.Title, ContentType: snapshot.Type,
			Status: snapshot.Status, Fields: fields,
		}, visible)
		return nil
	})
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for _, field := range s.siteFields(s.settings.Snapshot().Settings).Matching(item.PublicURL) {
		title := map[string]string{"site_icon_url": "站点图标", "social_default_image": "默认分享图片"}[field]
		add(Reference{Source: SourceSite, Title: title, Fields: []string{field}}, access.Settings)
	}
	themeSettings := s.themes.SettingsSnapshot()
	for _, field := range s.resolver.Values(themeSettings.Values).Matching(item.PublicURL) {
		title := field
		for _, group := range themeSettings.Groups {
			for _, setting := range group.Fields {
				if setting.Key == field {
					title = setting.Title
				}
			}
		}
		add(Reference{Source: SourceTheme, Title: title, Fields: []string{field}}, access.Appearance)
	}
	menus, err := s.navigation.List(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	sort.Slice(menus, func(i, j int) bool { return menus[i].ID < menus[j].ID })
	for _, menu := range menus {
		for _, entry := range menu.Items {
			if link, ok := s.resolver.URL(entry.URL, "/"); ok && link.Path == item.PublicURL {
				add(Reference{Source: SourceNavigation, ID: menu.ID, Version: menu.Version, Title: menu.Name + " · " + entry.Label, Fields: []string{"url"}}, access.Navigation)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if s.profiles != nil {
		for offset := 0; ; offset += 100 {
			users, err := s.profiles.ListUsers(ctx, auth.UserQuery{AvatarMediaID: item.ID, Limit: 100, Offset: offset})
			if errors.Is(err, auth.ErrUserStoreNotAvailable) {
				break
			}
			if err != nil {
				return Report{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			for _, user := range users.Items {
				add(Reference{Source: SourceProfile, ID: user.ID, Version: user.Version, Title: user.DisplayName, Status: user.Status, Fields: []string{"avatar_media_id"}}, access.Users)
			}
			if offset+len(users.Items) >= users.Total {
				break
			}
		}
	}
	if s.features != nil {
		items, err := s.features(ctx, item)
		if err != nil {
			return Report{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		for _, reference := range items {
			add(reference, access.Settings)
		}
	}
	report.HasMore = report.Visible > report.Offset+len(report.Items)
	report.CheckedAt = time.Now().UTC()
	return report, nil
}

func (s *Service) Delete(ctx context.Context, id, expectedVersion int64) error {
	return s.remove(ctx, id, expectedVersion, true)
}

func (s *Service) Trash(ctx context.Context, id, expectedVersion int64) error {
	return s.remove(ctx, id, expectedVersion, false)
}

func (s *Service) Restore(ctx context.Context, id, expectedVersion int64) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	_, err := s.library.Restore(ctx, id, expectedVersion)
	return err
}

func (s *Service) remove(ctx context.Context, id, expectedVersion int64, permanently bool) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	item, err := s.library.Get(id)
	if err != nil {
		return err
	}
	if expectedVersion <= 0 || item.Version != expectedVersion {
		return media.ErrVersionConflict
	}
	if permanently && item.Status() != media.StatusTrash {
		return media.ErrNotTrashed
	}
	if !permanently && item.Status() == media.StatusTrash {
		return media.ErrTrashed
	}
	report, err := s.report(ctx, item, Access{}, 0)
	if err != nil {
		return err
	}
	if report.Total > 0 {
		return ErrInUse
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if permanently {
		return s.library.Delete(id, expectedVersion)
	}
	_, err = s.library.Trash(id, expectedVersion)
	return err
}

func contentPath(kind, slug string) string {
	// Callers may supply a slug that NormalizePostInput has not processed yet.
	// Resolve relative resources at the eventual one-segment content path.
	slug = content.NormalizeSlug(slug)
	if strings.EqualFold(strings.TrimSpace(kind), content.TypePage) {
		return "/pages/" + slug
	}
	return "/posts/" + slug
}
