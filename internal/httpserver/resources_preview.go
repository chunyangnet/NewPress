package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"

	htmlparser "golang.org/x/net/html"
	"newpress.local/newpress/internal/resources"
)

type resourcePreviewView struct {
	mode, fallback, state string
}

// Preview reads the post snapshot already authorized by adminContentPreview.
// It never reads purchase records or grants access through the public API.
func (s *Server) resourcePreview(ctx context.Context, data *pageView, item resources.Resource) *resourcePreviewView {
	view := &resourcePreviewView{mode: "appearance"}
	private := data.Preview.EditorURL != ""
	if private {
		view.mode = "snapshot"
	}
	var fallback strings.Builder
	fallback.WriteString(`<p class="npr-intro">以下为资源版本与解锁条件的预览。</p>`)
	variants := []map[string]any{}
	for _, v := range item.Variants {
		_, groups := resources.Evaluate(v, func(resources.Condition) bool { return false })
		fmt.Fprintf(&fallback, `<div class="npr-preview-version"><h3>%s</h3>`, html.EscapeString(v.Name))
		for gi, g := range v.Groups {
			name := g.Name
			if name == "" {
				name = fmt.Sprintf("解锁方式 %d", gi+1)
			}
			if gi > 0 {
				fallback.WriteString(`<div class="npr-or"><span>或选择另一种方式</span></div>`)
			}
			fmt.Fprintf(&fallback, `<div class="npr-rule"><div class="npr-rule-heading"><b>%s</b><span>满足以下全部条件</span></div><ul>`, html.EscapeString(name))
			for ci, c := range g.Conditions {
				label := s.resourceConditionLabel(ctx, c)
				groups[gi].Conditions[ci].Label = label
				fmt.Fprintf(&fallback, `<li class="npr-unmet"><span>○</span><span>%s</span></li>`, html.EscapeString(label))
			}
			fallback.WriteString(`</ul></div>`)
		}
		fallback.WriteString(`</div>`)
		if !private {
			continue
		}
		links := []map[string]string{}
		for _, link := range v.Downloads {
			links = append(links, map[string]string{"id": link.ID, "name": link.Name, "provider": link.Provider, "provider_name": resources.ProviderName(link.Provider), "url": link.URL, "code": link.Code, "password": link.Password})
		}
		variants = append(variants, map[string]any{
			"id": v.ID, "name": v.Name, "kind": v.Kind, "allowed": false,
			"available": resources.Available(item, data.Now), "groups": groups,
			"body": v.Body, "media_url": v.MediaURL, "downloads": links,
			"questions": publicResourceQuestions(v), "fields": v.Fields,
			"pass_percent": v.PassPercent, "attempts_remaining": v.AttemptsPerDay,
		})
	}
	view.fallback = fallback.String()
	if private {
		payload, err := json.Marshal(map[string]any{
			"preview": true, "post": data.Post.ID, "id": item.ID, "title": item.Title,
			"variants": variants, "selection": item.Selection, "starts_at": item.StartsAt,
			"ends_at": item.EndsAt, "timezone": data.Site.Timezone,
			"remaining": resources.Remaining(item, 0, 0),
		})
		if err == nil {
			// Keep data inert through the shared article sanitizer and strict CSP.
			view.state = base64.RawURLEncoding.EncodeToString(payload)
		}
	}
	return view
}

// Inject only server-built snapshots after the theme's safeHTML sanitizer has
// stripped any author-supplied payloads. Allowing encoded HTML through the
// article policy would otherwise let authored JSON bypass body sanitization.
func withResourcePreviewStates(output []byte, data pageView) []byte {
	if !data.IsPreview || data.Preview.EditorURL == "" || len(data.resourcePreviews) == 0 {
		return output
	}
	var out bytes.Buffer
	tokens := htmlparser.NewTokenizer(bytes.NewReader(output))
	for kind := tokens.Next(); kind != htmlparser.ErrorToken; kind = tokens.Next() {
		raw := string(tokens.Raw())
		if kind == htmlparser.StartTagToken {
			token := tokens.Token()
			if token.Data == "aside" {
				var mode, resource, post string
				attrs := make([]htmlparser.Attribute, 0, len(token.Attr)+1)
				for _, attr := range token.Attr {
					switch attr.Key {
					case "data-npr-preview":
						mode = attr.Val
					case "data-npr-resource":
						resource = attr.Val
					case "data-npr-post":
						post = attr.Val
					}
					if attr.Key != "data-npr-preview-state" {
						attrs = append(attrs, attr)
					}
				}
				if state := data.resourcePreviews[resource]; mode == "snapshot" && post == strconv.FormatInt(data.Post.ID, 10) && state != "" {
					token.Attr = append(attrs, htmlparser.Attribute{Key: "data-npr-preview-state", Val: state})
					out.WriteString(token.String())
					continue
				}
			}
		}
		out.WriteString(raw)
	}
	return out.Bytes()
}
