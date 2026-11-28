package httpserver

import (
	"context"
	"errors"
	"slices"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
)

func (s *Server) RunAIWritingQueue(ctx context.Context) {
	s.aiJobs.Run(ctx, func() bool { return s.moduleEnabled("ai") }, s.generateAIWritingJob)
}

func (s *Server) writingActorAllowed(ctx context.Context, j aijobs.Job) bool {
	return s.aiTextActorAllowed(ctx, j, true)
}

func (s *Server) aiTextActorAllowed(ctx context.Context, j aijobs.Job, requirePluginManagement bool) bool {
	if !s.auth.Configured() {
		return false
	}
	u, err := s.auth.GetUser(ctx, j.UserID)
	if j.UserID == 0 { // Environment-only administrator, never substitute for a missing persistent account.
		return errors.Is(err, auth.ErrUserStoreNotAvailable) && j.Author == s.auth.Username()
	}
	return err == nil && u.Status == auth.UserStatusActive && u.Can(auth.CapabilityAdminAccess) && (!requirePluginManagement || u.Can(auth.CapabilityPluginsManage)) && (u.Can(auth.CapabilityContentWrite) || u.Can(auth.CapabilityContentWriteOwn))
}

func writingCategories(all []aieditor.Category, typ string, id int64) ([]aieditor.Category, error) {
	if typ == content.TypePage {
		if id != 0 {
			return nil, aijobs.ErrInvalid
		}
		return nil, nil
	}
	if id == 0 {
		return all, nil
	}
	i := slices.IndexFunc(all, func(c aieditor.Category) bool { return c.ID == id })
	if i < 0 {
		return nil, aijobs.ErrCategory
	}
	return all[i : i+1], nil
}

func writingMessages(topic, lang string, categories []aieditor.Category) ([]aieditor.Message, error) {
	return (aieditor.Generation{PromptID: "article", Brief: topic, Tone: "natural", Length: "medium", Language: lang}).Messages(categories)
}

func (s *Server) generateAIWritingJob(parent context.Context, j aijobs.Job) (string, error) {
	module, ok := s.siteSettings.ModuleContext("ai")
	if !ok {
		return "", context.Canceled
	}
	ctx, cancel := context.WithTimeout(parent, aieditor.GenerationTimeout)
	defer cancel()
	stop := context.AfterFunc(module, cancel)
	defer stop()
	if !s.writingActorAllowed(ctx, j) {
		return "", aijobs.ErrActor
	}
	if j.Work.Kind != "" {
		return s.runProJob(ctx, j)
	}
	all, err := s.aiEditorCategories(ctx)
	if err != nil {
		return "", err
	}
	categories, err := writingCategories(all, j.PostType, j.CategoryID)
	if err != nil {
		return "", err
	}
	messages, err := writingMessages(j.Topic, j.Language, categories)
	if err != nil {
		return "", aijobs.ErrInvalid
	}
	if !s.aiState.acquire(j.UserID) {
		return "", aijobs.ErrBusy
	}
	defer s.aiState.release(j.UserID)
	if module.Err() != nil {
		return "", context.Canceled
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	text, err := s.autoText(ctx, j, messages, nil, 4096)
	if err != nil {
		return "", err
	}
	if _, err = aieditor.ParseDocument(text, categories); err != nil {
		return "", aijobs.ErrOutput
	}
	return text, nil
}
