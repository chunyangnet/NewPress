package httpserver

import (
	"net/http"
	"strings"
)

func (s *Server) prepareStorageDelivery(w http.ResponseWriter) {
	origin := s.objectStorage.DeliveryOrigin(responseContext(w))
	if origin == "" {
		return
	}
	policy := w.Header().Get("Content-Security-Policy")
	parts := strings.Split(policy, ";")
	foundImage, foundMedia := false, false
	for i, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "img-src" {
			parts[i] = strings.TrimSpace(part) + " " + origin
			foundImage = true
		}
		if fields[0] == "media-src" {
			parts[i] = strings.TrimSpace(part) + " " + origin
			foundMedia = true
		}
	}
	if !foundImage {
		parts = append(parts, "img-src 'self' data: "+origin)
	}
	if !foundMedia {
		parts = append(parts, "media-src 'self' "+origin)
	}
	w.Header().Set("Content-Security-Policy", strings.Join(parts, ";"))
}
