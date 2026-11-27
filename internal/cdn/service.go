package cdn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/plugin"
)

const maxJobs = 200

type Job struct {
	ID               string            `json:"id"`
	ProfileID        string            `json:"profile_id"`
	Identity         string            `json:"identity"`
	Operation        string            `json:"operation"`
	Targets          []string          `json:"targets"`
	Status           string            `json:"status"`
	Source           string            `json:"source"`
	Actor            string            `json:"actor"`
	Attempts         int               `json:"attempts"`
	Created          time.Time         `json:"created"`
	Updated          time.Time         `json:"updated"`
	Next             time.Time         `json:"next"`
	RunToken         string            `json:"run_token,omitempty"`
	Error            string            `json:"error,omitempty"`
	Receipt          *Receipt          `json:"receipt,omitempty"`
	BenchmarkRequest *BenchmarkRequest `json:"benchmark_request,omitempty"`
	Benchmark        *Benchmark        `json:"benchmark,omitempty"`
	Diagnostic       *Diagnostic       `json:"diagnostic,omitempty"`
}
type diskState struct {
	Settings Settings     `json:"settings"`
	Jobs     []Job        `json:"jobs"`
	Routing  RoutingState `json:"routing"`
}
type Status struct {
	Settings     SettingsView   `json:"settings"`
	Enabled      bool           `json:"enabled"`
	Jobs         []Job          `json:"jobs"`
	Counts       map[string]int `json:"counts"`
	Capabilities []Capability   `json:"capabilities"`
	LastError    string         `json:"last_error,omitempty"`
}
type Service struct {
	manager        *plugin.Manager
	publicURL      string
	api            *ProviderClient
	mu             sync.Mutex // always acquire manager.WithData before mu
	state          diskState
	loaded         bool
	generation     uint64
	active         context.Context
	configCtx      context.Context
	configCancel   context.CancelFunc
	running        map[string]context.CancelFunc
	wake           chan struct{}
	closed         context.Context
	closeCancel    context.CancelFunc
	wg             sync.WaitGroup
	lastError      string
	lastDispatch   map[string]time.Time
	execute        func(context.Context, Profile, Job) (Receipt, error)
	benchmark      func(context.Context, Profile, BenchmarkRequest) (Benchmark, error)
	routingRunning bool
	routingCancel  context.CancelFunc
	routeInspect   func(context.Context, Profile, DNSProfile, Route) (RouteReport, error)
}

func New(m *plugin.Manager, publicURL string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	api := newProviderClient()
	s := &Service{manager: m, publicURL: strings.TrimRight(publicURL, "/"), api: api, closed: ctx, closeCancel: cancel, wake: make(chan struct{}, 1), running: map[string]context.CancelFunc{}, lastDispatch: map[string]time.Time{}}
	s.execute = api.Execute
	s.benchmark = runBenchmark
	s.routeInspect = s.inspectRoute
	return s
}
func randomID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b[:])
}
func (s *Service) Wake() {
	if s != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeCancel()
	s.mu.Lock()
	if s.configCancel != nil {
		s.configCancel()
	}
	for _, cancel := range s.running {
		cancel()
	}
	if s.routingCancel != nil {
		s.routingCancel()
	}
	s.mu.Unlock()
	s.api.client.CloseIdleConnections()
}

func (s *Service) with(enabled bool, g uint64, write bool, fn func(*diskState) error) error {
	if s == nil || s.manager == nil {
		return plugin.ErrInactive
	}
	return s.manager.WithData(plugin.CDN, enabled, g, func(dir string, generation uint64) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.loaded || s.generation != generation {
			st, e := plugin.ReadPrivate[diskState](filepath.Join(dir, "state.json"), 16<<20)
			if os.IsNotExist(e) {
				st = diskState{Settings: Defaults(), Jobs: []Job{}}
			} else if e != nil {
				return errors.New("CDN 配置或记录无法读取")
			}
			if len(st.Jobs) > maxJobs {
				return errors.New("CDN 记录数量超出限制")
			}
			if _, e = normalize(st.Settings); e != nil {
				return errors.New("CDN 配置校验失败")
			}
			if e = loadRouting(&st.Routing); e != nil {
				return e
			}
			seen := map[string]bool{}
			for i, j := range st.Jobs {
				_, idErr := hex.DecodeString(j.ID)
				validOp := slices.Contains([]string{"purge_url", "purge_directory", "purge_prefix", "purge_host", "purge_tag", "purge_all", "warm", "test", "diagnose", "benchmark"}, j.Operation)
				if len(j.ID) != 32 || idErr != nil || !idPattern.MatchString(j.ProfileID) || !validOp || j.Attempts < 0 || j.Attempts > 100 || seen[j.ID] || len(j.Targets) > 100 || j.Operation == "benchmark" && j.BenchmarkRequest == nil || j.Operation == "diagnose" && len(j.Targets) != 1 || j.Benchmark != nil && len(j.Benchmark.Results) > 64 || !slices.Contains([]string{"queued", "running", "retrying", "accepted", "succeeded", "failed", "cancelled", "interrupted"}, j.Status) {
					return errors.New("CDN 记录校验失败")
				}
				seen[j.ID] = true
				if j.Status == "running" {
					st.Jobs[i].Status = "interrupted"
					st.Jobs[i].Error = "进程或插件状态变化导致任务中断，云端结果尚未确认；请核对后手动重试"
				}
			}
			if s.configCancel != nil {
				s.configCancel()
			}
			s.configCtx, s.configCancel = context.WithCancel(s.closed)
			s.state = st
			s.loaded = true
			s.generation = generation
			s.active = nil
		}
		if !write {
			return fn(&s.state)
		}
		// Commit the complete snapshot atomically. A failed disk write must not
		// change in-memory receipts or optimistic settings versions.
		b, _ := json.Marshal(s.state)
		var next diskState
		if e := json.Unmarshal(b, &next); e != nil {
			return e
		}
		if e := fn(&next); e != nil {
			return e
		}
		b, e := json.Marshal(next)
		if e != nil || len(b) > 16<<20 {
			return errors.New("CDN 记录容量超出限制")
		}
		if e = plugin.WritePrivate(filepath.Join(dir, "state.json"), next); e != nil {
			return e
		}
		s.state = next
		return nil
	})
}
func (s *Service) Status() (Status, error) {
	v := Status{Counts: map[string]int{}, Capabilities: Capabilities(), Jobs: []Job{}}
	e := s.with(false, 0, false, func(st *diskState) error {
		v.Settings = view(st.Settings)
		v.LastError = s.lastError
		for i := len(st.Jobs) - 1; i >= 0; i-- {
			j := cloneJob(st.Jobs[i])
			v.Counts[j.Status]++
			j.Identity = ""
			j.RunToken = ""
			v.Jobs = append(v.Jobs, j)
		}
		return nil
	})
	if e == nil {
		v.Enabled = s.manager.WithData(plugin.CDN, true, 0, func(string, uint64) error { return nil }) == nil
	}
	return v, e
}
func (s *Service) Save(c Settings, clear []string) (SettingsView, error) {
	if len(clear) > 8 {
		return SettingsView{}, errors.New("清除令牌列表超过限制")
	}
	var result SettingsView
	e := s.with(false, 0, true, func(st *diskState) error {
		if st.Settings.Version != c.Version {
			return ErrConflict
		}
		if routingBusy(st.Routing) {
			return errors.New("接入任务正在处理，请完成后再修改 CDN 连接")
		}
		c.Profiles = slices.Clone(c.Profiles)
		for i, p := range c.Profiles {
			old, ok := profile(st.Settings, p.ID)
			// Never carry a secret into a different provider or account.
			if ok && old.Provider == p.Provider && old.AccessKey == strings.TrimSpace(p.AccessKey) && old.ResourceID == strings.TrimSpace(p.ResourceID) {
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
			c.Profiles[i] = p
		}
		var e error
		c, e = normalize(c)
		if e != nil {
			return e
		}
		for _, r := range st.Routing.Config.Routes {
			p, ok := profile(c, r.CFProfileID)
			if !ok || p.Provider != "cloudflare" {
				return errors.New("请先移除引用此 Cloudflare 连接的接入方案")
			}
		}
		if c.AssetBase != "" {
			u, _ := url.Parse(c.AssetBase)
			source, _ := url.Parse(s.publicURL)
			if source != nil && strings.EqualFold(u.Host, source.Host) {
				return errors.New("静态资源 CDN 应使用单独域名，并映射到本站 /theme-assets/ 路径")
			}
		}
		c.Version++
		for i, j := range st.Jobs {
			if j.Status == "running" {
				st.Jobs[i].Status = "interrupted"
				st.Jobs[i].Error = "设置已保存，任务已中断；云端已受理的操作可能继续执行"
			}
			if j.Status == "queued" || j.Status == "retrying" {
				p, ok := profile(c, j.ProfileID)
				if !ok || identity(p) != j.Identity {
					st.Jobs[i].Status = "cancelled"
					st.Jobs[i].Error = "连接已变更，请使用新配置提交任务"
				}
			}
		}
		st.Settings = c
		result = view(c)
		return nil
	})
	if e == nil {
		s.mu.Lock()
		if s.configCancel != nil {
			s.configCancel()
		}
		s.configCtx, s.configCancel = context.WithCancel(s.closed)
		s.mu.Unlock()
		s.Wake()
	}
	return result, e
}

type EnqueueRequest struct {
	ProfileID       string            `json:"profile_id"`
	Operation       string            `json:"operation"`
	Targets         []string          `json:"targets"`
	ExpectedVersion int64             `json:"expected_version"`
	ConfirmBroad    bool              `json:"confirm_broad"`
	Benchmark       *BenchmarkRequest `json:"benchmark,omitempty"`
}

func isTerminal(status string) bool {
	return status != "queued" && status != "retrying" && status != "running"
}
func prune(st *diskState) error {
	if len(st.Jobs) < maxJobs {
		return nil
	}
	for i, j := range st.Jobs {
		if isTerminal(j.Status) {
			st.Jobs = append(st.Jobs[:i], st.Jobs[i+1:]...)
			return nil
		}
	}
	return errors.New("已有 200 个待处理任务，请等待或取消后再提交")
}
func (s *Service) Enqueue(req EnqueueRequest, actor, source string) (Job, error) {
	var result Job
	e := s.with(true, 0, true, func(st *diskState) error {
		if req.ExpectedVersion != st.Settings.Version {
			return ErrConflict
		}
		p, ok := profile(st.Settings, req.ProfileID)
		if !ok {
			return errors.New("请选择已保存的连接")
		}
		if (req.Operation == "purge_all" || req.Operation == "purge_host" || req.Operation == "purge_prefix" || req.Operation == "purge_directory" || req.Operation == "purge_tag") && !req.ConfirmBroad {
			return errors.New("请确认刷新范围；全站刷新将影响整个 Zone / Distribution")
		}
		var targets []string
		var e error
		if req.Operation == "benchmark" {
			if p.Provider != "cloudflare" || req.Benchmark == nil || len(req.Targets) != 0 {
				return errors.New("Cloudflare 优选需要选择 Cloudflare 连接并填写测速参数")
			}
			if e = validateBenchmark(p, *req.Benchmark); e != nil {
				return e
			}
			for _, j := range st.Jobs {
				if j.Operation == "benchmark" && !isTerminal(j.Status) {
					return errors.New("已有优选任务，请等待完成或取消")
				}
			}
		} else {
			if req.Benchmark != nil {
				return errors.New("当前操作不接受测速参数")
			}
			targets, e = normalizeTargets(p, req.Operation, req.Targets)
			if e != nil {
				return e
			}
		}
		if p.Provider == "qiniu" && req.Operation == "purge_directory" && len(targets) > 10 {
			return errors.New("七牛单次最多刷新 10 个目录")
		}
		for _, j := range st.Jobs {
			if (j.Status == "queued" || j.Status == "retrying") && j.ProfileID == p.ID && j.Identity == identity(p) && j.Operation == req.Operation && req.Operation != "benchmark" && slices.Equal(j.Targets, targets) {
				result = cloneJob(j)
				return nil
			}
		}
		if e = prune(st); e != nil {
			return e
		}
		now := time.Now().UTC()
		result = cloneJob(Job{ID: randomID(), ProfileID: p.ID, Identity: identity(p), Operation: req.Operation, Targets: targets, Status: "queued", Source: source, Actor: actor, Created: now, Updated: now, BenchmarkRequest: req.Benchmark})
		st.Jobs = append(st.Jobs, cloneJob(result))
		return nil
	})
	if e == nil {
		s.Wake()
	}
	result.Identity, result.RunToken = "", ""
	return result, e
}
func (s *Service) JobAction(id, action string, version int64) error {
	var cancel context.CancelFunc
	e := s.with(false, 0, true, func(st *diskState) error {
		if st.Settings.Version != version {
			return ErrConflict
		}
		for i, j := range st.Jobs {
			if j.ID != id {
				continue
			}
			switch action {
			case "cancel":
				if isTerminal(j.Status) {
					return errors.New("此任务已结束；云端已受理任务不能撤回")
				}
				j.Status = "cancelled"
				j.Error = "管理员取消；已被云端受理的操作可能继续执行"
			case "retry":
				if !slices.Contains([]string{"failed", "interrupted", "cancelled"}, j.Status) {
					return errors.New("仅失败、中断和取消的任务可以重试")
				}
				p, ok := profile(st.Settings, j.ProfileID)
				if !ok || identity(p) != j.Identity {
					return errors.New("连接已变化，请重新提交任务")
				}
				if j.Operation == "benchmark" {
					for _, other := range st.Jobs {
						if other.Operation == "benchmark" && !isTerminal(other.Status) {
							return errors.New("已有优选任务正在处理")
						}
					}
				}
				j.Status = "queued"
				j.Error = ""
				j.Attempts = 0
				j.Next = time.Time{}
				j.Receipt = nil
				j.Benchmark = nil
				j.Diagnostic = nil
			default:
				return errors.New("不支持该任务操作")
			}
			j.Updated = time.Now().UTC()
			st.Jobs[i] = j
			cancel = s.running[id]
			return nil
		}
		return errors.New("任务不存在或已超出保留数量")
	})
	if e == nil {
		if cancel != nil {
			cancel()
		}
		s.Wake()
	}
	return e
}
func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer s.wg.Wait()
	for {
		s.dispatch()
		s.dispatchRouting()
		select {
		case <-ctx.Done():
			s.Close()
			return
		case <-s.closed.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}
func (s *Service) dispatch() {
	if s.closed.Err() != nil {
		return
	}
	var g uint64
	if e := s.with(true, 0, false, func(*diskState) error { g = s.generation; return nil }); e != nil {
		return
	}
	active, e := s.manager.ActiveContext(plugin.CDN, g)
	if e != nil {
		return
	}
	s.mu.Lock()
	s.active = active
	available := s.state.Settings.Workers - len(s.running)
	// An idle queue must not serialize/copy its entire history every tick.
	due := map[string]bool{}
	busy := map[string]bool{}
	for _, j := range s.state.Jobs {
		if _, ok := s.running[j.ID]; ok {
			busy[j.ProfileID] = true
		}
	}
	if !s.state.Settings.Paused {
		for _, j := range s.state.Jobs {
			if (j.Status == "queued" || j.Status == "retrying") && !j.Next.After(time.Now()) && !busy[j.ProfileID] && time.Since(s.lastDispatch[j.ProfileID]) >= time.Duration(s.state.Settings.IntervalSeconds)*time.Second {
				due[j.ProfileID] = true
			}
		}
	}
	available = min(available, len(due))
	s.mu.Unlock()
	if available <= 0 {
		return
	}
	for n := 0; n < available; n++ {
		var j Job
		var p Profile
		var configCtx context.Context
		var maxAttempts int
		e = s.with(true, g, true, func(st *diskState) error {
			if st.Settings.Paused {
				return errNoWork
			}
			now := time.Now().UTC()
			configCtx = s.configCtx
			maxAttempts = st.Settings.MaxAttempts
			busy := map[string]bool{}
			for _, old := range st.Jobs {
				if _, ok := s.running[old.ID]; ok {
					busy[old.ProfileID] = true
				}
			}
			for i, candidate := range st.Jobs {
				if candidate.Status != "queued" && candidate.Status != "retrying" || candidate.Next.After(now) || busy[candidate.ProfileID] || now.Sub(s.lastDispatch[candidate.ProfileID]) < time.Duration(st.Settings.IntervalSeconds)*time.Second {
					continue
				}
				var ok bool
				p, ok = profile(st.Settings, candidate.ProfileID)
				if !ok || identity(p) != candidate.Identity {
					continue
				}
				j = candidate
				j.Status = "running"
				j.Attempts++
				j.Updated = now
				j.RunToken = randomID()
				st.Jobs[i] = j
				return nil
			}
			return errNoWork
		})
		if e != nil {
			if !errors.Is(e, errNoWork) && !errors.Is(e, plugin.ErrInactive) {
				s.mu.Lock()
				s.lastError = "CDN 任务无法持久化，请检查数据目录"
				s.mu.Unlock()
			}
			return
		}
		jobCtx, cancel := context.WithTimeout(configCtx, 12*time.Minute)
		stop := context.AfterFunc(active, cancel)
		s.mu.Lock()
		s.running[j.ID] = cancel
		s.lastDispatch[p.ID] = time.Now()
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer cancel()
			defer stop()
			s.process(jobCtx, g, maxAttempts, p, j)
			s.mu.Lock()
			delete(s.running, j.ID)
			s.mu.Unlock()
			s.Wake()
		}()
	}
}

var errNoWork = errors.New("no pending work")

func (s *Service) process(ctx context.Context, g uint64, maxAttempts int, p Profile, j Job) {
	// A command can revoke the claim before dispatch registers its cancel
	// function. Recheck the durable attempt before starting any cloud request.
	if e := s.with(true, g, false, func(st *diskState) error {
		if j.RunToken == "" || !slices.ContainsFunc(st.Jobs, func(current Job) bool {
			return current.ID == j.ID && current.Status == "running" && current.RunToken == j.RunToken
		}) {
			return ErrConflict
		}
		return nil
	}); e != nil {
		return
	}
	var receipt Receipt
	var bench Benchmark
	var diag Diagnostic
	e := ctx.Err()
	if e == nil {
		switch j.Operation {
		case "benchmark":
			bench, e = s.benchmark(ctx, p, *j.BenchmarkRequest)
		case "diagnose":
			diag, e = diagnose(ctx, s.api.client, p, j.Targets[0])
		default:
			receipt, e = s.execute(ctx, p, j)
		}
	}
	finishErr := s.with(false, g, true, func(st *diskState) error {
		for i, current := range st.Jobs {
			if current.ID != j.ID {
				continue
			}
			if current.Status != "running" || current.RunToken != j.RunToken {
				return ErrConflict
			}
			j.Updated = time.Now().UTC()
			j.RunToken = ""
			j.Error = ""
			j.Next = time.Time{}
			if ctx.Err() != nil {
				j.Status = "interrupted"
				j.Error = PublicError(ctx.Err())
			} else if e != nil {
				j.Status = "failed"
				j.Error = PublicError(e)
				var op *OperationError
				if errors.As(e, &op) && op.Retry && j.Attempts < maxAttempts && j.Operation != "benchmark" {
					j.Status = "retrying"
					delay := max(time.Duration(1<<j.Attempts)*15*time.Second, op.After)
					j.Next = j.Updated.Add(delay + time.Duration(j.Attempts)*time.Second)
				}
			} else {
				j.Status = "accepted"
				j.Receipt = &receipt
				if j.Operation == "test" {
					j.Status = "succeeded"
				}
				if j.Operation == "benchmark" {
					j.Status = "succeeded"
					j.Receipt = nil
					j.Benchmark = &bench
				}
				if j.Operation == "diagnose" {
					j.Status = "succeeded"
					j.Receipt = nil
					j.Diagnostic = &diag
				}
			}
			st.Jobs[i] = j
			return nil
		}
		return ErrConflict
	})
	if finishErr != nil && !errors.Is(finishErr, plugin.ErrInactive) && !errors.Is(finishErr, ErrConflict) {
		s.mu.Lock()
		s.lastError = "云端操作已结束，但结果记录保存失败，请核对后再重试"
		s.mu.Unlock()
	}
}

// Delivery reads a small in-memory snapshot. Public requests never wait for
// cloud APIs or disk scans. Plugin lifecycle context revokes it immediately.
func (s *Service) Delivery() (string, int) {
	if s == nil {
		return "", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || s.active == nil || s.active.Err() != nil || s.closed.Err() != nil {
		return "", 0
	}
	return s.state.Settings.AssetBase, s.state.Settings.StaticTTL
}

// CoreDelivery is opt-in: existing CDN origins may only route theme assets.
func (s *Service) CoreDelivery() (string, int) {
	if s == nil {
		return "", 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded || s.active == nil || s.active.Err() != nil || s.closed.Err() != nil || !s.state.Settings.CoreAssets {
		return "", 0
	}
	return s.state.Settings.AssetBase, s.state.Settings.StaticTTL
}

// PurgeURLs enqueues configured URLs only. Failures remain visible in status;
// they cannot make a successful article save or storage upload fail afterwards.
func (s *Service) PurgeURLs(source string, rawURLs []string) {
	if s == nil {
		return
	}
	var c Settings
	if s.with(true, 0, false, func(st *diskState) error { c = st.Settings; return nil }) != nil {
		return
	}
	if source == "content" && !c.AutoContent || source == "comment" && !c.AutoComments || source == "storage" && !c.AutoStorage {
		return
	}
	for _, p := range c.Profiles {
		if !p.AutoPurge {
			continue
		}
		targets := []string{}
		for _, raw := range rawURLs {
			if strings.HasPrefix(raw, "/") {
				raw = s.publicURL + raw
			}
			if _, e := targetURL(p, raw); e == nil && !slices.Contains(targets, raw) {
				targets = append(targets, raw)
			}
		}
		for start := 0; start < len(targets); start += 100 {
			_, e := s.Enqueue(EnqueueRequest{ProfileID: p.ID, Operation: "purge_url", Targets: targets[start:min(start+100, len(targets))], ExpectedVersion: c.Version}, "系统联动", source)
			if e != nil {
				s.mu.Lock()
				s.lastError = "自动刷新入队失败，请查看连接设置和待处理任务"
				s.mu.Unlock()
			}
		}
	}
}
func (s *Service) DiagnosticClient() *http.Client { return s.api.client }
