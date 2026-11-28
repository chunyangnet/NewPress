package aijobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/plugin"
)

// Only fixed, actionable messages go to the queue; upstream error bodies may contain private data.
var (
	ErrBusy     = errors.New("模型正在处理其他任务")
	ErrActor    = errors.New("创建任务的账号已停用或不再有权限")
	ErrModel    = errors.New("请检查任务使用的接口和模型设置")
	ErrOutput   = errors.New("模型未返回完整、可编辑的文章")
	ErrCategory = errors.New("任务指定的分类已不可用，请重新创建计划")
)

type Generate func(context.Context, Job) (string, error)

func ValidResult(text string) bool {
	if text == "" || len(text) > MaxResult || !utf8.ValidString(text) {
		return false
	}
	// Escaped JSON can be much larger than the original text. The queue reserves
	// this encoded allowance before claiming a job and starting billable work.
	encoded, _ := json.Marshal(text)
	return len(encoded) <= maxEncodedResultBytes
}

func (q *Queue) Run(ctx context.Context, allowed func() bool, generate Generate) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if allowed() {
			now := time.Now()
			_ = q.tickSchedules(now)
			ran, err := q.step(ctx, now, generate)
			if err != nil && !errors.Is(err, plugin.ErrInactive) && !errors.Is(err, context.Canceled) {
				slog.Warn("AI Pro Max queue unavailable", "error", err)
			}
			if ran && err == nil {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		case <-ticker.C:
		}
	}
}

func (q *Queue) step(parent context.Context, now time.Time, generate Generate) (bool, error) {
	if err := parent.Err(); err != nil {
		return false, err
	}
	var job Job
	var generation uint64
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Claim and persist a single attempt before starting any billable work.
	err := q.with(true, 0, func(path string, g uint64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if q.state.Paused || q.active != "" {
			return nil
		}
		index := -1
		for i, j := range q.state.Jobs {
			if j.Status == "queued" && !q.state.planPaused(j.Work.ScheduleID) && !j.Due.After(now) && (index < 0 || j.Priority > q.state.Jobs[index].Priority || (j.Priority == q.state.Jobs[index].Priority && j.Due.Before(q.state.Jobs[index].Due))) {
				index = i
			}
		}
		if index < 0 {
			return nil
		}
		next := q.state
		next.Jobs = append([]Job(nil), next.Jobs...)
		j := &next.Jobs[index]
		j.Status = "running"
		j.Version++
		j.Attempts++
		j.Updated = now.UTC()
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeQueueState(path, next); err != nil {
			return err
		}
		job, generation = *j, g
		q.state = next
		q.active, q.cancel = j.ID, cancel
		return nil
	})
	if err != nil || job.ID == "" {
		return false, err
	}
	lease, err := q.m.ActiveContext(plugin.AIProMax, generation)
	if err != nil {
		return true, err
	}
	stop := context.AfterFunc(lease, cancel)
	defer stop()
	var result string
	if ctx.Err() == nil && lease.Err() == nil {
		result, err = generate(ctx, job)
	}
	if lease.Err() != nil {
		return true, plugin.ErrInactive
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && !ValidResult(result) {
		err = ErrOutput
	}
	finish := func(st *State) (bool, error) {
		for i := range st.Jobs {
			j := &st.Jobs[i]
			if j.ID != job.ID || j.Status != "running" || j.Version != job.Version {
				continue
			}
			j.Updated = time.Now().UTC()
			j.Version++
			var stage *NextStage
			switch {
			case errors.As(err, &stage):
				j.Work = stage.Work
				j.Status = "queued"
				j.Due = stage.Due.UTC()
				j.Problem = "已生成，等待预约时间"
				j.Attempts = 0
			case err == nil:
				j.Status = "succeeded"

				j.Result = result
				j.Problem = ""
			case errors.Is(err, ErrBusy):
				j.Status = "queued"
				j.Due = j.Updated.Add(15 * time.Second)
				j.Attempts--
			case errors.Is(err, ErrTransient) && j.Attempts <= j.Work.Rules.RetryLimit:
				j.Status = "queued"
				j.Due = j.Updated.Add(time.Minute << (j.Attempts - 1))
				j.Problem = "线路暂时不可用，已安排重试。"
			case errors.Is(err, context.Canceled):
				j.Status = "interrupted"
				j.Problem = "执行中断，模型可能已计费。"
			default:
				j.Status = "failed"
				j.Problem = "生成失败，请查看模型请求记录。"
				for _, known := range []error{ErrActor, ErrModel, ErrOutput, ErrCategory, ErrStale, ErrTransient, ErrSearch} {
					if errors.Is(err, known) {
						j.Problem = known.Error()
					}
				}
			}
			return true, nil
		}
		return false, nil
	}
	// A transient disk failure must not submit the model request a second time.
	for {
		e := q.change(true, generation, finish)
		if e == nil || errors.Is(e, plugin.ErrInactive) {
			return true, e
		}
		slog.Warn("cannot persist AI Pro Max result")
		select {
		case <-parent.Done():
			return true, parent.Err()
		case <-lease.Done():
			return true, plugin.ErrInactive
		case <-time.After(5 * time.Second):
		}
	}
}
