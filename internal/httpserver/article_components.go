package httpserver

import (
	"context"
	"html"
	"net/http"
	"strings"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/auth"
)

func (s *Server) prepareArticleComponents(ctx context.Context, data *pageView) {
	if data.IsAdmin || data.Post.ContentHTML == "" {
		return
	}
	skin := "inherit"
	if s.themes != nil && s.themes.Active().View == "chunnel/v1" {
		skin = "chunnel"
		data.Post.ContentHTML = articlewidgets.EnhanceCode(data.Post.ContentHTML, data.Post.ID)
		if strings.Contains(data.Post.ContentHTML, `data-np-widget=`) {
			data.BodyClass += " npw-skin-chunnel"
		}
	}
	sender, avatar := data.Post.AuthorName, ""
	for _, ref := range articlewidgets.References(data.Post.ContentHTML) {
		if ref.Valid && (ref.Config.Kind == "gift" || ref.Config.Kind == "redpacket") {
			if s.auth != nil && data.Post.AuthorID > 0 {
				if user, err := s.auth.GetUser(ctx, data.Post.AuthorID); err == nil && user.Status == auth.UserStatusActive {
					sender, avatar = user.DisplayName, s.avatarURL(user.AvatarMediaID)
					if sender == "" {
						sender = user.Username
					}
				}
			}
			break
		}
	}
	data.Post.ContentHTML = articlewidgets.ExpandWithSender(data.Post.ContentHTML, data.Post.ID, skin, sender, avatar)
}

func withArticleComponentAssets(output []byte, bases ...string) []byte {
	// Reuse the tokenizer-based head insertion, then append our own assets next
	// to its trusted link. No author-controlled text is used as an insertion point.
	result := withArticleWidgetStyles(output, bases...)
	marker := `<link rel="stylesheet" href="` + html.EscapeString(publicCoreAsset("css/newpress-article-widgets.css", bases...)) + `">`
	assets := marker + `<link rel="stylesheet" href="` + html.EscapeString(publicCoreAsset("css/newpress-components.css", bases...)) + `"><script defer src="` + html.EscapeString(publicCoreAsset("js/newpress-components.js", bases...)) + `"></script>`
	return []byte(strings.Replace(string(result), marker, assets, 1))
}

func prepareComponentFrames(w http.ResponseWriter, source string) {
	allowed := map[string]bool{}
	for _, r := range articlewidgets.References(source) {
		if !r.Valid {
			continue
		}
		switch r.Config.Provider {
		case "youtube":
			allowed["https://www.youtube-nocookie.com"] = true
		case "bilibili":
			if r.Config.Kind == "video" {
				allowed["https://player.bilibili.com"] = true
			}
		case "netease":
			allowed["https://music.163.com"] = true
		}
	}
	if len(allowed) == 0 {
		return
	}
	frames := "frame-src 'self'"
	for _, host := range []string{"https://www.youtube-nocookie.com", "https://player.bilibili.com", "https://music.163.com"} {
		if allowed[host] {
			frames += " " + host
		}
	}
	parts := strings.Split(w.Header().Get("Content-Security-Policy"), ";")
	found := false
	for i, part := range parts {
		fields := strings.Fields(part)
		if len(fields) > 0 && fields[0] == "frame-src" {
			found = true
			kept := []string{"frame-src"}
			for _, source := range fields[1:] {
				if source != "'none'" {
					kept = append(kept, source)
				}
			}
			for _, source := range strings.Fields(frames)[1:] {
				exists := false
				for _, prior := range kept {
					if source == prior {
						exists = true
					}
				}
				if !exists {
					kept = append(kept, source)
				}
			}
			parts[i] = strings.Join(kept, " ")
		}
	}
	if !found {
		parts = append(parts, frames)
	}
	w.Header().Set("Content-Security-Policy", strings.Join(parts, "; "))
}
