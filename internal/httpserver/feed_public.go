package httpserver

import (
	"encoding/xml"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
)

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Atom    string     `xml:"xmlns:atom,attr"`
	DC      string     `xml:"xmlns:dc,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	Description string      `xml:"description"`
	Language    string      `xml:"language"`
	Self        rssSelfLink `xml:"atom:link"`
	Items       []rssItem   `xml:"item"`
}

type rssSelfLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	Description string   `xml:"description"`
	Creator     string   `xml:"dc:creator,omitempty"`
	Categories  []string `xml:"category"`
	GUID        rssGUID  `xml:"guid"`
	PublishedAt string   `xml:"pubDate"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

// publicFeed is a bounded summary projection of the existing public collection.
// Sharing the public API switch avoids opening a second export channel after an
// administrator has explicitly disabled content syndication.
func (s *Server) publicFeed(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	settings := s.siteSnapshot(request.Context()).Settings
	if !settings.API.PublicPostsEnabled {
		http.NotFound(writer, request)
		return
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		http.Error(writer, "订阅参数不正确", http.StatusBadRequest)
		return
	}
	values, valid := strictQueryShape(values, map[string]bool{"category": true, "limit": true})
	limit, limitErr := positiveQueryInt(values.Get("limit"), 20, 100)
	query, queryErr := content.NormalizePublishedQuery(content.PublishedQuery{Category: values.Get("category"), Limit: limit, Sort: "published", IgnorePinning: true})
	if !valid || limitErr != nil || queryErr != nil {
		http.Error(writer, "订阅参数不正确", http.StatusBadRequest)
		return
	}
	result, err := s.repository.SearchPublished(request.Context(), query)
	if err != nil {
		slog.Error("list public feed", "error", err)
		http.Error(writer, "订阅暂时不可用", http.StatusInternalServerError)
		return
	}
	params := url.Values{}
	if query.Category != "" {
		params.Set("category", query.Category)
	}
	link := s.absolutePublicURL("/")
	if query.Category != "" {
		link = s.absolutePublicURL("/explore/?" + params.Encode())
	}
	if limit != 20 {
		params.Set("limit", fmt.Sprint(limit))
	}
	self := s.absolutePublicURL("/feed.xml")
	if len(params) > 0 {
		self += "?" + params.Encode()
	}
	description := settings.Identity.Description
	if description == "" {
		description = settings.Identity.Tagline
	}
	location := site.TimeLocation(settings)
	channel := rssChannel{
		Title: settings.Identity.Title, Link: link, Description: description, Language: settings.Identity.Locale,
		Self: rssSelfLink{Href: self, Rel: "self", Type: "application/rss+xml"},
	}
	for _, post := range result.Items {
		item := rssItem{
			Title: post.Title, Link: s.absolutePublicURL(s.contentPath(post)),
			// RSS readers interpret description as HTML after decoding XML. Escape
			// the plain-text excerpt once here and let encoding/xml escape the XML.
			Description: html.EscapeString(post.Excerpt), Creator: post.AuthorName,
			GUID:        rssGUID{Value: s.absolutePublicURL("/?p=" + fmt.Sprint(post.ID))},
			PublishedAt: post.PublishedAt.In(location).Format(time.RFC1123Z),
		}
		for _, category := range post.Categories {
			item.Categories = append(item.Categories, category.Name)
		}
		channel.Items = append(channel.Items, item)
	}
	payload, err := xml.MarshalIndent(rssFeed{
		Version: "2.0", Atom: "http://www.w3.org/2005/Atom", DC: "http://purl.org/dc/elements/1.1/", Channel: channel,
	}, "", "  ")
	if err != nil {
		slog.Error("encode public feed", "error", err)
		http.Error(writer, "订阅暂时不可用", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("X-Robots-Tag", "noindex")
	// Revalidate visibility on every fetch, including after an article is made
	// private. The payload hash still lets unchanged feeds return a cheap 304.
	serveGenerated(writer, request, "application/rss+xml; charset=utf-8", append([]byte(xml.Header), append(payload, '\n')...), "public, no-cache")
}
