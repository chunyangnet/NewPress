package httpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/content"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

type bridgeDocument struct {
	Site, Type, Title, Body, Revision string
	Status, Attempt                   string `json:",omitempty"`
	ID                                int64
	Categories                        []aieditor.Category
	Fields                            []string
}

var bridgeTypePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
var bridgeAttemptPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validBridgeDocument(in bridgeDocument) bool {
	site, err := url.Parse(in.Site)
	if err != nil || len(in.Site) > 256 || (site.Scheme != "https" && site.Scheme != "http") || site.Hostname() == "" || site.User != nil || site.RawQuery != "" || site.Fragment != "" {
		return false
	}
	revision, err := hex.DecodeString(in.Revision)
	if err != nil || len(revision) != 32 || !bridgeTypePattern.MatchString(in.Type) || in.ID < 1 || (in.Attempt != "" && !bridgeAttemptPattern.MatchString(in.Attempt)) || !slices.Contains([]string{"", "draft", "pending", "publish"}, in.Status) || strings.TrimSpace(in.Title) == "" || utf8.RuneCountInString(in.Title) > 240 || len(in.Body) > 24000 || len(in.Categories) > 100 || len(in.Fields) == 0 || len(in.Fields) > 4 {
		return false
	}
	fields := map[string]bool{}
	for _, f := range in.Fields {
		if fields[f] || !slices.Contains([]string{"excerpt", "tags", "slug", "categoryID"}, f) {
			return false
		}
		fields[f] = true
	}
	categories := map[int64]bool{}
	for _, c := range in.Categories {
		if c.ID < 1 || categories[c.ID] || strings.TrimSpace(c.Name) == "" || len(c.Name) > 240 {
			return false
		}
		categories[c.ID] = true
	}
	return true
}

func (s *Server) bridgeAccess(w http.ResponseWriter, r *http.Request) (aijobs.State, bool) {
	w.Header().Set("Cache-Control", "no-store")
	key := os.Getenv("NEWPRESS_AI_BRIDGE_TOKEN")
	given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	a, b := sha256.Sum256([]byte(key)), sha256.Sum256([]byte(given))
	if !ok || len(key) < 32 || len(key) > 256 || len(given) > 256 || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		s.writeAPIError(w, r, 401, "ai.bridge_auth", "配对凭据无效", nil)
		return aijobs.State{}, false
	}
	st, ok := s.automationState()
	if !ok || !st.Rules.External {
		s.writeAPIError(w, r, 404, "ai.bridge_disabled", "外部内容处理未启用", nil)
		return st, false
	}
	return st, true
}
func (s *Server) submitBridge(w http.ResponseWriter, r *http.Request) {
	st, ok := s.bridgeAccess(w, r)
	if !ok {
		return
	}
	var in bridgeDocument
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &in, 48<<10, "48 KiB"); !ok {
		return
	}
	if !validBridgeDocument(in) {
		s.writeAPIError(w, r, 422, "ai.bridge_invalid", "外部内容字段无效", nil)
		return
	}
	rules := st.Rules
	protected := rules.SafeUpdate && (in.Status == "" || in.Status == "publish")
	allowed := map[string]bool{"excerpt": rules.Summary, "slug": rules.Slug && !protected, "tags": rules.Tags, "categoryID": rules.Category && !protected}
	in.Fields = slices.DeleteFunc(in.Fields, func(x string) bool { return !allowed[x] })
	if len(in.Fields) == 0 {
		writeJSON(w, 200, map[string]any{"status": "succeeded", "result": aiEnrichment{}})
		return
	}
	raw, _ := json.Marshal(in)
	key := aiDigest(string(raw))
	job := aijobs.Job{Topic: in.Title, Author: rules.Author, UserID: rules.UserID, Provider: rules.Provider, Model: rules.Model, Language: rules.Language, Priority: 2, Work: aijobs.WorkSpec{Kind: "external", Key: "external/" + key, Source: string(raw), Rules: rules}}
	if err := s.aiJobs.AddWork(st.Epoch, []aijobs.Job{job}, time.Now()); err != nil {
		s.writeAPIError(w, r, 409, "ai.queue_full", "外部处理队列暂时不可写", nil)
		return
	}
	writeJSON(w, 202, map[string]string{"ticket": key, "status": "queued"})
}
func (s *Server) bridgeResult(w http.ResponseWriter, r *http.Request) {
	st, ok := s.bridgeAccess(w, r)
	if !ok {
		return
	}
	key := "external/" + r.PathValue("key")
	for _, j := range st.Jobs {
		if j.Work.Kind != "external" || j.Work.Key != key {
			continue
		}
		var out aiEnrichment
		if j.Status == "succeeded" {
			if decodeAIObject(j.Result, &out) != nil {
				s.writeAPIError(w, r, 503, "ai.bridge_result", "无法读取结果", nil)
				return
			}
		}
		writeJSON(w, 200, map[string]any{"status": j.Status, "result": out, "problem": j.Problem})
		return
	}
	s.writeAPIError(w, r, 404, "ai.ticket_missing", "任务记录不存在", nil)
}
func (s *Server) acknowledgeBridge(w http.ResponseWriter, r *http.Request) {
	st, ok := s.bridgeAccess(w, r)
	if !ok {
		return
	}
	err := s.aiJobs.AcknowledgeExternal(st.Epoch, r.PathValue("key"))
	if errors.Is(err, aijobs.ErrNotFound) {
		s.writeAPIError(w, r, 404, "ai.ticket_missing", "任务记录不存在", nil)
	} else if err != nil {
		s.writeAPIError(w, r, 409, "ai.ticket_not_ready", "任务尚不可确认", nil)
	} else {
		writeJSON(w, 200, map[string]string{"status": "acknowledged"})
	}
}
func (s *Server) externalEnrichment(ctx context.Context, j aijobs.Job) (string, error) {
	state, err := s.aiJobs.Snapshot()
	if err != nil {
		return "", err
	}
	if !state.Rules.External {
		return "", aijobs.ErrStale
	}
	var in bridgeDocument
	if json.Unmarshal([]byte(j.Work.Source), &in) != nil {
		return "", aijobs.ErrInvalid
	}
	raw, err := s.autoText(ctx, j, quotedMessages("为外部内容补全 Fields 中要求的信息。只返回 JSON，excerpt 为摘要，tags 为字符串数组，slug 为英文别名，categoryID 为给定分类 ID", in, j.Language), nil, 1200)
	if err != nil {
		return "", err
	}
	var out aiEnrichment
	if err = decodeAIObject(raw, &out); err != nil {
		return "", err
	}
	clean := aiEnrichment{}
	for _, f := range in.Fields {
		switch f {
		case "excerpt":
			clean.Excerpt = workbenchText(out.Excerpt, 500)
		case "tags":
			for _, t := range out.Tags[:min(8, len(out.Tags))] {
				if v := workbenchText(t, 80); v != "" {
					clean.Tags = append(clean.Tags, v)
				}
			}
		case "categoryID":
			if slices.ContainsFunc(in.Categories, func(c aieditor.Category) bool { return c.ID == out.CategoryID }) {
				clean.CategoryID = out.CategoryID
			}
		case "slug":
			if content.ValidSlug(out.Slug) {
				clean.Slug = out.Slug
			}
		}
	}
	result, _ := json.Marshal(clean)
	return string(result), nil
}
