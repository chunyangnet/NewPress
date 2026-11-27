package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/wechat"
)

func (s *Server) SetWeChatService(service *wechat.Service) {
	if service != nil {
		s.wechat = service
	}
}
func (s *Server) adminWeChat(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, map[string]bool{"post": true}); !ok {
		return
	}
	data := s.baseViewFor(w, "微信公众号", "管理公众号连接、文章同步与发表记录。", "/admin/wechat/")
	data.IsAdmin = true
	data.BodyClass = "is-admin is-admin-wechat"
	data.Admin = s.adminView("wechat", p, readOnly)
	s.render(w, 200, "admin-wechat", data)
}
func (s *Server) wechatWrite(w http.ResponseWriter, r *http.Request, contentAccess bool) (auth.Principal, bool) {
	p, ok := s.requireAdminJSONWrite(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return p, false
	}
	if contentAccess && !p.Can(auth.CapabilityContentWrite) {
		s.writeAPIError(w, r, 403, "wechat.content_required", "需要全站内容管理权限", nil)
		return p, false
	}
	if p.UserID <= 0 {
		s.writeAPIError(w, r, 403, "wechat.account_required", "需要持久管理员账号", nil)
		return p, false
	}
	_, ok = s.strictAPIQuery(w, r, nil)
	w.Header().Set("Cache-Control", "no-store")
	return p, ok
}
func (s *Server) wechatError(w http.ResponseWriter, r *http.Request, err error) {
	status := 422
	code := "wechat.operation_failed"
	if errors.Is(err, wechat.ErrConflict) || errors.Is(err, wechat.ErrBusy) {
		status = 409
		code = "wechat.conflict"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		status = 503
	}
	if errors.Is(err, wechat.ErrCheckBusy) {
		status = 503
	}
	s.writeAPIError(w, r, status, code, err.Error(), nil)
}
func (s *Server) adminWeChatRead(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySettingsManage)
	if !ok {
		return
	}
	if p.UserID <= 0 {
		s.writeAPIError(w, r, 403, "wechat.account_required", "请先登录管理员账号", nil)
		return
	}
	q, page, selectedID, ok := s.wechatQuery(w, r)
	if !ok {
		return
	}
	state, err := s.wechat.Store.View(r.Context())
	if err != nil {
		s.writeAPIError(w, r, 503, "wechat.unavailable", "公众号配置暂时不可用", nil)
		return
	}
	jobs := wechatJobList(state, q.Get("job_q"), q.Get("job_status"), page)
	type postItem struct {
		ID      int64  `json:"id"`
		Version int64  `json:"version"`
		Title   string `json:"title"`
		Status  string `json:"status"`
		Cover   string `json:"cover"`
	}
	posts := []postItem{}
	var selected *postItem
	if p.Can(auth.CapabilityContentWrite) {
		list, e := s.repository.ListAdmin(r.Context(), content.AdminQuery{Type: content.TypePost, Search: q.Get("q"), Limit: 50})
		if e != nil {
			s.wechatError(w, r, e)
			return
		}
		for _, post := range list.Items {
			if wechatSyncable(post) {
				posts = append(posts, postItem{post.ID, post.Version, post.Title, post.Status, post.CoverURL})
			}
		}
		if selectedID > 0 {
			post, e := s.repository.GetByID(r.Context(), selectedID)
			if e != nil && !errors.Is(e, content.ErrNotFound) {
				s.wechatError(w, r, e)
				return
			}
			if e == nil && wechatSyncable(post) {
				selected = &postItem{post.ID, post.Version, post.Title, post.Status, post.CoverURL}
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		wechatJobPage
		Config     wechat.Config `json:"config"`
		Posts      []postItem    `json:"posts"`
		Selected   *postItem     `json:"selected_post"`
		CanCollect bool          `json:"can_collect"`
		CanSync    bool          `json:"can_sync"`
		UserID     int64         `json:"user_id"`
	}{jobs, state.Config.Public(), posts, selected, canImport(p) && p.Can(auth.CapabilityMediaWrite), p.Can(auth.CapabilityContentWrite), p.UserID})
}
func (s *Server) adminWeChatConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.wechatWrite(w, r, false); !ok {
		return
	}
	var body struct {
		Config      wechat.Config `json:"config"`
		ClearSecret bool          `json:"clear_secret"`
	}
	raw, ok := s.decodeStrictAPIJSONLimit(w, r, &body, 16<<10, "16 KiB")
	if !ok {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		if token == nil {
			s.writeAPIError(w, r, 400, "wechat.invalid_config", "配置字段不能为 null", nil)
			return
		}
	}
	if body.Config.Version < 1 {
		s.writeAPIError(w, r, 400, "wechat.version_required", "请携带读取到的配置版本", nil)
		return
	}
	cfg, err := s.wechat.SaveConfig(r.Context(), body.Config, body.ClearSecret)
	if err != nil {
		s.wechatError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"config": cfg})
}
func (s *Server) adminWeChatTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.wechatWrite(w, r, false); !ok {
		return
	}
	var body struct{}
	if _, ok := s.decodeStrictAPIJSON(w, r, &body); !ok {
		return
	}
	cfg, err := wechat.ReadConfig(r.Context(), s.wechat.Store)
	if err != nil {
		s.wechatError(w, r, err)
		return
	}
	if cfg.AppID == "" || cfg.Secret == "" {
		s.wechatError(w, r, wechat.ErrDisabled)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err = s.wechat.Client.Token(ctx, cfg); err != nil {
		s.wechatError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"message": "已成功获取接口凭证。草稿与发表权限将以实际同步结果为准。"})
}
func (s *Server) adminWeChatSyncCheck(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.wechatWrite(w, r, true); !ok {
		return
	}
	var body struct {
		PostID        int64 `json:"post_id"`
		Version       int64 `json:"version"`
		ConfigVersion int64 `json:"config_version"`
	}
	if _, ok := s.decodeStrictAPIJSON(w, r, &body); !ok {
		return
	}
	if body.PostID < 1 || body.Version < 1 || body.ConfigVersion < 1 {
		s.writeAPIError(w, r, 400, "wechat.version_required", "请携带当前文章和配置版本再检查", nil)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(45 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	check, err := s.wechat.CheckSync(ctx, body.PostID, body.Version, body.ConfigVersion)
	if err != nil {
		s.wechatError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"check": check})
}
func (s *Server) adminWeChatSync(w http.ResponseWriter, r *http.Request) {
	p, ok := s.wechatWrite(w, r, true)
	if !ok {
		return
	}
	var body struct {
		PostID        int64 `json:"post_id"`
		Version       int64 `json:"version"`
		ConfigVersion int64 `json:"config_version"`
	}
	if _, ok = s.decodeStrictAPIJSON(w, r, &body); !ok {
		return
	}
	if body.ConfigVersion < 1 {
		s.writeAPIError(w, r, 400, "wechat.version_required", "请先刷新公众号配置再同步", nil)
		return
	}
	job, err := s.wechat.QueueWithConfig(r.Context(), body.PostID, body.Version, body.ConfigVersion, p.Username)
	if err != nil {
		s.wechatError(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]any{"job": job})
}
func (s *Server) adminWeChatJob(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.wechatWrite(w, r, true); !ok {
		return
	}
	var body struct {
		Action    string    `json:"action"`
		UpdatedAt time.Time `json:"updated_at"`
		MediaID   string    `json:"media_id"`
		PublishID string    `json:"publish_id"`
	}
	if _, ok := s.decodeStrictAPIJSON(w, r, &body); !ok {
		return
	}
	var err error
	if body.Action == "resolve" {
		err = s.wechat.Resolve(r.Context(), r.PathValue("id"), body.UpdatedAt, body.MediaID, body.PublishID)
	} else {
		err = s.wechat.Action(r.Context(), r.PathValue("id"), body.Action, body.UpdatedAt)
	}
	if err != nil {
		s.wechatError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func collectedDigest(items []wechat.Collected) string {
	b, _ := json.Marshal(items)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (s *Server) wechatCollectionEnabled(ctx context.Context) error {
	cfg, err := wechat.ReadConfig(ctx, s.wechat.Store)
	if err != nil {
		return err
	}
	if !cfg.CollectEnabled {
		return errors.New("文章采集模块已关闭；已保存图片保留，未导入文章可在重新开启后核对并继续")
	}
	return nil
}

func (s *Server) adminWeChatCollect(w http.ResponseWriter, r *http.Request) {
	p, ok := s.wechatWrite(w, r, true)
	if !ok {
		return
	}
	if !canImport(p) || !p.Can(auth.CapabilityMediaWrite) {
		s.writeAPIError(w, r, 403, "wechat.collect_forbidden", "采集需要站点设置、内容、分类和媒体管理权限", nil)
		return
	}
	err := s.wechatCollectionEnabled(r.Context())
	if err != nil {
		s.wechatError(w, r, err)
		return
	}
	if !s.importMu.TryLock() {
		s.writeAPIError(w, r, 429, "wechat.collect_busy", "另一个导入正在处理，请稍后重试", nil)
		return
	}
	defer s.importMu.Unlock()
	var body struct {
		URLs     []string           `json:"urls,omitempty"`
		Articles []wechat.Collected `json:"articles,omitempty"`
		Receipts map[string]string  `json:"receipts,omitempty"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &body, 16<<20, "16 MiB"); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	// Collection may legitimately exceed the normal 30-second response deadline.
	// Bound this endpoint independently without relaxing unrelated requests.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(55 * time.Second))
	r = r.WithContext(ctx)
	apply := strings.HasSuffix(r.URL.Path, "/apply")
	failures := []wechat.CollectFailure{}
	itemClaims := map[string]importClaims{}
	if apply {
		if len(body.URLs) > 0 || len(body.Articles) == 0 || len(body.Articles) > wechat.MaxCollect || len(body.Receipts) > wechat.MaxCollect {
			s.wechatError(w, r, errors.New("采集输入无效"))
			return
		}
		ok = len(body.Receipts) == len(body.Articles)
		for _, article := range body.Articles {
			claim, valid := s.verifyImport(body.Receipts[article.Key])
			_, duplicate := itemClaims[article.Key]
			if !valid || duplicate || claim.UserID != p.UserID || claim.Digest != "wechat:item:"+collectedDigest([]wechat.Collected{article}) {
				ok = false
				break
			}
			itemClaims[article.Key] = claim
		}
		if !ok {
			s.writeAPIError(w, r, 409, "wechat.preview_required", "预览已过期或内容已变化，请重新采集预览", nil)
			return
		}
	} else {
		if len(body.URLs) < 1 || len(body.URLs) > wechat.MaxCollect || len(body.Articles) > 0 || len(body.Receipts) != 0 {
			s.wechatError(w, r, errors.New("每批请输入 1–10 个文章链接"))
			return
		}
		body.Articles, failures, err = s.wechat.Client.CollectArticles(ctx, body.URLs)
		if err != nil {
			s.wechatError(w, r, err)
			return
		}
	}
	if err = s.wechatCollectionEnabled(ctx); err != nil {
		s.wechatError(w, r, err)
		return
	}
	singlePlans := map[string]content.ImportPlan{}
	if !apply {
		// Validate each fetched article before constructing the combined plan.
		// A content error belongs to that link; a storage error still fails the
		// request. Reuse these plans for receipts instead of reading them twice.
		accepted := make([]wechat.Collected, 0, len(body.Articles))
		for _, article := range body.Articles {
			one := wechat.Batch([]wechat.Collected{article}, nil, p.UserID, p.Label(), p.Username)
			single, e := s.repository.PreviewImport(ctx, one)
			if e != nil {
				fields := content.FieldErrors(e)
				if len(fields) == 0 {
					s.importError(w, r, e)
					return
				}
				keys := make([]string, 0, len(fields))
				for key := range fields {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				messages := make([]string, 0, len(keys))
				for _, key := range keys {
					messages = append(messages, fields[key])
				}
				failures = append(failures, wechat.CollectFailure{URL: article.URL, Message: "文章未通过导入校验：" + strings.Join(messages, "；")})
				continue
			}
			accepted = append(accepted, article)
			singlePlans[article.Key] = single
		}
		body.Articles = accepted
		if err = s.wechatCollectionEnabled(ctx); err != nil {
			s.wechatError(w, r, err)
			return
		}
	}
	sort.SliceStable(failures, func(i, j int) bool { return failures[i].URL < failures[j].URL })
	if len(body.Articles) == 0 {
		writeJSON(w, 200, map[string]any{"articles": []wechat.Collected{}, "failures": failures})
		return
	}
	batch := wechat.Batch(body.Articles, nil, p.UserID, p.Label(), p.Username)
	plan, err := s.repository.PreviewImport(ctx, batch)
	if err != nil {
		s.importError(w, r, err)
		return
	}
	if apply {
		// Each selected article is bound to the preview the administrator saw.
		// Unselected conflicts and their remote images do not enter this import.
		for _, article := range body.Articles {
			one := wechat.Batch([]wechat.Collected{article}, nil, p.UserID, p.Label(), p.Username)
			current, e := s.repository.PreviewImport(ctx, one)
			if e == nil {
				e = content.CheckImportPlan(current, itemClaims[article.Key].Revision)
			}
			if e != nil {
				s.importError(w, r, e)
				return
			}
		}
		if err = content.CheckImportPlan(plan, plan.Revision); err != nil {
			s.importError(w, r, err)
			return
		}
		mapping := map[string]string{}
		total := 0
		create := map[string]bool{}
		for _, row := range plan.Items {
			create[row.Key] = row.Action == "create"
		}
		// Reject a known oversized selection before any image request or media
		// write. Replayed/imported articles do not consume this batch's budget.
		images := map[string]bool{}
		for _, article := range body.Articles {
			if create[article.Key] {
				for _, address := range article.Images {
					images[address] = true
				}
			}
		}
		if len(images) > wechat.MaxCollectImages {
			s.writeAPIError(w, r, 422, "wechat.image_limit", fmt.Sprintf("所选文章含 %d 张不同图片，单批最多 %d 张；请取消部分文章后分批导入", len(images), wechat.MaxCollectImages), map[string]any{"images": len(images), "limit": wechat.MaxCollectImages})
			return
		}
		for _, a := range body.Articles {
			if !create[a.Key] {
				continue
			}
			for _, address := range a.Images {
				if mapping[address] != "" {
					continue
				}
				if err = s.wechatCollectionEnabled(ctx); err != nil {
					s.wechatError(w, r, err)
					return
				}
				data, e := s.wechat.Client.FetchImage(ctx, address)
				if e != nil {
					s.wechatError(w, r, fmt.Errorf("《%s》的图片导入失败：%w。已下载图片保留在媒体库，重试时会复用。", a.Title, e))
					return
				}
				if err = s.wechatCollectionEnabled(ctx); err != nil {
					s.wechatError(w, r, err)
					return
				}
				total += len(data)
				if total > 30<<20 {
					s.wechatError(w, r, errors.New("本批图片总计超过 30 MiB，请拆分链接"))
					return
				}
				item, e := s.media.Upload(ctx, bytes.NewReader(data), media.UploadInput{UploadSource: "wechat-import", Filename: "wechat-" + fmt.Sprintf("%x", sha256.Sum256([]byte(address)))[:16] + ".jpg", Title: trimWeChatTitle(a.Title), UploadedBy: p.Username}, 5<<20)
				if e != nil && !errors.Is(e, media.ErrDuplicate) {
					s.wechatError(w, r, errors.New("图片保存失败，请检查媒体库空间或回收站"))
					return
				}
				mapping[address] = item.PublicURL
			}
		}
		batch = wechat.Batch(body.Articles, mapping, p.UserID, p.Label(), p.Username)
		plan, err = s.repository.PreviewImport(ctx, batch)
		if err == nil {
			if err = s.wechatCollectionEnabled(ctx); err != nil {
				s.wechatError(w, r, err)
				return
			}
		}
		if err == nil {
			plan, err = s.repository.ApplyImport(ctx, batch, plan.Revision)
		}
		if err != nil {
			s.importError(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"plan": plan, "applied": true, "failures": failures})
		return
	}
	expires := time.Now().Add(15 * time.Minute)
	receipts := map[string]string{}
	for _, article := range body.Articles {
		single := singlePlans[article.Key]
		if single.Conflicts == 0 {
			receipts[article.Key] = s.signImport(importClaims{UserID: p.UserID, Digest: "wechat:item:" + collectedDigest([]wechat.Collected{article}), Revision: single.Revision, Expires: expires.Unix()})
		}
	}
	if err = s.wechatCollectionEnabled(ctx); err != nil {
		s.wechatError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"articles": body.Articles, "plan": plan, "receipts": receipts, "expires": expires.UTC().Format(time.RFC3339), "failures": failures, "applied": false})
}
func trimWeChatTitle(s string) string {
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160])
	}
	return s
}
