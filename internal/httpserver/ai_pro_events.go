package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"slices"
	"time"
)

// Observe confirmed writes, including the receipt-based editor/API, bulk publishing and imports.
// Internal AI writes use Repository directly to avoid generating their own events.
type aiContentEvents struct {
	content.Repository
	s *Server
}

func (e *aiContentEvents) UnderlyingRepository() content.Repository { return e.Repository }
func (e *aiContentEvents) Create(ctx context.Context, in content.PostInput) (content.Post, error) {
	p, err := e.Repository.Create(ctx, in)
	if err == nil {
		e.s.enqueuePublished(p)
	}
	return p, err
}
func (e *aiContentEvents) Update(ctx context.Context, id int64, m content.Mutation, in content.PostInput) (content.Post, error) {
	p, err := e.Repository.Update(ctx, id, m, in)
	if err == nil {
		e.s.enqueuePublished(p)
	}
	return p, err
}
func (e *aiContentEvents) ChangeStatus(ctx context.Context, id int64, m content.Mutation, status string) (content.Post, error) {
	p, err := e.Repository.ChangeStatus(ctx, id, m, status)
	if err == nil {
		e.s.enqueuePublished(p)
	}
	return p, err
}
func (e *aiContentEvents) RestoreRevision(ctx context.Context, id, rev int64, m content.Mutation) (content.Post, error) {
	p, err := e.Repository.RestoreRevision(ctx, id, rev, m)
	if err == nil {
		e.s.enqueuePublished(p)
	}
	return p, err
}
func (e *aiContentEvents) BulkChangeStatus(ctx context.Context, in content.BulkStatusChange) ([]content.Post, error) {
	posts, err := e.Repository.BulkChangeStatus(ctx, in)
	if err == nil {
		for _, p := range posts {
			e.s.enqueuePublished(p)
		}
	}
	return posts, err
}
func (e *aiContentEvents) Submit(ctx context.Context, in content.Submission) (content.SubmissionResult, error) {
	r, err := e.Repository.Submit(ctx, in)
	if err == nil && !r.Replayed {
		if p, err := e.GetByID(ctx, r.Receipt.PostID); err == nil && p.Version == r.Receipt.Version {
			e.s.enqueuePublished(p)
		}
	}
	return r, err
}
func (e *aiContentEvents) ApplyImport(ctx context.Context, in content.ImportBatch, rev string) (content.ImportPlan, error) {
	r, err := e.Repository.ApplyImport(ctx, in, rev)
	if err == nil {
		for _, row := range r.Items {
			if row.Action != "created" {
				continue
			}
			if p, err := e.GetByID(ctx, row.PostID); err == nil {
				e.s.enqueuePublished(p)
			}
		}
	}
	return r, err
}
func (s *Server) aiBaseContent() content.Repository {
	if e, ok := s.repository.(*aiContentEvents); ok {
		return e.Repository
	}
	return s.repository
}
func (s *Server) aiActive() bool {
	if s == nil || s.plugins == nil || s.siteSettings == nil {
		return false
	}
	lease, err := s.aiProMaxLease()
	return err == nil && lease.active()
}
func (s *Server) automationState() (aijobs.State, bool) {
	if !s.aiActive() {
		return aijobs.State{}, false
	}
	st, err := s.aiJobs.Snapshot()
	return st, err == nil
}
func categoryMatches(p content.Post, r aijobs.Rules) bool {
	if r.CategoryID == 0 {
		return true
	}
	return slices.ContainsFunc(p.Categories, func(t content.Term) bool { return t.ID == r.CategoryID }) != r.ExcludeCategory
}
func contentWork(p content.Post, r aijobs.Rules, kind string) aijobs.Job {
	return aijobs.Job{Topic: p.Title, PostType: p.Type, Provider: r.Provider, Model: r.Model, Language: r.Language, UserID: r.UserID, Author: r.Author, Priority: 3, Work: aijobs.WorkSpec{Kind: kind, PostID: p.ID, PostVersion: p.Version, Rules: r, Key: fmt.Sprintf("%s/%d/%d/%d", kind, p.ID, p.Version, r.Version)}}
}
func (s *Server) enqueuePublished(p content.Post) {
	if p.Status != content.StatusPublish && p.Status != content.StatusFuture {
		return
	}
	st, ok := s.automationState()
	if !ok || st.Rules.Version == 0 {
		return
	}
	r := st.Rules
	policy, _ := p.Meta["ai_pro_max"].(string)
	if policy == "off" || (policy != "on" && !(p.Type == content.TypePost && r.AutoPost || p.Type == content.TypePage && r.AutoPage)) {
		return
	}
	if !categoryMatches(p, r) && !slices.Contains(enrichmentFields(p, r), "categoryID") {
		return
	}
	jobs := []aijobs.Job{}
	if wantsEnrichment(p, r) {
		j := contentWork(p, r, "enrich")
		j.Due = p.PublishedAt
		jobs = append(jobs, j)
	}
	if categoryMatches(p, r) && p.CommentStatus == content.CommentOpen && r.CommentsPerPost > 0 {
		j := contentWork(p, r, "interact")
		j.Work.Key = fmt.Sprintf("interactions/%d", p.ID)
		j.Work.Remaining = r.CommentsPerPost
		j.Priority = 1
		j.Due = maxDate(time.Now(), p.PublishedAt).Add(time.Duration(r.DelayMinutes) * time.Minute)
		jobs = append(jobs, j)
	}
	if len(jobs) > 0 {
		if err := s.aiJobs.AddWork(st.Epoch, jobs, time.Now()); err != nil {
			slog.Warn("AI Pro Max publish queue", "post", p.ID, "error", err)
		}
	}
}
func maxDate(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func (s *Server) enqueueComment(ctx context.Context, id int64) {
	st, ok := s.automationState()
	if !ok || st.Rules.Version == 0 {
		return
	}
	r := st.Rules
	if !r.Moderate && !r.Reply && !r.Followup && r.WakeWords == "" {
		return
	}
	c, err := s.comments.Get(ctx, id)
	if err != nil || c.StaffReply || (c.Status != comment.StatusPending && c.Status != comment.StatusApproved) {
		return
	}
	p, err := s.repository.GetByID(ctx, c.PostID)
	if err != nil || !categoryMatches(p, r) || !content.PublicAt(p, time.Now()) {
		return
	}
	j := contentWork(p, r, "comment")
	j.Topic = "评论 · " + p.Title
	if len(j.Topic) > 4000 {
		j.Topic = workbenchText(j.Topic, 200)
	}
	j.Priority = 7
	j.Work.Key = fmt.Sprintf("comment/%d/%d", id, c.Version)
	j.Work.CommentID = id
	j.Work.CommentVersion = c.Version
	if err := s.aiJobs.AddWork(st.Epoch, []aijobs.Job{j}, time.Now()); err != nil {
		slog.Warn("AI Pro Max comment queue", "comment", id, "error", err)
	}
}
