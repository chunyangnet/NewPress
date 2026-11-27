package httpserver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"newpress.local/newpress/internal/i18n"
)

type translationsAPIResponse struct {
	Schema        int               `json:"schema"`
	Locale        string            `json:"locale"`
	DefaultLocale string            `json:"default_locale"`
	Theme         string            `json:"theme"`
	Locales       []string          `json:"locales"`
	Messages      map[string]string `json:"messages"`
}

func (s *Server) translationsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		s.writeAPIError(w, r, http.StatusMethodNotAllowed, "request.method_not_allowed", "此资源不支持当前 HTTP 方法", nil)
		return
	}
	query, ok := s.strictAPIQuery(w, r, map[string]bool{"locale": true, "key": true})
	if !ok {
		return
	}
	locale := s.siteSnapshot(r.Context()).Settings.Identity.Locale
	if query.Has("locale") {
		var err error
		locale, err = i18n.CanonicalLocale(query.Get("locale"))
		if err != nil {
			s.writeAPIError(w, r, http.StatusBadRequest, "request.invalid_query", "locale 必须是有效语言标识，例如 zh-CN 或 en-US", nil)
			return
		}
	}
	key := query.Get("key")
	if query.Has("key") && !i18n.ValidKey(key) {
		s.writeAPIError(w, r, http.StatusBadRequest, "request.invalid_query", "key 必须是有效翻译词条键名", nil)
		return
	}
	snapshot := s.themes.Translations(locale)
	translator := snapshot.Translator
	var messages map[string]string
	if query.Has("key") {
		value, found := translator.Lookup(key)
		if !found {
			s.writeAPIError(w, r, http.StatusNotFound, "translations.not_found", "没有找到翻译词条", nil)
			return
		}
		messages = map[string]string{key: value}
	} else {
		messages = translator.Messages()
	}
	response := translationsAPIResponse{Schema: 1, Locale: translator.Locale(), DefaultLocale: translator.DefaultLocale(), Theme: snapshot.Theme, Locales: translator.Locales(), Messages: messages}
	payload, err := json.Marshal(response)
	if err != nil {
		s.writeAPIError(w, r, http.StatusInternalServerError, "translations.unavailable", "翻译词条暂时不可用", nil)
		return
	}
	payload = append(payload, '\n')
	etag := fmt.Sprintf(`"translations-%x"`, sha256.Sum256(payload))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, no-cache")
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(payload)
	}
}
