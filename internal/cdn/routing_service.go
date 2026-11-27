package cdn

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"newpress.local/newpress/internal/plugin"
)

const maxRoutingPlans = 60
const maxRoutingTasks = 100

func loadRouting(st *RoutingState) error {
	if st.Config.Version == 0 {
		if len(st.Config.DNS) > 0 || len(st.Config.Routes) > 0 || len(st.Tasks) > 0 || len(st.Plans) > 0 {
			return errors.New("CDN 接入状态缺少有效版本")
		}
		*st = routingDefaults()
		return nil
	}
	normalized, e := normalizeRouting(st.Config)
	if e != nil {
		return errors.New("CDN 接入配置校验失败")
	}
	st.Config = normalized
	if len(st.Plans) > maxRoutingPlans || len(st.Tasks) > maxRoutingTasks || len(st.Owned) > 500 || len(st.Reports) > 20 || len(st.Monitors) > 20 {
		return errors.New("CDN 接入状态超出容量")
	}
	if st.Reports == nil {
		st.Reports = map[string]RouteReport{}
	}
	if st.Monitors == nil {
		st.Monitors = map[string]routeMonitor{}
	}
	for i, t := range st.Tasks {
		if t.Status == "running" || t.Status == "queued" {
			st.Tasks[i].Status = "interrupted"
			st.Tasks[i].Error = "进程或插件生命周期已变化，请检查计划及云端结果后重新预览"
		}
	}
	for i, p := range st.Plans {
		if len(p.Changes) > 32 || len(p.Snapshots) > 20 {
			return errors.New("CDN 接入计划超出容量")
		}
		if p.Status == "applying" {
			st.Plans[i].Status = "unknown"
			st.Plans[i].Error = "执行中断；保留各步骤回执，不自动重放"
		}
	}
	return nil
}
func routingBusy(st RoutingState) bool {
	return slices.ContainsFunc(st.Tasks, func(t RoutingTask) bool { return t.Status == "queued" || t.Status == "running" })
}
func cloneRouting(st RoutingState) RoutingState {
	var copy RoutingState
	_ = json.Unmarshal(jsonBody(st), &copy)
	return copy
}
func (s *Service) RoutingStatus() (RoutingState, error) {
	var result RoutingState
	e := s.with(false, 0, false, func(st *diskState) error { result = routingView(cloneRouting(st.Routing)); return nil })
	return result, e
}
func (s *Service) SaveRouting(c RoutingConfig, clear []string, cdnVersion int64) (RoutingState, error) {
	if len(clear) > 8 {
		return RoutingState{}, errors.New("清除令牌列表超过限制")
	}
	e := s.with(false, 0, true, func(st *diskState) error {
		if c.Version != st.Routing.Config.Version || cdnVersion != st.Settings.Version {
			return ErrConflict
		}
		if routingBusy(st.Routing) {
			return errors.New("接入任务正在处理，请完成后再修改配置")
		}
		c.DNS = slices.Clone(c.DNS)
		c.Routes = slices.Clone(c.Routes)
		for i, p := range c.DNS {
			old, ok := dnsProfile(st.Routing.Config, p.ID)
			if ok && old.Provider == p.Provider && old.Zone == cleanHost(p.Zone) && old.AccessKey == p.AccessKey {
				if p.Secret == "" {
					p.Secret = old.Secret
				}
				if p.Token == "" && !slices.Contains(clear, p.ID) {
					p.Token = old.Token
				}
			}
			if slices.Contains(clear, p.ID) {
				p.Token = ""
			}
			c.DNS[i] = p
		}
		var e error
		c, e = normalizeRouting(c)
		if e != nil {
			return e
		}
		for _, r := range c.Routes {
			cf, ok := profile(st.Settings, r.CFProfileID)
			if !ok || cf.Provider != "cloudflare" {
				return errors.New("请选择已保存的 Cloudflare 连接")
			}
		}
		c.Version++
		st.Routing.Config = c
		for id := range st.Routing.Reports {
			if _, ok := routingRoute(c, id); !ok {
				delete(st.Routing.Reports, id)
				delete(st.Routing.Monitors, id)
			}
		}
		// Removed configurations never leave unattended automation behind.
		st.Routing.Owned = slices.DeleteFunc(st.Routing.Owned, func(o ownedRecord) bool {
			r, ok := routingRoute(c, o.RouteID)
			if !ok {
				return true
			}
			d, ok := dnsProfile(c, r.DNSProfileID)
			return !ok || dnsIdentity(d) != o.DNSIdentity
		})
		return nil
	})
	if e != nil {
		return RoutingState{}, e
	}
	s.Wake()
	return s.RoutingStatus()
}

type RoutingRequest struct {
	Action          string `json:"action"`
	RouteID         string `json:"route_id"`
	PlanID          string `json:"plan_id"`
	ExpectedVersion int64  `json:"expected_version"`
	CDNVersion      int64  `json:"cdn_version"`
	Confirm         bool   `json:"confirm"`
}

func (s *Service) EnqueueRouting(req RoutingRequest, actor string) (RoutingTask, error) {
	var task RoutingTask
	e := s.with(true, 0, true, func(st *diskState) error {
		if st.Routing.Config.Version != req.ExpectedVersion || st.Settings.Version != req.CDNVersion {
			return ErrConflict
		}
		if !slices.Contains([]string{"inspect", "preview_setup", "preview_validation", "preview_route", "preview_fallback", "preview_restore", "preview_rollback", "apply"}, req.Action) {
			return errors.New("不支持此接入操作")
		}
		if routingBusy(st.Routing) {
			return errors.New("已有接入任务，请等待完成")
		}
		r, ok := routingRoute(st.Routing.Config, req.RouteID)
		if !ok {
			return errors.New("接入方案不存在")
		}
		d, ok := dnsProfile(st.Routing.Config, r.DNSProfileID)
		if !ok {
			return ErrConflict
		}
		cf, ok := profile(st.Settings, r.CFProfileID)
		if !ok {
			return ErrConflict
		}
		fingerprint := routeIdentity(r, d, cf)
		if req.Action == "apply" || req.Action == "preview_rollback" {
			p, ok := findRoutingPlan(st.Routing, req.PlanID)
			if !ok || p.RouteID != r.ID || p.Identity != fingerprint {
				return ErrConflict
			}
			if req.Action == "apply" && (!req.Confirm || p.Status != "ready" || time.Now().After(p.Expires)) {
				return errors.New("请确认未过期的计划；计划只能执行一次")
			}
			if req.Action == "preview_rollback" && (p.Kind == "setup" || !slices.Contains([]string{"applied", "partial", "unknown"}, p.Status)) {
				return errors.New("仅已执行的 DNS 计划支持生成回滚计划")
			}
		}
		now := time.Now().UTC()
		task = RoutingTask{ID: randomID(), RouteID: r.ID, Action: req.Action, PlanID: req.PlanID, Identity: fingerprint, Actor: actor, Status: "queued", Created: now, Updated: now}
		if len(st.Routing.Tasks) >= maxRoutingTasks {
			st.Routing.Tasks = st.Routing.Tasks[1:]
		}
		st.Routing.Tasks = append(st.Routing.Tasks, task)
		return nil
	})
	if e == nil {
		s.Wake()
	}
	task.Identity = ""
	return task, e
}
func findRoutingPlan(st RoutingState, id string) (RoutingPlan, bool) {
	for _, p := range st.Plans {
		if p.ID == id {
			return p, true
		}
	}
	return RoutingPlan{}, false
}
func putPlan(st *RoutingState, p RoutingPlan) error {
	p = cloneRoutingPlan(p)
	for i, old := range st.Plans {
		if old.ID == p.ID {
			st.Plans[i] = p
			return nil
		}
	}
	if len(st.Plans) >= maxRoutingPlans {
		index := slices.IndexFunc(st.Plans, func(p RoutingPlan) bool {
			return p.Status != "unknown" && p.Status != "partial" && p.Status != "applying"
		})
		if index < 0 {
			return errors.New("请先处理未确认的接入计划，记录容量已满")
		}
		st.Plans = append(st.Plans[:index], st.Plans[index+1:]...)
	}
	st.Plans = append(st.Plans, p)
	return nil
}

func (s *Service) dispatchRouting() {
	if s.closed.Err() != nil {
		return
	}
	var generation uint64
	var due bool
	e := s.with(true, 0, false, func(st *diskState) error {
		generation = s.generation
		if s.routingRunning || st.Settings.Paused {
			return nil
		}
		due = slices.ContainsFunc(st.Routing.Tasks, func(t RoutingTask) bool { return t.Status == "queued" })
		if !due {
			for _, r := range st.Routing.Config.Routes {
				if r.Monitor && !st.Routing.Monitors[r.ID].Next.After(time.Now()) {
					due = true
					break
				}
			}
		}
		return nil
	})
	if e != nil || !due {
		return
	}
	active, e := s.manager.ActiveContext(plugin.CDN, generation)
	if e != nil {
		return
	}
	var task RoutingTask
	var r Route
	var d DNSProfile
	var cf Profile
	var configCtx context.Context
	e = s.with(true, generation, true, func(st *diskState) error {
		if s.routingRunning || st.Settings.Paused {
			return errNoWork
		}
		index := slices.IndexFunc(st.Routing.Tasks, func(t RoutingTask) bool { return t.Status == "queued" })
		if index < 0 {
			for _, candidate := range st.Routing.Config.Routes {
				if candidate.Monitor && !st.Routing.Monitors[candidate.ID].Next.After(time.Now()) {
					dns, ok := dnsProfile(st.Routing.Config, candidate.DNSProfileID)
					if !ok {
						continue
					}
					cp, ok := profile(st.Settings, candidate.CFProfileID)
					if !ok {
						continue
					}
					now := time.Now().UTC()
					t := RoutingTask{ID: randomID(), RouteID: candidate.ID, Action: "monitor", Identity: routeIdentity(candidate, dns, cp), Actor: "自动监测", Status: "queued", Created: now, Updated: now}
					if len(st.Routing.Tasks) >= maxRoutingTasks {
						st.Routing.Tasks = st.Routing.Tasks[1:]
					}
					st.Routing.Tasks = append(st.Routing.Tasks, t)
					index = len(st.Routing.Tasks) - 1
					break
				}
			}
		}
		if index < 0 {
			return errNoWork
		}
		task = st.Routing.Tasks[index]
		var ok bool
		r, ok = routingRoute(st.Routing.Config, task.RouteID)
		if !ok {
			return ErrConflict
		}
		d, ok = dnsProfile(st.Routing.Config, r.DNSProfileID)
		if !ok {
			return ErrConflict
		}
		cf, ok = profile(st.Settings, r.CFProfileID)
		if !ok {
			return ErrConflict
		}
		if task.Identity != routeIdentity(r, d, cf) {
			st.Routing.Tasks[index].Status = "interrupted"
			st.Routing.Tasks[index].Error = "连接或方案已变化，请重新预览"
			return nil
		}
		task.Status = "running"
		task.Updated = time.Now().UTC()
		st.Routing.Tasks[index] = task
		configCtx = s.configCtx
		m := st.Routing.Monitors[r.ID]
		if m.Identity != task.Identity {
			m = routeMonitor{Identity: task.Identity}
		}
		m.Next = time.Now().UTC().Add(time.Duration(r.Interval) * time.Second)
		st.Routing.Monitors[r.ID] = m
		return nil
	})
	if e != nil || task.Status != "running" {
		return
	}
	ctx, cancel := context.WithTimeout(configCtx, 10*time.Minute)
	stop := context.AfterFunc(active, cancel)
	s.mu.Lock()
	s.routingRunning = true
	s.routingCancel = cancel
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer stop()
		s.processRouting(ctx, generation, task, r, d, cf)
		s.mu.Lock()
		s.routingRunning = false
		s.routingCancel = nil
		s.mu.Unlock()
		s.Wake()
	}()
}
func (s *Service) processRouting(ctx context.Context, g uint64, task RoutingTask, r Route, d DNSProfile, cf Profile) {
	report, e := s.routeInspect(ctx, cf, d, r)
	var snapshot RoutingState
	if e == nil {
		e = s.with(true, g, true, func(st *diskState) error {
			st.Routing.Reports[r.ID] = cloneRouteReport(report)
			snapshot = cloneRouting(st.Routing)
			return nil
		})
	}
	if e == nil {
		if task.Action == "preview_rollback" {
			old, ok := findRoutingPlan(snapshot, task.PlanID)
			if !ok {
				e = ErrConflict
			} else {
				for name := range old.Snapshots {
					current, err := s.api.dnsRecords(ctx, d, name)
					if err != nil {
						e = err
						break
					}
					report.Records = slices.DeleteFunc(report.Records, func(v DNSRecord) bool { return v.Name == name })
					report.Records = append(report.Records, current...)
				}
			}
		}
	}
	if e == nil {
		switch task.Action {
		case "inspect":
		case "monitor":
			e = s.monitorRoute(ctx, g, task, r, d, cf, report, snapshot)
		case "apply":
			var plan RoutingPlan
			var ok bool
			plan, ok = findRoutingPlan(snapshot, task.PlanID)
			if !ok {
				e = ErrConflict
			} else {
				e = s.applyRoutingPlan(ctx, g, task, r, d, cf, report, plan)
			}
		default:
			var plan RoutingPlan
			plan, e = buildRoutingPlan(task, r, d, cf, report, snapshot)
			if e == nil {
				e = s.with(true, g, true, func(st *diskState) error {
					if e := putPlan(&st.Routing, plan); e != nil {
						return e
					}
					for i, t := range st.Routing.Tasks {
						if t.ID == task.ID {
							st.Routing.Tasks[i].PlanID = plan.ID
						}
					}
					return nil
				})
			}
		}
	}
	finish := s.with(false, g, true, func(st *diskState) error {
		for i, t := range st.Routing.Tasks {
			if t.ID == task.ID && t.Status == "running" {
				t.Status = "succeeded"
				if e != nil {
					t.Status = "failed"
					t.Error = PublicError(e)
				}
				if ctx.Err() != nil {
					t.Status = "interrupted"
					t.Error = "操作中断，请核对云端及计划中的逐步回执"
				}
				t.Updated = time.Now().UTC()
				st.Routing.Tasks[i] = t
			}
		}
		if e != nil {
			m := st.Routing.Monitors[r.ID]
			m.Failures = 0
			m.Successes = 0
			st.Routing.Monitors[r.ID] = m
		}
		return nil
	})
	if finish != nil && !errors.Is(finish, plugin.ErrInactive) {
		s.mu.Lock()
		s.lastError = "接入任务结果保存失败，请核对云端记录后重新预览"
		s.mu.Unlock()
	}
}
