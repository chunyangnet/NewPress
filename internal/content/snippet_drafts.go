package content

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"golang.org/x/net/html"
	"newpress.local/newpress/internal/contenthtml"
)

const MaxSnippetDraftReferences = 200

// SnippetDraftRequest addresses a saved source version. Confirmation adds the
// preview digest and a durable request identity; it never accepts copied HTML.
type SnippetDraftRequest struct {
	PostID     int64  `json:"post_id"`
	Version    int64  `json:"version"`
	Digest     string `json:"digest,omitempty"`
	RequestKey string `json:"request_key,omitempty"`
}

type SnippetDraftReference struct {
	contenthtml.SnippetKey
	SnippetVersion int64  `json:"snippet_version"`
	Title          string `json:"title"`
}

type SnippetDraftPlan struct {
	Source      SnippetSelection        `json:"source"`
	Type        string                  `json:"type"`
	Title       string                  `json:"title"`
	Excerpt     string                  `json:"excerpt"`
	CoverURL    string                  `json:"cover_url"`
	ContentHTML string                  `json:"content_html"`
	References  []SnippetDraftReference `json:"references"`
	Occurrences int                     `json:"occurrences"`
	Digest      string                  `json:"digest"`
}

// SnippetDraftGuard is trusted input to Submit, never decoded from JSON.
// Rebuilding the plan inside the write transaction prevents a withdrawal or
// source edit between HTTP preview and creation from copying unapproved bytes.
type SnippetDraftGuard struct {
	Request SnippetDraftRequest
	Access  SnippetAccess
}

type SnippetDraftBody struct {
	Snippet  Snippet
	Revision SnippetRevision
}

func ValidateSnippetDraft(u SnippetDraftRequest, confirm bool) error {
	if u.PostID <= 0 || u.Version <= 0 ||
		(confirm && (!validSubmissionDigest(u.Digest) || !validSubmissionDigest(u.RequestKey))) ||
		(!confirm && (u.Digest != "" || u.RequestKey != "")) {
		return &ValidationError{Fields: map[string]string{"source": "请选择已保存的文章版本；确认需要原预览摘要和请求键"}}
	}
	return nil
}

func NewSnippetDraftSubmission(u SnippetDraftRequest, a SnippetAccess) (Submission, error) {
	if err := ValidateSnippetDraft(u, true); err != nil {
		return Submission{}, err
	}
	if err := CheckSnippetAccess(a, true); err != nil {
		return Submission{}, err
	}
	raw, _ := json.Marshal(u)
	return Submission{Action: "create", Actor: a.Actor,
		Key:          fmt.Sprintf("%x", sha256.Sum256([]byte(a.Identity+"\x00snippet-draft\x00"+u.RequestKey))),
		Fingerprint:  fmt.Sprintf("%x", sha256.Sum256(raw)),
		SnippetDraft: &SnippetDraftGuard{Request: u, Access: a}}, nil
}

func snippetDraftField(message string) error {
	return &ValidationError{Fields: map[string]string{"content_source": message}}
}

// SnippetDraftKeys validates the source before a repository loads any bodies.
func SnippetDraftKeys(p Post, u SnippetDraftRequest, a SnippetAccess) ([]contenthtml.SnippetKey, error) {
	if err := CheckSnippetAccess(a, true); err != nil {
		return nil, err
	}
	if p.ID != u.PostID || !SnippetPostWritable(p, a) {
		return nil, ErrNotFound
	}
	if p.Version != u.Version {
		return nil, ErrVersionConflict
	}
	if p.Status == StatusTrash || (p.Type != TypePost && p.Type != TypePage) {
		return nil, snippetDraftField("请从回收站以外的文章或页面生成独立草稿")
	}
	if len(p.ContentSource) > MaxContentBytes {
		return nil, snippetDraftField("原正文超过 2 MiB")
	}
	refs := contenthtml.SnippetReferences(p.ContentSource)
	if len(refs) == 0 || len(refs) > MaxSnippetDraftReferences {
		return nil, snippetDraftField("正文须含 1 至 200 处共享片段引用")
	}
	keys := []contenthtml.SnippetKey{}
	seen := map[contenthtml.SnippetKey]bool{}
	for _, ref := range refs {
		if ref.ID <= 0 || ref.Version <= 0 {
			return nil, snippetDraftField("正文含无效片段引用，请先修正并保存")
		}
		if !closedSnippetDraftReference(p.ContentSource[ref.Start:ref.End]) {
			return nil, snippetDraftField("片段引用须使用完整的 aside 开始与结束标签，请先修正并保存")
		}
		if !seen[ref.SnippetKey] {
			seen[ref.SnippetKey] = true
			keys = append(keys, ref.SnippetKey)
		}
	}
	if len(keys) > MaxSnippetResolve {
		return nil, snippetDraftField("一次最多展开 100 个不同片段版本")
	}
	return keys, nil
}

func closedSnippetDraftReference(source string) bool {
	tokens := html.NewTokenizer(strings.NewReader(source))
	if tokens.Next() != html.StartTagToken || tokens.Token().Data != "aside" {
		return false
	}
	depth := 1
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken && kind != html.EndTagToken {
			continue
		}
		if tokens.Token().Data != "aside" {
			continue
		}
		if kind == html.EndTagToken {
			depth--
		} else if kind == html.SelfClosingTagToken {
			return false
		} else {
			depth++
		}
		if depth == 0 {
			return tokens.Next() == html.ErrorToken
		}
	}
	return false
}

func BuildSnippetDraft(p Post, u SnippetDraftRequest, a SnippetAccess, bodies map[contenthtml.SnippetKey]SnippetDraftBody) (SnippetDraftPlan, error) {
	keys, err := SnippetDraftKeys(p, u, a)
	if err != nil {
		return SnippetDraftPlan{}, err
	}
	plan := SnippetDraftPlan{Source: SnippetSelection{ID: p.ID, Version: p.Version}, Type: p.Type,
		Title: string([]rune(p.Title)[:min(len([]rune(p.Title)), MaxTitleRunes-7)]) + " · 独立草稿", Excerpt: p.Excerpt, CoverURL: p.CoverURL,
		References: []SnippetDraftReference{}}
	resolved := map[contenthtml.SnippetKey]string{}
	for _, key := range keys {
		body, exists := bodies[key]
		if !exists || !SnippetVersionShared(body.Snippet, key.Version) {
			return SnippetDraftPlan{}, ErrNotFound
		}
		if ValidateSnippetMetadata(body.Snippet) != nil || body.Snippet.ID != key.ID || body.Revision.Version != key.Version {
			return SnippetDraftPlan{}, ErrSnippetCorrupt
		}
		if ValidateSnippetRevision(body.Snippet, body.Revision) != nil {
			return SnippetDraftPlan{}, ErrSnippetCorrupt
		}
		resolved[key] = "<aside>" + body.Revision.ContentHTML + "</aside>"
		plan.References = append(plan.References, SnippetDraftReference{SnippetKey: key, SnippetVersion: body.Snippet.Version, Title: body.Revision.Title})
	}
	refs := contenthtml.SnippetReferences(p.ContentSource)
	size := len(p.ContentSource)
	for _, ref := range refs {
		size += len(resolved[ref.SnippetKey]) - (ref.End - ref.Start)
	}
	if size > MaxContentBytes {
		return SnippetDraftPlan{}, snippetDraftField("展开后的正文超过 2 MiB，请减少引用或正文后重新预览")
	}
	body := contenthtml.RewriteSnippets(p.ContentSource, func(key contenthtml.SnippetKey) string { return resolved[key] })
	// Resource gates and interactive widgets depend on the original article's
	// configuration. Reject them explicitly rather than copying an empty gate.
	tokens := html.NewTokenizer(strings.NewReader(body))
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		for _, attr := range tokens.Token().Attr {
			if strings.HasPrefix(attr.Key, "data-np-") || strings.HasPrefix(attr.Key, "data-npr-") || strings.HasPrefix(attr.Key, "data-npw-") {
				return SnippetDraftPlan{}, snippetDraftField("正文含资源或交互组件，请先整理成静态正文后生成独立草稿")
			}
		}
	}
	plan.ContentHTML = contenthtml.Sanitize(body)
	if len(plan.ContentHTML) > MaxContentBytes {
		return SnippetDraftPlan{}, snippetDraftField("净化后的正文超过 2 MiB")
	}
	plan.Occurrences = len(refs)
	// Bind all displayed/copyable fields and sharing generations to the actor.
	raw, _ := json.Marshal(struct {
		Identity string
		Plan     SnippetDraftPlan
	}{a.Identity, plan})
	plan.Digest = fmt.Sprintf("%x", sha256.Sum256(raw))
	return plan, nil
}

func (plan SnippetDraftPlan) Input(a SnippetAccess, key string) PostInput {
	return PostInput{Type: plan.Type, Status: StatusDraft, Title: plan.Title, Excerpt: plan.Excerpt,
		ContentSource: plan.ContentHTML, CoverURL: plan.CoverURL, Slug: "snippet-draft-" + key,
		AuthorID: a.UserID, AuthorName: a.Actor, Actor: a.Actor, CommentStatus: CommentClosed, AIPolicy: "off"}
}

func (r *MemoryRepository) snippetDraftLocked(u SnippetDraftRequest, a SnippetAccess) (SnippetDraftPlan, error) {
	i := r.postIndexLocked(u.PostID)
	if i < 0 {
		return SnippetDraftPlan{}, ErrNotFound
	}
	p := r.posts[i]
	keys, err := SnippetDraftKeys(p, u, a)
	if err != nil {
		return SnippetDraftPlan{}, err
	}
	bodies := map[contenthtml.SnippetKey]SnippetDraftBody{}
	for _, key := range keys {
		s, _, err := r.snippetLocked(key.ID)
		if err != nil {
			return SnippetDraftPlan{}, err
		}
		index, found := slices.BinarySearchFunc(s.Revisions, key.Version, func(rev SnippetRevision, version int64) int {
			if rev.Version < version {
				return -1
			}
			if rev.Version > version {
				return 1
			}
			return 0
		})
		if found {
			bodies[key] = SnippetDraftBody{Snippet: s.Snippet, Revision: s.Revisions[index]}
		}
	}
	return BuildSnippetDraft(p, u, a, bodies)
}

func (r *MemoryRepository) PreviewSnippetDraft(ctx context.Context, u SnippetDraftRequest, a SnippetAccess) (SnippetDraftPlan, error) {
	if err := ValidateSnippetDraft(u, false); err != nil {
		return SnippetDraftPlan{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return SnippetDraftPlan{}, err
	}
	return r.snippetDraftLocked(u, a)
}

func (r *FileRepository) PreviewSnippetDraft(ctx context.Context, u SnippetDraftRequest, a SnippetAccess) (SnippetDraftPlan, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.state.PreviewSnippetDraft(ctx, u, a)
}

func (r *MemoryRepository) prepareSnippetDraft(ctx context.Context, request *Submission) error {
	g := request.SnippetDraft
	plan, err := r.snippetDraftLocked(g.Request, g.Access)
	if err != nil {
		return err
	}
	if plan.Digest != g.Request.Digest {
		return ErrVersionConflict
	}
	request.Input = plan.Input(g.Access, request.Key)
	return ctx.Err()
}

func (r *MemoryRepository) checkSnippetDraftReplay(receipt SubmissionReceipt, a SnippetAccess) error {
	if err := CheckSnippetAccess(a, true); err != nil {
		return err
	}
	i := r.postIndexLocked(receipt.PostID)
	if i < 0 {
		return ErrSubmissionResultUnavailable
	}
	if !SnippetPostWritable(r.posts[i], a) {
		return ErrSnippetPermission
	}
	return nil
}
