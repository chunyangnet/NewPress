package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/module"
)

// This is a projection of the owning domains, never a second settings store.
// Readiness describes saved configuration; execution results and actor permissions
// remain separate. Every failed condition is retained, including after a pause.
type featureStatusAction struct {
	Label       string `json:"label"`
	URL         string `json:"url,omitempty"`
	Target      string `json:"target,omitempty"`
	Module      string `json:"module,omitempty"`
	Capability  string `json:"-"`
	Allowed     bool   `json:"allowed"`
	Restriction string `json:"restriction,omitempty"`
}

type featureStatusCheck struct {
	ID     string               `json:"id"`
	Label  string               `json:"label"`
	State  string               `json:"state"`
	Reason string               `json:"reason"`
	Action *featureStatusAction `json:"action,omitempty"`
}

type featureStatusItem struct {
	ID        string               `json:"id"`
	Title     string               `json:"title"`
	Scope     string               `json:"scope"`
	State     string               `json:"state"`
	Label     string               `json:"label"`
	Summary   string               `json:"summary"`
	Mode      string               `json:"mode,omitempty"`
	Effective bool                 `json:"effective"`
	Partial   bool                 `json:"partial,omitempty"`
	Checks    []featureStatusCheck `json:"checks"`
	Action    *featureStatusAction `json:"action,omitempty"`
}

type adminFeatureStatus struct {
	Feature   string                `json:"feature"`
	Object    string                `json:"object,omitempty"`
	Revision  string                `json:"revision"`
	CheckedAt string                `json:"checked_at"`
	UserID    int64                 `json:"user_id"`
	ReadOnly  bool                  `json:"read_only"`
	Recovery  bool                  `json:"recovery"`
	Items     []featureStatusItem   `json:"items"`
	Overview  featureStatusOverview `json:"overview"`
}

// One presentation summary is shared by the initial HTML and later refreshes.
// The domain-ordered items and their complete checks remain available in details.
type featureStatusOverview struct {
	State       string               `json:"state"`
	Label       string               `json:"label"`
	Title       string               `json:"title"`
	Summary     string               `json:"summary"`
	Additional  int                  `json:"additional"`
	GlobalBlock bool                 `json:"global_block"`
	Action      *featureStatusAction `json:"action,omitempty"`
}

func summarizeFeatureStatus(v adminFeatureStatus) featureStatusOverview {
	o := featureStatusOverview{State: "ready", Label: "功能可用"}
	if v.Feature == "article-comments" {
		o.Label = "评论可用"
	}
	count := 0
	for _, item := range v.Items {
		if item.State != "ready" {
			if count == 0 {
				o.State, o.Label, o.Title, o.Summary, o.Action = item.State, item.Label, item.Title, item.Summary, item.Action
			}
			count++
		}
		if v.Feature == "article-comments" {
			for _, check := range item.Checks {
				if check.ID == "module" && check.State != "ready" && check.State != "info" {
					o.GlobalBlock = true
				}
			}
		}
	}
	if count > 1 {
		o.Additional = count - 1
	}
	return o
}

func (v adminFeatureStatus) JSON() string { b, _ := json.Marshal(v); return string(b) }
func (v adminFeatureStatus) Endpoint() string {
	q := url.Values{"feature": {v.Feature}}
	if v.Object != "" {
		q.Set("object", v.Object)
	}
	return "/api/v1/admin/feature-status?" + q.Encode()
}

func featureLink(label, href, capability string) *featureStatusAction {
	return &featureStatusAction{Label: label, URL: href, Capability: capability}
}
func featureFocus(label, href, target, capability string) *featureStatusAction {
	a := featureLink(label, href, capability)
	a.Target = target
	return a
}
func featureCheck(id, label string, ready bool, state, reason string, action *featureStatusAction) featureStatusCheck {
	if ready {
		return featureStatusCheck{ID: id, Label: label, State: "ready", Reason: "已满足"}
	}
	return featureStatusCheck{ID: id, Label: label, State: state, Reason: reason, Action: action}
}
func featureInfo(id, label, reason string) featureStatusCheck {
	return featureStatusCheck{ID: id, Label: label, State: "info", Reason: reason}
}
func featureFailure(id, title, reason string) featureStatusItem {
	return featureStatusItem{ID: id, Title: title, Scope: "全站", Checks: []featureStatusCheck{{ID: "storage", Label: "读取已保存配置", State: "error", Reason: reason}}}
}

func finishFeatureStatus(v *adminFeatureStatus, a adminView) {
	authorize := func(action *featureStatusAction) {
		if action == nil {
			return
		}
		action.Allowed = action.Capability == "" || a.Can(action.Capability)
		if action.Module != "" {
			action.Allowed = a.CanWrite(auth.CapabilitySettingsManage)
		}
		if !action.Allowed {
			action.Restriction = "需要相应管理权限"
			if a.ReadOnly || a.Preview {
				action.Restriction = "当前为只读预览"
			}
		}
	}
	for i := range v.Items {
		item := &v.Items[i]
		item.State, item.Label, item.Effective = "ready", "可用", true
		if item.Scope == "" {
			item.Scope = "全站"
		}
		for j := range item.Checks {
			check := &item.Checks[j]
			authorize(check.Action)
			if check.State == "ready" || check.State == "info" {
				continue
			}
			// The first unmet prerequisite is the next step. Do not discard the rest.
			if item.Effective {
				item.State, item.Summary = check.State, check.Reason
				item.Label = map[string]string{"paused": "已暂停", "needs_config": "待配置", "error": "异常", "unknown": "待检测"}[check.State]
				if item.Label == "" {
					item.Label = "待检测"
				}
				item.Action = check.Action
			}
			item.Effective = false
		}
		authorize(item.Action)
		if item.Effective && item.Partial {
			item.State, item.Label = "partial", "部分可用"
		}
	}
	v.Overview = summarizeFeatureStatus(*v)
}

func featureCapabilities(feature string) []string {
	switch feature {
	case "comments":
		return []string{auth.CapabilityCommentsModerate}
	case "article-comments":
		return []string{auth.CapabilityContentRead, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn}
	case "ai-queue":
		return []string{auth.CapabilityPluginsManage}
	case "ai-related":
		return []string{auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn}
	case "ai-usage":
		return []string{auth.CapabilitySystemRead}
	case "features", "feedback", "links", "wechat", "progression", "currency", "recognition", "checkin":
		return []string{auth.CapabilitySettingsManage}
	case "layout":
		return []string{auth.CapabilityAppearanceManage}
	case "navigation":
		return []string{auth.CapabilityNavigationManage}
	case "community":
		return []string{auth.CapabilitySettingsManage, auth.CapabilitySystemRead}
	}
	if d, ok := module.Lookup(feature); ok && d.Control == "switch" && d.Capability != "" {
		return []string{d.Capability}
	}
	return nil
}

func (s *Server) adminFeatureStatusAPI(w http.ResponseWriter, r *http.Request) {
	p, ro, ok := s.requireAdminJSONRead(w, r)
	if !ok {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"feature": true, "object": true})
	if !ok {
		return
	}
	feature, object := q.Get("feature"), q.Get("object")
	caps := featureCapabilities(feature)
	if len(caps) == 0 || len(object) > 80 {
		s.writeAPIError(w, r, 400, "feature_status.invalid", "未知的业务状态", nil)
		return
	}
	if !ro && !principalCanAny(p, caps...) {
		s.writeAPIError(w, r, 403, "auth.capability_required", "当前账号没有查看此业务状态的权限", nil)
		return
	}
	if object != "" && feature != "article-comments" && feature != "ai-related" && feature != "currency" && feature != "feedback" && feature != "links" {
		s.writeAPIError(w, r, 400, "feature_status.invalid", "此业务不接受对象参数", nil)
		return
	}
	if feature == "ai-related" || feature == "article-comments" && object != "" {
		id, err := strconv.ParseInt(object, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != object {
			http.NotFound(w, r)
			return
		}
		post, err := s.repository.GetByID(r.Context(), id)
		allowed := canReadContentObject(p, post)
		if feature == "ai-related" {
			allowed = canWriteContentObject(p, post)
		}
		if err != nil || (!ro && !allowed) {
			http.NotFound(w, r)
			return
		}
	}
	a := adminView{Principal: p, ReadOnly: ro, Preview: ro && !s.auth.Configured()}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, 200, s.featureStatus(ctx, feature, object, a))
}

func (s *Server) prepareAdminFeatureStatus(ctx context.Context, page string, data *pageView) {
	if !data.IsAdmin || data.FeatureStatus != nil {
		return
	}
	feature, object := "", ""
	switch page {
	case "admin-ai-pro-max":
		feature = "ai-queue"
	case "admin-ai-workbench":
		feature = "ai"
	case "admin-ai-related":
		feature, object = "ai-related", strconv.FormatInt(data.Post.ID, 10)
	case "admin-import":
		feature = "imports"
	case "admin-wechat":
		feature = "wechat"
	case "admin-comments", "admin-comment-editor":
		feature = "comments"
	case "admin-editor":
		feature = "article-comments"
		if data.Editor.Post.ID > 0 {
			object = strconv.FormatInt(data.Editor.Post.ID, 10)
		}
	case "admin-progression":
		feature = "progression"
	case "admin-currency":
		feature, object = "currency", data.Currency.Key
	case "admin-recognition":
		feature = "recognition"
	case "admin-features":
		feature = "features"
		if data.Features.Editing {
			feature, object = "feedback", data.Features.Current.Slug
		}
	case "admin-links":
		feature, object = "links", data.LinkPages.Board.Slug
	case "admin-circles":
		if data.Admin.Preview || principalCanAny(data.Admin.Principal, featureCapabilities("community")...) {
			feature = "community"
		}
	case "admin-analytics":
		feature = "analytics"
	case "admin-ai-usage":
		feature = "ai-usage"
	case "admin-layout", "admin-appearance":
		feature = "layout"
	case "admin-navigation":
		feature = "navigation"
	default:
		id := strings.TrimPrefix(page, "admin-")
		if d, ok := module.Lookup(id); ok && d.Control == "switch" {
			feature = id
		}
	}
	if feature == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	v := s.featureStatus(ctx, feature, object, data.Admin)
	data.FeatureStatus = &v
}

// Only the exact module landing page has a recovery surface. Mutations, public
// routes and arbitrary object URLs stay closed while the module is paused.
func (s *Server) renderModuleRecovery(w http.ResponseWriter, r *http.Request, id string) bool {
	d, ok := module.Lookup(id)
	if !ok || r.Method != http.MethodGet && r.Method != http.MethodHead || d.ConfigURL != r.URL.Path || !strings.HasPrefix(r.URL.Path, "/admin/") {
		return false
	}
	caps := featureCapabilities(id)
	if len(caps) == 0 {
		return false
	}
	p, ro, allowed := s.requireAdminPageCapability(w, r, caps...)
	if !allowed {
		return true
	}
	current := id
	if id == "community" {
		current = "circles"
	}
	s.renderFeatureRecovery(w, r, id, "", s.adminView(current, p, ro), 200, "")
	return true
}

func (s *Server) renderFeatureRecovery(w http.ResponseWriter, r *http.Request, feature, object string, a adminView, status int, reason string) {
	d, _ := module.Lookup(feature)
	if feature == "layout" {
		d.Title = "首页布局"
	}
	if feature == "ai-queue" {
		d.Title = "AI 写作队列"
	}
	description := "配置和数据仍然保留。下方列出此工作区的生效条件。"
	if reason != "" {
		description = "上次载入业务数据失败：" + reason
	}
	data := s.baseViewFor(w, d.Title, description, r.URL.Path)
	data.IsAdmin, data.BodyClass, data.Admin = true, "is-admin is-admin-feature-recovery", a
	data.Admin.DetailTitle = d.Title
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	v := s.featureStatus(ctx, feature, object, data.Admin)
	v.Recovery = true
	data.FeatureStatus = &v
	s.render(w, status, "admin-feature-recovery", data)
}
