// Package assetref resolves references to managed media without substring
// matching. It does not fetch URLs or interpret executable theme/plugin code.
package assetref

import (
	"net"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/articlewidgets"
)

type Resolver struct{ origin *url.URL }

type Link struct {
	ID   int64
	Path string
}

// Fields maps editor field names to the managed media URLs they reference.
type Fields map[string][]Link

func New(publicURL string) Resolver {
	origin, _ := url.Parse(publicURL)
	if origin == nil || origin.Host == "" {
		origin = &url.URL{Scheme: "https", Host: "newpress.invalid"}
	}
	return Resolver{origin: origin}
}

func (r Resolver) URL(value, basePath string) (Link, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "#") || strings.ContainsAny(value, "\x00\r\n\t\\") {
		return Link{}, false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Opaque != "" {
		return Link{}, false
	}
	origin := r.origin
	if origin == nil {
		origin = New("").origin
	}
	base := *origin
	base.Path, base.RawPath, base.RawQuery, base.Fragment = basePath, "", "", ""
	if base.Path == "" {
		base.Path = "/"
	}
	resolved := base.ResolveReference(parsed)
	if !strings.EqualFold(resolved.Scheme, origin.Scheme) || authority(resolved) != authority(origin) {
		return Link{}, false
	}
	// Browsers remove literal and percent-encoded dot segments. Do not decode
	// other escaped bytes: the public media endpoint requires its exact path.
	segments := strings.Split(resolved.EscapedPath(), "/")
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err == nil && (decoded == "." || decoded == "..") {
			segments[i] = decoded
		}
	}
	escaped := path.Clean(strings.Join(segments, "/"))
	parts := strings.Split(escaped, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "media" || parts[3] == "" {
		return Link{}, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != parts[2] {
		return Link{}, false
	}
	return Link{ID: id, Path: escaped}, true
}

func authority(value *url.URL) string {
	port := value.Port()
	if port == "" {
		if strings.EqualFold(value.Scheme, "https") {
			port = "443"
		} else if strings.EqualFold(value.Scheme, "http") {
			port = "80"
		}
	}
	return net.JoinHostPort(strings.ToLower(value.Hostname()), port)
}

// HTML collects URL-bearing attributes from source or rendered HTML. Text,
// comments and escaped code examples are not treated as embedded resources.
func (r Resolver) HTML(source, basePath string) []Link {
	tokens := html.NewTokenizer(strings.NewReader(source))
	links := make(map[string]Link)
	for token := tokens.Next(); token != html.ErrorToken; token = tokens.Next() {
		if token != html.StartTagToken && token != html.SelfClosingTagToken {
			continue
		}
		node := tokens.Token()
		for _, attr := range node.Attr {
			var values []string
			switch attr.Key {
			case "src", "href", "poster", "data", "background", "xlink:href":
				values = []string{attr.Val}
			case "srcset":
				values = srcsetURLs(attr.Val)
			case "data-np-config":
				if node.Data == "aside" {
					if config, err := articlewidgets.Decode(attr.Val); err == nil {
						values = []string{config.Image, config.URL}
						for _, item := range config.Items {
							values = append(values, item.Image, item.URL)
						}
					}
				}
			}
			for _, value := range values {
				if link, ok := r.URL(value, basePath); ok {
					links[link.Path] = link
				}
			}
		}
	}
	result := make([]Link, 0, len(links))
	for _, link := range links {
		result = append(result, link)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func srcsetURLs(value string) []string {
	var result []string
	for value != "" {
		value = strings.TrimLeft(value, " \t\n\r\f,")
		end := strings.IndexAny(value, " \t\n\r\f")
		if end < 0 {
			end = len(value)
		}
		candidate := value[:end]
		value = value[end:]
		if candidate == "" {
			break
		}
		result = append(result, strings.TrimRight(candidate, ","))
		if strings.HasSuffix(candidate, ",") {
			continue
		}
		if comma := strings.IndexByte(value, ','); comma >= 0 {
			value = value[comma+1:]
		} else {
			break
		}
	}
	return result
}

func (r Resolver) Content(cover, source, rendered, basePath string) Fields {
	fields := make(Fields)
	if link, ok := r.URL(cover, basePath); ok {
		fields["cover_url"] = []Link{link}
	}
	fields["content_source"] = append(r.HTML(source, basePath), r.HTML(rendered, basePath)...)
	return fields
}

func (r Resolver) Values(values map[string]any) Fields {
	fields := make(Fields)
	for key, raw := range values {
		if value, ok := raw.(string); ok {
			if link, ok := r.URL(value, "/"); ok {
				fields[key] = append(fields[key], link)
			}
			fields[key] = append(fields[key], r.HTML(value, "/")...)
		}
	}
	return fields
}

func (fields Fields) Matching(target string) []string {
	var matches []string
	for key, links := range fields {
		for _, link := range links {
			if link.Path == target {
				matches = append(matches, key)
				break
			}
		}
	}
	sort.Strings(matches)
	return matches
}
