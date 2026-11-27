package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/media"
)

func (s *Server) adminAIImages(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityContentWrite, auth.CapabilityContentWriteOwn)
	if !ok {
		return
	}
	if !principal.Can(auth.CapabilityMediaWrite) || !principal.Can(auth.CapabilityMediaRead) {
		s.writeAPIError(w, r, http.StatusForbidden, "ai.media_forbidden", "生图需要媒体上传与读取权限", nil)
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var input struct {
		aieditor.Connection
		aieditor.ImageGeneration
		Title string `json:"title,omitempty"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &input, 64<<10, "64 KiB"); !ok {
		return
	}
	connection, err := s.aiSettings.ResolveImage(input.Connection)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	generation, err := input.ImageGeneration.Validate(connection.Protocol)
	if err != nil {
		s.aiEditorError(w, r, err)
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	if utf8.RuneCountInString(input.Title) > 160 || strings.ContainsFunc(input.Title, unicode.IsControl) {
		s.writeAPIError(w, r, http.StatusBadRequest, "ai.invalid_image_title", "图片名称最多 160 字，不含换行或控制字符", nil)
		return
	}
	if !s.aiState.acquire(principal.UserID) {
		w.Header().Set("Retry-After", "10")
		s.writeAPIError(w, r, http.StatusTooManyRequests, "ai.busy", "已有生成任务进行中，请先停止它或稍后重试", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), aieditor.ImageGenerationTimeout)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(aieditor.ImageGenerationTimeout + 10*time.Second))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	writeEvent := func(event aiStreamEvent) bool {
		encoded, err := json.Marshal(event.Data)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, encoded); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !writeEvent(aiStreamEvent{"ready", map[string]string{"message": "正在生成画面", "model": connection.Model}}) {
		s.aiState.release(principal.UserID)
		return
	}
	events := make(chan aiStreamEvent, 4)
	send := func(event aiStreamEvent) bool {
		select {
		case events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	go func() {
		// Keep the concurrency slot until cancellation has actually reached the worker.
		defer s.aiState.release(principal.UserID)
		observed := s.observeAI(input.ProviderID, connection, "image", principal.UserID)
		defer observed.finish(ctx)
		images, err := s.aiImages.GenerateObserved(ctx, connection, generation, func(usage aieditor.Usage, count int) {
			observed.first()
			observed.record.Usage = usage
			observed.record.Images = count
		})
		if err != nil {
			observed.fail(err)
			message := "图片生成中断，本次不会自动重试；如已扣费，请在服务商控制台核对"
			var known *aieditor.Error
			if errors.As(err, &known) {
				message = known.Message
			}
			if errors.Is(err, context.DeadlineExceeded) {
				message = "生图超过 8 分钟，已停止等待；请在服务商控制台核对任务状态"
			}
			send(aiStreamEvent{"error", map[string]string{"message": message}})
			return
		}
		if !send(aiStreamEvent{"progress", map[string]string{"message": "画面已生成，正在保存到媒体库"}}) {
			return
		}
		items := []adminMediaAPIItem{}
		for i, image := range images {
			if ctx.Err() != nil {
				return
			}
			title := input.Title
			if title == "" {
				title = "AI 配图"
			}
			item, err := s.media.Upload(ctx, bytes.NewReader(image.Data), media.UploadInput{
				UploadSource: "ai-image",
				Filename:     fmt.Sprintf("ai-image-%d-%d.%s", time.Now().UnixMilli(), i+1, image.Extension),
				Title:        title, UploadedBy: principal.Username,
			}, min(s.effectiveMaxUploadBytes(), aieditor.MaxImageBytes))
			if err != nil && !errors.Is(err, media.ErrDuplicate) {
				observed.record.ErrorCode = "media_save_failed"
				message := "图片已生成，但保存到媒体库失败；已保存的图片仍可在媒体库查看"
				if errors.Is(err, media.ErrTooLarge) {
					message = "图片超过站点上传上限，请降低分辨率后重试"
				}
				if errors.Is(err, media.ErrDuplicateTrash) {
					message = "相同图片已在媒体回收站中，请先恢复该图片"
				}
				send(aiStreamEvent{"error", map[string]string{"message": message}})
				return
			}
			view := mediaAPIItem(item)
			items = append(items, view)
			if !send(aiStreamEvent{"image", view}) {
				return
			}
		}
		observed.record.Status, observed.record.ErrorCode = "success", ""
		send(aiStreamEvent{"done", map[string]any{"items": items, "model": connection.Model}})
	}()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			if r.Context().Err() == nil {
				writeEvent(aiStreamEvent{"error", map[string]string{"message": "生图等待超时，请在服务商控制台核对任务；已保存的图片可在媒体库查看"}})
			}
			return
		case <-heartbeat.C:
			if !writeEvent(aiStreamEvent{"ping", map[string]bool{"waiting": true}}) {
				return
			}
		case event := <-events:
			if !writeEvent(event) || event.Name == "done" || event.Name == "error" {
				return
			}
		}
	}
}
