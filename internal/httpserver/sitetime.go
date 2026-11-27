package httpserver

import (
	"context"
	"net/http"
	"time"

	"newpress.local/newpress/internal/site"
)

// Human-readable times and calendar rules use one immutable site settings
// snapshot per request. Protocol timestamps and elapsed durations stay absolute.
type siteRequestKey struct{}
type siteRequestState struct {
	path     string
	snapshot site.SettingsSnapshot
	location *time.Location
}

func (s *Server) siteSnapshot(ctx context.Context) site.SettingsSnapshot {
	if ctx != nil {
		if state, ok := ctx.Value(siteRequestKey{}).(siteRequestState); ok {
			return state.snapshot
		}
	}
	if s == nil || s.siteSettings == nil {
		return site.SettingsSnapshot{}
	}
	return s.siteSettings.Snapshot()
}

func (s *Server) siteLocation(contexts ...context.Context) *time.Location {
	if len(contexts) > 0 && contexts[0] != nil {
		if state, ok := contexts[0].Value(siteRequestKey{}).(siteRequestState); ok {
			return state.location
		}
	}
	return site.TimeLocation(s.siteSnapshot(nil).Settings)
}

func (s *Server) siteTimezone() string { return s.siteLocation().String() }

func (s *Server) captureSiteTime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot := s.siteSnapshot(r.Context())
		state := siteRequestState{snapshot: snapshot, location: site.TimeLocation(snapshot.Settings), path: r.URL.Path}
		ctx := context.WithValue(r.Context(), siteRequestKey{}, state)
		next.ServeHTTP(&siteTimeResponseWriter{ResponseWriter: w, ctx: ctx}, r.WithContext(ctx))
	})
}

// Rendering helpers also need the request context when handling errors or
// building pages without a *http.Request parameter. Unwrap keeps streaming via
// http.ResponseController working through both this and analytics middleware.
type siteTimeResponseWriter struct {
	http.ResponseWriter
	ctx context.Context
}

func (w *siteTimeResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func responseContext(w http.ResponseWriter) context.Context {
	for w != nil {
		if captured, ok := w.(*siteTimeResponseWriter); ok {
			return captured.ctx
		}
		next, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		w = next.Unwrap()
	}
	return context.Background()
}

func (s *Server) baseViewFor(w http.ResponseWriter, title, description, path string) pageView {
	return s.baseViewContext(responseContext(w), title, description, path)
}

// siteTime renders missing timestamps as empty, never year 0001.
func siteTime(location *time.Location, value time.Time, layout string) string {
	if value.IsZero() {
		return ""
	}
	if location == nil {
		location = time.UTC
	}
	return value.In(location).Format(layout)
}

func (s *Server) dateLabel(value time.Time, ctx ...context.Context) string {
	return siteTime(s.siteLocation(ctx...), value, "2006-01-02")
}
func (s *Server) monthLabel(value time.Time, ctx ...context.Context) string {
	return siteTime(s.siteLocation(ctx...), value, "2006.01")
}
func (s *Server) dateTimeLabel(value time.Time, ctx ...context.Context) string {
	return siteTime(s.siteLocation(ctx...), value, "2006-01-02 15:04")
}
func (s *Server) dateTimeSecondsLabel(value time.Time, ctx ...context.Context) string {
	return siteTime(s.siteLocation(ctx...), value, "2006-01-02 15:04:05")
}
func (s *Server) shortDateTimeLabel(value time.Time, ctx ...context.Context) string {
	return siteTime(s.siteLocation(ctx...), value, "01-02 15:04")
}
