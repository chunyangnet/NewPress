package httpserver

import (
	"errors"
	"mime"
	"net/http"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/media"
	"strconv"
	"time"
)

func (s *Server) avatarURL(id int64) string {
	if id <= 0 {
		return ""
	}
	item, err := s.media.Get(id)
	if err != nil || item.Kind != media.KindImage || item.Status() != media.StatusActive {
		return ""
	}
	return item.PublicURL
}

func (s *Server) profileDTO(u auth.User) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "bio": u.Bio, "avatar_url": s.avatarURL(u.AvatarMediaID), "locale": u.Locale, "version": u.Version, "created_at": u.CreatedAt}
}

func (s *Server) profileError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, auth.ErrUserVersionConflict) {
		s.writeAPIError(w, r, 409, "profile.conflict", "资料已在其他地方修改，请重新载入后重试", nil)
	} else if fields := auth.UserFieldErrors(err); len(fields) > 0 {
		s.writeAPIError(w, r, 422, "profile.invalid", "请检查昵称、简介和语言设置", fields)
	} else if errors.Is(err, media.ErrInvalidInput) || errors.Is(err, media.ErrDuplicateTrash) {
		s.writeAPIError(w, r, 409, "profile.avatar_unavailable", "头像当前不可用，请重新上传或移除头像", nil)
	} else {
		s.engagementError(w, r, err)
	}
}

func (s *Server) socialUploadAvatar(w http.ResponseWriter, r *http.Request) {
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	allowed, retry := s.accessRate.allow("avatar:"+strconv.FormatInt(p.UserID, 10), 10, time.Now())
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		s.writeAPIError(w, r, 429, "profile.rate_limited", "头像更换过于频繁，请稍后再试", nil)
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/form-data" {
		s.writeAPIError(w, r, 415, "profile.multipart_required", "头像上传需要 multipart/form-data", nil)
		return
	}
	maximum := min(media.MaxAvatarBytes, s.effectiveMaxUploadBytes())
	r.Body = http.MaxBytesReader(w, r.Body, maximum+(16<<10))
	if err := r.ParseMultipartForm(256 << 10); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeAPIError(w, r, 413, "profile.avatar_too_large", "头像超过上传上限（最多 2 MiB）", nil)
		} else {
			s.writeAPIError(w, r, 400, "profile.invalid_form", "头像表单格式无效", nil)
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.Value) != 1 || len(r.MultipartForm.Value["version"]) != 1 || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		s.writeAPIError(w, r, 400, "profile.invalid_form", "头像表单仅接受一个 file 和一个 version", nil)
		return
	}
	version, err := strconv.ParseInt(r.MultipartForm.Value["version"][0], 10, 64)
	if err != nil || version <= 0 {
		s.writeAPIError(w, r, 422, "profile.version_required", "请重新载入资料后上传", nil)
		return
	}
	u, err := s.auth.GetUser(r.Context(), p.UserID)
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	if version != u.Version {
		s.profileError(w, r, auth.ErrUserVersionConflict)
		return
	}
	header := r.MultipartForm.File["file"][0]
	if header.Size > maximum {
		s.writeAPIError(w, r, 413, "profile.avatar_too_large", "头像超过上传上限（最多 2 MiB）", nil)
		return
	}
	file, err := header.Open()
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	defer file.Close()
	prepared, err := media.PrepareAvatar(file)
	if errors.Is(err, media.ErrImageBusy) {
		w.Header().Set("Retry-After", "1")
		s.writeAPIError(w, r, http.StatusTooManyRequests, "profile.avatar_busy", "正在处理其他图片，请稍后重试", nil)
		return
	}
	if err != nil {
		s.writeAPIError(w, r, 422, "profile.avatar_invalid", "请选择 JPEG 或 PNG，宽高均不超过 2048 像素", nil)
		return
	}
	item, uploadErr := s.media.Upload(r.Context(), prepared, media.UploadInput{UploadSource: "avatar", Filename: "avatar.jpg", Title: "账号头像", UploadedBy: "profile:" + strconv.FormatInt(p.UserID, 10)}, media.MaxAvatarBytes)
	if uploadErr != nil && !errors.Is(uploadErr, media.ErrDuplicate) {
		s.profileError(w, r, uploadErr)
		return
	}
	release, err := s.mediaUsage.HoldAvatar(item.ID)
	if err != nil {
		s.profileError(w, r, err)
		return
	}
	updated, err := s.auth.UpdateProfile(r.Context(), p, version, auth.ProfileInput{DisplayName: u.DisplayName, Locale: u.Locale, Bio: u.Bio, AvatarMediaID: item.ID})
	release()
	if err != nil {
		if uploadErr == nil {
			s.cleanupProfileAvatar(r, item.ID, p.UserID)
		}
		s.profileError(w, r, err)
		return
	}
	if u.AvatarMediaID != item.ID {
		s.cleanupProfileAvatar(r, u.AvatarMediaID, p.UserID)
	}
	writeJSON(w, 200, s.profileDTO(updated))
}

// Only unused objects created by this account's avatar endpoint are eligible.
// Shared/deduplicated images and references from other features remain guarded.
func (s *Server) cleanupProfileAvatar(r *http.Request, id, user int64) {
	if id <= 0 {
		return
	}
	item, err := s.media.Get(id)
	if err != nil || item.UploadedBy != "profile:"+strconv.FormatInt(user, 10) {
		return
	}
	if item.Status() == media.StatusActive {
		if err := s.mediaUsage.Trash(r.Context(), id, item.Version); err != nil {
			return
		}
		item.Version++
	}
	_ = s.mediaUsage.Delete(r.Context(), id, item.Version)
}
