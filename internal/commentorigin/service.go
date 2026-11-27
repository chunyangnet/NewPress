package commentorigin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/safehttp"
)

var (
	ErrConflict   = errors.New("配置已被另一页面修改，请保留输入并刷新核对")
	errDailyLimit = errors.New("今日查询额度已用完")
)

const maxSettingsBytes = 32 << 10

type Record struct {
	ID          int64     `json:"id"`
	IP          string    `json:"ip,omitempty"`
	UA          string    `json:"ua,omitempty"`
	Device      Device    `json:"device"`
	Location    Location  `json:"location"`
	Created     time.Time `json:"created"`
	Checked     time.Time `json:"checked"`
	Next        time.Time `json:"next"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	Error       string    `json:"error,omitempty"`
	Source      string    `json:"source,omitempty"`
	SourceIssue string    `json:"source_issue,omitempty"`
}

// Source is supplied by the server's proxy trust resolver, never by a public
// form. An unresolved source records the reason and UA, but no proxy IP.
type Source struct {
	IP, Method, Issue string
}
type Badges struct {
	Location  string `json:"location,omitempty"`
	Precision string `json:"precision,omitempty"`
	Flag      string `json:"flag,omitempty"`
	Device    string `json:"device,omitempty"`
	Kind      string `json:"kind,omitempty"`
}
type Status struct {
	Settings    Settings `json:"settings"`
	KeySaved    bool     `json:"key_saved"`
	Enabled     bool     `json:"enabled"`
	Records     int      `json:"records"`
	Pending     int      `json:"pending"`
	Resolved    int      `json:"resolved"`
	Failed      int      `json:"failed"`
	Unavailable int      `json:"unavailable"`
	LastError   string   `json:"last_error,omitempty"`
}
type job struct {
	id         int64
	generation uint64
}
type cacheEntry struct {
	Location Location  `json:"location"`
	Expires  time.Time `json:"expires"`
}
type budget struct {
	Day      string `json:"day"`
	Requests int    `json:"requests"`
}

type Service struct {
	// locationFunc resolves the site timezone for the operator-visible daily
	// query budget. It is read on every use so settings changes apply live.
	locationFunc    func() *time.Location
	manager         *plugin.Manager
	lookup          Lookup
	queue           chan job
	wake            chan struct{}
	mu              sync.Mutex
	lastError       string
	errorGeneration uint64
	errorVersion    int64
	lookupGate      chan struct{}
	lastLookup      time.Time
	commentExists   CommentExists
	pruneAfter      string // owned by Run
}

// SetLocationFunc binds daily query-budget resets to 站点设置 → 站点时区.
func (s *Service) SetLocationFunc(location func() *time.Location) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locationFunc = location
}

func (s *Service) siteZone() *time.Location {
	if s == nil {
		return time.UTC
	}
	s.mu.Lock()
	locationFunc := s.locationFunc
	s.mu.Unlock()
	// Site settings have their own lock. Invoke the captured callback without
	// holding either the service mutex or the plugin manager's WithData lock.
	if locationFunc != nil {
		if location := locationFunc(); location != nil && location != time.Local && location.String() != "Local" {
			return location
		}
	}
	return time.UTC
}

// reserveBudget runs under WithData so manual queries and worker jobs share one
// reservation. The caller captures time and the site timezone before taking that
// lock; elapsed-time retention, cache expiry and retry deadlines remain absolute.
func reserveBudget(dir string, now time.Time, location *time.Location, cost, limit int) error {
	path := filepath.Join(dir, "budget.json")
	b, err := plugin.ReadPrivate[budget](path, 1024)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	day := now.In(location).Format("2006-01-02")
	if b.Day != day {
		b = budget{Day: day}
	}
	if b.Requests+cost > limit {
		return errDailyLimit
	}
	b.Requests += cost
	return plugin.WritePrivate(path, b)
}

func New(manager *plugin.Manager, exists ...CommentExists) *Service {
	s := &Service{manager: manager, lookup: NewLookup(), queue: make(chan job, 256), wake: make(chan struct{}, 1), lookupGate: make(chan struct{}, 1)}
	if len(exists) > 0 {
		s.commentExists = exists[0]
	}
	return s
}
func settingsAt(dir string) (Settings, error) {
	s, err := plugin.ReadPrivate[Settings](filepath.Join(dir, "settings.json"), maxSettingsBytes)
	if os.IsNotExist(err) {
		return Defaults(), nil
	}
	if err != nil {
		return s, err
	}
	return s, validate(s)
}
func recordPath(dir string, id int64) string {
	return filepath.Join(dir, "records", strconv.FormatInt(id, 10)+".json")
}

func (s *Service) Settings() (Settings, error) {
	var config Settings
	err := s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error { var err error; config, err = settingsAt(dir); return err })
	return config, err
}

func (s *Service) Save(config Settings, clearKey bool) (Settings, error) {
	var saved Settings
	err := s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error {
		current, err := settingsAt(dir)
		if err != nil {
			return err
		}
		if config.Version != current.Version {
			return ErrConflict
		}
		if config.Key == "" && !clearKey && config.Provider == current.Provider && (config.Provider != "custom" || config.Endpoint == current.Endpoint) {
			config.Key = current.Key
		}
		if clearKey {
			config.Key = ""
		}
		if err = validate(config); err != nil {
			return err
		}
		config.Version++
		// Include retained keys, normalized fields and the new version in the
		// same serialized limit used when loading settings after a restart.
		encoded, err := json.Marshal(config)
		if err != nil {
			return errors.New("评论来源配置无法编码")
		}
		if len(encoded) > maxSettingsBytes {
			return errors.New("评论来源配置总大小不能超过 32 KiB，请缩短配置内容")
		}
		if err = plugin.WritePrivate(filepath.Join(dir, "settings.json"), config); err != nil {
			return err
		}
		saved = config
		return nil
	})
	if err == nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	saved.Key = ""
	return saved, err
}

func (s *Service) Capture(id int64, ip, ua string) error {
	return s.CaptureSource(id, Source{IP: ip, Method: "tcp"}, ua)
}

func (s *Service) CaptureSource(id int64, source Source, ua string) error {
	if id < 1 {
		return errors.New("invalid comment id")
	}
	a, err := netip.ParseAddr(source.IP)
	if err != nil && source.Issue == "" {
		return nil
	}
	ip := ""
	if source.Issue == "" {
		ip = a.Unmap().String()
	}
	if len(ua) > 512 {
		ua = ua[:512]
	}
	ua = strings.ToValidUTF8(ua, "")
	var queued job
	err = s.manager.WithData(plugin.CommentOrigin, true, 0, func(dir string, generation uint64) error {
		if _, err := os.Lstat(recordPath(dir, id)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := settingsAt(dir); err != nil {
			return err
		}
		if err := packagefs.MkdirAllNoSymlink(filepath.Join(dir, "records"), 0o700); err != nil {
			return err
		}
		record := Record{ID: id, IP: ip, UA: ua, Device: ParseUA(ua), Created: time.Now().UTC(), Status: "pending", Source: source.Method, SourceIssue: source.Issue}
		if source.Issue != "" {
			record.Status = "unavailable"
		} else if !safehttp.PublicIP(a) {
			record.Status = "local"
		}
		if err := plugin.WritePrivate(recordPath(dir, id), record); err != nil {
			return err
		}
		queued = job{id, generation}
		return nil
	})
	if errors.Is(err, plugin.ErrInactive) {
		return nil
	}
	if err == nil && queued.id > 0 {
		select {
		case s.queue <- queued:
		default:
		}
	}
	return err
}

func (s *Service) Badges(id int64, roles []string) Badges {
	result := Badges{}
	_ = s.manager.WithData(plugin.CommentOrigin, true, 0, func(dir string, _ uint64) error {
		config, err := settingsAt(dir)
		if err != nil {
			return err
		}
		location, device := config.Allowed(roles)
		if !location && !device {
			return nil
		}
		record, err := plugin.ReadPrivate[Record](recordPath(dir, id), 16<<10)
		if err != nil {
			return err
		}
		if location && record.Status == "resolved" {
			result.Location = record.Location.Label()
			result.Precision = record.Location.Precision
			if result.Location != "" {
				result.Flag = flagCode(config.FlagMode, record.Location.CountryCode)
			}
		}
		if device {
			result.Device = record.Device.Label(config.Browser)
			result.Kind = record.Device.Kind
		}
		return nil
	})
	return result
}

func (s *Service) Status() (Status, error) {
	view := Status{}
	err := s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, generation uint64) error {
		config, err := settingsAt(dir)
		if err != nil {
			return err
		}
		view.KeySaved = config.Key != ""
		config.Key = ""
		view.Settings = config
		s.mu.Lock()
		if s.errorGeneration == generation && s.errorVersion == config.Version {
			view.LastError = s.lastError
		}
		s.mu.Unlock()
		entries, err := os.ReadDir(filepath.Join(dir, "records"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			r, err := plugin.ReadPrivate[Record](filepath.Join(dir, "records", entry.Name()), 16<<10)
			if err != nil {
				continue
			}
			view.Records++
			switch r.Status {
			case "resolved":
				view.Resolved++
			case "pending":
				view.Pending++
			case "failed":
				view.Failed++
			case "unavailable":
				view.Unavailable++
			}
		}
		return nil
	})
	view.Enabled = s.manager.WithData(plugin.CommentOrigin, true, 0, func(string, uint64) error { return nil }) == nil
	return view, err
}

// Run has one bounded worker. Capture commits the source before enqueueing; the
// periodic scan recovers pending work after a restart or a saturated queue.
func (s *Service) Run(ctx context.Context) {
	scan := time.NewTicker(time.Minute)
	defer scan.Stop()
	s.pruneDeleted(ctx)
	s.scan()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			s.pruneDeleted(ctx)
			s.scan()
		case <-scan.C:
			s.pruneDeleted(ctx)
			s.scan()
		case j := <-s.queue:
			s.process(ctx, j)
		}
	}
}

func (s *Service) scan() {
	active := s.manager.WithData(plugin.CommentOrigin, true, 0, func(string, uint64) error { return nil }) == nil
	_ = s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, generation uint64) error {
		config, err := settingsAt(dir)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(dir, "records"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			p := filepath.Join(dir, "records", entry.Name())
			r, e := plugin.ReadPrivate[Record](p, 16<<10)
			if e != nil {
				continue
			}
			if now.Sub(r.Created) > time.Duration(config.RetentionDays)*24*time.Hour && (r.IP != "" || r.UA != "") {
				r.IP = ""
				r.UA = ""
				if r.Status == "pending" {
					r.Status = "expired"
				}
				if e = plugin.WritePrivate(p, r); e != nil {
					return e
				}
			}
			if active && r.Status == "pending" && r.IP != "" && !r.Next.After(now) {
				select {
				case s.queue <- job{r.ID, generation}:
				default:
				}
			}
		}
		cached, _ := os.ReadDir(filepath.Join(dir, "cache"))
		for _, entry := range cached {
			p := filepath.Join(dir, "cache", entry.Name())
			c, e := plugin.ReadPrivate[cacheEntry](p, 2048)
			if e == nil && !c.Expires.After(now) {
				_ = os.Remove(p)
			}
		}
		return nil
	})
}

func (s *Service) process(ctx context.Context, j job) {
	var r Record
	var config Settings
	var cached *Location
	cacheFile := ""
	now := time.Now().UTC()
	zone := s.siteZone()
	err := s.manager.WithData(plugin.CommentOrigin, true, j.generation, func(dir string, _ uint64) error {
		var err error
		config, err = settingsAt(dir)
		if err != nil {
			return err
		}
		r, err = plugin.ReadPrivate[Record](recordPath(dir, j.id), 16<<10)
		if err != nil {
			return err
		}
		if r.Status != "pending" || r.IP == "" || r.Next.After(now) {
			return plugin.ErrInactive
		}
		if now.Sub(r.Created) > time.Duration(config.RetentionDays)*24*time.Hour {
			r.IP = ""
			r.UA = ""
			r.Status = "expired"
			if err = plugin.WritePrivate(recordPath(dir, j.id), r); err != nil {
				return err
			}
			return plugin.ErrInactive
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s", config.Version, config.Provider, config.Endpoint, r.IP)))
		cacheFile = filepath.Join(dir, "cache", hex.EncodeToString(digest[:])+".json")
		if c, e := plugin.ReadPrivate[cacheEntry](cacheFile, 2048); e == nil && c.Expires.After(now) {
			v := c.Location
			cached = &v
			return nil
		}
		cost := 1
		if config.Fallback && config.Provider != "ipwhois" {
			cost = 2
		}
		if err = reserveBudget(dir, now, zone, cost, config.DailyLimit); err != nil {
			if errors.Is(err, errDailyLimit) {
				return fmt.Errorf("%w，等待次日恢复", err)
			}
			return err
		}
		r.Attempts++
		r.Next = now.Add(10 * time.Minute)
		return plugin.WritePrivate(recordPath(dir, j.id), r)
	})
	if err != nil {
		if !errors.Is(err, plugin.ErrInactive) && !os.IsNotExist(err) {
			s.setError(j.generation, config.Version, err.Error())
		}
		return
	}
	var location Location
	if cached != nil {
		location = *cached
	} else {
		lease, e := s.manager.ActiveContext(plugin.CommentOrigin, j.generation)
		if e != nil {
			return
		}
		queryContext, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(lease, cancel)
		defer stop()
		defer cancel()
		ctx = queryContext
		location, err = s.query(ctx, config, r.IP)
		if err != nil && ctx.Err() == nil && config.Fallback && config.Provider != "ipwhois" {
			fallback := config
			fallback.Provider = "ipwhois"
			location, err = s.query(ctx, fallback, r.IP)
		}
	}
	if ctx.Err() != nil {
		return
	}
	commitErr := s.manager.WithData(plugin.CommentOrigin, true, j.generation, func(dir string, _ uint64) error {
		current, e := settingsAt(dir)
		if e != nil {
			return e
		}
		if current.Version != config.Version {
			return plugin.ErrInactive
		}
		// The outbound lookup runs without the lifecycle lock. Re-read before
		// committing so retention, retry, or deletion cannot be undone by r.
		latest, e := plugin.ReadPrivate[Record](recordPath(dir, j.id), 16<<10)
		if os.IsNotExist(e) {
			return plugin.ErrInactive
		}
		if e != nil {
			return e
		}
		finished := time.Now().UTC()
		if finished.Sub(latest.Created) >= time.Duration(current.RetentionDays)*24*time.Hour {
			latest.IP, latest.UA = "", ""
			if latest.Status == "pending" {
				latest.Status = "expired"
			}
			if e := plugin.WritePrivate(recordPath(dir, j.id), latest); e != nil {
				return e
			}
			return plugin.ErrInactive
		}
		if latest.Status != "pending" || latest.IP != r.IP || !latest.Created.Equal(r.Created) || latest.Attempts != r.Attempts || !latest.Next.Equal(r.Next) {
			return plugin.ErrInactive
		}
		r = latest
		if err != nil {
			r.Status = "pending"
			r.Error = err.Error()
			if r.Attempts >= 3 {
				r.Status = "failed"
			}
			r.Next = finished.Add(time.Duration(r.Attempts) * 10 * time.Minute)
		} else {
			r.Location = location
			r.Checked = finished
			r.Status = "resolved"
			r.Error = ""
			// A cache hit must not renew the provider result's lifetime: busy
			// network exits otherwise keep the same city indefinitely.
			if cached == nil {
				if e = packagefs.MkdirAllNoSymlink(filepath.Join(dir, "cache"), 0o700); e != nil {
					return e
				}
				if e = plugin.WritePrivate(cacheFile, cacheEntry{location, finished.Add(time.Duration(config.CacheDays) * 24 * time.Hour)}); e != nil {
					return e
				}
			}
		}
		return plugin.WritePrivate(recordPath(dir, j.id), r)
	})
	if commitErr != nil && !errors.Is(commitErr, plugin.ErrInactive) {
		s.setError(j.generation, config.Version, "无法保存查询结果，请检查插件数据目录")
	} else if commitErr == nil {
		message := ""
		if err != nil {
			message = err.Error()
		}
		s.setError(j.generation, config.Version, message)
	}
}
func (s *Service) setError(generation uint64, version int64, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError, s.errorGeneration, s.errorVersion = message, generation, version
}

func (s *Service) PrivateRecord(id int64) (Record, error) {
	var record Record
	err := s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error {
		config, err := settingsAt(dir)
		if err != nil {
			return err
		}
		record, err = plugin.ReadPrivate[Record](recordPath(dir, id), 16<<10)
		if err != nil {
			return err
		}
		if time.Since(record.Created) > time.Duration(config.RetentionDays)*24*time.Hour {
			record.IP = ""
			record.UA = ""
			if record.Status == "pending" {
				record.Status = "expired"
			}
			return plugin.WritePrivate(recordPath(dir, id), record)
		}
		return nil
	})
	return record, err
}

func (s *Service) Notice() string {
	enabled := s.manager.WithData(plugin.CommentOrigin, true, 0, func(string, uint64) error { return nil }) == nil
	if !enabled {
		return ""
	}
	return "本评论区记录来源 IP 与客户端信息，并向配置的 IP 服务查询大致归属地；公开范围由站点设置决定，真实 IP 不公开。"
}

func (s *Service) Retry() error {
	err := s.manager.WithData(plugin.CommentOrigin, true, 0, func(dir string, _ uint64) error {
		config, err := settingsAt(dir)
		if err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(dir, "records"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			p := filepath.Join(dir, "records", entry.Name())
			r, e := plugin.ReadPrivate[Record](p, 16<<10)
			if e != nil {
				return e
			}
			if (r.Status == "pending" || r.Status == "failed") && r.IP != "" && time.Since(r.Created) < time.Duration(config.RetentionDays)*24*time.Hour {
				r.Status = "pending"
				r.Error = ""
				r.Attempts = 0
				r.Next = time.Time{}
				if err = plugin.WritePrivate(p, r); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return err
}

// Test uses saved settings, the same SSRF restrictions and the worker's quota.
func (s *Service) Test(ctx context.Context, ip string) (Location, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil || !safehttp.PublicIP(a) {
		return Location{}, errors.New("请输入有效公网 IPv4 或 IPv6")
	}
	var config Settings
	var generation uint64
	now := time.Now().UTC()
	zone := s.siteZone()
	// Test traffic also consumes quota; a serialized reserve is shared with jobs.
	err = s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, currentGeneration uint64) error {
		var e error
		config, e = settingsAt(dir)
		if e != nil {
			return e
		}
		generation = currentGeneration
		return reserveBudget(dir, now, zone, 1, config.DailyLimit)
	})
	if err != nil {
		return Location{}, err
	}
	lease, err := s.manager.InstalledContext(plugin.CommentOrigin, generation)
	if err != nil {
		return Location{}, err
	}
	queryContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lease, cancel)
	defer stop()
	defer cancel()
	location, err := s.query(queryContext, config, a.Unmap().String())
	if queryContext.Err() != nil {
		return Location{}, queryContext.Err()
	}
	return location, err
}

func (s *Service) query(ctx context.Context, config Settings, ip string) (Location, error) {
	select {
	case s.lookupGate <- struct{}{}:
	case <-ctx.Done():
		return Location{}, ctx.Err()
	}
	defer func() { <-s.lookupGate }()
	if delay := 2*time.Second - time.Since(s.lastLookup); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return Location{}, ctx.Err()
		case <-timer.C:
		}
	}
	s.lastLookup = time.Now()
	return s.lookup(ctx, config, ip)
}
