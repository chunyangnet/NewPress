package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxImportItems = 100
const MaxImportMappings = 100000

var ErrImportConflict = errors.New("content import plan changed or contains conflicts")
var importSourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,79}$`)

type ImportEntry struct {
	Key    string
	Digest string
	// MetadataDigest is required for WeChat and covers the source excerpt and
	// cover before local media paths are substituted.
	MetadataDigest string `json:"metadata_digest,omitempty"`
	Input          PostInput
	Categories     []string
	OldPath        string
}
type ImportBatch struct {
	Source string
	Format string
	Items  []ImportEntry
}
type ImportMapping struct {
	Source         string    `json:"source"`
	Format         string    `json:"format"`
	Key            string    `json:"key"`
	Digest         string    `json:"digest"`
	MetadataDigest string    `json:"metadata_digest,omitempty"`
	PostID         int64     `json:"post_id"`
	CreatedAt      time.Time `json:"created_at"`
}
type ImportRow struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	URL     string `json:"url"`
	OldPath string `json:"old_path,omitempty"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	PostID  int64  `json:"post_id,omitempty"`
	Version int64  `json:"version,omitempty"`
}
type ImportPlan struct {
	Source    string      `json:"source"`
	Format    string      `json:"format"`
	Revision  string      `json:"revision"`
	Items     []ImportRow `json:"items"`
	New       int         `json:"new"`
	Existing  int         `json:"existing"`
	Conflicts int         `json:"conflicts"`
}

// ImportState contains only facts relevant to this bounded batch. SQL adapters
// must not load all site content to construct a preview.
type ImportState struct {
	Mappings      []ImportMapping
	Posts         []Post
	Redirects     []Redirect
	MappingCount  int
	RedirectCount int
}
type ImportRepository interface {
	PreviewImport(context.Context, ImportBatch) (ImportPlan, error)
	ApplyImport(context.Context, ImportBatch, string) (ImportPlan, error)
}

func ValidImportIdentity(source, format, key, digest string) bool {
	decoded, err := hex.DecodeString(digest)
	return importSourcePattern.MatchString(source) && (format == "markdown" || format == "wordpress" || format == "wechat") &&
		len(key) > 0 && len(key) <= 256 && utf8.ValidString(key) && !containsControl(key) &&
		err == nil && len(decoded) == sha256.Size && digest == strings.ToLower(digest)
}

func validImportMetadataDigest(format, digest string) bool {
	if format != "wechat" {
		return digest == ""
	}
	decoded, err := hex.DecodeString(digest)
	return format == "wechat" && err == nil && len(decoded) == sha256.Size && digest == strings.ToLower(digest)
}

func NormalizeImportBatch(batch ImportBatch, now time.Time) (ImportBatch, error) {
	if !importSourcePattern.MatchString(batch.Source) || (batch.Format != "markdown" && batch.Format != "wordpress" && batch.Format != "wechat") || len(batch.Items) < 1 || len(batch.Items) > MaxImportItems {
		return batch, &ValidationError{Fields: map[string]string{"import": "来源标识或格式不正确；每批需要 1–100 篇内容"}}
	}
	batch.Items = append([]ImportEntry(nil), batch.Items...)
	seen := make(map[string]bool)
	for i, item := range batch.Items {
		if !ValidImportIdentity(batch.Source, batch.Format, item.Key, item.Digest) || !validImportMetadataDigest(batch.Format, item.MetadataDigest) || seen[item.Key] {
			return batch, &ValidationError{Fields: map[string]string{"import": "源内容身份重复或不正确"}}
		}
		seen[item.Key] = true
		var fields map[string]string
		item.Input, fields = NormalizePostInput(item.Input, now)
		if len(fields) > 0 {
			return batch, &ValidationError{Fields: fields}
		}
		if item.OldPath != "" && !ValidRedirectPath(item.OldPath) {
			return batch, &ValidationError{Fields: map[string]string{"old_path": "旧路径不受支持"}}
		}
		if len(item.Categories) > MaxCategoriesPerPost || len(item.Input.CategoryIDs) > 0 {
			return batch, &ValidationError{Fields: map[string]string{"categories": "导入分类数量不正确；请使用名称映射"}}
		}
		item.Categories = append([]string(nil), item.Categories...)
		names := map[string]bool{}
		for j, name := range item.Categories {
			name = strings.TrimSpace(name)
			_, fields := NormalizeTermInput(TermInput{Taxonomy: TaxonomyCategory, Name: name})
			if len(fields) > 0 || names[strings.ToLower(name)] {
				return batch, &ValidationError{Fields: map[string]string{"categories": "导入分类无效或重复"}}
			}
			names[strings.ToLower(name)] = true
			item.Categories[j] = name
		}
		if item.Input.Type == TypePage && len(item.Categories) != 0 {
			return batch, &ValidationError{Fields: map[string]string{"categories": "页面不使用文章分类"}}
		}
		batch.Items[i] = item
	}
	return batch, nil
}

func BuildImportPlan(batch ImportBatch, state ImportState) ImportPlan {
	plan := ImportPlan{Source: batch.Source, Format: batch.Format, Items: make([]ImportRow, 0, len(batch.Items))}
	posts, paths, redirects, mappings := map[int64]Post{}, map[string]bool{}, map[string]bool{}, map[string]ImportMapping{}
	for _, post := range state.Posts {
		posts[post.ID] = post
		paths[PublicPath(post)] = true
	}
	for _, rule := range state.Redirects {
		redirects[rule.Path] = true
	}
	for _, mapping := range state.Mappings {
		if mapping.Source == batch.Source && mapping.Format == batch.Format {
			mappings[mapping.Key] = mapping
		}
	}
	newPaths, newOldPaths := map[string]int{}, map[string]int{}
	for _, item := range batch.Items {
		if _, exists := mappings[item.Key]; !exists {
			newPaths[PublicPath(Post{Type: item.Input.Type, Slug: item.Input.Slug})]++
			if item.OldPath != "" {
				newOldPaths[item.OldPath]++
			}
		}
	}
	neededRedirects := 0
	for _, item := range batch.Items {
		path := PublicPath(Post{Type: item.Input.Type, Slug: item.Input.Slug})
		row := ImportRow{Key: item.Key, Title: item.Input.Title, Type: item.Input.Type, Status: item.Input.Status, URL: path, OldPath: item.OldPath, Action: "create"}
		if mapping, exists := mappings[item.Key]; exists {
			post, exists := posts[mapping.PostID]
			if !validImportMetadataDigest(mapping.Format, mapping.MetadataDigest) {
				row.Action, row.Message = "conflict", "已有导入记录格式不完整，不能安全重导"
			} else if !exists {
				row.Action, row.Message = "conflict", "已有映射的目标不存在，不能安全重导"
			} else {
				row.PostID, row.Version, row.URL, row.Status = post.ID, post.Version, PublicPath(post), post.Status
				row.Action, row.Message = "skip", "已导入，保留当前内容与人工修改"
				if mapping.Digest != item.Digest {
					row.Action, row.Message = "changed", "来源已变化，保留现有目标；请在编辑器中核对更新"
				} else if batch.Format == "wechat" {
					if mapping.MetadataDigest != item.MetadataDigest {
						row.Action, row.Message = "changed", "来源摘要或封面已变化，保留现有目标；请在编辑器中核对更新"
					}
				}
			}
		} else if paths[path] || redirects[path] || newPaths[path] > 1 || (newOldPaths[path] > 0 && item.OldPath != path) {
			row.Action, row.Message = "conflict", "目标地址已被占用或与本批其他内容冲突"
		} else if item.OldPath != "" && item.OldPath != path && (paths[item.OldPath] || redirects[item.OldPath] || newOldPaths[item.OldPath] > 1 || newPaths[item.OldPath] > 0) {
			row.Action, row.Message = "conflict", "旧路径已被占用或与本批内容冲突"
		}
		switch row.Action {
		case "create":
			plan.New++
			if row.OldPath != "" && row.OldPath != path {
				neededRedirects++
			}
		case "conflict":
			plan.Conflicts++
		default:
			plan.Existing++
		}
		plan.Items = append(plan.Items, row)
	}
	if state.MappingCount+plan.New > MaxImportMappings || state.RedirectCount+neededRedirects > MaxRedirects {
		for i := range plan.Items {
			if plan.Items[i].Action == "create" {
				plan.Items[i].Action = "conflict"
				plan.Items[i].Message = "导入映射或旧链接注册表容量不足"
				plan.Conflicts++
				plan.New--
			}
		}
	}
	payload, _ := json.Marshal(struct {
		Batch ImportBatch
		Plan  ImportPlan
	}{batch, plan})
	digest := sha256.Sum256(payload)
	plan.Revision = fmt.Sprintf("%x", digest)
	return plan
}

func CheckImportPlan(plan ImportPlan, revision string) error {
	if revision == "" || plan.Conflicts > 0 || (plan.Revision != revision && plan.New > 0) {
		return ErrImportConflict
	}
	return nil
}
