package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/content"
)

func etagMatches(header, current string) bool {
	// If-None-Match uses weak comparison for cache validation. Write-side
	// If-Match preconditions use their separate strong-version parsers.
	current = strings.TrimPrefix(current, "W/")
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || current != "" && strings.TrimPrefix(candidate, "W/") == current {
			return true
		}
	}
	return false
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func validSlug(value string) bool {
	return content.ValidSlug(value)
}

func postContainsTerm(terms []content.Term, id int64) bool {
	for _, term := range terms {
		if term.ID == id {
			return true
		}
	}
	return false
}

func joinPostTerms(terms []content.Term) string {
	values := make([]string, 0, len(terms))
	for _, term := range terms {
		values = append(values, term.Name)
	}
	return strings.Join(values, " ")
}
