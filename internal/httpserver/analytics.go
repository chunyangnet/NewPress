package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
)

type analyticsContextKey struct{}
type analyticsObservation struct {
	PostID       int64
	Search, Miss bool
	Term         string
}
type analyticsResponseWriter struct {
	http.ResponseWriter
	status   int
	complete bool
}

func (w *analyticsResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *analyticsResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *analyticsResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(body)
}
func (s *Server) SetAnalytics(collector *analytics.Collector) {
	if collector != nil {
		collector.SetLocationFunc(func() *time.Location { return s.siteLocation() })
		s.analytics = collector
	}
}
func analyticsPath(path string) bool {
	if path == "/" || path == "/explore/" || path == "/community/" || path == "/series/" {
		return true
	}
	for _, prefix := range []string{"/posts/", "/pages/", "/categories/", "/tags/", "/authors/", "/series/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
func (s *Server) observeAnalytics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := strings.ToLower(r.UserAgent())
		skip := !s.moduleEnabled("analytics") || !s.config.AnalyticsEnabled || r.Method != http.MethodGet || !analyticsPath(r.URL.Path) || ua == "" || r.Header.Get("DNT") == "1" || r.Header.Get("Sec-GPC") == "1"
		for _, marker := range []string{"bot", "crawler", "spider", "curl/", "wget/", "go-http-client", "python-requests", "headlesschrome"} {
			if strings.Contains(ua, marker) {
				skip = true
			}
		}
		if strings.Contains(strings.ToLower(r.Header.Get("Sec-Purpose")+" "+r.Header.Get("Purpose")), "prefetch") || r.Header.Get("X-Moz") == "prefetch" {
			skip = true
		}
		if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" && dest != "document" {
			skip = true
		}
		if !skip {
			if p, ok := s.currentAdmin(r); ok && p.Can(auth.CapabilityAdminAccess) {
				skip = true
			}
		}
		if skip {
			next.ServeHTTP(w, r)
			return
		}
		observed := &analyticsObservation{}
		capture := &analyticsResponseWriter{ResponseWriter: w}
		next.ServeHTTP(capture, r.WithContext(context.WithValue(r.Context(), analyticsContextKey{}, observed)))
		if capture.status == 200 && capture.complete && strings.HasPrefix(capture.Header().Get("Content-Type"), "text/html") {
			s.analytics.Record(analytics.Event{At: time.Now(), PostID: observed.PostID, Source: analytics.Source(r.Referer(), s.config.PublicURL), Search: observed.Search, Miss: observed.Miss, Term: analytics.SearchTerm(observed.Term)})
		}
	})
}

type analyticsTrend struct {
	Date     string `json:"date"`
	Views    int64  `json:"views"`
	Searches int64  `json:"searches"`
	Misses   int64  `json:"misses"`
}
type analyticsRank struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}
type analyticsArticle struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Views     int64  `json:"views"`
	Likes     int    `json:"likes"`
	Bookmarks int    `json:"bookmarks"`
	Comments  int    `json:"comments"`
}
type analyticsView struct {
	Timezone     string             `json:"timezone"`
	Enabled      bool               `json:"enabled"`
	Days         int                `json:"days"`
	Start        string             `json:"start"`
	End          string             `json:"end"`
	Views        int64              `json:"views"`
	ArticleViews int64              `json:"article_views"`
	Searches     int64              `json:"searches"`
	Misses       int64              `json:"misses"`
	Dropped      int64              `json:"dropped_since_start"`
	UpdatedAt    time.Time          `json:"updated_at"`
	Trend        []analyticsTrend   `json:"trend"`
	Sources      []analyticsRank    `json:"sources"`
	Terms        []analyticsRank    `json:"terms"`
	Articles     []analyticsArticle `json:"articles"`
	Points       string             `json:"-"`
	Peak         int64              `json:"-"`
}

// BootstrapJSON shares this request's authorized report with the browser.
// html/template escapes the string in an HTML attribute; never mark it trusted HTML/JS.
func (report analyticsView) BootstrapJSON() (string, error) {
	data, err := json.Marshal(report)
	return string(data), err
}

func rankAnalytics(values map[string]int64, limit int) []analyticsRank {
	result := []analyticsRank{}
	for label, n := range values {
		result = append(result, analyticsRank{label, n})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return result[i].Label < result[j].Label
		}
		return result[i].Count > result[j].Count
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}
func (s *Server) analyticsReport(ctx context.Context, days int) (analyticsView, error) {
	// Daily buckets and the displayed range follow the same calendar as the
	// rest of the site (站点设置 → 站点时区).
	location := s.siteLocation(ctx)
	ctx = analytics.WithLocation(ctx, location)
	now := time.Now()
	local := now.In(location)
	state, err := s.analytics.Snapshot(ctx, now)
	if err != nil {
		return analyticsView{}, err
	}
	result := analyticsView{Timezone: location.String(), Enabled: s.config.AnalyticsEnabled, Days: days, Start: analytics.DateIn(local.AddDate(0, 0, -days+1), location), End: analytics.DateIn(local, location), UpdatedAt: state.UpdatedAt, Dropped: s.analytics.Dropped(), Trend: []analyticsTrend{}, Articles: []analyticsArticle{}}
	posts := map[int64]int64{}
	sources, terms := map[string]int64{}, map[string]int64{}
	for i := days - 1; i >= 0; i-- {
		date := analytics.DateIn(local.AddDate(0, 0, -i), location)
		d := state.Days[date]
		result.Trend = append(result.Trend, analyticsTrend{date, d.Views, d.Searches, d.Misses})
		result.Views += d.Views
		result.Searches += d.Searches
		result.Misses += d.Misses
		result.Peak = max(result.Peak, d.Views)
		for id, n := range d.Posts {
			result.ArticleViews += n
			if id > 0 {
				posts[id] += n
			}
		}
		for k, n := range d.Sources {
			sources[k] += n
		}
		for k, n := range d.Terms {
			terms[k] += n
		}
	}
	result.Sources = rankAnalytics(sources, 10)
	result.Terms = rankAnalytics(terms, 20)
	for i := range result.Sources {
		switch result.Sources[i].Label {
		case "direct":
			result.Sources[i].Label = "直接访问 / 未提供来源"
		case "internal":
			result.Sources[i].Label = "站内跳转"
		case analytics.Other:
			result.Sources[i].Label = "其他来源（含容量合并）"
		}
	}
	for i := range result.Terms {
		if result.Terms[i].Label == analytics.Other {
			result.Terms[i].Label = "其他词（容量合并）"
		}
	}
	ids := make([]int64, 0, len(posts))
	for id := range posts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if posts[ids[i]] == posts[ids[j]] {
			return ids[i] < ids[j]
		}
		return posts[ids[i]] > posts[ids[j]]
	})
	for _, id := range ids {
		post, err := s.repository.GetByID(ctx, id)
		if errors.Is(err, content.ErrNotFound) {
			continue
		}
		if err != nil {
			return analyticsView{}, err
		}
		if post.Type != content.TypePost || !content.PublicAt(post, now) {
			continue
		}
		_, comments, err := s.comments.ListApproved(ctx, id, 1, 0)
		if err != nil {
			return analyticsView{}, err
		}
		result.Articles = append(result.Articles, analyticsArticle{ID: id, Title: post.Title, URL: s.contentPath(post), Views: posts[id], Comments: comments})
		if len(result.Articles) == 20 {
			break
		}
	}
	ids = ids[:0]
	for _, item := range result.Articles {
		ids = append(ids, item.ID)
	}
	if len(ids) > 0 {
		for _, kind := range []string{engagement.Like, engagement.Bookmark} {
			states, err := s.engagements.States(ctx, 0, kind, ids)
			if err != nil {
				return analyticsView{}, err
			}
			counts := map[int64]int{}
			for _, state := range states {
				counts[state.TargetID] = state.Count
			}
			for i := range result.Articles {
				if kind == engagement.Like {
					result.Articles[i].Likes = counts[result.Articles[i].ID]
				} else {
					result.Articles[i].Bookmarks = counts[result.Articles[i].ID]
				}
			}
		}
	}
	points := make([]string, len(result.Trend))
	for i, d := range result.Trend {
		points[i] = fmt.Sprintf("%.1f,%.1f", float64(i)*900/float64(max(1, days-1)), 180-float64(d.Views)*160/float64(max(int64(1), result.Peak)))
	}
	result.Points = strings.Join(points, " ")
	return result, nil
}
func (s *Server) analyticsDays(w http.ResponseWriter, r *http.Request) (int, bool) {
	values, ok := s.strictAPIQuery(w, r, map[string]bool{"days": true})
	if !ok {
		return 0, false
	}
	days := 30
	if values.Get("days") != "" {
		n, err := strconv.Atoi(values.Get("days"))
		if err != nil || n != 7 && n != 30 && n != 90 {
			s.writeAPIError(w, r, 400, "analytics.invalid_range", "请选择 7、30 或 90 天", nil)
			return 0, false
		}
		days = n
	}
	return days, true
}
func (s *Server) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	days, ok := s.analyticsDays(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	report, err := s.analyticsReport(ctx, days)
	if err != nil {
		s.renderFeatureRecovery(w, r, "analytics", "", s.adminView("analytics", p, readOnly), 503, "统计数据暂时不可读，请检查服务状态后重新检测。")
		return
	}
	data := s.baseViewFor(w, "运营统计", "从真实阅读与反馈中，找到下一篇值得写的内容。", "/admin/analytics/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-analytics"
	data.Admin = s.adminView("analytics", p, readOnly)
	data.Analytics = report
	s.render(w, 200, "admin-analytics", data)
}
func (s *Server) adminAnalyticsAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	days, ok := s.analyticsDays(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	report, err := s.analyticsReport(ctx, days)
	if err != nil {
		s.writeAPIError(w, r, 503, "analytics.unavailable", "统计暂时不可用，请稍后重试", nil)
		return
	}
	writeJSON(w, 200, report)
}
func (s *Server) registerAnalyticsRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/analytics/{$}", s.adminAnalytics)
	mux.HandleFunc("GET /api/v1/admin/analytics", s.adminAnalyticsAPI)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/analytics", s.adminAPIMethodNotAllowed("GET"))
	}
}
