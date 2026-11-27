package httpserver

import (
	"net/http"
	"strings"

	"newpress.local/newpress/internal/commentorigin"
)

func (s *Server) commentOriginFlag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !strings.HasSuffix(name, ".svg") {
		http.NotFound(w, r)
		return
	}
	data, err := commentorigin.FlagSVG(strings.TrimSuffix(name, ".svg"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	serveGenerated(w, r, "image/svg+xml", data, "public, max-age=86400, must-revalidate")
}
