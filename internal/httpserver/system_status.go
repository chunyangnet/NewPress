package httpserver

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/systeminfo"
	"newpress.local/newpress/internal/version"
)

type systemStatusMonitor struct {
	mu        sync.Mutex
	resources systeminfo.Collector
	cached    systemStatusView
	cachedKey systemStatusCacheKey
	pending   *systemStatusPending
}

type systemStatusView struct {
	CollectedAt    time.Time             `json:"collected_at"`
	CollectedLabel string                `json:"collected_label"`
	Timezone       string                `json:"timezone"`
	Status         string                `json:"status"`
	StatusLabel    string                `json:"status_label"`
	StatusDetail   string                `json:"status_detail"`
	Healthy        int                   `json:"healthy"`
	Total          int                   `json:"total"`
	Uptime         string                `json:"uptime"`
	StartedAt      string                `json:"started_at"`
	Hostname       string                `json:"hostname"`
	Platform       string                `json:"platform"`
	Version        string                `json:"version"`
	Metrics        []systemStatusMetric  `json:"metrics"`
	Checks         []systemStatusCheck   `json:"checks"`
	Groups         []systemStatusGroup   `json:"groups"`
	Resources      systeminfo.Snapshot   `json:"resources"`
	Process        systemProcessSnapshot `json:"process"`
}

type systemProcessSnapshot struct {
	PID             int      `json:"pid"`
	UptimeSeconds   int64    `json:"uptime_seconds"`
	Goroutines      int      `json:"goroutines"`
	GoMaxProcs      int      `json:"go_max_procs"`
	HeapAllocBytes  uint64   `json:"heap_alloc_bytes"`
	HeapInuseBytes  uint64   `json:"heap_inuse_bytes"`
	GoReservedBytes uint64   `json:"go_reserved_bytes"`
	HeapObjects     uint64   `json:"heap_objects"`
	NextGCBytes     uint64   `json:"next_gc_bytes"`
	GCCount         uint32   `json:"gc_count"`
	LastGCPauseMS   *float64 `json:"last_gc_pause_ms"`
}

type systemStatusMetric struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Icon      string  `json:"icon"`
	Value     string  `json:"value"`
	Unit      string  `json:"unit"`
	Detail    string  `json:"detail"`
	Note      string  `json:"note"`
	Tone      string  `json:"tone"`
	Available bool    `json:"available"`
	Ring      bool    `json:"ring"`
	Percent   float64 `json:"percent"`
}

type systemStatusCheck struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Icon        string   `json:"icon"`
	Detail      string   `json:"detail"`
	Status      string   `json:"status"`
	StatusLabel string   `json:"status_label"`
	Latency     string   `json:"latency"`
	LatencyMS   *float64 `json:"latency_ms"`
}

type systemStatusGroup struct {
	ID      string            `json:"id"`
	Title   string            `json:"title"`
	Caption string            `json:"caption"`
	Icon    string            `json:"icon"`
	Rows    []systemStatusRow `json:"rows"`
}

type systemStatusRow struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value"`
}

func (s *Server) registerSystemStatusRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /admin/system/{$}", s.adminSystemStatus)
	mux.HandleFunc("GET /api/v1/admin/system-status", s.adminSystemStatusAPI)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/system-status", s.adminAPIMethodNotAllowed("GET"))
	}
}

func (s *Server) adminSystemStatus(w http.ResponseWriter, r *http.Request) {
	p, readOnly, ok := s.requireAdminPageCapability(w, r, auth.CapabilitySystemRead)
	if !ok {
		return
	}
	access, ok := s.newSystemStatusAuthority(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	report, err := s.authorizedSystemStatus(access)
	if err != nil {
		s.systemStatusError(w, r, access, err)
		return
	}
	data := s.baseViewFor(w, "系统状态", "服务健康、服务器资源与应用运行环境", "/admin/system/")
	data.IsAdmin, data.BodyClass = true, "is-admin is-admin-system"
	data.Admin = s.adminViewWithReadiness("system", p, readOnly, report.Checks)
	data.SystemStatus = report
	if !access.recheck(w) {
		return
	}
	s.renderChecked(w, http.StatusOK, "admin-system", data, r, func() bool { return access.recheck(w) }, access.access)
}

func (s *Server) adminSystemStatusAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminJSONReadCapability(w, r, auth.CapabilitySystemRead); !ok {
		return
	}
	access, ok := s.newSystemStatusAuthority(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.strictAPIQuery(w, r, nil); !ok {
		return
	}
	report, err := s.authorizedSystemStatus(access)
	if err != nil {
		s.systemStatusError(w, r, access, err)
		return
	}
	access.json(w, report)
}

func (s *Server) systemStatusReport(ctx context.Context) systemStatusView {
	report, _ := s.systemStatusReportChecked(ctx)
	return report
}

func (s *Server) collectSystemStatus(ctx context.Context) systemStatusView {
	if ctx.Err() != nil {
		return systemStatusView{}
	}
	resources := s.systemStatus.resources.Collect(s.config.DataDir)
	if ctx.Err() != nil {
		return systemStatusView{}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	uptime := max(int64(0), int64(time.Since(s.startedAt).Seconds()))
	process := systemProcessSnapshot{
		PID: os.Getpid(), UptimeSeconds: uptime, Goroutines: runtime.NumGoroutine(), GoMaxProcs: runtime.GOMAXPROCS(0),
		HeapAllocBytes: mem.HeapAlloc, HeapInuseBytes: mem.HeapInuse, GoReservedBytes: mem.Sys,
		HeapObjects: mem.HeapObjects, NextGCBytes: mem.NextGC, GCCount: mem.NumGC,
	}
	if mem.NumGC > 0 {
		pause := float64(mem.PauseNs[(mem.NumGC+255)%256]) / 1e6
		process.LastGCPauseMS = &pause
	}
	settings := s.siteSnapshot(ctx).Settings
	location := site.TimeLocation(settings)
	report := systemStatusView{
		Resources: resources, Process: process, Version: version.Version,
		Hostname: statusOrUnavailable(resources.Hostname), Platform: runtime.GOOS + " / " + runtime.GOARCH,
		Timezone: location.String(), Uptime: statusDuration(uptime), StartedAt: s.startedAt.In(location).Format("2006-01-02 15:04:05"),
		Metrics: statusMetrics(resources, process), Checks: s.systemReadinessChecks(ctx),
	}
	for _, check := range report.Checks {
		if check.Status == "disabled" {
			continue
		}
		report.Total++
		if check.Status == "pass" {
			report.Healthy++
		}
	}
	report.Status, report.StatusLabel = "pass", "核心服务可用"
	report.StatusDetail = "本次就绪检查全部通过，站点服务响应正常。"
	if report.Healthy != report.Total {
		report.Status, report.StatusLabel = "error", "部分服务需要检查"
		report.StatusDetail = "检测到未通过的服务，请查看下方检查结果。"
	}
	if ctx.Err() != nil {
		return systemStatusView{}
	}
	report.Groups = s.systemDetailGroups(resources, process, report.StartedAt, settings)
	report.CollectedAt = time.Now().UTC()
	report.CollectedLabel = report.CollectedAt.In(location).Format("15:04:05")
	return report
}

func statusMetrics(host systeminfo.Snapshot, process systemProcessSnapshot) []systemStatusMetric {
	metrics := []systemStatusMetric{
		{ID: "cpu", Label: "系统 CPU", Icon: "cpu", Value: "—", Unit: "%", Detail: fmt.Sprintf("%d 个逻辑处理器", host.LogicalCPUs), Note: "暂不可用", Tone: "muted", Ring: true},
		{ID: "memory", Label: "系统内存", Icon: "memory-stick", Value: "—", Unit: "%", Detail: "操作系统未返回内存指标", Note: "暂不可用", Tone: "muted", Ring: true},
		{ID: "disk", Label: "数据盘占用", Icon: "hard-drive", Value: "—", Unit: "%", Detail: "数据目录所在卷暂不可用", Note: "暂不可用", Tone: "muted", Ring: true},
		{ID: "process", Label: "应用驻留内存", Icon: "server", Value: "—", Detail: "Go 堆已分配 " + statusBytes(process.HeapAllocBytes), Note: "进程 RSS", Tone: "muted"},
	}
	set := func(index int, percent float64) {
		metric := &metrics[index]
		metric.Percent = math.Round(math.Max(0, math.Min(100, percent))*10) / 10
		metric.Value = fmt.Sprintf("%.1f", metric.Percent)
		metric.Available, metric.Tone, metric.Note = true, "normal", "余量充足"
		if index == 0 {
			metric.Note = "负载平稳"
		}
		if metric.Percent >= 85 {
			metric.Tone, metric.Note = "warning", "占用较高"
		}
		if metric.Percent >= 95 {
			metric.Tone, metric.Note = "error", "接近满载"
		}
	}
	if host.CPUPercent != nil {
		set(0, *host.CPUPercent)
		metrics[0].Detail = fmt.Sprintf("%d 个逻辑处理器 · 最近 %.0f 秒均值", host.LogicalCPUs, host.CPUSampleSeconds)
	} else if host.CPUSupported {
		metrics[0].Note, metrics[0].Detail = "等待采样", "第二次采样后显示实际使用率"
	}
	if host.Memory != nil {
		used := host.Memory.TotalBytes - host.Memory.AvailableBytes
		set(1, 100*float64(used)/float64(host.Memory.TotalBytes))
		metrics[1].Detail = statusBytes(used) + " / " + statusBytes(host.Memory.TotalBytes) + " 已用"
	}
	if host.Disk != nil {
		set(2, 100*float64(host.Disk.TotalBytes-host.Disk.FreeBytes)/float64(host.Disk.TotalBytes))
		metrics[2].Detail = "可用 " + statusBytes(host.Disk.AvailableBytes) + " / " + statusBytes(host.Disk.TotalBytes)
	}
	if host.ProcessRSSBytes != nil {
		parts := strings.SplitN(statusBytes(*host.ProcessRSSBytes), " ", 2)
		metrics[3].Value, metrics[3].Unit = parts[0], parts[1]
		metrics[3].Available, metrics[3].Tone = true, "normal"
	}
	return metrics
}

func (s *Server) systemReadinessChecks(parent context.Context) []systemStatusCheck {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	type probe struct {
		id, label, icon, detail string
		ready                   func(context.Context) error
		disabled                bool
	}
	probes := []probe{
		{"content", "内容存储", "database", "内容仓库连接与就绪状态", s.repository.Ready, false},
		{"media", "媒体资源", "image", "媒体目录与对象目录可访问", func(context.Context) error { return s.media.Ready() }, false},
		{"auth", "身份与会话", "shield-check", "用户与会话存储就绪", s.auth.Ready, false},
		{"comments", "评论服务", "message-circle", "评论存储就绪", s.comments.Ready, false},
		{"navigation", "导航服务", "route", "导航存储就绪", s.navigation.Ready, false},
		{"engagement", "互动服务", "heart", "点赞与收藏存储就绪", s.engagements.Ready, false},
		{"notifications", "通知服务", "bell-ring", "通知存储就绪", nil, false},
		{"links", "链接服务", "globe-2", "链接页面存储就绪", nil, false},
		{"feedback", "反馈服务", "message-square", "反馈存储就绪", nil, false},
		{"circles", "社区圈子", "users", "社区存储就绪", nil, !s.config.CommunityEnabled},
	}
	if s.notifications != nil && s.notifications.Store != nil {
		probes[6].ready = s.notifications.Store.Ready
	}
	if s.linkPages != nil {
		probes[7].ready = s.linkPages.Ready
	}
	if s.feedback != nil {
		probes[8].ready = s.feedback.Ready
	}
	if s.circles != nil {
		probes[9].ready = s.circles.Ready
	}
	checks := make([]systemStatusCheck, len(probes))
	var wg sync.WaitGroup
	for i, entry := range probes {
		checks[i] = systemStatusCheck{ID: entry.id, Label: entry.label, Icon: entry.icon, Detail: entry.detail, Status: "pass", StatusLabel: "正常", Latency: "—"}
		if entry.disabled {
			checks[i].Status, checks[i].StatusLabel, checks[i].Detail = "disabled", "未启用", "按当前站点配置关闭"
			continue
		}
		wg.Go(func() {
			check := &checks[i]
			defer func() {
				if recover() != nil {
					check.Status, check.StatusLabel, check.Detail = "error", "不可用", "就绪检查未通过，请查看服务器日志"
				}
			}()
			if entry.ready == nil {
				check.Status, check.StatusLabel, check.Detail = "error", "不可用", "服务尚未就绪"
				return
			}
			start := time.Now()
			err := entry.ready(ctx)
			ms := float64(time.Since(start).Microseconds()) / 1000
			check.LatencyMS = &ms
			check.Latency = fmt.Sprintf("%.0f ms", ms)
			if ms < 1 {
				check.Latency = "< 1 ms"
			}
			if err != nil {
				check.Status, check.StatusLabel, check.Detail = "error", "不可用", "就绪检查未通过，请查看服务器日志"
				if ctx.Err() != nil {
					check.Detail = "检查超时或请求已取消，请重试"
				}
			}
		})
	}
	wg.Wait()
	return checks
}

func (s *Server) systemDetailGroups(host systeminfo.Snapshot, process systemProcessSnapshot, startedAt string, settings site.Settings) []systemStatusGroup {
	hostUptime, physicalMemory, availableMemory, diskFree, diskCapacity := "暂不可用", "暂不可用", "暂不可用", "暂不可用", "暂不可用"
	if host.UptimeSeconds != nil {
		hostUptime = statusDuration(int64(*host.UptimeSeconds))
	}
	if host.Memory != nil {
		physicalMemory, availableMemory = statusBytes(host.Memory.TotalBytes), statusBytes(host.Memory.AvailableBytes)
	}
	if host.Disk != nil {
		diskCapacity, diskFree = statusBytes(host.Disk.TotalBytes), statusBytes(host.Disk.AvailableBytes)
	}
	storage := statusOrUnavailable(s.repositoryKind)
	pluginLabel := "暂不可用"
	if plugins, err := s.plugins.List(); err == nil {
		enabled, invalid := 0, 0
		for _, p := range plugins {
			if p.Enabled {
				enabled++
			}
			if p.Error != "" {
				invalid++
			}
		}
		pluginLabel = fmt.Sprintf("%d 已安装 · %d 启用", len(plugins), enabled)
		if invalid > 0 {
			pluginLabel += fmt.Sprintf(" · %d 待检查", invalid)
		}
	}
	lastGC := "尚未发生"
	if process.LastGCPauseMS != nil {
		lastGC = fmt.Sprintf("%.3f ms", *process.LastGCPauseMS)
	}
	onOff := func(on bool) string {
		if on {
			return "已开启"
		}
		return "已关闭"
	}
	buildValue := func(value string) string {
		if value == "" || value == "unknown" {
			return "构建时未记录"
		}
		return value
	}
	return []systemStatusGroup{
		{ID: "host", Title: "服务器信息", Caption: "HOST / 01", Icon: "server", Rows: []systemStatusRow{
			{"hostname", "主机名称", statusOrUnavailable(host.Hostname)}, {"os", "操作系统", statusOrUnavailable(host.OSVersion)},
			{"arch", "平台架构", runtime.GOOS + " / " + runtime.GOARCH}, {"cpu-model", "处理器型号", statusOrUnavailable(host.CPUModel)},
			{"cores", "逻辑处理器", fmt.Sprintf("%d 个", host.LogicalCPUs)}, {"host-uptime", "主机运行时间", hostUptime},
			{"physical-memory", "物理内存", physicalMemory}, {"available-memory", "可用内存", availableMemory},
		}},
		{ID: "application", Title: "应用环境", Caption: "APPLICATION / 02", Icon: "code-2", Rows: []systemStatusRow{
			{"version", "NewPress 版本", version.Version}, {"go-version", "Go 运行时", runtime.Version()},
			{"pid", "进程 PID", strconv.Itoa(process.PID)}, {"started", "应用启动时间", startedAt},
			{"theme", "当前主题", s.themes.Active().Name + " · " + s.themes.Active().Version}, {"plugins", "插件概况", pluginLabel},
			{"commit", "构建提交", buildValue(version.Commit)}, {"build-date", "构建时间", buildValue(version.BuildDate)},
		}},
		{ID: "runtime", Title: "运行时详情", Caption: "GO RUNTIME / 03", Icon: "cpu", Rows: []systemStatusRow{
			{"goroutines", "Goroutine 数", strconv.Itoa(process.Goroutines)}, {"max-procs", "并行执行上限", strconv.Itoa(process.GoMaxProcs)},
			{"heap", "Go 堆已分配", statusBytes(process.HeapAllocBytes)}, {"heap-inuse", "Go 堆页占用", statusBytes(process.HeapInuseBytes)},
			{"go-reserved", "Go 保留内存", statusBytes(process.GoReservedBytes)}, {"heap-objects", "存活堆对象", strconv.FormatUint(process.HeapObjects, 10)},
			{"gc-count", "GC 累计次数", strconv.FormatUint(uint64(process.GCCount), 10)}, {"gc-pause", "最近 GC 暂停", lastGC},
		}},
		{ID: "storage", Title: "存储与目录", Caption: "STORAGE / 04", Icon: "hard-drive", Rows: []systemStatusRow{
			{"storage-engine", "内容存储方式", storage}, {"data-dir", "数据目录", statusPath(s.config.DataDir)},
			{"themes-dir", "主题目录", statusPath(s.config.ThemesDir)}, {"plugins-dir", "插件目录", statusPath(s.config.PluginsDir)},
			{"disk-capacity", "所在卷总容量", diskCapacity}, {"disk-available", "当前账号可用", diskFree},
			{"upload-limit", "单次上传上限", statusLimit(s.effectiveMaxUploadBytes())}, {"site-timezone", "站点时区", site.TimeLocation(settings).String()},
		}},
		{ID: "configuration", Title: "服务配置", Caption: "CONFIGURATION / 05", Icon: "sliders-horizontal", Rows: []systemStatusRow{
			{"listen", "HTTP 监听地址", statusOrUnavailable(s.config.Address)}, {"read-timeout", "请求读取超时", statusTimeout(s.config.ReadTimeout)},
			{"write-timeout", "响应写入超时", statusTimeout(s.config.WriteTimeout)}, {"idle-timeout", "空闲连接超时", statusTimeout(s.config.IdleTimeout)},
			{"analytics", "运营统计采集", onOff(s.config.AnalyticsEnabled)}, {"community", "社区圈子", onOff(s.config.CommunityEnabled)},
			{"public-api", "公开内容 API", onOff(settings.API.PublicPostsEnabled)}, {"maintenance", "站点维护模式", onOff(settings.Operations.MaintenanceEnabled)},
		}},
	}
}

func statusOrUnavailable(value string) string {
	if strings.TrimSpace(value) == "" {
		return "暂不可用"
	}
	return value
}

// Capture the underlying store at assembly, before media-usage decorators wrap
// the repository. Configuration strings and connection credentials stay private.
func statusStorageLabel(repository content.Repository, databaseConfigured bool) string {
	if wrapped, ok := repository.(interface{ UnderlyingRepository() content.Repository }); ok {
		repository = wrapped.UnderlyingRepository()
	}
	switch repository.(type) {
	case *content.FileRepository:
		return "本地文件 · JSON"
	case *content.MemoryRepository:
		return "内存存储"
	default:
		if databaseConfigured {
			return "PostgreSQL"
		}
		return "自定义内容存储"
	}
}
func statusPath(value string) string {
	if value == "" {
		return "未设置"
	}
	if absolute, err := filepath.Abs(value); err == nil {
		return absolute
	}
	return value
}
func statusLimit(value int64) string {
	if value <= 0 {
		return "未设置"
	}
	return statusBytes(uint64(value))
}
func statusTimeout(value time.Duration) string {
	if value <= 0 {
		return "未设置"
	}
	return value.String()
}
func statusBytes(bytes uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	value, unit := float64(bytes), 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", bytes)
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}
func statusDuration(seconds int64) string {
	seconds = max(0, seconds)
	if seconds >= 86400 {
		return fmt.Sprintf("%d 天 %d 小时", seconds/86400, seconds%86400/3600)
	}
	if seconds >= 3600 {
		return fmt.Sprintf("%d 小时 %d 分", seconds/3600, seconds%3600/60)
	}
	if seconds >= 60 {
		return fmt.Sprintf("%d 分 %d 秒", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%d 秒", seconds)
}
