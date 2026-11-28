package httpserver

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
)

type analyticsRankStore struct {
	workbenchSourceAnalytics
	posts map[int64]int64
}

func (s analyticsRankStore) Snapshot(ctx context.Context, now time.Time) (analytics.State, error) {
	location, _ := analytics.LocationFromContext(ctx)
	return analytics.State{Days: map[string]analytics.Day{analytics.DateIn(now, location): {Views: 123, Posts: s.posts}}}, nil
}

type analyticsHeaderRepository struct {
	content.Repository
	headers     map[int64]content.PostHeader
	full, batch int
	failAt      int
	after       func()
}

func (r *analyticsHeaderRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.full++
	h, ok := r.headers[id]
	if !ok {
		return content.Post{}, content.ErrNotFound
	}
	return h.Post(), nil
}
func (r *analyticsHeaderRepository) GetPostHeaders(ctx context.Context, ids []int64) (map[int64]content.PostHeader, error) {
	r.batch++
	if len(ids) > 64 {
		return nil, errors.New("header batch exceeded 64")
	}
	if r.batch == r.failAt {
		return nil, errors.New("PRIVATE-HEADER-ERROR")
	}
	result := map[int64]content.PostHeader{}
	for _, id := range ids {
		if h, ok := r.headers[id]; ok {
			result[id] = h
		}
	}
	runAutosaveBoundary(&r.after)
	return result, nil
}

type analyticsCountStore struct {
	comment.Store
	batch, full int
	ids         []int64
	fail        bool
}

func (s *analyticsCountStore) ListApproved(context.Context, int64, int, int) ([]comment.Comment, int, error) {
	s.full++
	return nil, 9, nil
}
func (s *analyticsCountStore) CountApprovedByPost(ctx context.Context, ids []int64) (map[int64]int, error) {
	s.batch++
	s.ids = append([]int64{}, ids...)
	if s.fail {
		return nil, errors.New("PRIVATE-COUNT-ERROR")
	}
	counts := map[int64]int{}
	for _, id := range ids {
		counts[id] = 9
	}
	return counts, nil
}
func TestAnalyticsBatchedReadsAndRanking(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	posts := map[int64]int64{}
	repo := &analyticsHeaderRepository{Repository: s.repository, headers: map[int64]content.PostHeader{}}
	for id := int64(1); id <= 180; id++ {
		posts[id] = 1000 - id/2
		h := content.PostHeader{ID: id, Version: id + 7, Type: content.TypePost, Status: content.StatusPublish, Title: "rank", Slug: "rank", PublishedAt: time.Now().Add(-time.Hour)}
		if id <= 90 {
			h.Status = content.StatusDraft
		}
		if id%7 == 0 {
			continue
		}
		repo.headers[id] = h
	}
	counts := &analyticsCountStore{Store: s.comments}
	s.repository, s.comments = repo, counts
	s.SetAnalytics(analytics.NewCollector(analyticsRankStore{posts: posts}))
	report, err := s.analyticsReport(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{}
	for id := int64(91); len(want) < 20; id++ {
		if id%7 != 0 {
			want = append(want, id)
		}
	}
	got := []int64{}
	for _, a := range report.Articles {
		got = append(got, a.ID)
		if a.Comments != 9 || a.sourceVersion != a.ID+7 {
			t.Fatal("projection changed", a)
		}
	}
	if !reflect.DeepEqual(got, want) || report.Views != 123 {
		t.Fatal("ranking changed", got, want)
	}
	if repo.full != 0 || repo.batch != 2 || counts.full != 0 || counts.batch != 1 || !reflect.DeepEqual(counts.ids, want) {
		t.Fatal("unbounded/full repeated reads", repo.full, repo.batch, counts.full, counts.batch)
	}
}

func TestAnalyticsBatchedReadFailures(t *testing.T) {
	for _, failure := range []string{"header", "count", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			repo := &analyticsHeaderRepository{Repository: s.repository, headers: map[int64]content.PostHeader{1: {ID: 1, Version: 1, Type: content.TypePost, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)}}}
			counts := &analyticsCountStore{Store: s.comments}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch failure {
			case "header":
				repo.failAt = 1
			case "count":
				counts.fail = true
			case "cancel":
				repo.after = cancel
			}
			s.repository, s.comments = repo, counts
			s.SetAnalytics(analytics.NewCollector(analyticsRankStore{posts: map[int64]int64{1: 5}}))
			report, err := s.analyticsReport(ctx, 7)
			if err == nil || len(report.Articles) != 0 {
				t.Fatal("failed read returned partial successful report", err)
			}
		})
	}
}

func TestAnalyticsLegacyReadBudget(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	posts := map[int64]int64{}
	repo := &analyticsHeaderRepository{Repository: s.repository, headers: map[int64]content.PostHeader{}}
	for id := int64(1); id <= 100; id++ {
		posts[id] = 1
		repo.headers[id] = content.PostHeader{ID: id, Version: id, Type: content.TypePost, Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Hour)}
	}
	counts := &analyticsCountStore{Store: s.comments}
	s.repository = struct{ content.Repository }{repo}
	s.comments = struct{ comment.Store }{counts}
	s.SetAnalytics(analytics.NewCollector(analyticsRankStore{posts: posts}))
	report, err := s.analyticsReport(t.Context(), 7)
	if err != nil || len(report.Articles) != 20 {
		t.Fatal("legacy report failed", err)
	}
	for i, a := range report.Articles {
		if a.ID != int64(i+1) || a.Comments != 9 {
			t.Fatal("tie/order fallback changed", a)
		}
	}
	if repo.full != 20 || repo.batch != 0 || counts.full != 20 || counts.batch != 0 {
		t.Fatal("fallback reads beyond result limit", repo.full, repo.batch, counts.full, counts.batch)
	}
}
