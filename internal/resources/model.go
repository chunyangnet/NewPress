// Package resources owns private article deliveries and composable access rules.
// Only Public variants may cross the public HTTP boundary; configuration contains secrets.
package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/packagefs"
)

var ErrInvalid = errors.New("invalid resource configuration")
var ErrLimit = errors.New("resource limit reached")
var ErrUnavailable = errors.New("resource storage unavailable")

const MetaKey = "article_resources"
const MaxConfigBytes = 1 << 20

type Config struct {
	Items []Resource `json:"items"`
}
type Resource struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Cover         string     `json:"cover"`
	Size          string     `json:"size"`
	Updated       string     `json:"updated"`
	OriginalPrice string     `json:"original_price"`
	Badge         string     `json:"badge"`
	Features      []string   `json:"features"`
	MemberCTA     bool       `json:"member_cta"`
	Selection     string     `json:"selection"` // all or first matching version
	DailyLimit    int        `json:"daily_limit"`
	TotalLimit    int        `json:"total_limit"`
	StartsAt      *time.Time `json:"starts_at,omitempty"`
	EndsAt        *time.Time `json:"ends_at,omitempty"`
	Variants      []Variant  `json:"variants"`
}
type Variant struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	Body           string     `json:"body"`
	MediaURL       string     `json:"media_url"`
	Downloads      []Download `json:"downloads"`
	Groups         []Group    `json:"groups"`
	Questions      []Question `json:"questions"`
	PassPercent    int        `json:"pass_percent"`
	AttemptsPerDay int        `json:"attempts_per_day"`
	Fields         []Field    `json:"fields"`
}
type Download struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Code     string `json:"code"`
	Password string `json:"password"`
}

// Groups are alternatives. Every condition within the chosen group is required.
type Group struct {
	Name       string      `json:"name"`
	Conditions []Condition `json:"conditions"`
}
type Condition struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	Values   []string `json:"values"`
	Amount   int64    `json:"amount"` // smallest currency units
	Currency string   `json:"currency"`
	Minimum  int      `json:"minimum"`
	Term     string   `json:"term"` // monthly, annual, any
}
type Question struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Options []string `json:"options"`
	Answer  string   `json:"answer"`
}
type Field struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options"`
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func ValidID(s string) bool { return idPattern.MatchString(s) }
func SafeURL(s string) bool {
	if len(s) > 2048 || strings.ContainsAny(s, "\\\r\n\x00") {
		return false
	}
	u, e := url.Parse(s)
	if e != nil || u.User != nil {
		return false
	}
	return (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" || u.Scheme == "" && u.Host == "" && strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//")
}
func Decode(raw []byte) (Config, error) {
	var c Config
	if len(raw) > MaxConfigBytes || strings.TrimSpace(string(raw)) == "null" {
		return c, ErrInvalid
	}
	if err := packagefs.ValidateUniqueJSONFor(raw, &c); err != nil {
		return c, ErrInvalid
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if e := d.Decode(&c); e != nil {
		return c, ErrInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, ErrInvalid
	}
	return Normalize(c)
}
func FromMeta(meta map[string]any) Config {
	raw, e := json.Marshal(meta[MetaKey])
	if e != nil || string(raw) == "null" {
		return Config{Items: []Resource{}}
	}
	c, e := Decode(raw)
	if e != nil {
		return Config{Items: []Resource{}}
	}
	return c
}
func Normalize(c Config) (Config, error) {
	raw, e := json.Marshal(c)
	if e != nil || len(raw) > MaxConfigBytes || len(c.Items) > 24 {
		return c, ErrInvalid
	}
	// Own every slice before normalizing, including caller-owned answers and fields.
	_ = json.Unmarshal(raw, &c)
	ids := map[string]bool{}
	for ri := range c.Items {
		r := &c.Items[ri]
		if !ValidID(r.ID) || ids[r.ID] || strings.TrimSpace(r.Title) == "" || len([]rune(r.Title)) > 120 || len(r.Description) > 2000 || r.Cover != "" && !SafeURL(r.Cover) || r.DailyLimit < 0 || r.DailyLimit > 10000 || r.TotalLimit < 0 || r.TotalLimit > 1000000 || len(r.Features) > 8 || len(r.Variants) < 1 || len(r.Variants) > 12 {
			return c, ErrInvalid
		}
		ids[r.ID] = true
		if r.Selection == "" {
			r.Selection = "all"
		}
		if r.Selection != "all" && r.Selection != "first" {
			return c, ErrInvalid
		}
		if r.StartsAt != nil && r.EndsAt != nil && !r.EndsAt.After(*r.StartsAt) {
			return c, ErrInvalid
		}
		if len(r.Size) > 80 || len(r.Updated) > 40 || len(r.OriginalPrice) > 40 || len(r.Badge) > 60 {
			return c, ErrInvalid
		}
		for _, f := range r.Features {
			if len(f) > 120 {
				return c, ErrInvalid
			}
		}
		vids := map[string]bool{}
		for vi := range r.Variants {
			v := &r.Variants[vi]
			if !ValidID(v.ID) || vids[v.ID] || strings.TrimSpace(v.Name) == "" || len(v.Name) > 180 || len(v.Body) > 256<<10 || len(v.Downloads) > 12 || len(v.Groups) < 1 || len(v.Groups) > 12 {
				return c, ErrInvalid
			}
			vids[v.ID] = true
			switch v.Kind {
			case "content", "download", "image", "video":
			default:
				return c, ErrInvalid
			}
			if v.Kind == "download" && len(v.Downloads) == 0 || (v.Kind == "image" || v.Kind == "video") && !SafeURL(v.MediaURL) {
				return c, ErrInvalid
			}
			v.Body = contenthtml.Sanitize(v.Body)
			links := map[string]bool{}
			for _, d := range v.Downloads {
				if !ValidID(d.ID) || links[d.ID] || !SafeURL(d.URL) || len(d.Name) > 180 || len(d.Code) > 128 || len(d.Password) > 128 || !ValidProvider(d.Provider) {
					return c, ErrInvalid
				}
				links[d.ID] = true
			}
			conditions := map[string]bool{}
			for _, g := range v.Groups {
				if len(g.Name) > 180 || len(g.Conditions) < 1 || len(g.Conditions) > 16 {
					return c, ErrInvalid
				}
				for _, x := range g.Conditions {
					if !ValidID(x.ID) || conditions[x.ID] || len(x.Label) > 180 || len(x.Values) > 50 || x.Minimum < 0 || x.Minimum > 1000000 || x.Amount < 0 || x.Amount > 1000000000000 {
						return c, ErrInvalid
					}
					conditions[x.ID] = true
					for _, value := range x.Values {
						if !ValidID(value) {
							return c, ErrInvalid
						}
					}
					switch x.Kind {
					case "public", "guest", "login", "follow", "like", "comment":
					case "level":
						if x.Minimum < 1 || x.Minimum > 50 {
							return c, ErrInvalid
						}
					case "membership":
						if x.Term != "" && x.Term != "any" && x.Term != "monthly" && x.Term != "annual" {
							return c, ErrInvalid
						}
					case "group":
						if len(x.Values) == 0 {
							return c, ErrInvalid
						}
					case "points", "payment", "tip":
						if x.Amount < 1 || !ValidID(x.Currency) {
							return c, ErrInvalid
						}
						if x.Kind == "payment" && !strings.Contains(" CNY USD EUR GBP HKD JPY LDC ", " "+x.Currency+" ") {
							return c, ErrInvalid
						}
					case "quiz":
						if len(v.Questions) == 0 {
							return c, ErrInvalid
						}
					case "form":
						if len(v.Fields) == 0 {
							return c, ErrInvalid
						}
					default:
						return c, ErrInvalid
					}
				}
			}
			if len(v.Questions) > 20 || len(v.Fields) > 20 || v.PassPercent < 0 || v.PassPercent > 100 || v.AttemptsPerDay < 0 || v.AttemptsPerDay > 100 {
				return c, ErrInvalid
			}
			if v.PassPercent == 0 {
				v.PassPercent = 100
			}
			if v.AttemptsPerDay == 0 {
				v.AttemptsPerDay = 3
			}
			qids := map[string]bool{}
			for _, q := range v.Questions {
				if !ValidID(q.ID) || qids[q.ID] || q.Title == "" || len(q.Title) > 500 || len(q.Options) > 10 || strings.TrimSpace(q.Answer) == "" || len(q.Answer) > 500 {
					return c, ErrInvalid
				}
				qids[q.ID] = true
				if len(q.Options) > 0 && !Contains(q.Options, q.Answer) {
					return c, ErrInvalid
				}
				for _, o := range q.Options {
					if len(o) > 500 {
						return c, ErrInvalid
					}
				}
			}
			fids := map[string]bool{}
			for _, f := range v.Fields {
				if !ValidID(f.ID) || fids[f.ID] || f.Label == "" || len(f.Label) > 300 || len(f.Options) > 30 {
					return c, ErrInvalid
				}
				fids[f.ID] = true
				switch f.Type {
				case "text", "email", "textarea", "checkbox":
				case "select":
					if len(f.Options) == 0 {
						return c, ErrInvalid
					}
				default:
					return c, ErrInvalid
				}
				for _, o := range f.Options {
					if len(o) > 300 {
						return c, ErrInvalid
					}
				}
			}
		}
	}
	return c, nil
}
func Contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}
func ValidProvider(s string) bool {
	return Contains([]string{"direct", "baidu", "aliyun", "quark", "lanzou", "115", "123pan", "onedrive", "google", "dropbox"}, s)
}
func ProviderName(s string) string {
	names := map[string]string{"direct": "直接下载", "baidu": "百度网盘", "aliyun": "阿里云盘", "quark": "夸克网盘", "lanzou": "蓝奏云", "115": "115 网盘", "123pan": "123 云盘", "onedrive": "OneDrive", "google": "Google Drive", "dropbox": "Dropbox"}
	return names[s]
}
func Hash(parts ...any) string {
	b, _ := json.Marshal(parts)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func Scope(post int64, r Resource, v Variant) string {
	return fmt.Sprintf("%d:%s:%s", post, r.ID, v.ID)
}
func ConditionKey(post int64, r Resource, v Variant, c Condition) string {
	return Hash(Scope(post, r, v), c.ID, c.Kind, c.Currency, c.Amount)
}

// PurchaseKey identifies the purchased condition. Price and currency belong to
// the immutable receipt, so editing them cannot revoke an existing purchase.
// ConditionKey remains unchanged for verifying receipts written before v2.
func PurchaseKey(post int64, r Resource, v Variant, c Condition) string {
	return Hash("resource-purchase-v2", Scope(post, r, v), c.ID, c.Kind)
}
func SubmissionKey(actor string, post int64, r Resource, v Variant) string {
	return Hash(actor, Scope(post, r, v), v.Questions, v.Fields, v.PassPercent)
}
func Available(r Resource, now time.Time) bool {
	return (r.StartsAt == nil || !now.Before(*r.StartsAt)) && (r.EndsAt == nil || now.Before(*r.EndsAt))
}
