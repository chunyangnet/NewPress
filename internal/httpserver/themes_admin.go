package httpserver

import (
	"bytes"
	"net/http"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/packagefs"
)

func (s *Server) adminThemes(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilityAppearanceManage)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	view := extensionUpdateView{Marketplace: true, Title: "主题市场", Snapshot: extensionupdate.Snapshot{Kind: "theme", Channel: "stable"}}
	if s.extensions != nil {
		snapshot, err := s.extensions.Snapshot("theme", "stable")
		if err != nil {
			s.renderError(w, 503, "无法读取主题目录")
			return
		}
		view.Snapshot = snapshot
		// A completed check is rendered once without immediately starting another
		// job. Short revisits share the same catalog; manual refresh stays available.
		view.AutoCheck = !readOnly && snapshot.Market.Configured && !snapshot.Job.Busy() &&
			(snapshot.Market.Checked.IsZero() || time.Since(snapshot.Market.Checked) >= 30*time.Second) &&
			(snapshot.Job.Action != "check" || time.Since(snapshot.Job.UpdatedAt) >= 30*time.Second)
	}
	data := s.baseViewFor(w, "主题市场", "发现主题，管理站点外观", r.URL.Path)
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-themes"
	data.Admin = s.adminView("themes", p, readOnly)
	data.ExtensionUpdates = view
	s.render(w, http.StatusOK, "admin-themes", data)
}

func (s *Server) themeCover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityAppearanceManage); !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"digest": true})
	if !ok {
		return
	}
	slug, digest := r.PathValue("slug"), q.Get("digest")
	if packagefs.ValidateSlug(slug) != nil || digest != "" && !validCoverDigest(digest) {
		http.NotFound(w, r)
		return
	}
	if s.extensions == nil {
		http.NotFound(w, r)
		return
	}
	data, mime, err := s.extensions.ThemeCover(r.Context(), slug, digest)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if digest != "" {
		w.Header().Set("Cache-Control", "private, max-age=300")
	}
	http.ServeContent(w, r, "theme-cover", time.Time{}, bytes.NewReader(data))
}
func validCoverDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'f' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}
