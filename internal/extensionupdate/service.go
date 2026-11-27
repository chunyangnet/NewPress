// Package extensionupdate coordinates signed extension releases and bounded
// background jobs. Filesystem commits remain in the plugin/theme managers.
package extensionupdate

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/version"
)

type Market interface {
	View() plugin.MarketView
	Refresh(context.Context) error
	InstallWithProgress(context.Context, string, string, extension.Progress) error
	Update(context.Context, string, string, string, string, extension.Progress) error
	InstallTheme(context.Context, *theme.Manager, string, string, extension.Progress) error
	UpdateTheme(context.Context, *theme.Manager, string, string, string, string, extension.Progress) error
}

type Job struct {
	ID             string    `json:"id"`
	Kind           string    `json:"kind"`
	Action         string    `json:"action"`
	Slug           string    `json:"slug"`
	Target         string    `json:"target"`
	Phase          string    `json:"phase"`
	Downloaded     int64     `json:"downloaded"`
	Total          int64     `json:"total"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Message        string    `json:"message"`
	ReviewRequired bool      `json:"review_required"`
}

func (j Job) Busy() bool {
	return slices.Contains([]string{"queued", "checking", "downloading", "verifying", "validating", "preparing", "switching", "cancelling"}, j.Phase)
}

type Choice struct {
	Version        string              `json:"version"`
	Digest         string              `json:"digest"`
	Size           int64               `json:"size"`
	Notes          string              `json:"notes"`
	Requires       string              `json:"requires"`
	Release        *extension.Metadata `json:"release,omitempty"`
	Allowed        bool                `json:"allowed"`
	Action         string              `json:"action"`
	Reason         string              `json:"reason"`
	AddedGrants    []string            `json:"added_grants"`
	AddedHosts     []string            `json:"added_hosts"`
	SettingsChange bool                `json:"settings_change"`
}

type Card struct {
	Dependency  theme.DependencyStatus `json:"dependency"`
	Slug        string                 `json:"slug"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Current     string                 `json:"current"`
	Installed   bool                   `json:"installed"`
	Enabled     bool                   `json:"enabled"`
	Active      bool                   `json:"active"`
	Builtin     bool                   `json:"builtin"`
	State       extension.VersionState `json:"state"`
	Releases    []Choice               `json:"releases"`
	Error       string                 `json:"error,omitempty"`
	HasUpdate   bool                   `json:"has_update"`
	Latest      string                 `json:"latest"`
	ConfigURL   string                 `json:"config_url,omitempty"`
	CoverURL    string                 `json:"cover_url,omitempty"`
	Author      string                 `json:"author,omitempty"`
	License     string                 `json:"license,omitempty"`
}

type Snapshot struct {
	Kind        string            `json:"kind"`
	Channel     string            `json:"channel"`
	Core        string            `json:"core"`
	Market      plugin.MarketView `json:"market"`
	Cards       []Card            `json:"cards"`
	Job         Job               `json:"job"`
	Installed   int               `json:"installed"`
	Updates     int               `json:"updates"`
	ActiveTheme string            `json:"active_theme,omitempty"`
}

type Request struct {
	Kind            string `json:"kind"`
	Action          string `json:"action"`
	Slug            string `json:"slug"`
	Version         string `json:"version"`
	Digest          string `json:"digest"`
	ExpectedVersion string `json:"expected_version"`
	Revision        string `json:"revision"`
	Channel         string `json:"channel"`
	ExpectedActive  string `json:"expected_active,omitempty"`
}

type Service struct {
	mu             sync.Mutex
	market         Market
	themeMarket    Market
	plugins        *plugin.Manager
	themes         *theme.Manager
	stateFile      string
	job            Job
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	closed         bool
	persisted      time.Time
	persistenceErr error
}

type Option func(*Service)

func WithThemeMarket(market Market) Option { return func(s *Service) { s.themeMarket = market } }
func (s *Service) marketFor(kind string) Market {
	if kind == "theme" && s.themeMarket != nil {
		return s.themeMarket
	}
	return s.market
}
func (s *Service) ThemeCover(ctx context.Context, slug, digest string) ([]byte, string, error) {
	if digest == "" {
		return s.themes.ReadCover(slug)
	}
	if market, ok := s.marketFor("theme").(interface {
		ThemeCover(context.Context, string, string) ([]byte, string, error)
	}); ok {
		return market.ThemeCover(ctx, slug, digest)
	}
	return nil, "", os.ErrNotExist
}

func New(stateFile string, market Market, plugins *plugin.Manager, themes *theme.Manager, options ...Option) (*Service, error) {
	if market == nil || plugins == nil || themes == nil {
		return nil, errors.New("扩展更新服务缺少管理器")
	}
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(stateFile), 0700); err != nil {
		return nil, err
	}
	s := &Service{market: market, plugins: plugins, themes: themes, stateFile: stateFile, job: Job{Phase: "idle"}}
	for _, option := range options {
		option(s)
	}
	job, err := extension.ReadJSON[Job](stateFile, 16<<10)
	if err == nil {
		if !validKind(job.Kind) || job.ID == "" || len(job.ID) > 64 || len(job.Message) > 4096 ||
			!slices.Contains([]string{"idle", "queued", "checking", "downloading", "verifying", "validating", "preparing", "switching", "cancelling", "cancelled", "succeeded", "failed", "interrupted"}, job.Phase) {
			return nil, errors.New("扩展任务记录无效")
		}
		s.job = job
		if job.Busy() {
			s.job.Phase = "interrupted"
			s.job.Message = "上次任务被中断；启动时已处理目录恢复，请核对当前版本后重新检查。"
			s.job.UpdatedAt = time.Now().UTC()
			if err := extension.WriteJSON(stateFile, s.job); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func validKind(kind string) bool { return kind == "plugin" || kind == "theme" }

func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job.Busy()
}

func (s *Service) Job(kind string) Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.Kind != kind {
		if s.job.Busy() {
			return Job{Phase: "other", Message: "另一项扩展任务正在运行，完成后可以继续。"}
		}
		return Job{Phase: "idle"}
	}
	return s.job
}

func (s *Service) Snapshot(kind, channel string) (Snapshot, error) {
	if !validKind(kind) || (channel != "stable" && channel != "preview") {
		return Snapshot{}, errors.New("扩展类型或更新通道无效")
	}
	v := Snapshot{Kind: kind, Channel: channel, Core: version.Version, Market: s.marketFor(kind).View(), Job: s.Job(kind), Cards: []Card{}}
	bySlug := map[string]int{}
	manifests := map[string]plugin.Manifest{}
	add := func(slug, name, description string) *Card {
		if index, ok := bySlug[slug]; ok {
			return &v.Cards[index]
		}
		bySlug[slug] = len(v.Cards)
		v.Cards = append(v.Cards, Card{Slug: slug, Name: name, Description: description})
		return &v.Cards[len(v.Cards)-1]
	}
	if kind == "plugin" {
		items, err := s.plugins.List()
		if err != nil {
			return Snapshot{}, err
		}
		for _, item := range items {
			c := add(item.Slug, item.Manifest.Name, item.Manifest.Description)
			c.Installed, c.Enabled, c.Current, c.Error, c.State = true, item.Enabled, item.Manifest.Version, item.Error, item.VersionState
			if !item.Official && c.Error == "" {
				c.Error = "当前插件缺少官方校验记录，请先修复安装"
			}
			c.Builtin = item.Manifest.Runtime == "builtin/comment-origin-v1"
			if c.Builtin {
				c.ConfigURL = "/admin/plugins/comment-origin/"
			} else if len(item.Manifest.Hooks) > 0 {
				c.ConfigURL = "/admin/plugins/" + item.Slug + "/"
			}
			manifests[item.Slug] = item.Manifest
		}
		for _, release := range v.Market.Plugins {
			if !inChannel(release.Manifest.Version, release.Release, channel) {
				continue
			}
			m := release.Manifest
			c := add(m.Slug, m.Name, m.Description)
			c.Builtin = m.Runtime == "builtin/comment-origin-v1"
			choice := makeChoice(m.Version, m.NewPress, release.SHA256, release.Size, release.Notes, release.Release, *c, v.Market.Expires)
			if old, ok := manifests[m.Slug]; ok {
				for _, grant := range m.Capabilities {
					if !slices.Contains(old.Capabilities, grant) {
						choice.AddedGrants = append(choice.AddedGrants, grantLabel(grant))
					}
				}
				for _, host := range m.OutboundHosts {
					if !slices.Contains(old.OutboundHosts, host) {
						choice.AddedHosts = append(choice.AddedHosts, host)
					}
				}
				choice.SettingsChange = !reflect.DeepEqual(old.Settings, m.Settings)
			}
			c.Releases = append(c.Releases, choice)
		}
	} else {
		items, err := s.themes.List()
		if err != nil {
			return Snapshot{}, err
		}
		active := s.themes.Active().Slug
		v.ActiveTheme = active
		for _, item := range items {
			c := add(item.Slug, item.Name, item.Description)
			c.Installed, c.Active, c.Current = true, item.Slug == active, item.Version
			c.Dependency = s.themes.DependencyStatus(item)
			if c.Dependency.Error != "" {
				c.Error = c.Dependency.Error
			}
			c.Author, c.License = item.Author, item.License
			if item.Cover != "" {
				c.CoverURL = "/api/v1/admin/themes/" + item.Slug + "/cover"
			}
			c.State, err = s.themes.VersionState(item.Slug)
			if err != nil {
				c.Error = "主题版本记录不可用"
			}
			if c.Active {
				c.ConfigURL = "/admin/appearance/"
			}
		}
		for _, release := range v.Market.Themes {
			if !inChannel(release.Manifest.Version, release.Release, channel) {
				continue
			}
			m := release.Manifest
			c := add(m.Slug, m.Name, m.Description)
			if c.Author == "" {
				c.Author, c.License = m.Author, m.License
			}
			if c.CoverURL == "" && release.Cover != nil {
				c.CoverURL = "/api/v1/admin/themes/" + m.Slug + "/cover?digest=" + release.Cover.SHA256
			}
			dependency := s.themes.DependencyStatus(m)
			if !c.Installed {
				c.Dependency = dependency
			}
			choice := makeChoice(m.Version, m.NewPress, release.SHA256, release.Size, release.Notes, release.Release, *c, v.Market.Expires)
			if dependency.Error != "" {
				choice.Allowed = false
				choice.Reason = dependency.Error
			}
			c.Releases = append(c.Releases, choice)
		}
	}
	for i := range v.Cards {
		c := &v.Cards[i]
		if c.Name == "" {
			c.Name = c.Slug
		}
		sort.SliceStable(c.Releases, func(i, j int) bool {
			cmp, _ := version.Compare(c.Releases[i].Version, c.Releases[j].Version)
			return cmp > 0
		})
		if len(c.Releases) > 0 {
			c.Latest = c.Releases[0].Version
		}
		for _, release := range c.Releases {
			if release.Allowed && release.Action == "update" {
				c.HasUpdate = true
			}
		}
		if c.Installed {
			v.Installed++
		}
		if c.HasUpdate {
			v.Updates++
		}
	}
	sort.SliceStable(v.Cards, func(i, j int) bool {
		a, b := v.Cards[i], v.Cards[j]
		if a.HasUpdate != b.HasUpdate {
			return a.HasUpdate
		}
		if a.Installed != b.Installed {
			return a.Installed
		}
		return a.Slug < b.Slug
	})
	return v, nil
}

func inChannel(value string, meta *extension.Metadata, channel string) bool {
	if channel == "preview" {
		return true
	}
	if meta != nil {
		return meta.Channel == "stable"
	}
	v, err := version.Parse(value)
	return err == nil && v.Pre == ""
}

func makeChoice(value, requires, digest string, size int64, notes string, meta *extension.Metadata, card Card, expires time.Time) Choice {
	c := Choice{Version: value, Digest: digest, Size: size, Notes: notes, Requires: requires, Release: meta, Action: "install"}
	switch {
	case card.Error != "":
		c.Reason = "请先修复当前安装"
	case card.Builtin && card.Installed:
		c.Reason = "内置功能随 NewPress 更新"
	case !expires.After(time.Now()):
		c.Reason = "请先检查更新"
	default:
		if !card.Builtin {
			if err := version.Compatible(requires); err != nil {
				c.Reason = err.Error()
				return c
			}
		}
		if card.Installed {
			c.Action = "update"
			comparison, err := version.Compare(value, card.Current)
			switch {
			case err != nil:
				c.Reason = "当前版本不是可比较的语义版本"
			case comparison == 0:
				c.Reason = "当前已安装"
			case comparison < 0:
				c.Reason = "早于当前版本"
			case meta == nil || !meta.DataCompatible:
				c.Reason = "未声明数据兼容，需离线升级"
			default:
				c.Allowed = true
			}
		} else {
			c.Allowed = true
		}
	}
	return c
}

func (s *Service) Start(request Request) (Job, error) {
	if !validKind(request.Kind) || (request.Channel != "stable" && request.Channel != "preview") {
		return Job{}, errors.New("扩展类型或通道无效")
	}
	// Fast check first; never block a state poll on filesystem preparation.
	s.mu.Lock()
	if s.closed || s.job.Busy() {
		s.mu.Unlock()
		return Job{}, extension.ErrBusy
	}
	s.mu.Unlock()
	if err := s.validate(request); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.job.Busy() {
		return Job{}, extension.ErrBusy
	}
	now := time.Now().UTC()
	job := Job{ID: rand.Text(), Kind: request.Kind, Action: request.Action, Slug: request.Slug, Target: request.Version, Phase: "queued", StartedAt: now, UpdatedAt: now, Message: "正在准备任务"}
	if err := extension.WriteJSON(s.stateFile, job); err != nil {
		return Job{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	s.job, s.cancel, s.persisted, s.persistenceErr = job, cancel, now, nil
	s.wg.Add(1)
	go s.run(ctx, request)
	return job, nil
}

func (s *Service) validate(request Request) error {
	lifecycle := request.Kind == "theme" && (request.Action == "activate" || request.Action == "uninstall")
	if !lifecycle && request.ExpectedActive != "" {
		return errors.New("此操作不接受活动主题参数")
	}
	if request.Action == "check" {
		if request.Slug != "" || request.Version != "" || request.Digest != "" || request.ExpectedVersion != "" || request.Revision != "" {
			return errors.New("检查更新不能携带安装参数")
		}
		return nil
	}
	if packagefs.ValidateSlug(request.Slug) != nil || !lifecycle && !slices.Contains([]string{"install", "update", "rollback"}, request.Action) {
		return errors.New("扩展操作无效")
	}
	snapshot, err := s.Snapshot(request.Kind, request.Channel)
	if err != nil {
		return err
	}
	for _, card := range snapshot.Cards {
		if card.Slug != request.Slug {
			continue
		}
		if request.Action == "install" {
			if card.Installed || request.ExpectedVersion != "" || request.Revision != "" {
				return extension.ErrConflict
			}
		} else if !card.Installed || card.Current != request.ExpectedVersion || card.State.Revision != request.Revision || request.Revision == "" {
			return extension.ErrConflict
		}
		if lifecycle {
			if request.Digest != "" || request.Version != "" || request.ExpectedActive == "" || request.ExpectedActive != snapshot.ActiveTheme {
				return extension.ErrConflict
			}
			if card.Active {
				return errors.New("当前正在使用此主题，请先切换到其他主题")
			}
			if card.Error != "" {
				return errors.New(card.Error)
			}
			return nil
		}
		if request.Action == "rollback" {
			if card.Error != "" || card.Builtin || card.State.Previous == "" || card.State.Previous != request.Version || request.Digest != "" {
				return extension.ErrConflict
			}
			return nil
		}
		for _, choice := range card.Releases {
			if choice.Version == request.Version && choice.Digest == request.Digest && choice.Action == request.Action {
				if !choice.Allowed {
					return errors.New(choice.Reason)
				}
				return nil
			}
		}
	}
	return extension.ErrConflict
}

func (s *Service) report(phase string, received, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.job.Phase != phase
	s.job.Phase, s.job.UpdatedAt = phase, time.Now().UTC()
	if total > 0 {
		s.job.Downloaded, s.job.Total = received, total
	}
	s.job.Message = phaseMessage(phase)
	if changed || time.Since(s.persisted) >= 500*time.Millisecond {
		if err := extension.WriteJSON(s.stateFile, s.job); err != nil {
			s.persistenceErr = err
			s.cancel()
		}
		s.persisted = time.Now()
	}
}

func phaseMessage(phase string) string {
	switch phase {
	case "checking":
		return "正在连接官方仓库并校验目录"
	case "downloading":
		return "正在下载扩展包"
	case "verifying":
		return "正在核对文件大小与 SHA-256"
	case "validating":
		return "正在验证运行时、模板和版本兼容性"
	case "preparing":
		return "正在保存数据与回退快照"
	case "switching":
		return "正在提交版本切换"
	default:
		return "正在处理"
	}
}

func (s *Service) run(ctx context.Context, request Request) {
	defer s.wg.Done()
	var err error
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("extension update panic", "panic", recovered)
			err = errors.New("扩展任务异常中断，请核对当前版本")
		}
		s.finish(err)
	}()
	switch request.Action {
	case "check":
		s.report("checking", 0, 0)
		err = s.marketFor(request.Kind).Refresh(ctx)
	case "install":
		if request.Kind == "plugin" {
			err = s.market.InstallWithProgress(ctx, request.Slug, request.Digest, s.report)
		} else {
			err = s.marketFor("theme").InstallTheme(ctx, s.themes, request.Slug, request.Digest, s.report)
		}
	case "update":
		if request.Kind == "plugin" {
			err = s.market.Update(ctx, request.Slug, request.Digest, request.ExpectedVersion, request.Revision, s.report)
		} else {
			err = s.marketFor("theme").UpdateTheme(ctx, s.themes, request.Slug, request.Digest, request.ExpectedVersion, request.Revision, s.report)
		}
	case "activate":
		err = s.themes.Activate(ctx, request.Slug, request.ExpectedVersion, request.Revision, request.ExpectedActive, s.report)
	case "uninstall":
		err = s.themes.Uninstall(ctx, request.Slug, request.ExpectedVersion, request.Revision, request.ExpectedActive, s.report)
	case "rollback":
		if request.Kind == "plugin" {
			err = s.plugins.Rollback(ctx, request.Slug, request.ExpectedVersion, request.Revision, request.Version, s.report)
		} else {
			err = s.themes.Rollback(ctx, request.Slug, request.ExpectedVersion, request.Revision, request.Version, s.report)
		}
	}
}

func (s *Service) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.persistenceErr != nil {
		err = errors.New("任务状态无法保存，已中止更新，请检查存储后核对当前版本")
	}
	s.job.UpdatedAt = time.Now().UTC()
	switch {
	case errors.Is(err, context.Canceled):
		s.job.Phase, s.job.Message = "cancelled", "任务已取消，当前安装保持原状"
	case err != nil:
		slog.Warn("extension update failed", "kind", s.job.Kind, "slug", s.job.Slug, "error", err)
		s.job.Phase = "failed"
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			s.job.Message = "文件操作失败，请检查服务日志并核对当前版本；恢复快照已保留"
		} else {
			s.job.Message = err.Error()
		}
	default:
		s.job.Phase, s.job.Message = "succeeded", "操作已完成"
		if s.job.Action == "check" {
			s.job.Message = "检查已完成，版本列表已更新"
		} else if s.job.Kind == "plugin" {
			if state, err := s.plugins.VersionState(s.job.Slug); err == nil && state.ReviewRequired {
				s.job.ReviewRequired = true
				s.job.Message = "更新已完成，插件暂时停用；请在设置页重新确认配置与权限"
			}
		}
	}
	if len(s.job.Message) > 1000 {
		s.job.Message = "扩展操作失败，请检查服务日志并核对当前版本"
	}
	if saveErr := extension.WriteJSON(s.stateFile, s.job); saveErr != nil {
		// The operation may already have committed. Surface the missing
		// receipt without claiming that the installation was rolled back.
		s.persistenceErr = saveErr
		s.job.Phase = "failed"
		s.job.Message = "操作已结束，但任务结果无法保存；请检查存储并核对当前版本后再操作"
		slog.Error("save extension result", "error", saveErr)
	}
}

func (s *Service) Cancel(kind, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.Kind != kind || id == "" || s.job.ID != id {
		return extension.ErrConflict
	}
	if !s.job.Busy() || s.job.Phase == "switching" {
		return errors.New("任务已经提交切换或结束，请等待结果")
	}
	s.cancel()
	s.job.Phase, s.job.Message = "cancelling", "正在取消，请等待当前步骤结束"
	s.job.UpdatedAt = time.Now().UTC()
	return extension.WriteJSON(s.stateFile, s.job)
}

func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func grantLabel(grant string) string {
	labels := map[string]string{"content.read": "读取公开内容", "settings.read": "读取插件设置", "settings.write": "保存插件设置", "cache": "读写插件缓存", "http.outbound": "访问外部服务", "render.fragment": "展示页面内容"}
	if label := labels[grant]; label != "" {
		return label
	}
	return grant
}
