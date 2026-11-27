package httpserver

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

const wechatJobsPerPage = 20

type wechatJobPage struct {
	Jobs      []wechat.Job `json:"jobs"`
	Total     int          `json:"total_jobs"`
	Filtered  int          `json:"filtered_jobs"`
	Page      int          `json:"job_page"`
	Pages     int          `json:"job_pages"`
	Active    int          `json:"active_jobs"`
	Attention int          `json:"attention_jobs"`
}

func wechatJobList(state wechat.State, query, status string, page int) wechatJobPage {
	result := wechatJobPage{Jobs: []wechat.Job{}, Total: len(state.Jobs)}
	matched := []wechat.Job{}
	query = strings.ToLower(strings.TrimSpace(query))
	for i := len(state.Jobs) - 1; i >= 0; i-- {
		job := state.Jobs[i]
		if job.AppID == state.Config.AppID {
			if job.Status == "queued" || job.Status == "working" || job.Status == "publishing" {
				result.Active++
			}
			if job.Status == "failed" || job.Status == "uncertain" {
				result.Attention++
			}
		}
		if status != "" && job.Status != status || !strings.Contains(strings.ToLower(job.Title), query) {
			continue
		}
		matched = append(matched, job.Public())
	}
	result.Filtered = len(matched)
	result.Pages = max(1, (len(matched)+wechatJobsPerPage-1)/wechatJobsPerPage)
	result.Page = min(max(1, page), result.Pages)
	start := (result.Page - 1) * wechatJobsPerPage
	result.Jobs = append(result.Jobs, matched[start:min(start+wechatJobsPerPage, len(matched))]...)
	return result
}

func wechatPositiveQuery(q url.Values, key string, fallback int64) (int64, bool) {
	if _, exists := q[key]; !exists {
		return fallback, true
	}
	value, err := strconv.ParseInt(q.Get(key), 10, 64)
	return value, err == nil && value > 0
}

func (s *Server) wechatQuery(w http.ResponseWriter, r *http.Request) (url.Values, int, int64, bool) {
	q, ok := s.strictAPIQuery(w, r, map[string]bool{"q": true, "post": true, "job_q": true, "job_status": true, "job_page": true})
	if !ok {
		return nil, 0, 0, false
	}
	page, validPage := wechatPositiveQuery(q, "job_page", 1)
	post, validPost := wechatPositiveQuery(q, "post", 0)
	status := q.Get("job_status")
	validStatus := status == "" || status == "queued" || status == "working" || status == "draft" || status == "publishing" || status == "published" || status == "failed" || status == "uncertain" || status == "skipped"
	if !validPage || page > wechat.MaxJobs || !validPost || !validStatus || len([]rune(q.Get("q"))) > 100 || len([]rune(q.Get("job_q"))) > 100 {
		s.writeAPIError(w, r, 400, "wechat.query_invalid", "筛选参数无效；关键词最多 100 字，页码与文章 ID 须为正整数", nil)
		return nil, 0, 0, false
	}
	return q, int(page), post, true
}

func wechatSyncable(post content.Post) bool {
	return post.Type == content.TypePost && (post.Status == content.StatusDraft || post.Status == content.StatusPublish || post.Status == content.StatusFuture)
}
