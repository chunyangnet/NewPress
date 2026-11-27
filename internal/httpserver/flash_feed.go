package httpserver

import (
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"newpress.local/newpress/internal/content"
	"time"
)

func (s *Server) flashFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.siteSnapshot(r.Context()).Settings.API.PublicPostsEnabled {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	result, e := s.repository.SearchPublished(r.Context(), content.PublishedQuery{Type: content.TypeFlash, Limit: 50})
	if e != nil {
		s.flashError(w, r, e)
		return
	}
	settings := s.siteSnapshot(r.Context()).Settings
	channel := rssChannel{Title: settings.Identity.Title + " · 快报", Link: s.absolutePublicURL("/flash/"), Description: "最新快报", Language: settings.Identity.Locale, Self: rssSelfLink{Href: s.absolutePublicURL("/flash/feed.xml"), Rel: "self", Type: "application/rss+xml"}}
	for _, p := range result.Items {
		channel.Items = append(channel.Items, rssItem{Title: p.Title, Link: s.absolutePublicURL(content.PublicPath(p)), Description: html.EscapeString(p.Excerpt), Creator: p.AuthorName, GUID: rssGUID{Value: s.absolutePublicURL("/flash/?id=" + fmt.Sprint(p.ID))}, PublishedAt: p.PublishedAt.Format(time.RFC1123Z)})
	}
	payload, e := xml.Marshal(rssFeed{Version: "2.0", Atom: "http://www.w3.org/2005/Atom", DC: "http://purl.org/dc/elements/1.1/", Channel: channel})
	if e != nil {
		s.flashError(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.Write([]byte(xml.Header))
	w.Write(payload)
}
