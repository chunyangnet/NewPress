package update

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/packagefs"
)

type Manager struct {
	root, current, themes string
	managed               bool
	databaseBackup        bool
	siteURL               string
	client                *giteeClient
	mu                    sync.Mutex
	cancel                context.CancelFunc
	wg                    sync.WaitGroup
	closed                bool
}

func New(root, current, themes string, managed bool, options ...func(*Manager)) (*Manager, error) {
	for _, dir := range []string{root, filepath.Join(root, "operation"), filepath.Join(root, "supervisor")} {
		if err := packagefs.MkdirAllNoSymlink(dir, 0700); err != nil {
			return nil, err
		}
	}
	m := &Manager{root: root, current: current, themes: themes, managed: managed, client: newGitee()}
	for _, option := range options {
		option(m)
	}
	if err := m.recoverPreparation(); err != nil {
		return nil, err
	}
	return m, nil
}

// The operation lock proves that a preparation worker no longer owns this
// receipt. Cutover records and phases remain the supervisor's responsibility.
func (m *Manager) recoverPreparation() error {
	lock, err := dirlock.Acquire(filepath.Join(m.root, "operation"))
	if errors.Is(err, dirlock.ErrBusy) {
		return nil
	}
	if err != nil {
		return errors.New("无法核对更新任务是否仍在运行")
	}
	defer lock.Close()
	for _, name := range []string{"request.json", "transaction.json"} {
		if _, err := os.Lstat(filepath.Join(m.root, name)); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("无法核对更新切换记录")
		}
	}
	state, err := m.state()
	if err != nil {
		return errors.New("无法读取更新任务状态")
	}
	switch state.Phase {
	case "checking", "downloading", "verifying", "staging":
		state.step("failed", state.Percent, "上次更新任务被中断，当前版本未切换，请重新检查更新")
		if err := m.save(state); err != nil {
			return errors.New("无法保存已中断的更新任务")
		}
	}
	return nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
func (m *Manager) state() (State, error) {
	s := State{Phase: "idle", Logs: []Log{}}
	err := readJSON(filepath.Join(m.root, "state.json"), &s)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return s, err
}
func (m *Manager) save(s State) error { return writeJSON(filepath.Join(m.root, "state.json"), s) }

func (m *Manager) Snapshot() (Snapshot, error) {
	s, envToken, err := loadSource(m.root)
	if err != nil {
		return Snapshot{}, err
	}
	state, err := m.state()
	if err != nil {
		return Snapshot{}, errors.New("无法读取更新任务状态")
	}
	v := Snapshot{Current: m.current, Platform: runtime.GOOS + "/" + runtime.GOARCH, Environment: "本机进程", Managed: m.managed, Configured: s.validate() == nil, TokenSet: s.Token != "", EnvironmentToken: envToken, State: state}
	if _, err := os.Stat("/.dockerenv"); err == nil || os.Getenv("NEWPRESS_CONTAINER") == "true" {
		v.Environment = "Docker 容器"
	}
	_, keyErr := publisherKey(s)
	v.TrustedPublisher = keyErr == nil
	v.Policy, err = m.policy()
	if err != nil {
		return Snapshot{}, err
	}
	v.Discovery = m.discovery()
	v.Discovery.Interval = v.Policy.interval().String()
	if a, ok := m.cached(s); ok {
		v.Available = &a.Manifest
		v.Revision = a.revision()
	}
	s.Token = ""
	v.Source = s
	return v, nil
}

func (m *Manager) lock() (*dirlock.Set, error) {
	l, err := dirlock.Acquire(filepath.Join(m.root, "operation"))
	if err != nil {
		return nil, ErrBusy
	}
	s, err := m.state()
	if err != nil {
		l.Close()
		return nil, errors.New("无法读取更新任务状态")
	}
	if s.BlocksChanges() {
		l.Close()
		return nil, ErrBusy
	}
	return l, nil
}

func (m *Manager) Configure(s Source, clearToken bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("更新服务已停止")
	}
	l, err := m.lock()
	if err != nil {
		return err
	}
	defer l.Close()
	old, envToken, err := loadSource(m.root)
	if err != nil {
		return err
	}
	if s.Official {
		// Never copy a retained private credential into the public configuration.
		s = officialSource()
	} else {
		if s.Token == "" && !clearToken {
			s.Token = old.Token
		}
		if clearToken {
			s.Token = ""
		}
		// A retained credential must never silently follow a change of repository.
		if old.Owner != "" && (old.Owner != s.Owner || old.Repo != s.Repo) && s.Token == old.Token && !envToken {
			return errors.New("更换仓库时请重新填写访问令牌")
		}
		validate := s
		if envToken {
			validate.Token = old.Token
		}
		if clearToken && !envToken {
			validate.Token = "unconfigured"
		}
		if err = validate.validate(); err != nil {
			return err
		}
		if envToken {
			s.Token = ""
		}
	}
	if err = writeJSON(filepath.Join(m.root, "source.json"), s); err != nil {
		return errors.New("无法保存更新源配置")
	}
	if err := os.Remove(filepath.Join(m.root, "discovery.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("无法重置检查状态")
	}
	return m.invalidate()
}

func (m *Manager) launch(phase, ticket string, prepare func() error, work func(context.Context, *State) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("更新服务已停止")
	}
	l, err := m.lock()
	if err != nil {
		return err
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			l.Close()
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	s := State{ID: randomID(), StartedAt: time.Now().UTC(), Logs: []Log{}}
	s.step(phase, 0, "任务已创建")
	if ticket != "" {
		if err = writeJSON(filepath.Join(m.root, "ticket.json"), progressTicket{digest([]byte(ticket)), s.ID, time.Now().Add(30 * time.Minute)}); err != nil {
			cancel()
			l.Close()
			return errors.New("无法保存进度凭据")
		}
	}
	if err = m.save(s); err != nil {
		cancel()
		l.Close()
		return errors.New("无法保存更新任务")
	}
	m.cancel = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer l.Close()
		defer cancel()
		if err := work(ctx, &s); err != nil {
			if ctx.Err() != nil {
				s.step("cancelled", s.Percent, "任务已取消或超时，当前版本未切换")
			} else {
				s.step("failed", s.Percent, err.Error())
			}
			_ = m.save(s)
		}
		m.mu.Lock()
		m.cancel = nil
		m.mu.Unlock()
	}()
	return nil
}

func (m *Manager) Check() error {
	return m.launch("checking", "", nil, func(ctx context.Context, s *State) error {
		s.step("checking", 5, "连接发布源，读取并验证签名清单")
		if err := m.save(*s); err != nil {
			return errors.New("无法保存任务状态")
		}
		source, _, err := loadSource(m.root)
		if err != nil {
			return err
		}
		a, err := m.refreshCatalog(ctx, source)
		if err != nil {
			return err
		}
		s.Target = a.Manifest.Version
		if newer(a.Manifest.Version, m.current) {
			s.step("available", 0, "发现可用版本 "+a.Manifest.Version)
		} else {
			s.step("current", 100, "当前已是此更新源的最新版本")
		}
		if err = m.save(*s); err != nil {
			return errors.New("无法保存检查结果")
		}
		return nil
	})
}

type progressTicket struct {
	Hash    string    `json:"hash"`
	Job     string    `json:"job"`
	Expires time.Time `json:"expires"`
}

func (m *Manager) Start(version string, backupConfirmed bool, revision string) (string, error) {
	if !m.managed {
		return "", errors.New("请先使用 newpress run 启动托管模式，以启用在线切换")
	}
	if !backupConfirmed {
		return "", errors.New("请先确认已有可恢复的站点和数据库备份")
	}
	var source Source
	var expected Available
	ticket := randomID() + randomID()
	prepare := func() error {
		var err error
		source, _, err = loadSource(m.root)
		if err != nil {
			return err
		}
		if err = source.validate(); err != nil {
			return err
		}
		var ok bool
		expected, ok = m.cached(source)
		if !ok || expected.Manifest.Version != version || expected.revision() != revision {
			return errors.New("安装授权已失效，请重新检查并确认该发布")
		}
		if m.databaseBackup {
			if err := verifyBackupReceipt(m.siteURL, m.current, version); err != nil {
				return err
			}
		}
		return m.invalidate()
	}
	err := m.launch("downloading", ticket, prepare, func(ctx context.Context, s *State) error {
		revalidate := func() (Available, error) {
			fresh, err := m.discover(ctx, source)
			if err != nil {
				return Available{}, err
			}
			if fresh.revision() != revision {
				return Available{}, errors.New("发布已经变化，请重新检查并确认安装")
			}
			return fresh, nil
		}
		available, err := revalidate()
		if err != nil {
			return err
		}
		a, err := available.Manifest.artifact()
		if err != nil {
			return err
		}
		s.Target = version
		s.Total = a.Size
		s.step("downloading", 5, "开始下载更新包，已锁定仓库提交")
		if err := m.save(*s); err != nil {
			return errors.New("无法保存下载状态")
		}
		f, err := os.CreateTemp(m.root, "download-*.zip")
		if err != nil {
			return errors.New("无法创建更新缓存")
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if err = m.downloadArchive(ctx, source, available, a, f, s); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return errors.New("更新缓存写入失败")
		}
		if err = f.Close(); err != nil {
			return errors.New("更新缓存关闭失败")
		}
		s.step("verifying", 68, "下载完成，正在验证 SHA-256 摘要")
		if err = m.save(*s); err != nil {
			return errors.New("无法保存校验状态")
		}
		s.step("staging", 74, "校验通过，正在准备程序和主题快照")
		if err = m.save(*s); err != nil {
			return errors.New("无法保存准备状态")
		}
		release, err := stageArchive(ctx, f.Name(), m.root, s.ID, m.themes)
		if err != nil {
			return errors.New("更新包准备失败，请检查包内路径、大小限制、主题目录权限和可用空间")
		}
		release.Version = version
		queued := false
		defer func() {
			if !queued {
				_ = os.RemoveAll(filepath.Join(m.root, "releases", release.ID))
			}
		}()
		if _, err = revalidate(); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = writeJSON(filepath.Join(m.root, "releases", release.ID, "release.json"), release); err != nil {
			return errors.New("无法保存发布记录")
		}
		s.step("queued", 82, "更新包已就绪，等待托管进程切换；完成后需要重新登录")
		if err = m.save(*s); err != nil {
			return errors.New("无法保存切换状态")
		}
		if err = writeJSON(filepath.Join(m.root, "request.json"), Transaction{Generation: LauncherGeneration, Expires: available.Manifest.Expires, Candidate: release, State: *s}); err != nil {
			return errors.New("无法提交更新切换请求")
		}
		queued = true
		return nil
	})
	return ticket, err
}

type downloadProgress struct {
	m    *Manager
	s    *State
	last time.Time
}

func (p *downloadProgress) Write(b []byte) (int, error) {
	p.s.Downloaded += int64(len(b))
	p.s.Percent = 5 + int(min(p.s.Downloaded, p.s.Total)*60/p.s.Total)
	if time.Since(p.last) >= 200*time.Millisecond {
		p.s.UpdatedAt = time.Now().UTC()
		if err := p.m.save(*p.s); err != nil {
			return 0, err
		}
		p.last = time.Now()
	}
	return len(b), nil
}

func (m *Manager) Cancel() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.state()
	if err != nil {
		return err
	}
	if m.cancel == nil || s.Phase == "queued" || s.Phase == "stopping" || s.Phase == "backup" || s.Phase == "starting" || s.Phase == "observing" {
		return errors.New("当前阶段不能取消")
	}
	m.cancel()
	return nil
}
func (m *Manager) Progress(ticket string) (State, bool) {
	if len(ticket) != 64 {
		return State{}, false
	}
	var t progressTicket
	if readJSON(filepath.Join(m.root, "ticket.json"), &t) != nil || time.Now().After(t.Expires) || subtle.ConstantTimeCompare([]byte(digest([]byte(ticket))), []byte(t.Hash)) != 1 {
		return State{}, false
	}
	s, err := m.state()
	return s, err == nil && s.ID == t.Job
}
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
