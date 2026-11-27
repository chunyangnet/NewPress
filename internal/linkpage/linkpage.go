// Package linkpage owns theme-provided link directories, independently of plugins.
package linkpage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/safehttp"
)

const (
	MaxBoards        = 64
	MaxBoardBytes    = 8 << 20
	MaxDocumentBytes = 64 << 20
)

var (
	ErrInvalid   = errors.New("请检查名称、网址、样式和图标格式")
	ErrNotFound  = errors.New("友链页面不存在")
	ErrConflict  = errors.New("内容已更新，请重新打开页面核对；当前输入仍保留")
	ErrChanged   = errors.New("数据文件已被外部修改，请重新启动后再编辑")
	ErrForbidden = errors.New("没有管理友链页面的权限")
	ErrCapacity  = errors.New("已达到友链存储上限")
	slugPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

type Settings struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Style       string `json:"style"`
	Accent      string `json:"accent"`
	Enabled     bool   `json:"enabled"`
	Search      bool   `json:"search"`
	Switcher    bool   `json:"switcher"`
	NewTab      bool   `json:"new_tab"`
}
type Item struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Logo        string `json:"logo,omitempty"`     // normalized PNG, base64; served only through the local icon route
	LogoURL     string `json:"logo_url,omitempty"` // stable media-library URL; legacy inline icons remain readable
	Order       int    `json:"order"`
	Enabled     bool   `json:"enabled"`
	Featured    bool   `json:"featured"`
}
type Receipt struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
	Result Result `json:"result"`
}
type Board struct {
	Slug      string    `json:"slug"`
	Version   int64     `json:"version"`
	Settings  Settings  `json:"settings"`
	Items     []Item    `json:"items,omitempty"`
	Receipts  []Receipt `json:"receipts,omitempty"`
	NextID    int64     `json:"next_id"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Actor struct {
	ID    int64
	Admin bool
}
type Command struct {
	Action         string    `json:"action"`
	Key            string    `json:"key"`
	Version        int64     `json:"version"`
	ExpectedUserID int64     `json:"expected_user_id"`
	Settings       *Settings `json:"settings,omitempty"`
	Item           *Item     `json:"item,omitempty"`
	ItemID         int64     `json:"item_id,omitempty"`
}
type Result struct {
	Slug    string `json:"slug"`
	Version int64  `json:"version"`
	ItemID  int64  `json:"item_id"`
	Replay  bool   `json:"replay"`
}
type Store interface {
	Ready(context.Context) error
	List(context.Context) ([]Board, error)
	Get(context.Context, string) (Board, error)
	Apply(context.Context, string, Actor, Command, time.Time) (Result, error)
}

func Defaults() Settings {
	return Settings{Name: "友链", Description: "独立的站点，有趣的人。沿着这些链接，去看看更大的世界。", Style: "cards", Accent: "teal", Enabled: true, Search: true, Switcher: true, NewTab: true}
}
func ValidSlug(s string) bool { return slugPattern.MatchString(s) }
func textOK(s string, max int, required bool) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && (!required || strings.TrimSpace(s) != "") && !strings.ContainsAny(s, "\x00\r")
}
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if len(raw) > 2048 {
		return "", ErrInvalid
	}
	u, e := safehttp.ValidateURL(raw)
	if e != nil {
		return "", e
	}
	host := strings.ToLower(u.Hostname())
	if !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return "", ErrInvalid
	}
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
func Host(raw string) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return ""
	}
	return u.Hostname()
}
func Validate(b Board) error {
	s := b.Settings
	if !ValidSlug(b.Slug) || b.Version < 1 || b.NextID < 1 || !textOK(s.Name, 60, true) || !textOK(s.Description, 500, false) || !slices.Contains([]string{"cards", "list", "wall"}, s.Style) || !slices.Contains([]string{"teal", "blue", "amber"}, s.Accent) || len(b.Items) > 200 || len(b.Receipts) > 128 {
		return ErrInvalid
	}
	ids := map[int64]bool{}
	urls := map[string]bool{}
	for _, i := range b.Items {
		u, e := NormalizeURL(i.URL)
		if e != nil || u != i.URL || i.ID < 1 || i.ID >= b.NextID || ids[i.ID] || urls[u] || !textOK(i.Name, 100, true) || !textOK(i.Description, 500, false) || !textOK(i.Category, 30, false) || i.Order < 0 || i.Order > 9999 {
			return ErrInvalid
		}
		if i.Logo != "" {
			if _, e := DecodeLogo(i.Logo); e != nil {
				return e
			}
		}
		if i.LogoURL != "" && (!mediaLogoPattern.MatchString(i.LogoURL) || i.Logo != "") {
			return ErrInvalid
		}
		ids[i.ID] = true
		urls[u] = true
	}
	return nil
}

var mediaLogoPattern = regexp.MustCompile(`^/media/[1-9][0-9]*/(?:[A-Za-z0-9._~()-]|%[A-Fa-f0-9]{2})+\.(?:png|jpe?g|gif|webp)$`)

func DecodeBoard(b []byte) (Board, error) {
	v, e := packagefs.DecodeJSON[Board](bytes.NewReader(b), MaxBoardBytes, "link directory")
	if e == nil {
		e = Validate(v)
	}
	return v, e
}
func Clone(b Board) Board {
	b.Items = slices.Clone(b.Items)
	b.Receipts = slices.Clone(b.Receipts)
	return b
}
func Header(b Board) Board { b.Items = nil; b.Receipts = nil; return b }
func Mutate(old *Board, slug string, a Actor, c Command, now time.Time) (Board, Result, error) {
	fail := func(e error) (Board, Result, error) { return Board{}, Result{}, e }
	if !a.Admin || a.ID <= 0 {
		return fail(ErrForbidden)
	}
	if !ValidSlug(slug) || len(c.Key) < 8 || len(c.Key) > 100 || c.ExpectedUserID != a.ID {
		return fail(ErrInvalid)
	}
	raw, _ := json.Marshal(c)
	h := sha256.Sum256(raw)
	digest := hex.EncodeToString(h[:])
	b := Board{Slug: slug, Version: 0, NextID: 1, Settings: Defaults()}
	if old != nil {
		b = Clone(*old)
		for _, r := range b.Receipts {
			if r.Key == c.Key {
				if r.Digest != digest {
					return fail(ErrConflict)
				}
				v := r.Result
				v.Replay = true
				return b, v, nil
			}
		}
	}
	if c.Version != b.Version {
		return fail(ErrConflict)
	}
	if old == nil && c.Action != "create" {
		return fail(ErrNotFound)
	}
	result := Result{Slug: slug, Version: b.Version + 1}
	switch c.Action {
	case "create", "configure":
		if c.Settings == nil || c.Item != nil || c.ItemID != 0 || (c.Action == "create" && old != nil) {
			return fail(ErrInvalid)
		}
		b.Settings = *c.Settings
	case "save_item":
		if c.Item == nil || c.Settings != nil || c.ItemID != 0 {
			return fail(ErrInvalid)
		}
		i := *c.Item
		var e error
		i.URL, e = NormalizeURL(i.URL)
		if e != nil {
			return fail(e)
		}
		if i.Logo != "" {
			data, e := DecodeLogo(i.Logo)
			if e != nil {
				return fail(e)
			}
			i.Logo, e = NormalizeLogo(data)
			if e != nil {
				return fail(e)
			}
		}
		if i.ID == 0 {
			i.ID = b.NextID
			b.NextID++
			b.Items = append(b.Items, i)
		} else {
			index := slices.IndexFunc(b.Items, func(v Item) bool { return v.ID == i.ID })
			if index < 0 {
				return fail(ErrNotFound)
			}
			b.Items[index] = i
		}
		result.ItemID = i.ID
	case "delete_item":
		if c.Item != nil || c.Settings != nil || c.ItemID < 1 {
			return fail(ErrInvalid)
		}
		index := slices.IndexFunc(b.Items, func(v Item) bool { return v.ID == c.ItemID })
		if index < 0 {
			return fail(ErrNotFound)
		}
		b.Items = slices.Delete(b.Items, index, index+1)
	default:
		return fail(ErrInvalid)
	}
	b.Version++
	b.UpdatedAt = now
	b.Receipts = append(b.Receipts, Receipt{c.Key, digest, result})
	if len(b.Receipts) > 128 {
		b.Receipts = b.Receipts[len(b.Receipts)-128:]
	}
	if e := Validate(b); e != nil {
		return fail(e)
	}
	encoded, _ := json.Marshal(b)
	if len(encoded) > MaxBoardBytes {
		return fail(ErrCapacity)
	}
	return b, result, nil
}
