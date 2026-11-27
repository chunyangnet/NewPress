package objectstorage

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"newpress.local/newpress/internal/media"
)

// RedirectURL only serves objects with a committed receipt in the active
// installation. No network lookup or bucket listing occurs on page requests.
func (s *Service) RedirectURL(ctx context.Context, item media.Item) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	if !s.loaded || s.active == nil || s.active.Err() != nil || s.configContext.Err() != nil {
		s.mu.Unlock()
		return "", false
	}
	c := s.settings
	g := s.generation
	target, mode := routeFor(c, item.UploadSource)
	p, ok := profileByID(c, target)
	r, found := s.records[recordID(target, item.ID, item.ObjectKey)]
	s.mu.Unlock()
	if mode != "redirect" || !ok || !found || r.Status != "synced" || r.Operation == "delete" || r.SHA256 != item.ChecksumSHA256 || r.Identity != profileIdentity(p) {
		return "", false
	}
	if p.PublicURL != "" {
		return publicObjectURL(p.PublicURL, r.RemoteKey), s.deliveryCurrent(g, c.Version)
	}
	if p.AllowPrivate || p.AllowHTTP {
		return "", false
	}
	b, err := s.backend(p)
	if err != nil {
		return "", false
	}
	u, err := b.SignedURL(ctx, r.RemoteKey, time.Duration(c.SignedURLSeconds)*time.Second)
	return u, err == nil && u != "" && ctx.Err() == nil && s.deliveryCurrent(g, c.Version)
}

func (s *Service) deliveryCurrent(g uint64, v int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded && s.generation == g && s.settings.Version == v && s.active != nil && s.active.Err() == nil && s.configContext.Err() == nil
}

// DeliveryOrigin grants CSP the configured route delivery hosts, never arbitrary
// cloud domains. Signing uses explicit in-memory credentials without I/O.
func (s *Service) DeliveryOrigin(ctx context.Context) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	if !s.loaded || s.active == nil || s.active.Err() != nil || s.configContext.Err() != nil {
		s.mu.Unlock()
		return ""
	}
	c, g := s.settings, s.generation
	s.mu.Unlock()
	targets := map[string]bool{}
	for _, source := range media.UploadSources() {
		target, mode := routeFor(c, source.ID)
		if target != "" && mode == "redirect" {
			targets[target] = true
		}
	}
	origins := map[string]bool{}
	for target := range targets {
		p, ok := profileByID(c, target)
		if !ok {
			continue
		}
		if origin := s.profileOrigin(ctx, p); origin != "" {
			origins[origin] = true
		}
	}
	if !s.deliveryCurrent(g, c.Version) {
		return ""
	}
	result := []string{}
	for origin := range origins {
		result = append(result, origin)
	}
	sort.Strings(result)
	return strings.Join(result, " ")
}

func (s *Service) profileOrigin(ctx context.Context, p Profile) string {
	raw := p.PublicURL
	if raw == "" {
		if p.AllowHTTP || p.AllowPrivate {
			return ""
		}
		b, err := s.backend(p)
		if err != nil {
			return ""
		}
		raw, err = b.SignedURL(ctx, "newpress-csp-probe", time.Minute)
		if err != nil {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
