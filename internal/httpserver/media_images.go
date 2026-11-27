package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/media"
)

type mediaImageVariantView struct {
	Name          string `json:"name"`
	Label         string `json:"label"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	URL           string `json:"url"`
	NativeURL     string `json:"native_url"`
	WebPURL       string `json:"webp_url"`
	MIMEType      string `json:"mime_type"`
	SizeBytes     int64  `json:"size_bytes"`
	WebPBytes     int64  `json:"webp_bytes"`
	NativeBytes   int64  `json:"native_bytes"`
	SizeLabel     string `json:"-"`
	WebPSizeLabel string `json:"-"`
}

type mediaImageView struct {
	Processed   bool                    `json:"processed"`
	FocalX      int                     `json:"focal_x"`
	FocalY      int                     `json:"focal_y"`
	Width       int                     `json:"width"`
	Height      int                     `json:"height"`
	GeneratedAt time.Time               `json:"generated_at,omitzero"`
	Variants    []mediaImageVariantView `json:"variants"`
	CanProcess  bool                    `json:"can_process"`
	Unavailable string                  `json:"unavailable,omitempty"`
}

func imageView(item media.Item) mediaImageView {
	view := mediaImageView{Processed: item.Image.Recipe > 0, FocalX: 50, FocalY: 50, Width: item.Width, Height: item.Height, CanProcess: item.CanProcessImage() == nil, Variants: []mediaImageVariantView{}}
	if err := item.CanProcessImage(); err != nil {
		_, _, view.Unavailable = imageProcessingError(err)
	}
	if !view.Processed {
		return view
	}
	view.FocalX, view.FocalY, view.Width, view.Height, view.GeneratedAt = item.Image.FocalX, item.Image.FocalY, item.Image.Width, item.Image.Height, item.Image.GeneratedAt
	labels := map[string]string{"square": "方形缩略图", "card": "列表封面", "card-large": "大幅封面", "small": "正文 · 小", "medium": "正文 · 中", "large": "正文 · 大"}
	for _, variant := range item.Image.Variants {
		object, _ := variant.Object("")
		view.Variants = append(view.Variants, mediaImageVariantView{Name: variant.Name, Label: labels[variant.Name], Width: variant.Width, Height: variant.Height, URL: item.VariantURL(variant.Name, ""), NativeURL: item.VariantURL(variant.Name, "native"), WebPURL: item.VariantURL(variant.Name, "webp"), MIMEType: object.MIMEType, SizeBytes: object.SizeBytes, WebPBytes: variant.WebP.SizeBytes, NativeBytes: variant.Native.SizeBytes, SizeLabel: formatBytes(object.SizeBytes), WebPSizeLabel: formatBytes(variant.WebP.SizeBytes)})
	}
	return view
}

func imageProcessingError(err error) (int, string, string) {
	switch {
	case errors.Is(err, media.ErrNotFound):
		return 404, "media.not_found", "媒体没有找到"
	case errors.Is(err, media.ErrVersionConflict):
		return 409, "media.version_conflict", "媒体已更新。已保留本次焦点，请核对后重新生成。"
	case errors.Is(err, media.ErrImageBusy):
		return 503, "media.image_busy", "另一张图片正在处理，请稍后重试。"
	case errors.Is(err, media.ErrImageBudget):
		return 422, "media.image_budget", "处理支持不超过 32 MiB、1600 万像素、单边 8192 px 的图片。请先缩小图片。"
	case errors.Is(err, media.ErrImageUnsupported):
		return 422, "media.image_unsupported", "支持静态 JPEG、PNG 和 WebP；GIF、APNG 和动画 WebP 保留原样。"
	case errors.Is(err, media.ErrTrashed):
		return 409, "media.trashed", "请先恢复回收站中的图片。"
	case errors.Is(err, media.ErrInvalidInput):
		return 422, "media.invalid_image", "焦点必须是 0–100 的整数，图片必须可以完整解码。"
	case errors.Is(err, media.ErrObjectCorrupt), errors.Is(err, media.ErrObjectMissing):
		return 409, "media.image_corrupt", "原图或派生文件缺失、校验不一致，请从备份恢复。"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 503, "media.image_interrupted", "处理已中止，已有图片已保留，请重试。"
	default:
		return 500, "media.image_failed", "图片生成失败，已有图片已保留，请稍后重试。"
	}
}

func (s *Server) adminMediaProcessAPI(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityMediaWrite); !ok {
		return
	}
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	current, ok := s.loadMediaAPIItem(w, r)
	if !ok || !s.requireMediaAPIPrecondition(w, r, current) {
		return
	}
	var body struct {
		FocalX *int `json:"focal_x"`
		FocalY *int `json:"focal_y"`
	}
	if _, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 1024, "1 KiB"); !ok {
		return
	}
	if body.FocalX == nil || body.FocalY == nil {
		s.writeAPIError(w, r, 400, "media.focal_required", "需要 focal_x 和 focal_y 两个整数", nil)
		return
	}
	updated, err := s.media.ProcessImage(r.Context(), current.ID, current.Version, media.ImageInput{FocalX: *body.FocalX, FocalY: *body.FocalY})
	if errors.Is(err, media.ErrVersionConflict) {
		s.handleMediaAPIError(w, r, current.ID, err)
		return
	}
	if err != nil {
		status, code, message := imageProcessingError(err)
		if status == 500 {
			slog.Error("process media image", "id", current.ID, "error", err)
		}
		if errors.Is(err, media.ErrImageBusy) {
			w.Header().Set("Retry-After", "5")
		}
		s.writeAPIError(w, r, status, code, message, nil)
		return
	}
	writeMediaAPISuccess(w, updated)
}

func (s *Server) adminMediaProcess(w http.ResponseWriter, r *http.Request) {
	principal, form, ok := s.requireMediaWrite(w, r)
	if !ok {
		return
	}
	allowed := map[string]bool{"csrf_token": true, "version": true, "focal_x": true, "focal_y": true}
	_, valid := strictQueryShape(form, allowed)
	if !valid || len(form) != len(allowed) || r.URL.RawQuery != "" {
		s.renderError(w, 400, "图片处理字段未知、重复或缺失")
		return
	}
	id, err := positivePathID(r.PathValue("id"))
	if err != nil {
		s.renderError(w, 404, "媒体没有找到")
		return
	}
	version, err := strconv.ParseInt(form.Get("version"), 10, 64)
	if err != nil || version <= 0 {
		s.renderError(w, 400, "媒体版本不正确")
		return
	}
	item, err := s.media.Get(id)
	if err != nil {
		s.handleMediaLoadError(w, id, err)
		return
	}
	x, xerr := strconv.Atoi(form.Get("focal_x"))
	y, yerr := strconv.Atoi(form.Get("focal_y"))
	if xerr != nil || yerr != nil {
		err = media.ErrInvalidInput
	} else {
		_, err = s.media.ProcessImage(r.Context(), id, version, media.ImageInput{FocalX: x, FocalY: y})
	}
	if err == nil {
		http.Redirect(w, r, fmt.Sprintf("/admin/media/%d/edit/?notice=processed#media-image-processing", id), http.StatusSeeOther)
		return
	}
	status, _, message := imageProcessingError(err)
	if status == 500 {
		slog.Error("process media image form", "id", id, "error", err)
	}
	if latest, loadErr := s.media.Get(id); loadErr == nil {
		item = latest
	}
	s.renderMediaLibrary(w, r, status, principal, false, media.Query{Status: item.Status(), Limit: mediaPageSize}, 1, &item, media.UpdateInput{Title: item.Title, AltText: item.AltText, Caption: item.Caption}, media.UploadInput{}, nil, adminNotice{Message: message, Tone: "error"})
}

func mediaImageQuery(r *http.Request) (name, format string, err error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", err
	}
	// Preserve historical ignored queries on original URLs. Derivative selectors
	// are deliberately strict and never permit arbitrary dimensions or formats.
	if !q.Has("variant") && !q.Has("format") {
		return "", "", nil
	}
	if _, ok := strictQueryShape(q, map[string]bool{"variant": true, "format": true}); !ok {
		return "", "", media.ErrInvalidInput
	}
	name, format = q.Get("variant"), q.Get("format")
	if name == "" || (format != "" && format != "native" && format != "webp") {
		return "", "", media.ErrInvalidInput
	}
	return name, format, nil
}
