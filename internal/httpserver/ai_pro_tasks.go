package httpserver

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math/rand/v2"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Server) runProJob(ctx context.Context, j aijobs.Job) (string, error) {
	if !s.aiState.acquire(j.UserID) {
		return "", aijobs.ErrBusy
	}
	defer s.aiState.release(j.UserID)
	if !s.jobCapability(ctx, j, auth.CapabilityContentWrite) {
		return "", aijobs.ErrActor
	}
	switch j.Work.Kind {
	case "external":
		return s.externalEnrichment(ctx, j)
	case "workflow":
		return s.runAIWorkflow(ctx, j)
	case "random":
		return s.randomInteraction(ctx, j)
	}
	p, err := s.repository.GetByID(ctx, j.Work.PostID)
	if err != nil {
		return "", aijobs.ErrStale
	}
	switch j.Work.Kind {
	case "enrich":
		return s.enrichment(ctx, j, p)
	case "comment", "interact", "release":
		return s.aiDiscussion(ctx, j, p)
	}
	return "", aijobs.ErrInvalid
}
func (s *Server) runAIWorkflow(ctx context.Context, j aijobs.Job) (string, error) {
	key := aiDigest("workflow/" + j.Work.Key)
	if saved, err := s.aiBaseContent().FindSubmission(ctx, key, key); err == nil {
		// The process may have stopped after saving the post but before the
		// queue recorded success. Reuse the durable creation receipt before
		// requesting another title, article or image from the provider.
		return fmt.Sprintf("文章 #%d · 已保存", saved.Receipt.PostID), nil
	} else if !errors.Is(err, content.ErrNotFound) {
		return "", err
	}
	all, err := s.aiEditorCategories(ctx)
	if err != nil {
		return "", err
	}
	categories, err := writingCategories(all, "post", j.CategoryID)
	if err != nil {
		return "", err
	}
	recent, err := s.repository.ListAdmin(ctx, content.AdminQuery{Type: "post", Limit: 50})
	if err != nil {
		return "", err
	}
	titles := []string{}
	for _, p := range recent.Items {
		if !content.PublicAt(p, time.Now()) && !(p.AuthorID == j.UserID && p.AuthorName == j.Author && p.Status == content.StatusPending) {
			continue
		}
		titles = append(titles, p.Title)
	}
	facts := map[string]any{"方向": j.Topic, "分类": categories, "避免重复的标题": titles}
	title := j.Work.PlannedTitle
	if title == "" {
		n := max(1, min(j.Work.Strategy.Ahead, 20))
		topics, err := s.autoText(ctx, j, quotedMessages(fmt.Sprintf("策划 %d 个具体且各不相同的新选题，只返回 JSON 标题数组", n), facts, j.Language), nil, 1600)
		if err != nil {
			return "", err
		}
		candidates, err := parseTopicPool(topics, 20)
		if err != nil || len(candidates) == 0 {
			return "", aijobs.ErrOutput
		}
		title = candidates[0]
		if j.Work.ScheduleID != "" && len(candidates) > 1 {
			if err = s.aiJobs.StoreTitles(ctx, j, candidates[1:]); err != nil {
				return "", err
			}
		}
	}
	var sources []proSearchSource
	if j.Work.Strategy.Search {
		sources, err = searchProSources(ctx, title)
		if err != nil {
			return "", err
		}
	}
	messages, err := writingMessages(workflowBrief(title, j.Work.Strategy, sources), j.Language, categories)
	if err != nil {
		return "", err
	}
	raw, err := s.autoText(ctx, j, messages, nil, 4096)
	if err != nil {
		return "", err
	}
	doc, err := aieditor.ParseDocument(raw, categories)
	if err != nil {
		return "", aijobs.ErrOutput
	}
	in := content.PostInput{Type: "post", Status: j.Work.Destination, Title: doc.Title, Slug: doc.Slug, Excerpt: doc.Excerpt, ContentSource: doc.ContentSource, CategoryIDs: doc.CategoryIDs, TagNames: doc.Tags, CommentStatus: content.CommentOpen, AuthorID: j.UserID, AuthorName: j.Author, Actor: "AI Pro Max · " + j.Author}
	if j.CategoryID != 0 {
		in.CategoryIDs = []int64{j.CategoryID}
	}
	if in.Status != content.StatusPublish {
		in.Status = content.StatusPending
	}
	for i := 0; i < j.Work.Strategy.Images; i++ {
		if !s.jobCapability(ctx, j, auth.CapabilityMediaWrite) {
			return "", aijobs.ErrActor
		}
		prompt := doc.CoverPrompt
		if prompt == "" {
			prompt = doc.Title
		}
		item, e := s.autoImage(ctx, j, doc.Title, fmt.Sprintf("%s。第 %d 幅，不同视角", prompt, i+1))
		if e != nil {
			return "", e
		}
		if i == 0 {
			in.CoverURL = item.PublicURL
		} else {
			in.ContentSource += "\n<figure><img src=\"" + html.EscapeString(item.PublicURL) + "\" alt=\"" + html.EscapeString(doc.Title) + "\"></figure>"
		}
	}
	var id int64
	err = s.aiJobs.Commit(ctx, j, func() error {
		if !s.jobCapability(ctx, j, auth.CapabilityContentWrite) {
			return aijobs.ErrActor
		}
		result, e := s.aiBaseContent().Submit(ctx, content.Submission{Key: key, Fingerprint: key, Action: "create", Actor: in.Actor, Input: in})
		id = result.Receipt.PostID
		return e
	})
	if err == nil && in.Status == content.StatusPublish {
		if post, e := s.repository.GetByID(ctx, id); e == nil {
			s.enqueuePublished(post)
		}
	}
	return fmt.Sprintf("文章 #%d · %s", id, doc.Title), err
}
func (s *Server) randomInteraction(ctx context.Context, j aijobs.Job) (string, error) {
	if !s.jobCapability(ctx, j, auth.CapabilityCommentsModerate) {
		return "", aijobs.ErrActor
	}
	first, err := s.repository.ListPublishedPage(ctx, 1, 0)
	if err != nil {
		return "", err
	}
	if first.Total == 0 {
		return "没有已发布文章", nil
	}
	page, err := s.repository.ListPublishedPage(ctx, 20, rand.IntN(first.Total))
	if err != nil {
		return "", err
	}
	for _, p := range page.Items {
		if p.CommentStatus == content.CommentOpen && categoryMatches(p, j.Work.Rules) {
			j.Work.PostID = p.ID
			j.Work.Kind = "interact"
			return s.aiDiscussion(ctx, j, p)
		}
	}
	return "没有匹配的开放评论文章", nil
}
func hitsRule(text, words string) bool {
	text = strings.ToLower(text)
	for _, w := range strings.FieldsFunc(words, func(r rune) bool { return strings.ContainsRune(",，\n\r", r) }) {
		w = strings.TrimSpace(strings.ToLower(w))
		if w != "" && strings.Contains(text, w) {
			return true
		}
	}
	return false
}
func (s *Server) aiDiscussion(ctx context.Context, j aijobs.Job, p content.Post) (string, error) {
	if !s.moduleEnabled("comments") || !s.jobCapability(ctx, j, auth.CapabilityCommentsModerate) {
		return "", aijobs.ErrActor
	}
	if !content.PublicAt(p, time.Now()) || p.CommentStatus != content.CommentOpen {
		return "", aijobs.ErrStale
	}
	r := j.Work.Rules
	c := comment.Comment{}
	if j.Work.CommentID != 0 {
		var err error
		c, err = s.comments.Get(ctx, j.Work.CommentID)
		if err != nil || c.PostID != p.ID || c.Version != j.Work.CommentVersion || c.Status != comment.StatusPending && c.Status != comment.StatusApproved {
			return "", aijobs.ErrStale
		}
	}
	if j.Work.Kind == "release" {
		return s.releaseAIComment(ctx, j, p, c)
	}
	facts := map[string]any{"文章": p.Title, "正文": aiCommentContext(p.ContentSource, 6000), "评论": c.ContentSource, "角色": r.Persona}
	var text string
	if j.Work.Kind == "comment" {
		wake := hitsRule(c.ContentSource, r.WakeWords)
		reply := wake || (c.ParentID == 0 && r.Reply) || (c.ParentID != 0 && r.Followup)
		decision := ""
		moderate := r.Moderate && c.Status == comment.StatusPending
		if moderate && hitsRule(c.ContentSource, r.BlockWords) {
			decision = "spam"
		} else if moderate && hitsRule(c.ContentSource, r.HoldWords) {
			return "命中人工复核规则，保留待审", nil
		} else if moderate || reply {
			facts["唤醒角色"] = wake
			raw, err := s.autoText(ctx, j, quotedMessages("根据文章和评论审核垃圾广告并拟回复。只返回 JSON {decision: approved、pending 或 spam, reply: 回复文本}。不确定时保留 pending。不得编造购买经历或冒充读者", facts, j.Language), nil, 900)
			if err != nil {
				return "", err
			}
			var out struct{ Decision, Reply string }
			if err = decodeAIObject(raw, &out); err != nil {
				return "", err
			}
			decision = out.Decision
			text = out.Reply
		}
		if moderate {
			switch decision {
			case "pending":
				return "保留人工复核", nil
			case "approved", "spam":
			default:
				return "", aijobs.ErrOutput
			}
			err := s.aiJobs.Commit(ctx, j, func() error {
				if !s.jobCapability(ctx, j, auth.CapabilityCommentsModerate) {
					return aijobs.ErrActor
				}
				v, e := s.comments.ChangeStatus(ctx, c.ID, comment.Mutation{ExpectedVersion: c.Version, Actor: "AI Pro Max · " + j.Author}, decision)
				if e == nil {
					c = v
				}
				return e
			})
			if err != nil {
				return "", err
			}
		}
		if !reply || c.Status != comment.StatusApproved {
			return "已完成评论审核", nil
		}
		j.Work.CommentVersion = c.Version
	} else {
		raw, err := s.autoText(ctx, j, quotedMessages("以标注为 AI 的讨论助手身份，就文章提出一个具体问题或补充看法，仅输出一条评论正文。不要编造亲身经历", facts, j.Language), nil, 450)
		if err != nil {
			return "", err
		}
		text = raw
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > comment.MaxContentRunes || len(text) > comment.MaxContentBytes {
		return "", aijobs.ErrOutput
	}
	j.Work.Kind = "release"
	j.Work.Destination = text
	j.Work.PostID = p.ID
	due := time.Now()
	if c.ID != 0 {
		due = due.Add(time.Duration(r.DelayMinutes) * time.Minute)
	}
	return "", &aijobs.NextStage{Work: j.Work, Due: due}
}
func (s *Server) releaseAIComment(ctx context.Context, j aijobs.Job, p content.Post, c comment.Comment) (string, error) {
	key := aiDigest(fmt.Sprintf("comment/%s/%d", j.Work.Key, j.Work.Remaining))
	name := strings.TrimSpace(j.Work.Rules.Persona)
	if name == "" {
		name = "讨论助手"
	}
	name = "AI · " + aiCommentContext(name, 60)
	err := s.aiJobs.Commit(ctx, j, func() error {
		if !s.jobCapability(ctx, j, auth.CapabilityCommentsModerate) || !s.moduleEnabled("comments") {
			return aijobs.ErrActor
		}
		current, e := s.aiBaseContent().GetByID(ctx, p.ID)
		if e != nil || !content.PublicAt(current, time.Now()) || current.CommentStatus != content.CommentOpen {
			return aijobs.ErrStale
		}
		if c.ID != 0 {
			parent, e := s.comments.Get(ctx, c.ID)
			if e != nil || parent.Status != comment.StatusApproved || parent.Version != c.Version {
				return aijobs.ErrStale
			}
		}
		_, e = s.comments.CreateOnce(ctx, comment.Submission{Key: key, Fingerprint: key, Input: comment.CreateInput{PostID: p.ID, ParentID: c.ID, AuthorName: name, AuthorEmail: "ai@newpress.invalid", ContentSource: j.Work.Destination, Status: comment.StatusApproved}})
		return e
	})
	if errors.Is(err, comment.ErrInvalidParent) {
		err = aijobs.ErrStale
	}
	if err == nil && j.Work.Remaining > 1 {
		j.Work.Remaining--
		j.Work.Kind = "interact"
		j.Work.Destination = ""
		return "", &aijobs.NextStage{Work: j.Work, Due: time.Now().Add(time.Duration(max(1, j.Work.Rules.DelayMinutes)) * time.Minute)}
	}
	return "已发布 AI 评论", err
}
