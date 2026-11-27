// Package articlewidgets owns the portable, versioned article component format.
// Content is data: authors cannot supply executable markup, CSS or frame URLs.
package articlewidgets

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxConfigBytes = 64 << 10

var ErrInvalid = errors.New("组件配置无效或超出限制")
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{12,64}$`)
var languagePattern = regexp.MustCompile(`^[a-zA-Z0-9_+.#-]{0,40}$`)
var numericID = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
var githubID = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)
var youtubeID = regexp.MustCompile(`^[a-zA-Z0-9_-]{11}$`)
var bilibiliID = regexp.MustCompile(`^(BV[a-zA-Z0-9]{10}|av[1-9][0-9]{0,18})$`)
var amountPattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,6})?$`)
var currencyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

var Kinds = strings.Fields("callout quote code download gift redpacket gallery image-card audio video table accordion tabs card badges progress counter poll rating compare carousel annotation glossary ai mention chart")

type Item struct {
	Title string  `json:"title,omitempty"`
	Text  string  `json:"text,omitempty"`
	URL   string  `json:"url,omitempty"`
	Image string  `json:"image,omitempty"`
	Value float64 `json:"value,omitempty"`
}

type Config struct {
	Version  int        `json:"v"`
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Title    string     `json:"title,omitempty"`
	Sender   string     `json:"sender,omitempty"`
	Text     string     `json:"text,omitempty"`
	URL      string     `json:"url,omitempty"`
	Image    string     `json:"image,omitempty"`
	Caption  string     `json:"caption,omitempty"`
	Tone     string     `json:"tone,omitempty"`
	Style    string     `json:"style,omitempty"`
	Width    string     `json:"width,omitempty"`
	Density  string     `json:"density,omitempty"`
	Layout   string     `json:"layout,omitempty"`
	Language string     `json:"language,omitempty"`
	Provider string     `json:"provider,omitempty"`
	UID      string     `json:"uid,omitempty"`
	Mode     string     `json:"mode,omitempty"`
	Unit     string     `json:"unit,omitempty"`
	Amount   string     `json:"amount,omitempty"`
	Currency string     `json:"currency,omitempty"`
	Value    float64    `json:"value,omitempty"`
	Max      float64    `json:"max,omitempty"`
	Count    int        `json:"count,omitempty"`
	Columns  int        `json:"columns,omitempty"`
	Open     bool       `json:"open,omitempty"`
	Loop     bool       `json:"loop,omitempty"`
	Items    []Item     `json:"items,omitempty"`
	Rows     [][]string `json:"rows,omitempty"`
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func choice(value, choices string) bool {
	return value == "" || contains(strings.Fields(choices), value)
}
func textOK(value string, limit int) bool {
	return utf8.ValidString(value) && len(value) <= limit && !strings.ContainsRune(value, 0)
}
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= 1e12
}

// LocalURL includes object-storage delivery URLs served through this origin.
func LocalURL(value string) bool {
	if len(value) > 2048 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n\t <>\"'") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil && !strings.HasPrefix(u.Path, "//")
}

func LinkURL(value string) bool {
	if LocalURL(value) {
		return true
	}
	if len(value) > 2048 || strings.ContainsAny(value, "\\\r\n\t <>\"'") {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func (c Config) Validate() error {
	if c.Version != 1 || !idPattern.MatchString(c.ID) || !contains(Kinds, c.Kind) {
		return ErrInvalid
	}
	for _, v := range []string{c.Title, c.Sender, c.Caption, c.Unit} {
		if !textOK(v, 600) {
			return ErrInvalid
		}
	}
	if !textOK(c.Text, 48<<10) || len(c.Items) > 30 || len(c.Rows) > 50 || !finite(c.Value) || !finite(c.Max) || c.Count < 0 || c.Count > 10000 || c.Columns < 0 || c.Columns > 6 {
		return ErrInvalid
	}
	if !choice(c.Tone, "theme blue teal amber green red purple gray") || !choice(c.Style, "soft outline solid") || !choice(c.Width, "full reading compact") || !choice(c.Density, "normal compact airy") || !choice(c.Layout, "grid mosaic list bar line radar") || !languagePattern.MatchString(c.Language) || !choice(c.Provider, "local netease youtube bilibili github site") || !choice(c.Mode, "login guest both") {
		return ErrInvalid
	}
	if c.URL != "" && !LinkURL(c.URL) || c.Image != "" && !LocalURL(c.Image) {
		return ErrInvalid
	}
	if len(c.UID) > 64 || len(c.Amount) > 24 || len(c.Currency) > 40 {
		return ErrInvalid
	}
	for _, item := range c.Items {
		if !textOK(item.Title, 400) || !textOK(item.Text, 8000) || !finite(item.Value) || item.URL != "" && !LinkURL(item.URL) || item.Image != "" && !LocalURL(item.Image) {
			return ErrInvalid
		}
	}
	for _, row := range c.Rows {
		if len(row) < 1 || len(row) > 10 || len(row) != len(c.Rows[0]) {
			return ErrInvalid
		}
		for _, cell := range row {
			if !textOK(cell, 1000) {
				return ErrInvalid
			}
		}
	}
	switch c.Kind {
	case "audio", "video":
		if c.Provider == "" || c.Provider == "local" {
			if c.URL != "" && !LocalURL(c.URL) {
				return ErrInvalid
			}
			for _, i := range c.Items {
				if i.URL != "" && !LocalURL(i.URL) {
					return ErrInvalid
				}
			}
		} else if c.Kind == "audio" && c.Provider == "netease" {
			if !numericID.MatchString(c.UID) {
				return ErrInvalid
			}
		} else if c.Kind == "video" && c.Provider == "youtube" {
			if !youtubeID.MatchString(c.UID) {
				return ErrInvalid
			}
		} else if c.Kind == "video" && c.Provider == "bilibili" {
			if !bilibiliID.MatchString(c.UID) {
				return ErrInvalid
			}
		} else {
			return ErrInvalid
		}
	case "mention":
		switch c.Provider {
		case "github":
			if !githubID.MatchString(c.UID) {
				return ErrInvalid
			}
		case "site", "bilibili":
			if !numericID.MatchString(c.UID) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	case "poll":
		if len(c.Items) < 2 || len(c.Items) > 12 || c.Mode == "" {
			return ErrInvalid
		}
		for _, i := range c.Items {
			if strings.TrimSpace(i.Title) == "" {
				return ErrInvalid
			}
		}
	case "rating":
		if c.Mode == "" || c.Max != 5 && c.Max != 10 {
			return ErrInvalid
		}
	case "redpacket":
		if !amountPattern.MatchString(c.Amount) || strings.Trim(c.Amount, "0.") == "" || c.Count < 1 || !currencyPattern.MatchString(c.Currency) || c.Mode != "login" {
			return ErrInvalid
		}
	case "progress":
		if c.Max <= 0 || c.Value < 0 || c.Value > c.Max {
			return ErrInvalid
		}
	case "chart":
		if len(c.Items) < 2 || c.Max <= 0 || !contains([]string{"bar", "line", "radar"}, c.Layout) || c.Layout == "radar" && (len(c.Items) < 3 || len(c.Items) > 12) {
			return ErrInvalid
		}
		for _, i := range c.Items {
			if i.Value < 0 || i.Value > c.Max {
				return ErrInvalid
			}
		}
	case "compare":
		if len(c.Items) != 2 {
			return ErrInvalid
		}
	}
	encoded, err := c.jsonBytes()
	if err != nil || len(encoded) > MaxConfigBytes {
		return ErrInvalid
	}
	return nil
}

func Decode(value string) (Config, error) {
	var c Config
	if len(value) > base64.RawURLEncoding.EncodedLen(MaxConfigBytes) {
		return c, ErrInvalid
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || !utf8.Valid(b) {
		return c, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return Config{}, ErrInvalid
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) jsonBytes() ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// The JSON is carried in base64url, so HTML escaping only inflates code
	// samples and can make a valid browser configuration exceed its budget.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(c); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func (c Config) Encode() string {
	b, _ := c.jsonBytes()
	return base64.RawURLEncoding.EncodeToString(b)
}
func (c Config) Fingerprint() string {
	// Keep the original canonical bytes for existing interaction records.
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ValidID(value string) bool { return idPattern.MatchString(value) }

// Presentation edits do not discard votes or reopen a previously claimed
// packet. Changes to the question, choices or spending terms start a new round.
func (c Config) InteractionFingerprint() string {
	terms := Config{Version: c.Version, ID: c.ID, Kind: c.Kind}
	switch c.Kind {
	case "redpacket":
		terms.Amount, terms.Currency, terms.Count = c.Amount, c.Currency, c.Count
	case "poll":
		terms.Title, terms.Mode = c.Title, c.Mode
		for _, item := range c.Items {
			terms.Items = append(terms.Items, Item{Title: item.Title})
		}
	case "rating":
		terms.Title, terms.Mode, terms.Max = c.Title, c.Mode, c.Max
	default:
		return c.Fingerprint()
	}
	return terms.Fingerprint()
}

func (c Config) ExternalURL() string {
	switch c.Provider {
	case "youtube":
		return "https://www.youtube.com/watch?v=" + c.UID
	case "bilibili":
		if c.Kind == "mention" {
			return "https://space.bilibili.com/" + c.UID
		}
		return "https://www.bilibili.com/video/" + c.UID
	case "netease":
		return "https://music.163.com/#/song?id=" + c.UID
	case "github":
		return "https://github.com/" + c.UID
	case "site":
		return "/authors/" + c.UID + "/"
	default:
		return c.URL
	}
}
