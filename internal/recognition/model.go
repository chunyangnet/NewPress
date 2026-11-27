// Package recognition owns article tips and publicly awarded community identities.
// Administrative roles and private verification evidence never become public badges.
package recognition

import (
	"context"
	"errors"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/virtualcurrency"
)

var (
	ErrInvalid     = errors.New("invalid recognition input")
	ErrConflict    = errors.New("recognition version changed")
	ErrDisabled    = errors.New("recognition feature disabled")
	ErrForbidden   = errors.New("recognition operation forbidden")
	ErrUnavailable = errors.New("recognition unavailable")
	ErrPending     = errors.New("recognition request pending")
)

type Badge struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Background  string `json:"background"`
	Style       string `json:"style"`
	Image       string `json:"image"`
	Enabled     bool   `json:"enabled"`
}
type TipCurrency struct {
	Key     string   `json:"key"`
	Presets []string `json:"presets"`
}
type Settings struct {
	Version             int64         `json:"version"`
	TipsEnabled         bool          `json:"tips_enabled"`
	HideAmounts         bool          `json:"hide_amounts"`
	AllowHideAmount     bool          `json:"allow_hide_amount"`
	VerificationEnabled bool          `json:"verification_enabled"`
	VerificationHelp    string        `json:"verification_help"`
	MaxMedals           int           `json:"max_medals"`
	Currencies          []TipCurrency `json:"currencies"`
	Badges              []Badge       `json:"badges"`
}
type Grant struct {
	Key       string    `json:"key"`
	Label     string    `json:"label"`
	GrantedAt time.Time `json:"granted_at"`
	ExpiresAt time.Time `json:"expires_at"`
	ActorID   int64     `json:"actor_id"`
}

func (g Grant) Active(now time.Time) bool {
	return !now.Before(g.GrantedAt) && (g.ExpiresAt.IsZero() || now.Before(g.ExpiresAt))
}

type Application struct {
	Key       string    `json:"key"`
	Label     string    `json:"label"`
	Evidence  string    `json:"evidence"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Event struct {
	Action    string    `json:"action"`
	Key       string    `json:"key"`
	Reason    string    `json:"reason"`
	ActorID   int64     `json:"actor_id"`
	CreatedAt time.Time `json:"created_at"`
}
type Identity struct {
	Version     int64        `json:"version"`
	Grants      []Grant      `json:"grants"`
	Equipped    []string     `json:"equipped"`
	Application *Application `json:"application,omitempty"`
	History     []Event      `json:"history"`
}
type Tip struct {
	UserID          int64     `json:"user_id"`
	AuthorID        int64     `json:"author_id"`
	PostID          int64     `json:"post_id"`
	RequestID       string    `json:"request_id"`
	Currency        string    `json:"currency"`
	Symbol          string    `json:"symbol"`
	Precision       int       `json:"precision"`
	Amount          int64     `json:"amount"`
	CurrencyVersion int64     `json:"currency_version"`
	Hidden          bool      `json:"hidden"`
	EntryID         int64     `json:"entry_id"`
	CreatedAt       time.Time `json:"created_at"`
}
type State struct {
	PrivacyUnified bool                `json:"privacy_unified,omitempty"`
	Schema         int                 `json:"schema"`
	Settings       Settings            `json:"settings"`
	Identities     map[string]Identity `json:"identities"`
	Tips           map[string]Tip      `json:"tips"`
}
type Backend interface {
	Read(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}

func InitialState() State {
	return State{Schema: 1, Settings: Settings{Version: 1, AllowHideAmount: true, MaxMedals: 3,
		VerificationHelp: "介绍你的创作领域，并提供可核验的主页或作品链接。请勿提交身份证件、密码或其他敏感信息。",
		Currencies:       []TipCurrency{{Key: "main", Presets: []string{"10", "30", "50"}}}, Badges: []Badge{
			{Key: "contributor", Kind: "title", Name: "热心分享者", Description: "持续分享与认真交流", Icon: "sparkles", Color: "#2563eb", Background: "#eff6ff", Style: "soft", Enabled: true},
			{Key: "original", Kind: "medal", Name: "原创之星", Description: "用原创作品为社区增添价值", Icon: "award", Color: "#a16207", Background: "#fef9c3", Style: "soft", Enabled: true},
			{Key: "creator", Kind: "verified", Name: "认证创作者", Description: "已通过站点人工审核", Icon: "badge-check", Color: "#0284c7", Background: "#e0f2fe", Style: "soft", Enabled: true},
		}}, Identities: map[string]Identity{}, Tips: map[string]Tip{}}
}
func TextOK(v string, min, max int) bool {
	if !utf8.ValidString(v) || strings.TrimSpace(v) != v || utf8.RuneCountInString(v) < min || utf8.RuneCountInString(v) > max {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '<' || r == '>' {
			return false
		}
	}
	return true
}
func ColorOK(v string) bool {
	if len(v) != 7 || v[0] != '#' {
		return false
	}
	for _, r := range v[1:] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
func ImageOK(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > 500 || !strings.HasPrefix(v, "/media/") || strings.ContainsAny(v, "\\\r\n\t") {
		return false
	}
	u, e := url.Parse(v)
	return e == nil && u.RawQuery == "" && u.Fragment == "" && !strings.Contains(u.Path, "..") && slices.Contains([]string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif"}, strings.ToLower(path.Ext(u.Path)))
}
func (s Settings) Badge(key string) (Badge, bool) {
	for _, b := range s.Badges {
		if b.Key == key {
			return b, true
		}
	}
	return Badge{}, false
}
func ValidateSettings(s Settings) error {
	if s.Version < 1 || s.MaxMedals < 1 || s.MaxMedals > 8 || !TextOK(s.VerificationHelp, 0, 500) || len(s.Badges) > 100 || len(s.Currencies) > 16 {
		return ErrInvalid
	}
	keys := map[string]bool{}
	for _, b := range s.Badges {
		if !virtualcurrency.ValidCurrencyKey(b.Key) || keys[b.Key] || !slices.Contains([]string{"title", "medal", "verified"}, b.Kind) || !TextOK(b.Name, 1, 20) || !TextOK(b.Description, 0, 160) || !ColorOK(b.Color) || !ColorOK(b.Background) || !ImageOK(b.Image) || !slices.Contains([]string{"soft", "outline", "solid"}, b.Style) || !slices.Contains([]string{"award", "sparkles", "star", "heart", "diamond", "shield-check", "badge-check", "zap", "pen-line", "newspaper"}, b.Icon) {
			return ErrInvalid
		}
		keys[b.Key] = true
	}
	keys = map[string]bool{}
	for _, c := range s.Currencies {
		if !virtualcurrency.ValidCurrencyKey(c.Key) || keys[c.Key] || len(c.Presets) < 1 || len(c.Presets) > 6 {
			return ErrInvalid
		}
		keys[c.Key] = true
		for _, p := range c.Presets {
			n, e := virtualcurrency.ParseAmount(p, 6)
			if e != nil || n <= 0 {
				return ErrInvalid
			}
		}
	}
	if s.TipsEnabled && len(s.Currencies) == 0 {
		return ErrInvalid
	}
	return nil
}
func ValidateState(s State) error {
	if s.Schema != 1 || ValidateSettings(s.Settings) != nil || s.Identities == nil || s.Tips == nil {
		return ErrInvalid
	}
	for id, a := range s.Identities {
		n, e := strconv.ParseInt(id, 10, 64)
		if e != nil || n < 1 || strconv.FormatInt(n, 10) != id || a.Version < 1 || len(a.Grants) > 100 || len(a.Equipped) > 100 || len(a.History) > 100 {
			return ErrInvalid
		}
		keys := map[string]bool{}
		for _, g := range a.Grants {
			if _, ok := s.Settings.Badge(g.Key); !ok || keys[g.Key] || g.ActorID < 1 || g.GrantedAt.IsZero() || !TextOK(g.Label, 0, 30) || !g.ExpiresAt.IsZero() && !g.ExpiresAt.After(g.GrantedAt) {
				return ErrInvalid
			}
			keys[g.Key] = true
		}
		seen := map[string]bool{}
		for _, key := range a.Equipped {
			if !keys[key] || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
		}
		if p := a.Application; p != nil {
			b, ok := s.Settings.Badge(p.Key)
			if !ok || b.Kind != "verified" || !slices.Contains([]string{"pending", "approved", "rejected", "revoked"}, p.Status) || !TextOK(p.Label, 1, 30) || !TextOK(p.Evidence, 1, 1000) || !TextOK(p.Reason, 0, 200) || p.UpdatedAt.IsZero() {
				return ErrInvalid
			}
		}
		for _, h := range a.History {
			if h.ActorID < 1 || h.CreatedAt.IsZero() || !TextOK(h.Reason, 0, 200) || !slices.Contains([]string{"grant", "revoke", "approve", "reject", "apply", "equip"}, h.Action) {
				return ErrInvalid
			}
		}
	}
	for key, t := range s.Tips {
		if key != TipKey(t.UserID, t.RequestID) || t.UserID < 1 || t.AuthorID < 1 || t.UserID == t.AuthorID || t.PostID < 1 || !virtualcurrency.ValidRequestID(t.RequestID) || !virtualcurrency.ValidCurrencyKey(t.Currency) || t.Amount <= 0 || t.EntryID < 0 || t.CurrencyVersion < 1 || t.Precision < 0 || t.Precision > 6 || !TextOK(t.Symbol, 1, 12) || t.CreatedAt.IsZero() {
			return ErrInvalid
		}
	}
	return nil
}
func TipKey(user int64, request string) string { return strconv.FormatInt(user, 10) + ":" + request }
func (s State) Identity(user int64) Identity {
	if a, ok := s.Identities[strconv.FormatInt(user, 10)]; ok {
		return a
	}
	return Identity{Version: 1, Grants: []Grant{}, Equipped: []string{}, History: []Event{}}
}

// PublicBadges returns only active awards and explicit public labels.
func (s State) PublicBadges(user int64, now time.Time) []Badge {
	a := s.Identity(user)
	result := []Badge{}
	counts := map[string]int{}
	for _, b := range s.Settings.Badges {
		if !b.Enabled || b.Kind == "verified" && !s.Settings.VerificationEnabled {
			continue
		}
		for _, g := range a.Grants {
			if g.Key != b.Key || !g.Active(now) || b.Kind == "medal" && (!slices.Contains(a.Equipped, b.Key) || counts["medal"] >= s.Settings.MaxMedals) || b.Kind != "medal" && counts[b.Kind] > 0 {
				continue
			}
			if g.Label != "" {
				b.Description = g.Label
			}
			result = append(result, b)
			counts[b.Kind]++
		}
	}
	return result
}
