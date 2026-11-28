package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contentimport"
)

type importClaims struct {
	UserID   int64  `json:"u"`
	Digest   string `json:"d"`
	Revision string `json:"r"`
	Expires  int64  `json:"e"`
}
type importAuthor struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type importResponse struct {
	Plan          content.ImportPlan     `json:"plan"`
	Document      contentimport.Document `json:"document"`
	Token         string                 `json:"token,omitempty"`
	Expires       string                 `json:"expires,omitempty"`
	Targets       []importAuthor         `json:"targets"`
	DefaultAuthor int64                  `json:"default_author"`
	Applied       bool                   `json:"applied"`
}

func canImport(p auth.Principal) bool {
	return p.Can(auth.CapabilitySettingsManage) && p.Can(auth.CapabilityContentWrite) && p.Can(auth.CapabilityTaxonomyManage) && p.UserID > 0
}
func (s *Server) adminContentImport(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if !readOnly && !canImport(p) {
		s.renderError(w, 403, "当前账号没有内容迁入权限")
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	data := s.baseViewFor(w, "内容迁入", "把原来的文章，带到新的写作空间。", "/admin/import/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-import"
	data.Admin = s.adminView("import", p, readOnly)
	s.render(w, 200, "admin-import", data)
}
func importRequestDigest(input contentimport.Request) string {
	b, _ := json.Marshal(input)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *Server) signImport(claims importClaims) string {
	b, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(b)
	mac := hmac.New(sha256.New, s.previewSigningKey[:])
	mac.Write([]byte("content-import/v1\x00" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) verifyImport(value string) (importClaims, bool) {
	var c importClaims
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(value) > 1500 {
		return c, false
	}
	signature, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return c, false
	}
	mac := hmac.New(sha256.New, s.previewSigningKey[:])
	mac.Write([]byte("content-import/v1\x00" + parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return c, false
	}
	payload, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || json.Unmarshal(payload, &c) != nil || c.Expires <= time.Now().Unix() || c.Expires > time.Now().Add(16*time.Minute).Unix() {
		return c, false
	}
	return c, true
}
func (s *Server) importError(w http.ResponseWriter, r *http.Request, err error) {
	if fields := content.FieldErrors(err); len(fields) > 0 {
		s.writeAPIError(w, r, 422, "import.validation_failed", "导入内容未通过校验", map[string]any{"fields": fields})
		return
	}
	if errors.Is(err, content.ErrImportConflict) || errors.Is(err, content.ErrSlugConflict) || errors.Is(err, content.ErrRedirectConflict) {
		s.writeAPIError(w, r, 409, "import.plan_changed", "目标内容或路径发生变化，请重新预览后确认", nil)
		return
	}
	slog.Error("content import failed", "error", err)
	s.writeAPIError(w, r, 503, "import.unavailable", "导入暂时不可用，保留文件后重新预览重试", nil)
}
func (s *Server) adminContentImportAPI(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if !canImport(p) {
		s.writeAPIError(w, r, 403, "import.capability_required", "需要站点设置、全站内容和分类管理权限", nil)
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	if !s.importMu.TryLock() {
		w.Header().Set("Retry-After", "3")
		s.writeAPIError(w, r, 429, "import.busy", "另一个导入正在处理，请稍后重试", nil)
		return
	}
	defer s.importMu.Unlock()
	var body struct {
		Input contentimport.Request `json:"input"`
		Token string                `json:"token,omitempty"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 32<<20, "32 MiB（正文源总计 8 MiB）"); !ok {
		return
	}
	apply := r.URL.Path == "/api/v1/admin/imports/apply"
	var claims importClaims
	if apply {
		claims, ok = s.verifyImport(body.Token)
		if !ok || claims.UserID != p.UserID || claims.Digest != importRequestDigest(body.Input) {
			s.writeAPIError(w, r, 409, "import.preview_required", "预览已过期、输入已变化或凭证无效，请重新预览", nil)
			return
		}
	} else if body.Token != "" {
		s.writeAPIError(w, r, 400, "import.invalid_request", "预览请求不接受提交凭证", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	doc, err := contentimport.Parse(body.Input, time.Now().UTC())
	if err != nil {
		s.importError(w, r, err)
		return
	}
	known := map[string]bool{}
	for _, name := range doc.Authors {
		known[name] = true
	}
	for name := range body.Input.Authors {
		if !known[name] {
			s.writeAPIError(w, r, 422, "import.unknown_author", "作者映射包含来源中不存在的作者", nil)
			return
		}
	}
	users := map[int64]auth.User{}
	for i := range doc.Batch.Items {
		id := body.Input.Authors[doc.Items[i].Author]
		if id == 0 {
			id = p.UserID
		}
		u, exists := users[id]
		if !exists {
			u, err = s.auth.GetUser(ctx, id)
			if errors.Is(err, auth.ErrUserNotFound) || (err == nil && u.Status != auth.UserStatusActive) {
				s.writeAPIError(w, r, 422, "import.author_unavailable", "映射目标须为存在且活动的本站账号", nil)
				return
			}
			if err != nil {
				s.importError(w, r, err)
				return
			}
			users[id] = u
		}
		doc.Batch.Items[i].Input.AuthorID = u.ID
		doc.Batch.Items[i].Input.AuthorName = u.DisplayName
		doc.Batch.Items[i].Input.Actor = p.Username
	}
	initial, err := s.repository.PreviewImport(ctx, doc.Batch)
	if err != nil {
		s.importError(w, r, err)
		return
	}
	contentimport.AlignTargets(&doc, initial, time.Now().UTC())
	response := importResponse{Document: doc, DefaultAuthor: p.UserID, Targets: []importAuthor{}}
	list, err := s.auth.ListUsers(ctx, auth.UserQuery{Status: auth.UserStatusActive, Limit: 100})
	if err != nil {
		s.importError(w, r, err)
		return
	}
	for _, u := range list.Items {
		response.Targets = append(response.Targets, importAuthor{u.ID, u.DisplayName})
	}
	if apply {
		// Parsing and repository reads can outlive the request's initial
		// authorization. Recheck the session immediately before the write.
		r, ok = s.freshSessionRequest(w, r)
		if !ok {
			return
		}
		current, authorized := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
		if !authorized {
			return
		}
		if current.UserID != p.UserID || !canImport(current) {
			s.writeAPIError(w, r, 403, "import.capability_required", "需要站点设置、全站内容和分类管理权限", nil)
			return
		}
		response.Plan, err = s.repository.ApplyImport(ctx, doc.Batch, claims.Revision)
		if err != nil {
			s.importError(w, r, err)
			return
		}
		response.Applied = true
	} else {
		response.Plan, err = s.repository.PreviewImport(ctx, doc.Batch)
		if err != nil {
			s.importError(w, r, err)
			return
		}
		if response.Plan.Conflicts == 0 {
			expires := time.Now().Add(15 * time.Minute)
			response.Expires = expires.UTC().Format(time.RFC3339)
			response.Token = s.signImport(importClaims{UserID: p.UserID, Digest: importRequestDigest(body.Input), Revision: response.Plan.Revision, Expires: expires.Unix()})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, response)
}
