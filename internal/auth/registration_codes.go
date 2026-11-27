package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxRegistrationCodes = 5000
const MaxRegistrationCodeBatch = 500

var (
	ErrRegistrationCodeInvalid  = errors.New("注册码无效、未生效、已停用、已过期或次数已用完")
	ErrRegistrationCodeConflict = errors.New("注册码已变更或重复，请刷新列表后重试")
	ErrRegistrationCodeLimit    = errors.New("注册码最多保存 5000 个，请先清理不再使用的注册码")
	registrationCodePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{4,128}$`)
	registrationPrefixPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{0,24}$`)
)

// RegistrationCode is private inventory. Only an authorized administrator may
// retrieve Code; it must never be included in public users, audit or diagnostics.
type RegistrationCode struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	RegistrationCodeRules
	Used       int64      `json:"used"`
	Version    int64      `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type RegistrationCodeRules struct {
	Enabled   bool       `json:"enabled"`
	MaxUses   int64      `json:"max_uses"`
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Note      string     `json:"note"`
}

type RegistrationCodeTarget struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// Omitted patch fields are preserved. An empty date string removes that bound.
type RegistrationCodePatch struct {
	Enabled   *bool   `json:"enabled,omitempty"`
	MaxUses   *int64  `json:"max_uses,omitempty"`
	StartsAt  *string `json:"starts_at,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
	Note      *string `json:"note,omitempty"`
}

type RegistrationCodeMutation struct {
	Action    string                    `json:"action"`
	Codes     []string                  `json:"codes,omitempty"`
	Targets   []RegistrationCodeTarget  `json:"targets,omitempty"`
	Rules     RegistrationCodeRules     `json:"rules"`
	Patch     RegistrationCodePatch     `json:"patch"`
	Generator RegistrationCodeGenerator `json:"generator,omitempty"`
}

type RegistrationCodeGenerator struct {
	Count   int    `json:"count"`
	Prefix  string `json:"prefix"`
	Length  int    `json:"length"`
	Charset string `json:"charset"`
	Group   int    `json:"group"`
}

type RegistrationCodeStore interface {
	ListRegistrationCodes(context.Context) ([]RegistrationCode, error)
	MutateRegistrationCodes(context.Context, RegistrationCodeMutation, string, time.Time) ([]RegistrationCode, error)
}

func (m *Manager) ListRegistrationCodes(ctx context.Context) ([]RegistrationCode, error) {
	store, ok := m.users.(RegistrationCodeStore)
	if !ok {
		return nil, ErrUserStoreNotAvailable
	}
	return store.ListRegistrationCodes(ctx)
}

func (m *Manager) MutateRegistrationCodes(ctx context.Context, input RegistrationCodeMutation, actor string) ([]RegistrationCode, error) {
	store, ok := m.users.(RegistrationCodeStore)
	if !ok {
		return nil, ErrUserStoreNotAvailable
	}
	return store.MutateRegistrationCodes(ctx, input, actor, m.now())
}

func RegistrationCodeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func codeField(key, message string) error {
	return &UserValidationError{Fields: map[string]string{key: message}}
}

func GenerateRegistrationCodes(g RegistrationCodeGenerator) ([]string, error) {
	return generateRegistrationCodes(g, nil, rand.Reader)
}

func generateRegistrationCodes(g RegistrationCodeGenerator, excluded map[string]bool, random io.Reader) ([]string, error) {
	alphabet := map[string]string{
		"friendly": "ABCDEFGHJKLMNPQRSTUVWXYZ23456789",
		"mixed":    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789",
		"digits":   "0123456789", "hex": "0123456789ABCDEF",
	}[g.Charset]
	if g.Count < 1 || g.Count > MaxRegistrationCodeBatch {
		return nil, codeField("count", "每批可生成 1 至 500 个注册码")
	}
	if g.Length < 6 || g.Length > 64 {
		return nil, codeField("length", "随机部分长度须为 6 至 64 位")
	}
	if !registrationPrefixPattern.MatchString(g.Prefix) {
		return nil, codeField("prefix", "前缀最多 24 位，只支持英文字母、数字、横线和下划线")
	}
	if alphabet == "" || g.Group < 0 || g.Group > 16 {
		return nil, codeField("charset", "请选择有效的字符格式和分组长度（0 至 16）")
	}
	items := make([]string, 0, g.Count)
	seen := map[string]bool{}
	for attempts := 0; len(items) < g.Count && attempts < g.Count*10; attempts++ {
		var b strings.Builder
		b.WriteString(g.Prefix)
		for i := 0; i < g.Length; i++ {
			if g.Group > 0 && i > 0 && i%g.Group == 0 {
				b.WriteByte('-')
			}
			n, err := rand.Int(random, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return nil, err
			}
			b.WriteByte(alphabet[n.Int64()])
		}
		code := b.String()
		if !registrationCodePattern.MatchString(code) {
			return nil, codeField("length", "含前缀和分隔符的总长度不能超过 128 位")
		}
		if !seen[code] && !excluded[code] {
			seen[code] = true
			items = append(items, code)
		}
	}
	if len(items) != g.Count {
		return nil, ErrRegistrationCodeConflict
	}
	return items, nil
}

func normalizeCodeRules(r RegistrationCodeRules) (RegistrationCodeRules, error) {
	r.Note = strings.TrimSpace(r.Note)
	if !utf8.ValidString(r.Note) || utf8.RuneCountInString(r.Note) > 200 || strings.ContainsAny(r.Note, "\x00\r\n") {
		return r, codeField("note", "备注最多 200 字且不能换行")
	}
	if r.MaxUses < 0 || r.MaxUses > 1000000 {
		return r, codeField("max_uses", "有效次数须为 0 至 1000000，0 表示不限次数")
	}
	for _, p := range []**time.Time{&r.StartsAt, &r.ExpiresAt} {
		if *p != nil {
			t := (*p).UTC().Truncate(time.Second)
			if t.Year() < 2000 || t.Year() > 9999 {
				return r, codeField("time", "请选择有效的时间（2000 至 9999 年）")
			}
			*p = &t
		}
	}
	if r.StartsAt != nil && r.ExpiresAt != nil && !r.ExpiresAt.After(*r.StartsAt) {
		return r, codeField("expires_at", "到期时间必须晚于生效时间")
	}
	return r, nil
}

func patchCodeRules(r RegistrationCodeRules, p RegistrationCodePatch) (RegistrationCodeRules, error) {
	if p.Enabled != nil {
		r.Enabled = *p.Enabled
	}
	if p.MaxUses != nil {
		r.MaxUses = *p.MaxUses
	}
	if p.Note != nil {
		r.Note = *p.Note
	}
	for _, field := range []struct {
		raw  *string
		dest **time.Time
	}{{p.StartsAt, &r.StartsAt}, {p.ExpiresAt, &r.ExpiresAt}} {
		if field.raw == nil {
			continue
		}
		*field.dest = nil
		if *field.raw != "" {
			t, err := time.Parse(time.RFC3339, *field.raw)
			if err != nil {
				return r, codeField("time", "时间格式不正确")
			}
			*field.dest = &t
		}
	}
	return normalizeCodeRules(r)
}

func CloneRegistrationCodes(items []RegistrationCode) []RegistrationCode {
	out := make([]RegistrationCode, len(items))
	for i, c := range items {
		c.StartsAt = cloneTime(c.StartsAt)
		c.ExpiresAt = cloneTime(c.ExpiresAt)
		c.LastUsedAt = cloneTime(c.LastUsedAt)
		out[i] = c
	}
	return out
}

func RegistrationCodeStatus(c RegistrationCode, now time.Time) string {
	if !c.Enabled {
		return "disabled"
	}
	if c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) {
		return "expired"
	}
	if c.MaxUses > 0 && c.Used >= c.MaxUses {
		return "exhausted"
	}
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return "scheduled"
	}
	return "active"
}

func ValidRegistrationCode(c RegistrationCode) bool {
	r, err := normalizeCodeRules(c.RegistrationCodeRules)
	return err == nil && r.Note == c.Note && registrationCodePattern.MatchString(c.Code) && validCodeRecordID(c.ID) && c.Used >= 0 && (c.MaxUses == 0 || c.Used <= c.MaxUses) && c.Version > 0 && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt) && (c.LastUsedAt == nil && c.Used == 0 || c.LastUsedAt != nil && c.Used > 0 && !c.LastUsedAt.Before(c.CreatedAt))
}

// Both stores call this while holding their inventory lock. Validation finishes
// before any mutation is committed, including stale-version checks for a batch.
func ApplyRegistrationCodeMutation(current []RegistrationCode, input RegistrationCodeMutation, now time.Time) ([]RegistrationCode, []RegistrationCode, error) {
	now = now.UTC().Truncate(time.Second)
	items := CloneRegistrationCodes(current)
	index := map[string]int{}
	codeValues := map[string]bool{}
	for i, c := range items {
		index[c.ID] = i
		codeValues[c.Code] = true
	}
	affected := []RegistrationCode{}
	switch input.Action {
	case "create", "generate":
		if input.Action == "generate" {
			if len(input.Codes) > 0 || len(input.Targets) > 0 {
				return nil, nil, codeField("generator", "生成请求不能同时包含导入或修改数据")
			}
			// Generation shares the store's inventory lock, including retries for
			// collisions with both imported codes and concurrent generated batches.
			codes, err := generateRegistrationCodes(input.Generator, codeValues, rand.Reader)
			if err != nil {
				return nil, nil, err
			}
			input.Codes = codes
		}
		if len(input.Codes) < 1 || len(input.Codes) > MaxRegistrationCodeBatch || len(input.Targets) > 0 {
			return nil, nil, codeField("codes", "每批可导入或生成 1 至 500 个注册码")
		}
		if len(items)+len(input.Codes) > MaxRegistrationCodes {
			return nil, nil, ErrRegistrationCodeLimit
		}
		rules, err := normalizeCodeRules(input.Rules)
		if err != nil {
			return nil, nil, err
		}
		for _, raw := range input.Codes {
			code := strings.TrimSpace(raw)
			if !registrationCodePattern.MatchString(code) {
				return nil, nil, codeField("codes", "注册码须为 4 至 128 位英文字母、数字、横线或下划线，区分大小写")
			}
			if codeValues[code] {
				return nil, nil, ErrRegistrationCodeConflict
			}
			var identity [16]byte
			if _, err := rand.Read(identity[:]); err != nil {
				return nil, nil, err
			}
			id := hex.EncodeToString(identity[:])
			if _, exists := index[id]; exists {
				return nil, nil, ErrRegistrationCodeConflict
			}
			codeValues[code] = true
			c := RegistrationCode{ID: id, Code: code, RegistrationCodeRules: rules, Version: 1, CreatedAt: now, UpdatedAt: now}
			index[id] = len(items)
			items = append(items, c)
			affected = append(affected, c)
		}
	case "update", "delete":
		if len(input.Targets) < 1 || len(input.Targets) > MaxRegistrationCodeBatch || len(input.Codes) > 0 {
			return nil, nil, codeField("targets", "每批请选择 1 至 500 个注册码")
		}
		p := input.Patch
		if input.Action == "update" && p.Enabled == nil && p.MaxUses == nil && p.StartsAt == nil && p.ExpiresAt == nil && p.Note == nil {
			return nil, nil, codeField("patch", "请至少选择一项要修改的设置")
		}
		seen := map[string]bool{}
		for _, target := range input.Targets {
			i, ok := index[target.ID]
			if !ok || seen[target.ID] || target.Version != items[i].Version {
				return nil, nil, ErrRegistrationCodeConflict
			}
			seen[target.ID] = true
			c := items[i]
			if input.Action == "update" {
				rules, err := patchCodeRules(c.RegistrationCodeRules, input.Patch)
				if err != nil {
					return nil, nil, err
				}
				if rules.MaxUses > 0 && rules.MaxUses < c.Used {
					return nil, nil, codeField("max_uses", "有效总次数不能小于已使用次数；填写 0 可设为不限次数")
				}
				c.RegistrationCodeRules = rules
				c.Version++
				c.UpdatedAt = now
				items[i] = c
			}
			affected = append(affected, c)
		}
		if input.Action == "delete" {
			kept := []RegistrationCode{}
			for _, c := range items {
				if !seen[c.ID] {
					kept = append(kept, c)
				}
			}
			items = kept
		}
	default:
		return nil, nil, codeField("action", "不支持的注册码操作")
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, CloneRegistrationCodes(affected), nil
}

func ConsumeRegistrationCode(current []RegistrationCode, hash string, now time.Time) ([]RegistrationCode, error) {
	if hash == "" {
		return current, nil
	}
	for i, c := range current {
		if RegistrationCodeHash(c.Code) != hash {
			continue
		}
		if RegistrationCodeStatus(c, now) != "active" {
			return nil, ErrRegistrationCodeInvalid
		}
		items := CloneRegistrationCodes(current)
		now = now.UTC().Truncate(time.Second)
		c.Used++
		c.Version++
		c.UpdatedAt = now
		c.LastUsedAt = &now
		items[i] = c
		return items, nil
	}
	return nil, ErrRegistrationCodeInvalid
}

func validCodeRecordID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
