package httpserver

import (
	"context"
	"errors"
	"time"

	"newpress.local/newpress/internal/content"
)

var (
	errAIWorkbenchSourceChanged     = errors.New("预览来源已变化，请重新预览资料")
	errAIWorkbenchSourceUnavailable = errors.New("暂时无法确认预览来源，请稍后重新预览")
)

// These references describe exactly the snapshots collected for confirmation.
// Do not replace their versions with a later read, or silently rebuild context.
type workbenchSources struct {
	server    *Server
	versions  map[int64]int64
	analytics context.Context
	conflict  bool
}

func (s *workbenchSources) add(id, version int64) {
	if old, ok := s.versions[id]; ok && old != version {
		s.conflict = true
	}
	s.versions[id] = version
}

func (s *workbenchSources) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.conflict || s.analytics != nil && s.analytics.Err() != nil {
		return errAIWorkbenchSourceChanged
	}
	if len(s.versions) != 0 {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if s.analytics != nil {
			stop := context.AfterFunc(s.analytics, cancel)
			defer stop()
		}
		ids := make([]int64, 0, len(s.versions))
		for id := range s.versions {
			ids = append(ids, id)
		}
		headers, err := content.ReadPostHeaders(ctx, s.server.repository, ids)
		if s.analytics != nil && s.analytics.Err() != nil {
			return errAIWorkbenchSourceChanged
		}
		if err != nil {
			return errAIWorkbenchSourceUnavailable
		}
		now := time.Now()
		for id, version := range s.versions {
			h, ok := headers[id]
			if !ok || h.Version != version || h.Type != content.TypePost || !content.PublicAt(h.Post(), now) {
				return errAIWorkbenchSourceChanged
			}
		}
	}
	if s.analytics != nil && s.analytics.Err() != nil {
		return errAIWorkbenchSourceChanged
	}
	return ctx.Err()
}
