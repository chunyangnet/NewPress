package cdn

import (
	"context"
	"net/url"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

func escapedPath(path string) string { return (&url.URL{Path: path}).EscapedPath() }
func postPaths(posts ...content.Post) []string {
	paths := []string{"/", "/feed.xml", "/sitemap.xml"}
	for _, p := range posts {
		if p.ID == 0 || p.Slug == "" {
			continue
		}
		paths = append(paths, escapedPath(content.PublicPath(p)))
		for _, t := range p.Categories {
			paths = append(paths, escapedPath("/categories/"+t.Slug))
		}
		for _, t := range p.Tags {
			paths = append(paths, escapedPath("/tags/"+t.Slug))
		}
	}
	return paths
}

type contentHooks struct {
	content.Repository
	service *Service
}

func (r *contentHooks) UnderlyingRepository() content.Repository { return r.Repository }

func WrapRepository(r content.Repository, s *Service) content.Repository { return &contentHooks{r, s} }
func (r *contentHooks) changed(before, after content.Post, err error) {
	if err == nil && (content.PublicAt(before, time.Now()) || content.PublicAt(after, time.Now())) {
		r.service.PurgeURLs("content", postPaths(before, after))
	}
}
func (r *contentHooks) Create(ctx context.Context, in content.PostInput) (content.Post, error) {
	p, e := r.Repository.Create(ctx, in)
	r.changed(content.Post{}, p, e)
	return p, e
}
func (r *contentHooks) Update(ctx context.Context, id int64, m content.Mutation, in content.PostInput) (content.Post, error) {
	old, _ := r.GetByID(ctx, id)
	p, e := r.Repository.Update(ctx, id, m, in)
	r.changed(old, p, e)
	return p, e
}
func (r *contentHooks) ChangeStatus(ctx context.Context, id int64, m content.Mutation, status string) (content.Post, error) {
	old, _ := r.GetByID(ctx, id)
	p, e := r.Repository.ChangeStatus(ctx, id, m, status)
	r.changed(old, p, e)
	return p, e
}
func (r *contentHooks) RestoreRevision(ctx context.Context, id, rev int64, m content.Mutation) (content.Post, error) {
	old, _ := r.GetByID(ctx, id)
	p, e := r.Repository.RestoreRevision(ctx, id, rev, m)
	r.changed(old, p, e)
	return p, e
}
func (r *contentHooks) BulkChangeStatus(ctx context.Context, in content.BulkStatusChange) ([]content.Post, error) {
	old := map[int64]content.Post{}
	for _, item := range in.Items {
		p, _ := r.GetByID(ctx, item.ID)
		old[item.ID] = p
	}
	posts, e := r.Repository.BulkChangeStatus(ctx, in)
	if e == nil {
		paths := []string{}
		for _, p := range posts {
			if content.PublicAt(old[p.ID], time.Now()) || content.PublicAt(p, time.Now()) {
				paths = append(paths, postPaths(old[p.ID], p)...)
			}
		}
		r.service.PurgeURLs("content", paths)
	}
	return posts, e
}
func (r *contentHooks) Submit(ctx context.Context, in content.Submission) (content.SubmissionResult, error) {
	old, _ := r.GetByID(ctx, in.PostID)
	result, e := r.Repository.Submit(ctx, in)
	if e == nil && !result.Replayed {
		p, loadErr := r.GetByID(context.WithoutCancel(ctx), result.Receipt.PostID)
		r.changed(old, p, loadErr)
	}
	return result, e
}

// Publication and text-update events also cover writes committed directly by
// the transactional plugin service. Existing queued purges coalesce by targets.
func (s *Service) Observe(ctx context.Context, r content.Repository, event activity.Event) error {
	if event.Kind != activity.Publication && event.Kind != activity.ContentUpdated {
		return nil
	}
	p, e := r.GetByID(ctx, event.PostID)
	if e == nil && content.PublicAt(p, time.Now()) {
		s.PurgeURLs("content", postPaths(p))
	}
	return nil
}

type commentHooks struct {
	comment.Store
	posts   content.Repository
	service *Service
}

func WrapComments(c comment.Store, r content.Repository, s *Service) comment.Store {
	return &commentHooks{c, r, s}
}
func (c *commentHooks) changed(ctx context.Context, id int64, e error) {
	if e != nil {
		return
	}
	p, err := c.posts.GetByID(context.WithoutCancel(ctx), id)
	if err == nil && content.PublicAt(p, time.Now()) {
		c.service.PurgeURLs("comment", postPaths(p))
	}
}
func (c *commentHooks) Create(ctx context.Context, in comment.CreateInput) (comment.Comment, error) {
	v, e := c.Store.Create(ctx, in)
	if v.Status == comment.StatusApproved {
		c.changed(ctx, v.PostID, e)
	}
	return v, e
}
func (c *commentHooks) CreateOnce(ctx context.Context, in comment.Submission) (comment.SubmissionResult, error) {
	v, e := c.Store.CreateOnce(ctx, in)
	if e == nil && !v.Replayed && in.Input.Status == comment.StatusApproved {
		c.changed(ctx, v.Receipt.PostID, nil)
	}
	return v, e
}
func (c *commentHooks) Edit(ctx context.Context, id int64, m comment.Mutation, in comment.EditInput) (comment.Comment, error) {
	v, e := c.Store.Edit(ctx, id, m, in)
	c.changed(ctx, v.PostID, e)
	return v, e
}
func (c *commentHooks) Reply(ctx context.Context, in comment.ReplyInput) (comment.Comment, error) {
	v, e := c.Store.Reply(ctx, in)
	c.changed(ctx, v.PostID, e)
	return v, e
}
func (c *commentHooks) RestoreRevision(ctx context.Context, id, rev int64, m comment.Mutation) (comment.Comment, error) {
	v, e := c.Store.RestoreRevision(ctx, id, rev, m)
	c.changed(ctx, v.PostID, e)
	return v, e
}
func (c *commentHooks) ChangeStatus(ctx context.Context, id int64, m comment.Mutation, status string) (comment.Comment, error) {
	v, e := c.Store.ChangeStatus(ctx, id, m, status)
	c.changed(ctx, v.PostID, e)
	return v, e
}
func (c *commentHooks) BulkChangeStatus(ctx context.Context, in comment.BulkStatusChange) ([]comment.Comment, error) {
	v, e := c.Store.BulkChangeStatus(ctx, in)
	if e == nil {
		seen := map[int64]bool{}
		for _, item := range v {
			if !seen[item.PostID] {
				seen[item.PostID] = true
				c.changed(ctx, item.PostID, nil)
			}
		}
	}
	return v, e
}
func (c *commentHooks) Delete(ctx context.Context, id int64, m comment.Mutation) error {
	old, _ := c.Get(ctx, id)
	e := c.Store.Delete(ctx, id, m)
	c.changed(ctx, old.PostID, e)
	return e
}
