package httpserver

import (
	"context"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/objectstorage"
)

func (s *Server) objectStorageFileContext(r *http.Request, principal auth.Principal) context.Context {
	_, token, ok := s.currentAdminSession(r)
	return objectstorage.WithFileAuthorization(r.Context(), func(ctx context.Context) error {
		if !ok {
			return auth.ErrSessionNotFound
		}
		// Recheck the session that authorized this request. A second cookie must
		// not silently take over a transfer after the original session is revoked.
		current, err := s.auth.ResolveSession(ctx, token)
		if err != nil {
			return err
		}
		if current.UserID != principal.UserID || current.Username != principal.Username ||
			!current.Can(auth.CapabilityAdminAccess) || !current.Can(auth.CapabilityPluginsManage) {
			return auth.ErrSessionForbidden
		}
		return nil
	})
}

func (s *Server) objectStorageFiles(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if readOnly {
		s.writeAPIError(w, r, 403, "storage.authentication", "请登录后管理云端文件", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{"profile": true, "key": true, "token": true, "version": true, "download": true}); !ok {
		return
	}
	if s.objectStorage == nil {
		s.writeAPIError(w, r, 503, "storage.unavailable", "存储服务不可用", nil)
		return
	}
	q := r.URL.Query()
	version, err := strconv.ParseInt(q.Get("version"), 10, 64)
	if err != nil || version < 1 || len(q.Get("key")) > 1024 || len(q.Get("token")) > 4096 {
		s.writeAPIError(w, r, 400, "storage.query", "文件查询参数无效", nil)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	ctx := s.objectStorageFileContext(r, p)
	if q.Get("download") == "1" {
		reader, info, err := s.objectStorage.RemoteDownload(ctx, q.Get("profile"), q.Get("key"), version)
		if err != nil {
			s.objectStorageError(w, r, err, false)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(q.Get("key"))}))
		if info.Size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		}
		written, err := io.Copy(w, reader)
		if err != nil {
			if written > 0 {
				// Status and file bytes have already been sent. Abort the stream
				// so a partial file cannot be mistaken for a successful download.
				panic(http.ErrAbortHandler)
			}
			w.Header().Del("Content-Disposition")
			w.Header().Del("Content-Length")
			s.objectStorageError(w, r, err, false)
		}
		return
	}
	page, err := s.objectStorage.RemoteList(ctx, q.Get("profile"), q.Get("key"), q.Get("token"), version)
	if err != nil {
		s.objectStorageError(w, r, err, false)
		return
	}
	writeJSON(w, 200, page)
}

func (s *Server) objectStorageFileAction(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if s.objectStorage == nil {
		s.writeAPIError(w, r, 503, "storage.unavailable", "存储服务不可用", nil)
		return
	}
	var body struct {
		Action        string `json:"action"`
		Profile       string `json:"profile"`
		Key           string `json:"key"`
		TargetProfile string `json:"target_profile"`
		Target        string `json:"target"`
		Version       int64  `json:"expected_version"`
		UserID        int64  `json:"expected_user_id"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 8192, "8 KiB"); !ok || !s.pluginIdentity(w, r, p, body.UserID) {
		return
	}
	if err := s.objectStorage.RemoteAction(s.objectStorageFileContext(r, p), body.Profile, body.Key, body.TargetProfile, body.Target, body.Action, body.Version); err != nil {
		s.objectStorageError(w, r, err, false)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) objectStorageFileUpload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilityPluginsManage)
	if !ok {
		return
	}
	if s.objectStorage == nil {
		s.writeAPIError(w, r, 503, "storage.unavailable", "存储服务不可用", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{"profile": true, "key": true, "version": true, "expected_user_id": true}); !ok {
		return
	}
	q := r.URL.Query()
	uid, _ := strconv.ParseInt(q.Get("expected_user_id"), 10, 64)
	if !s.pluginIdentity(w, r, p, uid) {
		return
	}
	version, _ := strconv.ParseInt(q.Get("version"), 10, 64)
	r.Body = http.MaxBytesReader(w, r.Body, objectstorage.RemoteUploadLimit)
	if err := s.objectStorage.RemoteUpload(s.objectStorageFileContext(r, p), q.Get("profile"), q.Get("key"), version, r.Body, "application/octet-stream"); err != nil {
		s.objectStorageError(w, r, err, false)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
