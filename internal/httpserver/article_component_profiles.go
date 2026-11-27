package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/safehttp"
)

type componentProfile struct {
	Name   string `json:"name"`
	Bio    string `json:"bio"`
	Avatar string `json:"avatar"`
	URL    string `json:"url"`
	Meta   string `json:"meta"`
	Cached bool   `json:"cached"`
}
type componentProfileEntry struct {
	profile    componentProfile
	avatar     []byte
	expires    time.Time
	staleUntil time.Time
	retryAfter time.Time
}
type componentProfileCache struct {
	mu      sync.Mutex
	entries map[string]componentProfileEntry
	loading map[string]bool
}

var componentHTTP = safehttp.Client(5 * time.Second)

func boundedComponentGET(ctx context.Context, address string, max int64) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", "NewPress-Article-Components")
	r.Header.Set("Accept", "application/vnd.github+json")
	res, err := componentHTTP.Do(r)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("profile status %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, fmt.Errorf("profile response too large")
	}
	return b, nil
}

func (s *Server) cachedGithub(ctx context.Context, uid string) componentProfileEntry {
	c := &s.componentProfiles
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]componentProfileEntry{}
		c.loading = map[string]bool{}
	}
	now := time.Now()
	previous := c.entries[uid]
	if now.Before(previous.expires) || now.Before(previous.retryAfter) {
		c.mu.Unlock()
		if previous.profile.Name != "" && !now.Before(previous.staleUntil) {
			return componentProfileEntry{}
		}
		return previous
	}
	if c.loading[uid] || len(c.loading) >= 4 {
		old := c.entries[uid]
		c.mu.Unlock()
		if !time.Now().Before(old.staleUntil) {
			return componentProfileEntry{}
		}
		return old
	}
	_, exists := c.entries[uid]
	if !exists && len(c.entries) >= 128 {
		for k, v := range c.entries {
			if !now.Before(v.staleUntil) && !now.Before(v.retryAfter) && !c.loading[k] {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= 128 {
			for k := range c.entries {
				if c.loading[k] {
					continue
				}
				delete(c.entries, k)
				break
			}
		}
	}
	c.loading[uid] = true
	// Reserve capacity before releasing the lock, including cold in-flight entries.
	c.entries[uid] = previous
	c.mu.Unlock()
	if previous.profile.Name != "" && now.Before(previous.staleUntil) {
		if !s.requests.background(func(refreshContext context.Context) { s.refreshGithub(refreshContext, uid, previous) }) {
			c.mu.Lock()
			delete(c.loading, uid)
			c.mu.Unlock()
		}
		return previous
	}
	return s.refreshGithub(ctx, uid, previous)
}

func (s *Server) refreshGithub(ctx context.Context, uid string, previous componentProfileEntry) componentProfileEntry {
	c := &s.componentProfiles
	defer func() { c.mu.Lock(); delete(c.loading, uid); c.mu.Unlock() }()
	e := componentProfileEntry{retryAfter: time.Now().Add(2 * time.Minute)}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, err := boundedComponentGET(ctx, "https://api.github.com/users/"+url.PathEscape(uid), 64<<10)
	if err == nil {
		var p struct {
			Name, Login, Bio string
			Followers        int
			Repos            int    `json:"public_repos"`
			AvatarURL        string `json:"avatar_url"`
		}
		if json.Unmarshal(body, &p) == nil && p.Login != "" {
			if p.Name == "" {
				p.Name = p.Login
			}
			e.profile = componentProfile{Name: p.Name, Bio: p.Bio, URL: "https://github.com/" + url.PathEscape(p.Login), Meta: fmt.Sprintf("%d 位关注者 · %d 个公开仓库", p.Followers, p.Repos), Cached: true}
			e.expires = time.Now().Add(30 * time.Minute)
			e.staleUntil = time.Now().Add(24 * time.Hour)
			u, err := url.Parse(p.AvatarURL)
			if err == nil && u.Scheme == "https" && u.Host == "avatars.githubusercontent.com" && u.User == nil {
				query := u.Query()
				query.Set("s", "128")
				u.RawQuery = query.Encode()
				if raw, err := boundedComponentGET(ctx, u.String(), 256<<10); err == nil {
					cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
					if err == nil && cfg.Width > 0 && cfg.Width <= 1024 && cfg.Height > 0 && cfg.Height <= 1024 {
						if img, _, err := image.Decode(bytes.NewReader(raw)); err == nil {
							var buf bytes.Buffer
							if png.Encode(&buf, img) == nil && buf.Len() < 256<<10 {
								e.avatar = buf.Bytes()
							}
						}
					}
				}
			}
		}
	}
	if e.profile.Name == "" && previous.profile.Name != "" && time.Now().Before(previous.staleUntil) {
		previous.retryAfter = e.retryAfter
		e = previous
	}
	c.mu.Lock()
	c.entries[uid] = e
	c.mu.Unlock()
	return e
}

func (s *Server) articleComponentProfile(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.componentTarget(w, r)
	if !ok {
		return
	}
	if c.Kind != "mention" {
		s.componentError(w, r, fmt.Errorf("not a mention"))
		return
	}
	profile := componentProfile{Name: c.Title, Bio: c.Text, Avatar: c.Image, URL: c.ExternalURL(), Meta: "作者提供的资料"}
	switch c.Provider {
	case "site":
		var id int64
		_, _ = fmt.Sscan(c.UID, &id)
		user, err := s.auth.GetUser(r.Context(), id)
		if err != nil || user.Status != auth.UserStatusActive {
			s.writeAPIError(w, r, 404, "widget.profile_unavailable", "这个用户当前不可用", nil)
			return
		}
		profile.Name, profile.Bio, profile.Avatar, profile.Meta = user.DisplayName, user.Bio, s.avatarURL(user.AvatarMediaID), "本站用户"
		if profile.Name == "" {
			profile.Name = user.Username
		}
	case "github":
		if allowed, _ := s.accessRate.allow("widget-profile:"+s.commentGuard.sourceDigest(s.clientIPs.string(r)), 30, time.Now()); !allowed {
			s.writeAPIError(w, r, 429, "widget.rate_limit", "请稍后重试", nil)
			return
		}
		e := s.cachedGithub(r.Context(), c.UID)
		if e.profile.Name != "" {
			profile = e.profile
			if len(e.avatar) > 0 {
				profile.Avatar = fmt.Sprintf("/api/v1/article-component-avatar/%s/%s", url.PathEscape(r.PathValue("post")), c.ID)
			}
		} else {
			profile.Meta = "GitHub 暂时不可达 · 显示作者提供的资料"
		}
	case "bilibili":
		profile.Meta = "哔哩哔哩 · UID " + c.UID + " · 作者提供的资料"
	}
	writeJSON(w, 200, profile)
}

func (s *Server) articleComponentAvatar(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.componentTarget(w, r)
	if !ok {
		return
	}
	if c.Kind != "mention" || c.Provider != "github" {
		http.NotFound(w, r)
		return
	}
	s.componentProfiles.mu.Lock()
	e, found := s.componentProfiles.entries[c.UID]
	s.componentProfiles.mu.Unlock()
	if !found || len(e.avatar) == 0 || !time.Now().Before(e.staleUntil) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(e.avatar)
}
