// Package aijobs manages the single NewPress worker for scheduled writing briefs.
package aijobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/plugin"
)

const (
	MaxJobs               = 100
	MaxTopics             = 20
	MaxResult             = 64 << 10
	maxStateBytes         = 12 << 20
	maxEncodedResultBytes = 96 << 10
)

var (
	ErrInvalid  = errors.New("任务参数无效")
	ErrConflict = errors.New("任务状态已变化，请刷新后再操作")
	ErrFull     = errors.New("任务记录已满，请移除已处理的记录")
	ErrNotFound = errors.New("没有找到该任务")
	keyPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
)

type Plan struct {
	Key, Author, Provider, Model, PostType, Language string
	UserID, CategoryID                               int64
	Topics                                           []string
	Start                                            time.Time // Zero means immediately; remains zero in the idempotency fingerprint.
	IntervalMinutes                                  int
}

type Job struct {
	Work                                               WorkSpec `json:",omitzero"`
	Priority                                           int      `json:",omitempty"`
	ID, Batch, Fingerprint                             string
	Topic, Author, Provider, Model, PostType, Language string
	UserID, CategoryID                                 int64
	Due, Updated                                       time.Time
	Status                                             string
	Version                                            int64
	Attempts                                           int
	Result, Problem                                    string
}

func (j Job) Label() string {
	switch j.Status {
	case "queued":
		return "待生成"
	case "running":
		return "生成中"
	case "succeeded":
		if j.Work.Kind != "" {
			return "已完成"
		}
		return "待编辑"
	case "failed":
		return "失败"
	case "interrupted":
		return "已中断"
	case "cancelled":
		return "已取消"
	}
	return j.Status
}
func (j Job) Terminal() bool { return j.Status != "queued" && j.Status != "running" }

type State struct {
	NextRandom     time.Time  `json:",omitzero"`
	Rules          Rules      `json:",omitzero"`
	Schedules      []Schedule `json:",omitempty"`
	Schema         int
	Paused         bool
	ControlVersion int64
	Instance       string
	Epoch          string `json:"-"`
	Jobs           []Job
	// Keep recent submission receipts even after a user deletes the last job of a plan.
	Receipts []receipt `json:",omitempty"`
}
type receipt struct{ Batch, Fingerprint string }
type Queue struct {
	m *plugin.Manager
	// All fields except wake are protected by Manager.WithData, including state reload on reinstall.
	generation uint64
	state      State
	active     string
	cancel     context.CancelFunc
	wake       chan struct{}
}

func New(m *plugin.Manager) *Queue { return &Queue{m: m, wake: make(chan struct{}, 1)} }
func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func validPlan(p Plan) bool {
	if !keyPattern.MatchString(p.Key) || p.UserID < 0 || p.CategoryID < 0 || p.Author == "" || len(p.Author) > 320 || p.Provider == "" || len(p.Provider) > 64 || p.Model == "" || len(p.Model) > 200 || (p.PostType != "post" && p.PostType != "page") || (p.PostType == "page" && p.CategoryID != 0) || p.Language == "" || len(p.Language) > 48 || len(p.Topics) < 1 || len(p.Topics) > MaxTopics || p.IntervalMinutes < 0 || p.IntervalMinutes > 43200 {
		return false
	}
	for _, t := range p.Topics {
		if strings.TrimSpace(t) == "" || utf8.RuneCountInString(t) > 1000 || strings.ContainsRune(t, 0) {
			return false
		}
	}
	return true
}

func (q *Queue) Enqueue(epoch string, p Plan, now time.Time) error {
	if epoch == "" || !validPlan(p) || aieditor.LanguageName(p.Language) == "" {
		return ErrInvalid
	}
	p.Start = p.Start.UTC()
	p.Topics = slices.Clone(p.Topics)
	for i, t := range p.Topics {
		p.Topics[i] = strings.TrimSpace(t)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return ErrInvalid
	}
	fingerprint := sha256.Sum256(encoded)
	fp := hex.EncodeToString(fingerprint[:])
	batchBytes, _ := json.Marshal([]any{p.UserID, p.Author, p.Key})
	b := sha256.Sum256(batchBytes)
	batch := hex.EncodeToString(b[:])
	start := p.Start
	if start.IsZero() {
		start = now
	}
	start = start.UTC()
	err = q.change(true, 0, func(st *State) (bool, error) {
		if st.Epoch != epoch {
			return false, ErrConflict
		}
		for _, r := range st.Receipts {
			if r.Batch == batch {
				if r.Fingerprint != fp {
					return false, ErrConflict
				}
				return false, nil
			}
		}
		for _, j := range st.Jobs {
			if j.Batch == batch {
				if j.Fingerprint != fp {
					return false, ErrConflict
				}
				return false, nil
			}
		}
		if len(st.Jobs)+len(p.Topics) > MaxJobs {
			return false, ErrFull
		}
		if len(st.Receipts) == MaxJobs {
			st.Receipts = slices.Delete(st.Receipts, 0, 1)
		}
		st.Receipts = append(st.Receipts, receipt{batch, fp})
		for i, t := range p.Topics {
			st.Jobs = append(st.Jobs, Job{Work: WorkSpec{Rules: st.Rules}, ID: rand.Text(), Batch: batch, Fingerprint: fp, Topic: t, Author: p.Author, UserID: p.UserID, Provider: p.Provider, Model: p.Model, PostType: p.PostType, Language: p.Language, CategoryID: p.CategoryID, Due: start.Add(time.Duration(i*p.IntervalMinutes) * time.Minute), Updated: now.UTC(), Version: 1, Status: "queued"})
		}
		return true, nil
	})
	if err == nil {
		q.signal()
	}
	return err
}

func (q *Queue) Snapshot() (state State, err error) {
	err = q.with(false, 0, func(_ string, _ uint64) error {
		state = q.state
		state.Receipts = nil
		state.Schedules = slices.Clone(state.Schedules)
		for i := range state.Schedules {
			state.Schedules[i].Titles = slices.Clone(state.Schedules[i].Titles)
		}
		state.Jobs = slices.Clone(state.Jobs)

		return nil
	})
	return
}

func (q *Queue) Action(epoch string, id string, version int64, action string, now time.Time) error {
	if epoch == "" || version < 1 {
		return ErrInvalid
	}
	err := q.change(true, 0, func(st *State) (bool, error) {
		if st.Epoch != epoch {
			return false, ErrConflict
		}
		if action == "pause" || action == "resume" {
			if id != "" {
				return false, ErrInvalid
			}
			if version != st.ControlVersion {
				return false, ErrConflict
			}
			pause := action == "pause"
			if st.Paused == pause {
				return false, nil
			}
			st.Paused = pause
			st.ControlVersion++
			return true, nil
		}
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if j.ID != id {
				continue
			}
			if version != j.Version {
				return false, ErrConflict
			}
			switch action {
			case "cancel":
				if j.Terminal() {
					return false, ErrConflict
				}
				j.Status = "cancelled"
			case "retry":
				if j.Status != "failed" && j.Status != "interrupted" && j.Status != "cancelled" {
					return false, ErrConflict
				}
				if j.Work.ScheduleID != "" && !slices.ContainsFunc(st.Schedules, func(p Schedule) bool { return p.ID == j.Work.ScheduleID }) {
					return false, ErrConflict
				}
				j.Status = "queued"
				j.Due = now.UTC()
				j.Problem = ""
				j.Attempts = 0
			case "promote":
				if j.Status != "queued" {
					return false, ErrConflict
				}
				j.Priority = 9
			case "advance":
				if j.Status != "queued" {
					return false, ErrConflict
				}
				j.Due = now.UTC()
			case "remove":
				if !j.Terminal() {
					return false, ErrConflict
				}
				st.Jobs = slices.Delete(st.Jobs, i, i+1)
				return true, nil
			default:
				return false, ErrInvalid
			}
			j.Version++
			j.Updated = now.UTC()
			return true, nil
		}
		return false, ErrNotFound
	})
	if err == nil {
		q.signal()
	}
	return err
}

// with reloads private data on every installation generation. A running job may already
// have been billed; recovery requires an explicit retry, never an automatic replay.
func (q *Queue) with(enabled bool, generation uint64, fn func(path string, g uint64) error) error {
	if q == nil || q.m == nil {
		return plugin.ErrInactive
	}
	return q.m.WithData(plugin.AIProMax, enabled, generation, func(dir string, g uint64) error {
		path := filepath.Join(dir, "writing-queue.json")
		if q.generation != g {
			st, err := plugin.ReadPrivate[State](path, maxStateBytes)
			if errors.Is(err, os.ErrNotExist) {
				st = State{Schema: 1}
			} else if err != nil {
				return err
			}
			if st.ControlVersion == 0 {
				st.ControlVersion = 1
			}
			if st.Instance == "" {
				st.Instance = rand.Text()
			}
			st.Epoch = st.Instance + "." + strconv.FormatUint(g, 10)
			if st.Schema != 1 || len(st.Jobs) > MaxJobs || len(st.Receipts) > MaxJobs || st.ControlVersion < 1 {
				return ErrInvalid
			}
			seen := map[string]bool{}
			changed := false
			for i := range st.Jobs {
				j := &st.Jobs[i]
				if !keyPattern.MatchString(j.ID) || seen[j.ID] || j.Version < 1 || j.Due.IsZero() || len(j.Result) > MaxResult || !slices.Contains([]string{"queued", "running", "succeeded", "failed", "cancelled", "interrupted"}, j.Status) {
					return ErrInvalid
				}
				seen[j.ID] = true
				if j.Status == "running" {
					j.Status = "interrupted"
					j.Problem = "执行中断，模型可能已计费。"
					j.Version++
					j.Updated = time.Now().UTC()
					changed = true
				}
			}
			if changed {
				if err := writeQueueState(path, st); err != nil {
					return err
				}
			}
			if q.cancel != nil {
				q.cancel()
			}
			q.active, q.cancel = "", nil
			q.state, q.generation = st, g
		}
		return fn(path, g)
	})
}

// Store first, then expose state and signal cancellation. A failed write cannot
// acknowledge a command or resurrect data after uninstall.
func (q *Queue) change(enabled bool, generation uint64, fn func(*State) (bool, error)) error {
	return q.with(enabled, generation, func(path string, _ uint64) error {
		next := q.state
		next.Schedules = slices.Clone(next.Schedules)
		next.Jobs = slices.Clone(next.Jobs)
		next.Receipts = slices.Clone(next.Receipts)
		dirty, err := fn(&next)
		if err != nil || !dirty {
			return err
		}
		if err = writeQueueState(path, next); err != nil {
			return err
		}
		q.state = next
		if q.cancel != nil && !slices.ContainsFunc(next.Jobs, func(j Job) bool { return j.ID == q.active && j.Status == "running" }) {
			q.cancel()
			q.cancel = nil
			q.active = ""
		}
		return nil
	})
}

func writeQueueState(path string, state State) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	required := len(encoded)
	for _, job := range state.Jobs {
		if job.Status == "running" {
			// Reserve a validated result and completion metadata before billing.
			// Other queue writes must keep this space available until completion.
			required += maxEncodedResultBytes + 1024
		}
	}
	if required > maxStateBytes {
		return ErrFull
	}
	return plugin.WritePrivate(path, state)
}
