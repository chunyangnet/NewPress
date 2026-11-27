package wechat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"log/slog"
	"maps"
	"net/url"
	"sync"
	"time"

	imagedraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/assetref"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

type Service struct {
	// ModuleEnabled gates new work without replacing the service configuration.
	ModuleEnabled    func() bool
	Store            Store
	Client           *Client
	Content          content.Repository
	Media            *media.Manager
	PublicURL        string
	localPreparation sync.Mutex
}

func (s *Service) SaveConfig(ctx context.Context, input Config, clear bool) (Config, error) {
	var result Config
	err := s.Store.Update(ctx, func(state *State) error {
		old := state.Config
		if input.Version != old.Version {
			return ErrConflict
		}
		if clear {
			if input.Enabled {
				return errors.New("清除密钥前请先关闭同步")
			}
			input.Secret = ""
		}
		if input.Secret == "" && !clear && input.AppID == old.AppID {
			input.Secret = old.Secret
		}
		if input.AppID != old.AppID {
			for _, j := range state.Jobs {
				if j.Status == "working" || j.Status == "publishing" {
					return errors.New("请先等待当前公众号任务结束，再切换 AppID")
				}
			}
		}
		input.Since = old.Since
		if input.Enabled && input.AutoSync && (!old.Enabled || !old.AutoSync || input.AppID != old.AppID) {
			input.Since = time.Now().UTC()
		}
		input.Version++
		input.HasSecret = false
		if err := input.Validate(); err != nil {
			return err
		}
		state.Config = input
		result = input.Public()
		return nil
	})
	return result, err
}
func (s *Service) Observe(ctx context.Context, event activity.Event) error {
	if s.ModuleEnabled != nil && !s.ModuleEnabled() {
		return nil
	}
	if event.Kind != activity.Publication {
		return nil
	}
	cfg, err := ReadConfig(ctx, s.Store)
	if err != nil {
		return err
	}
	if !cfg.Enabled || !cfg.AutoSync || event.At.Before(cfg.Since) {
		return nil
	}
	post, err := s.Content.GetByID(ctx, event.PostID)
	if errors.Is(err, content.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !content.PublicAt(post, time.Now()) || post.Type != content.TypePost {
		return nil
	}
	_, err = s.queue(ctx, post, "自动同步", true, 0)
	return err
}
func (s *Service) Queue(ctx context.Context, id, version int64, actor string) (Job, error) {
	return s.QueueWithConfig(ctx, id, version, 0, actor)
}

// QueueWithConfig binds manual sends to the draft/publication mode the user saw.
func (s *Service) QueueWithConfig(ctx context.Context, id, version, configVersion int64, actor string) (Job, error) {
	post, err := s.Content.GetByID(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if post.Version != version {
		return Job{}, ErrConflict
	}
	if post.Type != content.TypePost || post.Status == content.StatusTrash || post.Status == content.StatusPrivate || post.Status == content.StatusPending {
		return Job{}, errors.New("仅支持同步可编辑的文章草稿或已发布文章")
	}
	return s.queue(ctx, post, actor, false, configVersion)
}
func (s *Service) queue(ctx context.Context, post content.Post, actor string, automatic bool, configVersion int64) (Job, error) {
	var result Job
	err := s.Store.Update(ctx, func(state *State) error {
		cfg := state.Config
		if configVersion != 0 && configVersion != cfg.Version {
			return ErrConflict
		}
		if !cfg.Enabled {
			if automatic {
				return nil
			}
			return ErrDisabled
		}
		if automatic && (!cfg.AutoSync || post.CreatedAt.Before(cfg.Since) && post.PublishedAt.Before(cfg.Since)) {
			return nil
		}
		id := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%s:%d:%d", cfg.AppID, post.ID, post.Version)))
		for _, job := range state.Jobs {
			if job.AppID == cfg.AppID && job.PostID == post.ID {
				if automatic || job.ID == id {
					result = job
					return nil
				}
				if job.Status == "working" || job.Status == "queued" || job.Status == "publishing" || job.Status == "uncertain" {
					return ErrBusy
				}
			}
		}
		if len(state.Jobs) >= MaxJobs {
			return errors.New("公众号同步记录已达容量上限")
		}
		now := time.Now().UTC()
		result = Job{ID: id, AppID: cfg.AppID, PostID: post.ID, Version: post.Version, Title: post.Title, Actor: actor, AutoPublish: cfg.AutoPublish, Status: "queued", Stage: "prepare", CreatedAt: now, UpdatedAt: now, NextAt: now, Images: map[string]string{}}
		state.Jobs = append(state.Jobs, result)
		return nil
	})
	return result.Public(), err
}
func (s *Service) Action(ctx context.Context, id, action string, expected time.Time) error {
	// Fetch content outside the state transaction: PostgreSQL may use a small
	// connection pool. The worker rechecks the version before any remote write.
	var resumePost struct {
		ID, Version int64
		Allowed     bool
	}
	if action == "resume" {
		state, err := s.Store.View(ctx)
		if err != nil {
			return err
		}
		for _, job := range state.Jobs {
			if job.ID == id && job.PublishID == "" {
				post, err := s.Content.GetByID(ctx, job.PostID)
				if err != nil {
					return errors.New("文章不存在或暂不可读取，请刷新后核对")
				}
				resumePost.ID, resumePost.Version = post.ID, post.Version
				resumePost.Allowed = post.Type == content.TypePost && (post.Status == content.StatusDraft || post.Status == content.StatusPublish || post.Status == content.StatusFuture)
				break
			}
		}
	}
	return s.Store.Update(ctx, func(state *State) error {
		for i := range state.Jobs {
			j := &state.Jobs[i]
			if j.ID != id {
				continue
			}
			if !j.UpdatedAt.Equal(expected) {
				return ErrConflict
			}
			if action != "cancel" && (j.AppID != state.Config.AppID || action != "draft_only" && !state.Config.Enabled) {
				return ErrDisabled
			}
			if j.Status == "uncertain" {
				return errors.New("操作结果待确认，请先在公众号后台核对，不能直接重复提交")
			}
			clearError := true
			switch action {
			case "resume":
				if j.Status != "skipped" || j.Error != "" {
					return ErrConflict
				}
				if j.PublishID == "" && (!resumePost.Allowed || resumePost.ID != j.PostID || resumePost.Version != j.Version) {
					return errors.New("文章已更新或不可同步，请选择最新保存版本重新同步")
				}
				for _, other := range state.Jobs {
					if other.ID != j.ID && other.AppID == j.AppID && other.PostID == j.PostID && (other.Status == "queued" || other.Status == "working" || other.Status == "publishing" || other.Status == "uncertain") {
						return ErrBusy
					}
				}
				if j.PublishID != "" {
					j.Status, j.Stage = "publishing", "poll"
				} else {
					j.Status, j.Stage = "queued", "prepare"
				}
			case "draft_only":
				if !j.AutoPublish || j.PublishID != "" || (j.Status != "queued" && j.Status != "failed" && j.Status != "skipped") {
					return ErrConflict
				}
				j.AutoPublish = false
				clearError = false
				if j.MediaID != "" {
					j.Status, j.Stage = "draft", "draft"
					clearError = true
				}
			case "retry":
				if j.Status != "failed" {
					return ErrConflict
				}
				if j.PublishID != "" {
					j.Status = "publishing"
					j.Stage = "poll"
				} else {
					j.Status = "queued"
					if j.MediaID != "" {
						j.Stage = "publish"
					} else {
						j.Stage = "prepare"
					}
				}
			case "publish":
				if j.Status != "draft" || j.MediaID == "" {
					return ErrConflict
				}
				j.Status = "queued"
				j.Stage = "publish"
				j.AutoPublish = true
			case "refresh":
				if j.PublishID == "" || j.Status == "working" {
					return ErrConflict
				}
				j.Status = "publishing"
				j.Stage = "poll"
			case "cancel":
				if j.Status != "queued" && j.Status != "failed" {
					return ErrConflict
				}
				j.Status = "skipped"
			default:
				return errors.New("无效任务操作")
			}
			if clearError {
				j.Error = ""
			}
			j.NextAt = time.Now().UTC()
			j.UpdatedAt = j.NextAt
			return nil
		}
		return errors.New("同步记录不存在")
	})
}

func (s *Service) Resolve(ctx context.Context, id string, expected time.Time, mediaID, publishID string) error {
	if (mediaID == "") == (publishID == "") || !validRemoteID(mediaID+publishID) {
		return errors.New("请填写一个核对后的草稿素材 ID 或发表任务 ID")
	}
	return s.Store.Update(ctx, func(state *State) error {
		for i := range state.Jobs {
			j := &state.Jobs[i]
			if j.ID != id {
				continue
			}
			if j.Status != "uncertain" || !j.UpdatedAt.Equal(expected) {
				return ErrConflict
			}
			if j.AppID != state.Config.AppID {
				return ErrConflict
			}
			if (j.Stage == "publish" && publishID == "") || (j.Stage == "draft" && mediaID == "") {
				return errors.New("请填写当前待确认阶段对应的标识，发表结果待确认时不能退回草稿重发")
			}
			if publishID != "" {
				j.PublishID = publishID
				j.Stage = "poll"
				j.Status = "publishing"
			} else {
				j.MediaID = mediaID
				j.Stage = "draft"
				j.Status = "draft"
			}
			j.Error = ""
			j.UpdatedAt = time.Now().UTC()
			j.NextAt = j.UpdatedAt
			return nil
		}
		return ErrConflict
	})
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.Tick(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.Error("wechat worker unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	if s.ModuleEnabled != nil && !s.ModuleEnabled() {
		return nil
	}
	var job Job
	var cfg Config
	err := s.Store.Update(ctx, func(state *State) error {
		cfg = state.Config
		for i := range state.Jobs {
			j := &state.Jobs[i]
			if j.Status == "working" && !j.LeaseUntil.After(now) {
				j.UpdatedAt = now
				if j.Stage == "draft" || j.Stage == "publish" {
					j.Status = "uncertain"
					j.Error = "处理进程中断，请在公众平台核对结果后填写素材或发表任务 ID"
				} else if j.Stage == "poll" {
					j.Status = "publishing"
				} else {
					j.Status = "queued"
				}
			}
		}
		if !cfg.Enabled {
			return nil
		}
		for i := range state.Jobs {
			j := &state.Jobs[i]
			if (j.Status == "queued" || j.Status == "publishing") && !j.NextAt.After(now) && j.AppID == cfg.AppID {
				j.Status = "working"
				j.Attempts++
				j.UpdatedAt = now
				j.LeaseUntil = now.Add(3 * time.Minute)
				job = *j
				job.Images = maps.Clone(j.Images)
				break
			}
		}
		return nil
	})
	if err != nil || job.ID == "" {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	err = s.process(runCtx, cfg, &job)
	if err != nil {
		var api *APIError
		job.Error = err.Error()
		job.Status = "failed"
		if errors.Is(err, ErrCheckBusy) {
			// Local contention happens before any upload in this attempt.
			job.Status = "queued"
			job.Error = ""
			job.NextAt = time.Now().Add(5 * time.Second)
		}
		if errors.As(err, &api) && api.Uncertain {
			job.Status = "uncertain"
		}
		if job.Stage == "poll" && job.PublishID != "" {
			job.Status = "publishing"
			job.NextAt = time.Now().Add(2 * time.Minute)
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	return s.checkpoint(finishCtx, &job)
}
func (s *Service) checkpoint(ctx context.Context, j *Job) error {
	return s.Store.Update(ctx, func(state *State) error {
		for i, old := range state.Jobs {
			if old.ID == j.ID {
				if old.Status != "working" || old.Attempts != j.Attempts {
					return ErrConflict
				}
				j.UpdatedAt = time.Now().UTC()
				state.Jobs[i] = *j
				state.Jobs[i].Images = maps.Clone(j.Images)
				return nil
			}
		}
		return ErrConflict
	})
}
func (s *Service) stillEnabled(ctx context.Context, cfg Config) error {
	current, err := ReadConfig(ctx, s.Store)
	if err != nil {
		return err
	}
	if !current.Enabled || current.Version != cfg.Version {
		return errors.New("公众号配置已变化，请核对后重试")
	}
	return nil
}
func (s *Service) stillCurrent(ctx context.Context, cfg Config, j *Job) error {
	if err := s.stillEnabled(ctx, cfg); err != nil {
		return err
	}
	post, err := s.Content.GetByID(ctx, j.PostID)
	if err != nil {
		return errors.New("本站文章不存在或暂时不可读取")
	}
	if post.Version != j.Version || !syncablePost(post) {
		return errors.New("本站文章已更新或不可同步，请选择最新保存版本重新同步")
	}
	return nil
}
func (s *Service) process(ctx context.Context, cfg Config, j *Job) error {
	if j.Stage == "poll" {
		r, err := s.Client.Poll(ctx, cfg, j.PublishID)
		if err != nil {
			return err
		}
		j.Error = ""
		if *r.Status == 1 {
			j.Status = "publishing"
			j.NextAt = time.Now().Add(30 * time.Second)
			return nil
		}
		if *r.Status != 0 {
			j.Status = "failed"
			j.Error = publishFailure(*r.Status)
			return nil
		}
		if r.ArticleID == "" {
			return errors.New("微信已返回成功，但文章标识缺失，稍后继续核对")
		}
		j.Status = "published"
		j.ArticleID = r.ArticleID
		if len(r.Detail.Items) > 0 {
			u, err := ArticleURL(r.Detail.Items[0].URL)
			if err == nil {
				j.ArticleURL = u
			}
		}
		j.Error = ""
		return nil
	}
	post, err := s.Content.GetByID(ctx, j.PostID)
	if err != nil {
		return errors.New("本站文章不存在或暂时不可读取")
	}
	if post.Version != j.Version || post.Status == content.StatusTrash || post.Status == content.StatusPrivate || post.Status == content.StatusPending {
		j.Status = "skipped"
		j.Error = "本站文章已更新或不可同步，请核对最新版本后重新操作"
		return nil
	}
	if j.MediaID == "" {
		article, err := s.prepare(ctx, cfg, j, post)
		if err != nil {
			return err
		}
		if err = s.stillCurrent(ctx, cfg, j); err != nil {
			return err
		}
		j.Stage = "draft"
		if err = s.checkpoint(ctx, j); err != nil {
			return err
		}
		j.MediaID, err = s.Client.draft(ctx, cfg, article, func(ctx context.Context) error { return s.stillCurrent(ctx, cfg, j) })
		if err != nil {
			return err
		}
		j.Stage = "prepare"
		if err = s.checkpoint(ctx, j); err != nil {
			return err
		}
	}
	if !j.AutoPublish {
		j.Status = "draft"
		j.Stage = "draft"
		return nil
	}
	if err = s.stillCurrent(ctx, cfg, j); err != nil {
		return err
	}
	j.Stage = "publish"
	if err = s.checkpoint(ctx, j); err != nil {
		return err
	}
	j.PublishID, err = s.Client.publish(ctx, cfg, j.MediaID, func(ctx context.Context) error { return s.stillCurrent(ctx, cfg, j) })
	if err != nil {
		return err
	}
	j.Status = "publishing"
	j.Stage = "poll"
	j.NextAt = time.Now().Add(15 * time.Second)
	return nil
}
func (s *Service) localImage(raw string) ([]byte, error) {
	ref, ok := assetref.New(s.PublicURL).URL(raw, "/")
	if !ok {
		return nil, errors.New("请先将图片上传到本站媒体库，外部图片不直接同步")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("图片地址无效")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, errors.New("图片参数无效")
	}
	for key, values := range query {
		if (key != "variant" && key != "format") || len(values) != 1 {
			return nil, errors.New("图片参数未知或重复")
		}
	}
	var item media.Item
	var r media.ReadSeekCloser
	if name := query.Get("variant"); name != "" {
		item, r, err = s.Media.OpenVariant(ref.ID, name, query.Get("format"), false)
	} else {
		if query.Get("format") != "" {
			return nil, errors.New("图片格式参数需要指定版本")
		}
		item, r, err = s.Media.Open(ref.ID)
	}
	if err != nil {
		return nil, errors.New("图片不存在或已移入回收站")
	}
	defer r.Close()
	if item.PublicURL != ref.Path || item.Kind != media.KindImage || item.SizeBytes > 10<<20 {
		return nil, errors.New("图片地址不匹配或超过 10 MiB")
	}
	return io.ReadAll(io.LimitReader(r, 10<<20))
}
func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Re-encode to a bounded JPEG accepted by both WeChat image endpoints. This
// applies display orientation before stripping metadata, and avoids decoding an
// attacker-controlled huge pixel canvas. Rotate only the reduced canvas.
func JPEG(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 20_000_000 {
		return nil, errors.New("图片无法解码或像素过大（最多 2000 万像素）")
	}
	orientation := 1
	if format == "jpeg" || format == "png" || format == "webp" {
		orientation, err = media.ReadImageOrientation(data, "image/"+format)
		if err != nil {
			return nil, errors.New("图片方向信息损坏，请重新导出图片后重试")
		}
	}
	src, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("图片无法读取")
	}
	src = media.NormalizeDecodedColors(src, decodedFormat)
	w, h := cfg.Width, cfg.Height
	if max(w, h) > 1600 {
		scale := 1600 / float64(max(w, h))
		w = max(1, int(float64(w)*scale))
		h = max(1, int(float64(h)*scale))
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	imagedraw.ApproxBiLinear.Scale(canvas, canvas.Bounds(), src, src.Bounds(), draw.Over, nil)
	oriented := media.ApplyImageOrientation(canvas, orientation)
	for _, quality := range []int{85, 70, 55, 40} {
		var out bytes.Buffer
		if err = jpeg.Encode(&out, oriented, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		if out.Len() < 1<<20 {
			return out.Bytes(), nil
		}
	}
	return nil, errors.New("图片压缩后仍超过微信 1 MiB 限制")
}
