package httpserver

import (
	"io"
	"strings"

	"newpress.local/newpress/internal/coreui"
	"newpress.local/newpress/internal/theme"
)

func privatePage(page string) bool {
	switch page {
	case "questions", "flash-submit", "flash-single", "single", "page", "series", "circles", "feedback", "billing", "shop", "account", "account-access", "notification-unsubscribe", "maintenance":
		return true
	}
	return coreui.IsAdmin(page)
}
func trackingPage(page string) bool {
	switch page {
	case "account", "account-access", "billing", "shop", "notification-unsubscribe", "maintenance", "404", "403", "500", "503", "error", "service-unavailable":
		return false
	}
	return !coreui.IsAdmin(page)
}

func (s *Server) pageAvailable(page string) bool {
	return s.themes.HasTemplate(page) || coreui.HasTemplate(page)
}

func (s *Server) renderTheme(w io.Writer, page string, data pageView) error {
	return s.themes.RenderPublic(w, page, chunnelPage(data, s.permalinkBuilder()), publicPage(data, s.permalinkBuilder()))
}

func (s *Server) prepareRenderer(page string, data *pageView) (string, bool) {
	data.Kind = page
	data.IsAdmin = data.IsAdmin || coreui.IsAdmin(page)
	data.Admin.LayoutAvailable = s.themes.Active().Supports(theme.HomeLayoutV1)
	if page == "page" && !s.themes.HasTemplate(page) {
		page = "single"
	}
	core := data.IsAdmin || !s.themes.HasTemplate(page)
	if core {
		data.ThemeValues, data.ThemeClass = coreui.Defaults()
		data.ThemeCSSURL = coreui.AssetURL("css/core-defaults.css")
		data.UIAssetVersion = coreui.Revision()
		data.CDNAssetBase = ""
	}
	return page, core
}

func adminRequestPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/")
}
