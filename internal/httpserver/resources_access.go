package httpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/virtualcurrency"
)

type resourceFacts struct {
	user            int64
	roles           []string
	member          progression.Snapshot
	liked, followed bool
	comments        []comment.Comment
	tips            []recognition.Tip
	orders          []commerce.Order
	state           resources.State
	actor           string
	now             time.Time
	points          map[string]resourcePointReceipt
}

func (s *Server) resourceFacts(ctx context.Context, p content.Post, actor string, user int64) (resourceFacts, error) {
	f := resourceFacts{user: user, actor: actor, now: time.Now()}
	var err error
	if s.resources == nil {
		return f, resources.ErrUnavailable
	}
	f.state, err = s.resources.Backend.Read(ctx)
	if err != nil {
		return f, err
	}
	if user == 0 {
		return f, nil
	}
	u, err := s.auth.GetUser(ctx, user)
	if err != nil || u.Status != auth.UserStatusActive {
		return f, resources.ErrUnavailable
	}
	for _, role := range u.Roles {
		f.roles = append(f.roles, role.Slug)
	}
	// Load only facts used by this article. Disabled services never satisfy a rule.
	kinds := map[string]bool{}
	for _, r := range resources.FromMeta(p.Meta).Items {
		for _, v := range r.Variants {
			for _, g := range v.Groups {
				for _, c := range g.Conditions {
					kinds[c.Kind] = true
				}
			}
		}
	}
	if s.progression != nil && (kinds["level"] || kinds["membership"]) {
		f.member, err = s.progression.Store.Snapshot(ctx, user, 0)
		if err != nil {
			return f, err
		}
	}
	if s.engagements != nil {
		for _, entry := range []struct {
			kind  string
			id    int64
			value *bool
		}{{"like", p.ID, &f.liked}, {"follow", p.AuthorID, &f.followed}} {
			if kinds[entry.kind] && entry.id > 0 {
				items, e := s.engagements.States(ctx, user, entry.kind, []int64{entry.id})
				if e != nil {
					return f, e
				}
				if len(items) > 0 {
					*entry.value = items[0].Active
				}
			}
		}
	}
	if s.comments != nil && kinds["comment"] {
		list, e := s.comments.ListAdmin(ctx, comment.Query{PostID: p.ID, UserID: user, OwnOnly: true, Status: comment.StatusApproved, Limit: 100})
		if e != nil {
			return f, e
		}
		f.comments = list.Items
	}
	if s.recognition != nil && kinds["tip"] {
		st, e := s.recognition.Backend.Read(ctx)
		if e != nil {
			return f, e
		}
		for _, tip := range st.Tips {
			if tip.UserID == user && tip.PostID == p.ID && tip.EntryID > 0 {
				f.tips = append(f.tips, tip)
			}
		}
	}
	if s.commerce != nil && kinds["payment"] {
		var orders commerce.State
		orders, err = s.commerce.Backend.Read(ctx)
		for _, order := range orders.Orders {
			if order.UserID == user {
				f.orders = append(f.orders, order)
			}
		}
		if err != nil {
			return f, err
		}
	}
	f.points = map[string]resourcePointReceipt{}
	histories := map[string][]virtualcurrency.Entry{}
	for _, item := range resources.FromMeta(p.Meta).Items {
		for _, v := range item.Variants {
			for _, group := range v.Groups {
				for _, condition := range group.Conditions {
					if condition.Kind != "points" {
						continue
					}
					receipt, err := s.resourcePointReceipt(ctx, p, item, v, condition, f, histories)
					if err != nil {
						return f, err
					}
					f.points[resources.PurchaseKey(p.ID, item, v, condition)] = receipt
				}
			}
		}
	}
	return f, nil
}
func resourceSpend(user, post int64, r resources.Resource, v resources.Variant, c resources.Condition, version int64) virtualcurrency.SpendInput {
	key := resources.ConditionKey(post, r, v, c)
	return virtualcurrency.SpendInput{UserID: user, Amount: c.Amount, ExpectedVersion: version, RequestID: "resource-" + key[:48], Reason: "文章资源 " + key[:48]}
}
func (s *Server) resourceCoin(ctx context.Context, key string) (virtualcurrency.Store, error) {
	if s.currencyCatalog != nil {
		return s.currencyCatalog.Store(ctx, key)
	}
	if key == "main" && s.currency != nil {
		return s.currency, nil
	}
	return nil, resources.ErrUnavailable
}

func (s *Server) resourceConditionLabel(ctx context.Context, c resources.Condition) string {
	if c.Label == "" && (c.Kind == "points" || c.Kind == "tip") {
		if coin, err := s.resourceCoin(ctx, c.Currency); err == nil {
			if rules, err := coin.Settings(ctx); err == nil {
				verb := "使用"
				if c.Kind == "tip" {
					verb = "累计打赏达到"
				}
				return verb + " " + virtualcurrency.FormatAmount(c.Amount, rules.Precision) + " " + rules.Name
			}
		}
	}
	return resources.Label(c)
}

func (s *Server) resourceCheck(ctx context.Context, p content.Post, r resources.Resource, v resources.Variant, c resources.Condition, f resourceFacts) bool {
	if c.Kind == "public" {
		return true
	}
	if c.Kind == "guest" {
		return f.user == 0
	}
	sub := f.state.Submissions[resources.SubmissionKey(f.actor, p.ID, r, v)]
	if c.Kind == "quiz" {
		return sub.Passed
	}
	if c.Kind == "form" {
		return sub.FormComplete
	}
	if f.user == 0 {
		return false
	}
	switch c.Kind {
	case "login":
		return true
	case "follow":
		return f.followed
	case "like":
		return f.liked
	case "comment":
		for _, entry := range f.comments {
			if entry.UserID == f.user && entry.PostID == p.ID && entry.Status == comment.StatusApproved && virtualcurrency.VisibleLength(entry.ContentHTML) >= max(1, c.Minimum) {
				return true
			}
		}
	case "group":
		for _, role := range f.roles {
			if resources.Contains(c.Values, role) {
				return true
			}
		}
	case "level":
		if f.member.Settings.Levels.Enabled {
			level := 0
			for i, l := range f.member.Settings.Levels.Items {
				if f.member.Account.Experience >= l.Threshold {
					level = i + 1
				}
			}
			return level >= c.Minimum
		}
	case "membership":
		if !f.member.MembershipActive {
			return false
		}
		m := f.member.Account.Membership
		if len(c.Values) > 0 && !resources.Contains(c.Values, m.Plan) {
			return false
		}
		plan, ok := f.member.Settings.Plan(m.Plan)
		if !ok {
			return false
		}
		if max(1, plan.Tier) < c.Minimum {
			return false
		}
		switch c.Term {
		case "annual":
			return plan.Days >= 365
		case "monthly":
			return plan.Days >= 28 && plan.Days <= 31
		default:
			return true
		}
	case "points":
		return f.points[resources.PurchaseKey(p.ID, r, v, c)].entry.Kind == virtualcurrency.PurchasePayment
	case "payment":
		for _, o := range f.orders {
			if o.UserID == f.user && o.Status == "completed" && commerce.ResourceMatches(o, resources.Terms(p.ID, r, v, c)) {
				return true
			}
		}
	case "tip":
		var amount int64
		for _, tip := range f.tips {
			if tip.Currency == c.Currency {
				amount += tip.Amount
			}
		}
		return amount >= c.Amount
	}
	return false
}
func (s *Server) resourceAccess(ctx context.Context, p content.Post, r resources.Resource, v resources.Variant, f resourceFacts) (bool, []resources.GroupStatus) {
	allowed, groups := resources.Evaluate(v, func(c resources.Condition) bool { return s.resourceCheck(ctx, p, r, v, c, f) })
	if resourceRequiresAccount(r, v) && f.user == 0 {
		for i := range groups {
			groups[i].Passed = false
			groups[i].Conditions = append(groups[i].Conditions, resources.Check{Kind: "login", Label: "请登录后使用有次数限制的资源"})
		}
		return false, groups
	}
	return allowed, groups
}

// Browser cookies are replaceable and cannot enforce per-person quotas.
// Bind download quotas and quiz attempts to an authenticated account instead.
func resourceRequiresAccount(r resources.Resource, v resources.Variant) bool {
	return r.DailyLimit > 0 || r.TotalLimit > 0 || resourceNeeds(v, "quiz")
}

// resourceVersion applies the same availability and selection rules to state,
// purchase and delivery. A later version cannot be bought while an earlier
// version already matches a first-selection resource.
func (s *Server) resourceVersion(ctx context.Context, p content.Post, r resources.Resource, v resources.Variant, f resourceFacts) (bool, string, []resources.GroupStatus) {
	allowed, groups := s.resourceAccess(ctx, p, r, v, f)
	if !resources.Available(r, f.now) {
		return false, "closed", groups
	}
	if r.Selection == "first" {
		for _, prior := range r.Variants {
			if prior.ID == v.ID {
				break
			}
			if match, _ := s.resourceAccess(ctx, p, r, prior, f); match {
				return false, "selection", groups
			}
		}
	}
	return allowed, "", groups
}

func resourceGroupPrerequisites(group resources.GroupStatus) bool {
	for _, c := range group.Conditions {
		if c.Kind != "points" && c.Kind != "payment" && !c.Passed {
			return false
		}
	}
	return true
}
func resourceActorID(user int64) string { return fmt.Sprintf("u:%d", user) }
func resourceNeeds(v resources.Variant, kind string) bool {
	for _, g := range v.Groups {
		for _, c := range g.Conditions {
			if c.Kind == kind {
				return true
			}
		}
	}
	return false
}
func publicResourceQuestions(v resources.Variant) []map[string]any {
	out := []map[string]any{}
	for _, q := range v.Questions {
		out = append(out, map[string]any{"id": q.ID, "title": q.Title, "options": q.Options})
	}
	return out
}
func resourceKindLabel(kind string) string {
	switch kind {
	case "download":
		return "资源下载"
	case "image":
		return "专属图片"
	case "video":
		return "专属视频"
	}
	return "隐藏内容"
}
func trimResourceTitle(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 80 {
		return string(r[:80])
	}
	return string(r)
}
