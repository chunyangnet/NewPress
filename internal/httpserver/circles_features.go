package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/presentation"
)

func circleAttachmentFacts(c community.Circle, ids []string) []community.Attachment {
	files := []community.Attachment{}
	for _, id := range ids {
		if f, ok := community.AttachmentByID(c, id); ok {
			files = append(files, f)
		}
	}
	return files
}
func circleAttachments(c community.Circle, ids []string) []presentation.CircleAttachment {
	items := []presentation.CircleAttachment{}
	for _, f := range circleAttachmentFacts(c, ids) {
		items = append(items, presentation.CircleAttachment{ID: f.ID, Name: f.Name, URL: "/api/v1/community/circles/" + c.Slug + "/attachments/" + f.ID, MIME: f.MIME, Size: f.Size, Image: strings.HasPrefix(f.MIME, "image/"), Unbound: f.TopicID == 0})
	}
	return items
}
func (s *Server) circleManagementProjection(r *http.Request, c community.Circle, a community.Actor, q url.Values, out *circlesPageView, management bool) {
	location := s.siteLocation(r.Context())
	reports := []community.Report{}
	for i := len(c.Reports) - 1; i >= 0; i-- {
		report := c.Reports[i]
		if management && out.Circle.Manage || report.UserID == a.ID {
			reports = append(reports, report)
		}
	}
	reportPage, _ := positiveQueryInt(q.Get("reports_page"), 1, 100000)
	reportPages := max(1, (len(reports)+19)/20)
	reportPage = min(reportPage, reportPages)
	start := (reportPage - 1) * 20
	for _, report := range reports[start:min(start+20, len(reports))] {
		v := presentation.CircleReport{ID: report.ID, TopicID: report.TopicID, ReplyID: report.ReplyID, Category: report.Category, Reason: report.Reason, Status: report.Status, Note: report.Note, Date: siteTime(location, report.CreatedAt, "2006.01.02 15:04")}
		if management && out.Circle.Manage {
			v.Reporter = s.circleName(r.Context(), report.UserID)
			v.URL = out.Circle.URL + fmt.Sprintf("topics/%d", report.TopicID)
		}
		out.Reports = append(out.Reports, v)
	}
	if reportPage > 1 {
		out.ReportPrevious = circlePageURL(out.Circle.URL, q, "reports_page", reportPage-1, "#circle-reports")
	}
	if reportPage < reportPages {
		out.ReportNext = circlePageURL(out.Circle.URL, q, "reports_page", reportPage+1, "#circle-reports")
	}
	if management && out.Circle.Manage {
		page, _ := positiveQueryInt(q.Get("history_page"), 1, 100000)
		pages := max(1, (len(c.Changes)+24)/25)
		page = min(page, pages)
		for i := len(c.Changes) - 1 - (page-1)*25; i >= max(0, len(c.Changes)-page*25); i-- {
			ch := c.Changes[i]
			out.Changes = append(out.Changes, presentation.CircleChange{Version: ch.Version, Actor: s.circleName(r.Context(), ch.ActorID), Action: circleActionLabel(ch.Action), Date: siteTime(location, ch.At, "2006.01.02 15:04")})
		}
		if page > 1 {
			out.HistoryPrevious = circlePageURL(out.Circle.URL, q, "history_page", page-1, "#circle-history")
		}
		if page < pages {
			out.HistoryNext = circlePageURL(out.Circle.URL, q, "history_page", page+1, "#circle-history")
		}
	}
	for _, f := range c.Attachments {
		if f.TopicID == 0 && (f.AuthorID == a.ID || management && out.Circle.Manage) {
			out.Files = append(out.Files, circleAttachments(c, []string{f.ID})...)
		}
	}
}
func circleActionLabel(action string) string {
	labels := map[string]string{"create": "创建圈子", "settings": "更新圈子设置", "join": "申请或加入圈子", "leave": "离开圈子", "member": "调整成员身份", "remove_member": "移除成员或申请", "transfer": "转让圈主", "topic": "发布话题", "reply": "发布回应", "edit_topic": "编辑话题", "edit_reply": "编辑回应", "accept": "更新采纳", "pin": "调整置顶", "feature": "调整精华", "lock": "调整回复权限", "hide_topic": "调整话题展示", "hide_reply": "调整回应展示", "archive_circle": "调整圈子归档", "archive_topic": "调整话题归档", "delete_topic": "永久清理话题", "delete_reply": "永久清理回应", "add_attachment": "上传附件", "remove_attachment": "清理附件", "report": "提交举报", "review_report": "处理举报", "clear_reports": "清理已处理举报"}
	if label, ok := labels[action]; ok {
		return label
	}
	return action
}
func (s *Server) circleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.circleEnabled(w, r) {
		return
	}
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	if s.circleFiles == nil {
		s.circleError(w, r, community.ErrNotFound)
		return
	}
	c, err := s.circles.Get(r.Context(), r.PathValue("circle"))
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	a := community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}
	if !community.CanPost(c, a) {
		s.circleError(w, r, community.ErrForbidden)
		return
	}
	if allowed, _ := s.accessRate.allow("circle-upload:"+strconv.FormatInt(p.UserID, 10), 20, time.Now()); !allowed {
		s.writeAPIError(w, r, 429, "community.rate_limited", "附件上传较频繁，请稍后再试", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, community.MaxAttachmentBytes+(1<<20))
	if err = r.ParseMultipartForm(512 << 10); err != nil {
		s.writeAPIError(w, r, 413, "community.upload_invalid", "上传失败：文件上限为 10 MiB", nil)
		return
	}
	defer r.MultipartForm.RemoveAll()
	a, err = s.circleUploadActor(r.Context(), r)
	if err != nil {
		s.circleAttachmentError(w, r, err)
		return
	}
	c, err = s.circles.Get(r.Context(), c.Slug)
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	if !community.CanPost(c, a) {
		s.circleError(w, r, community.ErrForbidden)
		return
	}
	values := r.MultipartForm.Value
	files := r.MultipartForm.File
	if len(values) != 2 || len(values["version"]) != 1 || len(values["key"]) != 1 || len(files) != 1 || len(files["file"]) != 1 {
		s.circleError(w, r, community.ErrInvalid)
		return
	}
	version, err := strconv.ParseInt(values["version"][0], 10, 64)
	if err != nil || version < 1 {
		s.circleError(w, r, community.ErrInvalid)
		return
	}
	if version != c.Version {
		found := false
		for _, receipt := range c.Receipts {
			if receipt.ActorID == p.UserID && receipt.Key == values["key"][0] {
				found = true
			}
		}
		if !found {
			s.circleError(w, r, community.ErrConflict)
			return
		}
	}
	header := files["file"][0]
	reader, err := header.Open()
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	store := &circleAttachmentUploadStore{Store: s.circles, actor: func(ctx context.Context) (community.Actor, error) {
		return s.circleUploadActor(ctx, r)
	}}
	file, result, err := s.circleFiles.Upload(ctx, store, c.Slug, a, values["key"][0], version, header.Filename, reader, time.Now().UTC())
	if err != nil {
		if store.authErr != nil {
			err = store.authErr
		}
		s.circleAttachmentError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"slug": c.Slug, "version": result.Version, "attachment_id": result.AttachmentID, "replay": result.Replay, "attachment": presentation.CircleAttachment{ID: file.ID, Name: file.Name, URL: "/api/v1/community/circles/" + c.Slug + "/attachments/" + file.ID, MIME: file.MIME, Size: file.Size, Image: strings.HasPrefix(file.MIME, "image/"), Unbound: true}})
}
func (s *Server) circleDownload(w http.ResponseWriter, r *http.Request) {
	if !s.circleEnabled(w, r) {
		return
	}
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"inline": true})
	if !ok {
		return
	}
	if q.Get("inline") != "" && q.Get("inline") != "1" {
		s.circleError(w, r, community.ErrInvalid)
		return
	}
	c, err := s.circles.Get(r.Context(), r.PathValue("circle"))
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	a, _ := s.circleActor(r)
	file, ok := community.AttachmentByID(c, r.PathValue("attachment"))
	if !ok || !community.CanReadAttachment(c, file, a) {
		s.circleError(w, r, community.ErrNotFound)
		return
	}
	if s.circleFiles == nil {
		s.circleError(w, r, community.ErrNotFound)
		return
	}
	data, remote := s.objectStorage.ReadCircle(r.Context(), c.Slug, file)
	if !remote {
		data, err = s.circleFiles.Read(r.Context(), file)
	}
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	access := func() error { return s.circleAttachmentAccess(r, c.Slug, file) }
	if err = access(); err != nil {
		s.circleAttachmentError(w, r, err)
		return
	}
	disposition := "attachment"
	if q.Get("inline") == "1" && strings.HasPrefix(file.MIME, "image/") {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", file.MIME)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Referrer-Policy", "no-referrer")
	writer := privateResponse(w, r, access)
	http.ServeContent(writer, r, file.Name, file.CreatedAt, bytes.NewReader(data))
}
func (s *Server) circlePreview(w http.ResponseWriter, r *http.Request) {
	if !s.circleEnabled(w, r) {
		return
	}
	p, ok := s.socialPrincipal(w, r, true)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	var in struct {
		Body        string   `json:"body"`
		Format      string   `json:"format"`
		Attachments []string `json:"attachments"`
	}
	if _, ok = s.decodeStrictAPIJSONLimit(w, r, &in, 64<<10, "64 KiB"); !ok {
		return
	}
	if len([]rune(in.Body)) > 10000 || len(in.Attachments) > community.MaxAttachmentsPerPost || in.Format != "plain" && in.Format != "markdown" {
		s.circleError(w, r, community.ErrInvalid)
		return
	}
	c, err := s.circles.Get(r.Context(), r.PathValue("circle"))
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	a := community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}
	if !community.CanRead(c, a) {
		s.circleError(w, r, community.ErrForbidden)
		return
	}
	files := []community.Attachment{}
	for _, id := range in.Attachments {
		f, ok := community.AttachmentByID(c, id)
		if !ok || !community.CanReadAttachment(c, f, a) {
			s.circleError(w, r, community.ErrForbidden)
			return
		}
		files = append(files, f)
	}
	writeJSON(w, 200, map[string]string{"html": community.RenderBody(c.Slug, in.Body, in.Format, files)})
}
func (s *Server) circleExport(w http.ResponseWriter, r *http.Request) {
	if !s.circleEnabled(w, r) {
		return
	}
	p, ok := s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	if _, ok = s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	c, err := s.circles.Get(r.Context(), r.PathValue("circle"))
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	r, ok = s.freshSessionRequest(w, r)
	if !ok {
		return
	}
	p, ok = s.socialPrincipal(w, r, false)
	if !ok {
		return
	}
	if !community.Owns(c, community.Actor{ID: p.UserID, Admin: p.Can(auth.CapabilitySettingsManage)}) {
		s.circleError(w, r, community.ErrForbidden)
		return
	}
	c.Receipts, c.Activities = nil, nil
	payload, err := json.Marshal(map[string]any{"schema": 1, "exported_at": time.Now().UTC(), "circle": c})
	if err != nil {
		s.circleError(w, r, err)
		return
	}
	access := func() error { return s.circleExportAccess(r, c.Slug) }
	if err = access(); err != nil {
		s.circleAttachmentError(w, r, err)
		return
	}
	payload = append(payload, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "circle-" + c.Slug + ".json"}))
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	_, _ = privateResponse(w, r, access).Write(payload)
}
func (s *Server) removeCircleOrphans(ctx context.Context, previous community.Circle) {
	if s.circleFiles == nil {
		return
	}
	if err := s.circleFiles.RemoveUnreferenced(ctx, s.circles, previous.Slug, previous.Attachments); err != nil {
		slog.Warn("remove detached circle file", "error", err)
	}
}
