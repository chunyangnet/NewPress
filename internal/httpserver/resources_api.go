package httpserver

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strconv"
	"time"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

func (s *Server) SetResourceStore(st *resources.Store) { s.resources = st }
func (s *Server) registerResourceRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/v1/article-resources/{post}/{resource}", s.resourceState)
	mux.HandleFunc("POST /api/v1/article-resources/{post}/{resource}", s.resourceAction)
	mux.HandleFunc("GET /api/v1/social/downloads", s.resourceHistory)
	mux.HandleFunc("GET /api/v1/admin/resource-options", s.resourceOptions)
	mux.HandleFunc("GET /api/v1/admin/resource-submissions/{post}", s.resourceSubmissions)
	mux.HandleFunc("GET /downloads/{$}", s.resourceLibraryPage)
}
func (s *Server) resourceError(w http.ResponseWriter, r *http.Request, e error) {
	status, code, message := 503, "resource.unavailable", "资源服务暂不可用，请稍后重试"
	if errors.Is(e, resources.ErrInvalid) {
		status, code, message = 422, "resource.invalid", "请检查资源、条件、答题或表单内容"
	}
	if errors.Is(e, resources.ErrLimit) {
		status, code, message = 429, "resource.limit", "已达到今日答题次数或下载限额"
	}
	if errors.Is(e, virtualcurrency.ErrInsufficient) {
		status, code, message = 409, "resource.balance", "积分余额不足，请充值或选择其他解锁方式"
	}
	if errors.Is(e, virtualcurrency.ErrConflict) {
		status, code, message = 409, "resource.changed", "积分设置已更新，请刷新后重试"
	}
	if errors.Is(e, resources.ErrPurchaseChanged) {
		status, code, message = 409, "resource.changed", "购买条件或预留订单已更新，请重新核验后重试"
	}
	if errors.Is(e, virtualcurrency.ErrDisabled) {
		status, code, message = 409, "resource.currency_disabled", "此积分币种暂未开放购买，请选择其他解锁方式"
	}
	s.writeAPIError(w, r, status, code, message, nil)
}
func (s *Server) resourceTarget(w http.ResponseWriter, r *http.Request) (content.Post, resources.Resource, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Add("Vary", "Cookie")
	if _, ok := s.strictAPIQuery(w, r, map[string]bool{}); !ok {
		return content.Post{}, resources.Resource{}, false
	}
	id, e := strconv.ParseInt(r.PathValue("post"), 10, 64)
	if e != nil || id < 1 || !resources.ValidID(r.PathValue("resource")) {
		s.resourceError(w, r, resources.ErrInvalid)
		return content.Post{}, resources.Resource{}, false
	}
	p, e := s.repository.GetByID(r.Context(), id)
	if e == nil && content.PublicAt(p, time.Now()) && (p.Type == content.TypePost || p.Type == content.TypePage) {
		for _, item := range resources.FromMeta(p.Meta).Items {
			if item.ID == r.PathValue("resource") {
				return p, item, true
			}
		}
	}
	s.writeAPIError(w, r, 404, "resource.not_found", "资源不存在或文章尚未公开", nil)
	return content.Post{}, resources.Resource{}, false
}
func (s *Server) resourceState(w http.ResponseWriter, r *http.Request) {
	p, item, ok := s.resourceTarget(w, r)
	if !ok {
		return
	}
	actor, token, user, ok := s.componentActor(w, r, articlewidgets.Config{}, false)
	if !ok {
		return
	}
	f, e := s.resourceFacts(r.Context(), p, actor, user)
	if e != nil {
		s.resourceError(w, r, e)
		return
	}
	day := resources.Day(f.now, s.siteSnapshot(r.Context()).Settings.Identity.Timezone)
	daily, total := resources.Counts(f.state, actor, p.ID, item.ID, day)
	variants := []map[string]any{}
	for _, v := range item.Variants {
		allowed, blocked, groups := s.resourceVersion(r.Context(), p, item, v, f)
		for gi := range groups {
			prerequisites := resourceGroupPrerequisites(groups[gi])
			for ci := range groups[gi].Conditions {
				c := &groups[gi].Conditions[ci]
				if c.Kind != "points" && c.Kind != "payment" || c.Passed {
					continue
				}
				c.PurchaseReason = blocked
				if allowed {
					c.PurchaseReason = "already_unlocked"
				} else if c.PurchaseReason == "" && v.Kind == "download" && resources.Remaining(item, daily, total) == 0 {
					c.PurchaseReason = "limit"
				} else if c.PurchaseReason == "" && !prerequisites {
					c.PurchaseReason = "prerequisite"
				}
				c.Purchasable = c.PurchaseReason == ""
			}
		}
		for gi, g := range v.Groups {
			for ci, c := range g.Conditions {
				if c.Kind == "payment" {
					var latest commerce.Order
					for _, o := range f.orders {
						if commerce.ResourceMatches(o, resources.Terms(p.ID, item, v, c)) && (latest.ID == "" || o.CreatedAt.After(latest.CreatedAt)) {
							latest = o
							groups[gi].Conditions[ci].OrderID = o.ID
							groups[gi].Conditions[ci].OrderStatus = o.Status
							groups[gi].Conditions[ci].OrderAmount = virtualcurrency.FormatAmount(o.Amount, o.Precision)
							groups[gi].Conditions[ci].OrderCurrency = o.Currency
						}
					}
				}
				groups[gi].Conditions[ci].Label = s.resourceConditionLabel(r.Context(), c)
			}
		}
		available := resources.Available(item, f.now)
		sub := f.state.Submissions[resources.SubmissionKey(actor, p.ID, item, v)]
		attempts := 0
		if sub.Day == day {
			attempts = sub.Attempts
		}
		links := []map[string]string{}
		for _, link := range v.Downloads {
			links = append(links, map[string]string{"id": link.ID, "name": link.Name, "provider": link.Provider, "provider_name": resources.ProviderName(link.Provider)})
		}
		projection := map[string]any{"id": v.ID, "name": v.Name, "kind": v.Kind, "allowed": allowed, "available": available, "blocked": blocked, "groups": groups, "downloads": links, "questions": publicResourceQuestions(v), "fields": v.Fields, "quiz_passed": sub.Passed, "form_complete": sub.FormComplete, "attempts_remaining": max(0, v.AttemptsPerDay-attempts), "pass_percent": v.PassPercent}
		if allowed {
			projection["body"] = v.Body
			if v.Kind == "image" || v.Kind == "video" {
				projection["media_url"] = v.MediaURL
			}
		}
		variants = append(variants, projection)
	}
	channels := []map[string]string{}
	refund := ""
	disclaimer := ""
	paymentVersion := "0"
	if s.commerce != nil {
		if cfg, e := s.commerce.Settings(r.Context()); e == nil && cfg.Enabled {
			paymentVersion = strconv.FormatInt(cfg.Version, 10)
			refund, disclaimer = cfg.RefundPolicy, cfg.Disclaimer
			for _, ch := range cfg.Channels {
				if ch.Enabled && !(ch.Provider == "stripe" && item.EndsAt != nil && item.EndsAt.Before(time.Now().Add(31*time.Minute))) {
					channels = append(channels, map[string]string{"key": ch.Key, "name": ch.Name, "currency": ch.Currency})
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"author": p.AuthorID, "post": p.ID, "id": item.ID, "revision": resources.Hash(item), "csrf": token, "logged_in": user > 0, "user_id": strconv.FormatInt(user, 10), "title": item.Title, "starts_at": item.StartsAt, "ends_at": item.EndsAt, "timezone": s.siteSnapshot(r.Context()).Settings.Identity.Timezone, "payment_version": paymentVersion, "variants": variants, "remaining": resources.Remaining(item, daily, total), "daily_used": daily, "total_used": total, "channels": channels, "refund_policy": refund, "disclaimer": disclaimer, "selection": item.Selection})
}

type resourceActionInput struct {
	Action         string            `json:"action"`
	Variant        string            `json:"variant"`
	Condition      string            `json:"condition"`
	Group          int               `json:"group"`
	Revision       string            `json:"revision"`
	Channel        string            `json:"channel"`
	Accepted       bool              `json:"accepted"`
	Link           string            `json:"link"`
	Request        string            `json:"request"`
	Fields         map[string]string `json:"fields"`
	Answers        map[string]string `json:"answers"`
	PaymentVersion int64             `json:"payment_version,string"`
}

func (s *Server) resourceAction(w http.ResponseWriter, r *http.Request) {
	p, item, ok := s.resourceTarget(w, r)
	if !ok {
		return
	}
	if !s.sameOriginRequest(r) {
		s.writeAPIError(w, r, 403, "resource.origin", "请从本站页面操作", nil)
		return
	}
	actor, _, user, ok := s.componentActor(w, r, articlewidgets.Config{}, true)
	if !ok {
		return
	}
	if !s.currencyRate(w, r, "resource:"+actor, 45) {
		return
	}
	var in resourceActionInput
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &in, 96<<10, "96 KiB"); !ok {
		return
	}
	if in.Revision != resources.Hash(item) {
		s.writeAPIError(w, r, 409, "resource.changed", "资源已更新，请刷新后核对解锁条件", nil)
		return
	}
	if !resources.Available(item, time.Now()) {
		s.writeAPIError(w, r, 403, "resource.closed", "资源尚未开放或已结束访问", nil)
		return
	}
	var v resources.Variant
	for _, variant := range item.Variants {
		if variant.ID == in.Variant {
			v = variant
		}
	}
	if v.ID == "" {
		s.resourceError(w, r, resources.ErrInvalid)
		return
	}
	f, e := s.resourceFacts(r.Context(), p, actor, user)
	if e != nil {
		s.resourceError(w, r, e)
		return
	}
	day := resources.Day(time.Now(), s.siteSnapshot(r.Context()).Settings.Identity.Timezone)
	switch in.Action {
	case "submit":
		if !resourceNeeds(v, "quiz") && in.Answers != nil || !resourceNeeds(v, "form") && in.Fields != nil {
			s.resourceError(w, r, resources.ErrInvalid)
			return
		}
		result, e := s.resources.Submit(r.Context(), actor, p.ID, item, v, in.Fields, in.Answers, day)
		if e != nil {
			s.resourceError(w, r, e)
			return
		}
		writeJSON(w, 200, map[string]any{"quiz_passed": result.Passed, "form_complete": result.FormComplete, "attempts_remaining": max(0, v.AttemptsPerDay-result.Attempts)})
		return
	case "points", "pay":
		if user == 0 {
			s.writeAPIError(w, r, 401, "auth.required", "请先登录后购买", nil)
			return
		}
		if !in.Accepted || in.Group < 0 || in.Group >= len(v.Groups) {
			s.resourceError(w, r, resources.ErrInvalid)
			return
		}
		var selected resources.Condition
		allowed, blocked, groups := s.resourceVersion(r.Context(), p, item, v, f)
		for _, c := range v.Groups[in.Group].Conditions {
			if c.ID == in.Condition {
				selected = c
			}
			if c.Kind != "points" && c.Kind != "payment" && !s.resourceCheck(r.Context(), p, item, v, c, f) {
				s.writeAPIError(w, r, 403, "resource.prerequisite", "请先完成这组中的身份、互动、答题或表单条件，再购买", nil)
				return
			}
		}
		if selected.Kind != "points" && selected.Kind != "payment" || in.Action == "points" && selected.Kind != "points" || in.Action == "pay" && selected.Kind != "payment" {
			s.resourceError(w, r, resources.ErrInvalid)
			return
		}
		paidCondition := s.resourceCheck(r.Context(), p, item, v, selected, f)
		if !paidCondition && (blocked != "" || allowed) {
			s.writeAPIError(w, r, 409, "resource.selection", "当前版本不可购买或已有可用的解锁方式，请重新核验版本", nil)
			return
		}
		if !paidCondition && v.Kind == "download" {
			daily, total := resources.Counts(f.state, actor, p.ID, item.ID, day)
			if resources.Remaining(item, daily, total) == 0 {
				s.resourceError(w, r, resources.ErrLimit)
				return
			}
		}
		if !resourceGroupPrerequisites(groups[in.Group]) {
			s.writeAPIError(w, r, 403, "resource.prerequisite", "请先完成这组中的身份、互动、答题或表单条件，再购买", nil)
			return
		}
		if in.Action == "points" && selected.Kind == "points" {
			entry, replayed, e := s.buyResourcePoints(r.Context(), p, item, v, selected, f)
			if e != nil {
				s.resourceError(w, r, e)
				return
			}
			writeJSON(w, 200, map[string]any{"paid": entry.Kind == virtualcurrency.PurchasePayment, "replayed": replayed})
			return
		}
		if in.Action == "pay" && selected.Kind == "payment" && s.commerce != nil {
			terms := resources.Terms(p.ID, item, v, selected)
			order, e := s.commerce.CreateResourcePayment(r.Context(), commerce.ResourcePayment{URL: s.contentPath(p) + "#resource-" + item.ID, UserID: user, Key: terms.LegacyKey(), Terms: &terms, ExpectedVersion: in.PaymentVersion, Title: trimResourceTitle(item.Title + " · " + v.Name), Currency: selected.Currency, Amount: selected.Amount, Channel: in.Channel, ClientIP: s.clientIPs.string(r), Accepted: in.Accepted})
			if e != nil {
				if errors.Is(e, commerce.ErrPending) && commerce.ValidOrderID(order.ID) {
					s.writeAPIError(w, r, 409, "payment.pending", "原订单仍需核对支付结果，请打开原订单继续确认", map[string]any{"order": commerce.PublicOrder(order)})
					return
				}
				s.commerceError(w, r, e)
				return
			}
			writeJSON(w, 200, commerce.PublicOrder(order))
			return
		}
	case "download":
		allowed, _, _ := s.resourceVersion(r.Context(), p, item, v, f)
		if !allowed {
			s.writeAPIError(w, r, 403, "resource.locked", "请先满足此版本的全部解锁条件", nil)
			return
		}
		if !resources.ValidID(in.Request) {
			s.resourceError(w, r, resources.ErrInvalid)
			return
		}
		for _, link := range v.Downloads {
			if link.ID == in.Link {
				replay, e := s.resources.Consume(r.Context(), resources.Delivery{Actor: actor, PostID: p.ID, Resource: item.ID, Variant: v.ID, Link: link.ID, Request: in.Request, Title: item.Title, Day: day, CreatedAt: time.Now().UTC()}, item)
				if e != nil {
					s.resourceError(w, r, e)
					return
				}
				remaining := -1
				if latest, err := s.resources.Backend.Read(r.Context()); err == nil {
					daily, total := resources.Counts(latest, actor, p.ID, item.ID, day)
					remaining = resources.Remaining(item, daily, total)
				}
				writeJSON(w, 200, map[string]any{"url": link.URL, "code": link.Code, "password": link.Password, "replayed": replay, "remaining": remaining})
				return
			}
		}
	}
	s.resourceError(w, r, resources.ErrInvalid)
}
func (s *Server) resourceHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := s.currencyPrincipal(w, r, false)
	if !ok {
		return
	}
	st, e := s.resources.Backend.Read(r.Context())
	if e != nil {
		s.resourceError(w, r, e)
		return
	}
	out := []map[string]any{}
	for i := len(st.Deliveries) - 1; i >= 0 && len(out) < 200; i-- {
		d := st.Deliveries[i]
		if d.Actor == resourceActorID(p.UserID) {
			post, e := s.repository.GetByID(r.Context(), d.PostID)
			if e != nil || !content.PublicAt(post, time.Now()) {
				continue
			}
			out = append(out, map[string]any{"title": d.Title, "url": s.contentPath(post) + "#resource-" + d.Resource, "variant": d.Variant, "created_at": d.CreatedAt})
		}
	}
	for i := len(st.Unlocks) - 1; i >= 0 && len(out) < 200; i-- {
		u := st.Unlocks[i]
		if u.Actor == resourceActorID(p.UserID) {
			post, e := s.repository.GetByID(r.Context(), u.PostID)
			if e == nil && content.PublicAt(post, time.Now()) {
				out = append(out, map[string]any{"title": u.Title, "url": s.contentPath(post) + "#resource-" + u.Resource, "variant": u.Variant, "created_at": u.CreatedAt})
			}
		}
	}
	if s.commerce != nil {
		if cs, e := s.commerce.Backend.Read(r.Context()); e == nil {
			for _, o := range cs.Orders {
				if o.UserID == p.UserID && o.Kind == "resource" && o.Status == "completed" && o.ResourceURL != "" {
					out = append(out, map[string]any{"title": o.Title, "url": o.ResourceURL, "created_at": o.CompletedAt})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["created_at"].(time.Time).After(out[j]["created_at"].(time.Time)) })
	seen := map[string]bool{}
	unique := []map[string]any{}
	for _, entry := range out {
		key := entry["url"].(string)
		if !seen[key] && len(unique) < 200 {
			seen[key] = true
			unique = append(unique, entry)
		}
	}
	writeJSON(w, 200, map[string]any{"items": unique})
}
func (s *Server) resourceOptions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentRead, auth.CapabilityContentWriteOwn); !ok {
		return
	}
	out := map[string]any{}
	if s.progression != nil {
		if cfg, e := s.progression.Store.Settings(r.Context()); e == nil {
			out["plans"] = cfg.Membership.Plans
			out["levels"] = cfg.Levels.Items
		}
	}
	if roles, e := s.auth.ListRoles(r.Context()); e == nil {
		out["roles"] = roles
	}
	if s.currencyCatalog != nil {
		if items, e := s.currencyCatalog.List(r.Context()); e == nil {
			out["currencies"] = items
		}
	}
	writeJSON(w, 200, out)
}
func (s *Server) resourceSubmissions(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityContentRead, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	id, e := strconv.ParseInt(r.PathValue("post"), 10, 64)
	if e != nil {
		s.resourceError(w, r, resources.ErrInvalid)
		return
	}
	post, e := s.repository.GetByID(r.Context(), id)
	if e != nil || !canReadContentObject(p, post) {
		s.writeAPIError(w, r, 404, "resource.not_found", "文章不存在", nil)
		return
	}
	st, e := s.resources.Backend.Read(r.Context())
	if e != nil {
		s.resourceError(w, r, e)
		return
	}
	out := []resources.Submission{}
	for _, sub := range st.Submissions {
		if sub.PostID == id {
			out = append(out, sub)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > 200 {
		out = out[:200]
	}
	writeJSON(w, 200, map[string]any{"items": out})
}
func (s *Server) resourceLibraryPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A compact account page shares the resource bundle across all installed themes.
	fmt.Fprintf(w, `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>我的下载</title><link rel="stylesheet" href="%s"><script defer src="%s"></script></head><body class="npr-library"><main><a href="/">← 返回首页</a><p class="npr-eyebrow">MY COLLECTION</p><h1>我的下载</h1><p>重新下载时会校验当前权限与剩余次数。</p><div data-npr-library role="status">正在加载下载记录…</div></main></body></html>`, html.EscapeString(resourceAsset("css")), html.EscapeString(resourceAsset("js")))
}
