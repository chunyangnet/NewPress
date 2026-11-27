package aijobs

import (
	"context"
	"crypto/rand"
	"errors"
	"newpress.local/newpress/internal/aieditor"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// A rule is authorized by its owner. Workers recheck the account and the target at execution.
type Rules struct {
	TopicLimit                                                                       int
	TopicImages                                                                      bool
	External                                                                         bool
	Version                                                                          int64
	UserID                                                                           int64
	Author, Provider, Model, Language                                                string
	AutoPost, AutoPage, Summary, Tags, Slug, Category, Topics, Cover, Inline, Vision bool
	SafeUpdate, Fallback, Moderate, Reply, Followup                                  bool
	BlockWords, HoldWords, Persona, WakeWords                                        string
	DelayMinutes, RetryLimit, RandomHours, CommentsPerPost                           int
	CategoryID                                                                       int64
	ExcludeCategory                                                                  bool
}
type WorkSpec struct {
	Collected                                      bool     `json:",omitempty"`
	Remaining                                      int      `json:",omitempty"`
	ScheduleID, PlannedTitle                       string   `json:",omitempty"`
	Strategy                                       Strategy `json:",omitzero"`
	Source                                         string   `json:",omitempty"`
	Kind, Key, Destination                         string
	PostID, PostVersion, CommentID, CommentVersion int64
	Rules                                          Rules
}
type Strategy struct {
	Style         string
	Search, Links bool
	Images, Ahead int
}

type Schedule struct {
	Titles                                                    []string `json:",omitempty"`
	Strategy                                                  Strategy `json:",omitzero"`
	Paused                                                    bool     `json:",omitempty"`
	ID, Brief, Author, Provider, Model, Language, Destination string
	UserID, CategoryID                                        int64
	Hours, Remaining                                          int
	Next                                                      time.Time
}
type NextStage struct {
	Work WorkSpec
	Due  time.Time
}

func (n *NextStage) Error() string { return "任务进入预约发布阶段" }

var ErrSearch = errors.New("联网检索不可用，请检查搜索凭据或调整写作计划")
var ErrStale = errors.New("原内容或权限已变化，请重新选择当前版本")
var ErrTransient = errors.New("模型线路临时不可用")

func (q *Queue) SaveRules(epoch string, r Rules) error {
	if r.TopicLimit < 0 || r.TopicLimit > 10 || r.Author == "" || r.RetryLimit < 0 || r.RetryLimit > 3 || r.RandomHours < 0 || r.RandomHours > 8760 || r.CommentsPerPost < 0 || r.CommentsPerPost > 100 || r.DelayMinutes < 0 || r.DelayMinutes > 86400 || r.CategoryID < 0 || aieditor.LanguageName(r.Language) == "" || len(r.Persona) > 2048 || len(r.WakeWords) > 512 || len(r.BlockWords)+len(r.HoldWords) > 8192 {
		return ErrInvalid
	}
	return q.change(true, 0, func(s *State) (bool, error) {
		if s.Epoch != epoch || r.Version != s.Rules.Version {
			return false, ErrConflict
		}
		if r.RandomHours != s.Rules.RandomHours || s.NextRandom.IsZero() {
			s.NextRandom = time.Now().UTC().Add(time.Duration(r.RandomHours) * time.Hour)
		}
		r.Version++
		s.Rules = r
		return true, nil
	})
}

func appendWork(s *State, j Job, now time.Time) (bool, error) {
	if j.Work.Kind == "" || j.Work.Key == "" || len(j.Work.Key) > 180 || j.Author == "" || utf8.RuneCountInString(j.Topic) > 1000 {
		return false, ErrInvalid
	}
	if slices.ContainsFunc(s.Jobs, func(existing Job) bool { return existing.Work.Key == j.Work.Key }) {
		return false, nil
	}
	// Keep editor drafts and failures. Completed machine actions can rotate when capacity is needed.
	if len(s.Jobs) >= MaxJobs {
		i := slices.IndexFunc(s.Jobs, func(old Job) bool {
			// The paired WordPress site polls asynchronously. Retain its
			// result throughout the connector's 24-hour collection window.
			if old.Work.Kind == "external" && old.Status == "succeeded" && !old.Work.Collected && old.Updated.Add(24*time.Hour).After(now) {
				return false
			}
			return old.Work.Kind != "" && (old.Status == "succeeded" || old.Status == "cancelled")
		})
		if i < 0 {
			return false, ErrFull
		}
		s.Jobs = slices.Delete(s.Jobs, i, i+1)
	}
	j.ID = rand.Text()
	j.Version = 1
	j.Status = "queued"
	j.Updated = now.UTC()
	if j.Due.IsZero() {
		j.Due = j.Updated
	}
	s.Jobs = append(s.Jobs, j)
	return true, nil
}

// AcknowledgeExternal releases a collected result for normal queue rotation.
// Lost acknowledgements are harmless: the result remains readable for 24 hours.
func (q *Queue) AcknowledgeExternal(epoch, key string) error {
	return q.change(true, 0, func(s *State) (bool, error) {
		if epoch != s.Epoch {
			return false, ErrConflict
		}
		for i := range s.Jobs {
			j := &s.Jobs[i]
			if j.Work.Kind != "external" || j.Work.Key != "external/"+key {
				continue
			}
			if j.Status != "succeeded" {
				return false, ErrConflict
			}
			if j.Work.Collected {
				return false, nil
			}
			j.Work.Collected = true
			j.Version++
			return true, nil
		}
		return false, ErrNotFound
	})
}
func (q *Queue) AddWork(epoch string, jobs []Job, now time.Time) error {
	err := q.change(true, 0, func(s *State) (bool, error) {
		if s.Epoch != epoch {
			return false, ErrConflict
		}
		dirty := false
		for _, j := range jobs {
			changed, err := appendWork(s, j, now)
			if err != nil {
				return false, err
			}
			dirty = dirty || changed
		}
		return dirty, nil
	})
	if err == nil {
		q.signal()
	}
	return err
}
func (q *Queue) Schedule(epoch string, plan Schedule, remove bool) error {
	err := q.change(true, 0, func(s *State) (bool, error) {
		if s.Epoch != epoch {
			return false, ErrConflict
		}
		i := slices.IndexFunc(s.Schedules, func(p Schedule) bool { return p.ID == plan.ID })
		if remove {
			if i < 0 {
				return false, ErrNotFound
			}
			for k := range s.Jobs {
				j := &s.Jobs[k]
				if j.Work.ScheduleID == plan.ID && !j.Terminal() {
					j.Status = "cancelled"
					j.Version++
					j.Updated = time.Now().UTC()
				}
			}
			s.Schedules = slices.Delete(s.Schedules, i, i+1)
			return true, nil
		}
		if !keyPattern.MatchString(plan.ID) || len(plan.Strategy.Style) > 1500 || plan.Strategy.Images < 0 || plan.Strategy.Images > 3 || plan.Strategy.Ahead < 0 || plan.Strategy.Ahead > 20 || plan.Hours < 1 || plan.Hours > 8760 || plan.Remaining < 1 || plan.Remaining > 10000 || strings.TrimSpace(plan.Brief) == "" || utf8.RuneCountInString(plan.Brief) > 500 || plan.Next.IsZero() || aieditor.LanguageName(plan.Language) == "" || !slices.Contains([]string{"pending", "publish"}, plan.Destination) {
			return false, ErrInvalid
		}
		if i >= 0 {
			return false, nil
		}
		if len(s.Schedules) >= 100 {
			return false, ErrFull
		}
		// The queue owns its snapshot even if the caller reuses the title pool.
		plan.Titles = slices.Clone(plan.Titles)
		s.Schedules = append(s.Schedules, plan)
		return true, nil
	})
	if err == nil {
		q.signal()
	}
	return err
}

// Materialize at most one missed occurrence per strategy after downtime.
func (q *Queue) tickSchedules(now time.Time) error {
	return q.change(true, 0, func(s *State) (bool, error) {
		if s.Paused {
			return false, nil
		}
		dirty := false
		if s.Rules.RandomHours > 0 && !s.NextRandom.After(now) {
			r := s.Rules
			j := Job{Topic: "随机文章互动", PostType: "post", Author: r.Author, UserID: r.UserID, Provider: r.Provider, Model: r.Model, Language: r.Language, Priority: 1, Work: WorkSpec{Kind: "random", Key: "random/" + s.NextRandom.Format(time.RFC3339Nano), Rules: r}}
			if _, err := appendWork(s, j, now); err != nil {
				return false, err
			}
			s.NextRandom = now.Add(time.Duration(r.RandomHours) * time.Hour)
			dirty = true
		}
		for i := range s.Schedules {
			p := &s.Schedules[i]
			if p.Paused || p.Remaining == 0 || p.Next.After(now) {
				continue
			}
			j := Job{Topic: p.Brief, Provider: p.Provider, Model: p.Model, PostType: "post", Language: p.Language, Author: p.Author, UserID: p.UserID, CategoryID: p.CategoryID, Work: WorkSpec{Strategy: p.Strategy, Kind: "workflow", Key: p.ID + "/" + p.Next.UTC().Format(time.RFC3339Nano), Destination: p.Destination, Rules: s.Rules}}
			j.Work.ScheduleID = p.ID
			if len(p.Titles) > 0 {
				j.Work.PlannedTitle = p.Titles[0]
				j.Topic = p.Titles[0]
			}
			if _, err := appendWork(s, j, now); err != nil {
				// Keep occurrences that fitted. Rolling them back would leave a
				// single free slot unusable whenever multiple plans are due.
				if errors.Is(err, ErrFull) && dirty {
					return true, nil
				}
				return false, err
			}
			if len(p.Titles) > 0 {
				p.Titles = slices.Clone(p.Titles[1:])
			}
			p.Next = now.Add(time.Duration(p.Hours) * time.Hour)
			p.Remaining--
			dirty = true
		}
		return dirty, nil
	})
}

// Commit serializes the final local side effect with cancellation and plugin removal.
func (q *Queue) Commit(ctx context.Context, j Job, write func() error) error {
	return q.with(true, 0, func(_ string, _ uint64) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !slices.ContainsFunc(q.state.Jobs, func(v Job) bool { return v.ID == j.ID && v.Status == "running" && v.Version == j.Version }) {
			return ErrStale
		}
		return write()
	})
}

func (q *Queue) PauseSchedule(epoch, id string, paused bool) error {
	err := q.change(true, 0, func(s *State) (bool, error) {
		if s.Epoch != epoch {
			return false, ErrConflict
		}
		i := slices.IndexFunc(s.Schedules, func(p Schedule) bool { return p.ID == id })
		if i < 0 {
			return false, ErrNotFound
		}
		s.Schedules[i].Paused = paused
		return true, nil
	})
	if err == nil {
		q.signal()
	}
	return err
}
func (s State) planPaused(id string) bool {
	if id == "" {
		return false
	}
	for _, p := range s.Schedules {
		if p.ID == id {
			return p.Paused
		}
	}
	return true
}
func (q *Queue) StoreTitles(ctx context.Context, j Job, titles []string) error {
	return q.change(true, 0, func(s *State) (bool, error) {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if !slices.ContainsFunc(s.Jobs, func(v Job) bool { return v.ID == j.ID && v.Version == j.Version && v.Status == "running" }) {
			return false, ErrStale
		}
		for i := range s.Schedules {
			p := &s.Schedules[i]
			if p.ID != j.Work.ScheduleID {
				continue
			}
			p.Titles = slices.Clone(titles[:min(len(titles), p.Remaining, 20)])
			return true, nil
		}
		return false, ErrStale
	})
}
