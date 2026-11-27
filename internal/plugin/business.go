package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/safehttp"
)

const businessBytes = 64 << 10

var errBusinessCallTimeout = errors.New("plugin execution deadline exceeded")

type Setting struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Help    string `json:"help,omitempty"`
	Type    string `json:"type"`
	Default any    `json:"default"`
	Min     int    `json:"min,omitempty"`
	Max     int    `json:"max,omitempty"`
}
type Configuration struct {
	Version int64          `json:"version"`
	Values  map[string]any `json:"values"`
	Grants  []string       `json:"grants"`
}
type ExecutionStatus struct {
	Calls          uint64
	Failures       uint64
	Consecutive    int
	LastError      string
	LastDurationMS int64
	LastRun        time.Time
	BlockedUntil   time.Time
}
type Fragment struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Tone  string `json:"tone"`
}
type BusinessResult struct {
	Fragment *Fragment       `json:"fragment,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}
type BusinessInput struct {
	Schema  int             `json:"schema"`
	Event   string          `json:"event"`
	Now     int64           `json:"now"`
	Post    any             `json:"post,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
type ContentReader func(context.Context, int64) (any, error)

// TaskEnqueuer is a host-bound callback; the guest supplies neither actor nor epoch.
type TaskEnqueuer func(context.Context, string, json.RawMessage) (any, error)

func (m *Manager) DispatchAdmin(ctx context.Context, slug string, in BusinessInput, read ContentReader, enqueue TaskEnqueuer) (BusinessResult, error) {
	if in.Event != "admin.action" {
		return BusinessResult{}, ErrInactive
	}
	return m.dispatch(ctx, slug, in, read, "", enqueue)
}

var ErrConfigurationConflict = errors.New("插件配置已变化，请刷新核对后重试")

func validateBusinessManifest(m Manifest) error {
	if err := validateSettingsRegistration(m); err != nil {
		return err
	}
	if len(m.Settings) > 32 {
		return errors.New("too many plugin settings")
	}
	seen := map[string]bool{}
	for _, s := range m.Settings {
		if !exportNamePattern.MatchString(s.Key) || seen[s.Key] || !validManifestText(s.Label, 80, true) || !validManifestText(s.Help, 400, false) || !slices.Contains([]string{"text", "number", "boolean"}, s.Type) || s.Min < 0 || s.Max < s.Min || s.Max > 1000000 {
			return errors.New("invalid plugin setting schema")
		}
		seen[s.Key] = true
		if s.Type == "text" && (s.Max < 1 || s.Max > 4000) {
			return errors.New("text settings require max between 1 and 4000")
		}
		if e := validateValue(s, s.Default); e != nil {
			return e
		}
	}
	if len(m.Hooks) == 0 {
		if len(m.Settings) > 0 {
			return errors.New("settings require business hooks")
		}
		return nil
	}
	for hook, exports := range m.Hooks {
		if (hook != "content.render" && hook != "admin.action" && hook != "task.execute.v1" && hook != "event.deliver.v1") || len(exports) != 1 {
			return errors.New("unsupported business hook or export count")
		}
	}
	for _, cap := range m.Capabilities {
		if cap == "content.write" && len(m.Hooks["task.execute.v1"])+len(m.Hooks["event.deliver.v1"]) == 0 {
			return fmt.Errorf("capability %s requires a durable background hook", cap)
		}
	}
	if len(m.Hooks["content.render"]) > 0 && !slices.Contains(m.Capabilities, "render.fragment") {
		return errors.New("content.render requires render.fragment")
	}
	if m.Admin != nil && len(m.Admin.Navigation) > 0 {
		return errors.New("custom admin navigation is not supported; use the generated plugin settings page")
	}
	return nil
}
func validateValue(s Setting, v any) error {
	switch s.Type {
	case "boolean":
		if _, ok := v.(bool); ok {
			return nil
		}
	case "text":
		if t, ok := v.(string); ok && validManifestText(t, max(1, min(s.Max, 4000)), false) {
			return nil
		}
	case "number":
		b, _ := json.Marshal(v)
		var n float64
		if len(b) > 0 && b[0] >= '0' && b[0] <= '9' && json.Unmarshal(b, &n) == nil && n >= float64(s.Min) && n <= float64(s.Max) && n == float64(int(n)) {
			return nil
		}
	}
	return fmt.Errorf("invalid setting %s", s.Key)
}
func defaultConfiguration(m Manifest) Configuration {
	c := Configuration{Values: map[string]any{}, Grants: []string{}}
	for _, s := range m.Settings {
		c.Values[s.Key] = s.Default
	}
	return c
}
func validateConfiguration(m Manifest, c Configuration) error {
	if c.Version < 0 || len(c.Values) != len(m.Settings) || len(c.Grants) > len(m.Capabilities) {
		return errors.New("invalid plugin configuration")
	}
	for _, s := range m.Settings {
		if e := validateValue(s, c.Values[s.Key]); e != nil {
			return e
		}
	}
	seen := map[string]bool{}
	for _, g := range c.Grants {
		if seen[g] || !slices.Contains(m.Capabilities, g) {
			return errors.New("permission was not declared by plugin")
		}
		seen[g] = true
	}
	return nil
}
func loadConfiguration(dir string, m Manifest) (Configuration, error) {
	c, e := ReadPrivate[Configuration](filepath.Join(dir, "runtime.json"), businessBytes)
	if os.IsNotExist(e) {
		return defaultConfiguration(m), nil
	}
	if e == nil {
		e = validateConfiguration(m, c)
	}
	return c, e
}

// All configuration writes, including upgrade snapshots, must fit the same
// serialized limit used by loadConfiguration and rollback recovery.
func writeConfiguration(path string, c Configuration) error {
	if c.Version < 0 {
		return errors.New("插件配置版本超出范围")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return errors.New("插件配置无法编码")
	}
	if len(data) > businessBytes {
		return errors.New("插件配置总大小不能超过 64 KiB，请缩短配置内容")
	}
	return writeMarker(path, data)
}

func (m *Manager) Configuration(slug string) (Manifest, Configuration, ExecutionStatus, error) {
	var manifest Manifest
	var c Configuration
	var status ExecutionStatus
	e := m.WithData(slug, false, 0, func(dir string, _ uint64) error {
		var e error
		manifest, e = readManifest(filepath.Join(filepath.Dir(dir), "plugin.json"))
		if e != nil {
			return e
		}
		if manifest.Runtime != "wasm/core-v1" || len(manifest.Hooks) == 0 {
			return errors.New("插件没有业务接口")
		}
		ctx, cancel := taskControlContext()
		defer cancel()
		c, e = m.configurationForData(ctx, dir, manifest)
		status = m.execution[slug]
		return e
	})
	return manifest, c, status, e
}
func (m *Manager) Configure(slug string, c Configuration) error {
	return m.configureAs(slug, c, 0)
}

// ConfigureAs binds event subscription consent to a host-authenticated account.
// The actor is never decoded from plugin configuration.
func (m *Manager) ConfigureAs(slug string, c Configuration, actorID int64) error {
	if actorID <= 0 {
		return ErrInactive
	}
	return m.configureAs(slug, c, actorID)
}

func (m *Manager) configureAs(slug string, c Configuration, actorID int64) error {
	return m.WithData(slug, false, 0, func(dir string, _ uint64) error {
		manifest, e := readManifest(filepath.Join(filepath.Dir(dir), "plugin.json"))
		if e != nil {
			return e
		}
		if actorID > 0 && len(manifest.Hooks["event.deliver.v1"]) != 1 {
			return errors.New("插件未声明事件订阅接口")
		}
		if e = validateConfiguration(manifest, c); e != nil {
			return e
		}
		if m.taskControls != nil {
			return m.configureTaskControl(dir, manifest, c, actorID)
		}
		old, e := loadConfiguration(dir, manifest)
		if e != nil {
			return e
		}
		if c.Version != old.Version {
			return ErrConfigurationConflict
		}
		// Reject invalid/oversized configuration before changing authorization.
		c.Version++
		encoded, encodeErr := json.Marshal(c)
		if c.Version < 0 {
			return errors.New("插件配置版本超出范围")
		}
		if encodeErr != nil {
			return errors.New("插件配置无法编码")
		}
		if len(encoded) > businessBytes {
			return errors.New("插件配置总大小不能超过 64 KiB，请缩短配置内容")
		}
		// Invalidate durable work before committing new grants. On a later
		// write failure old work stays invalidated (fail closed).
		if e = rotateTaskEpoch(dir); e != nil {
			return e
		}
		if e = writeConfiguration(filepath.Join(dir, "runtime.json"), c); e != nil {
			return e
		}
		if actorID > 0 && len(manifest.Hooks["event.deliver.v1"]) == 1 {
			epoch, err := readTaskEpoch(dir)
			if err != nil {
				return err
			}
			epoch.ActorID = actorID
			if err = WritePrivate(filepath.Join(dir, "task-epoch.json"), epoch); err != nil {
				return err
			}
		}
		m.nextGeneration(slug)
		delete(m.execution, slug)
		return clearReview(filepath.Dir(dir))
	})
}

func clearReview(directory string) error {
	name := filepath.Join(directory, ".review-required")
	info, err := packagefs.LstatNoSymlink(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("插件权限确认记录不可用")
	}
	return os.Remove(name)
}

type integrity struct {
	Manifest string `json:"manifest"`
	Module   string `json:"module"`
}

func digestBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func recordIntegrity(dir string, manifest Manifest) error {
	if manifest.Runtime != "wasm/core-v1" {
		return nil
	}
	module, _, e := readModule(filepath.Join(dir, filepath.FromSlash(manifest.Entry)))
	if e != nil {
		return e
	}
	b, _ := json.Marshal(manifest)
	return WritePrivate(filepath.Join(dir, ".integrity.json"), integrity{digestBytes(b), digestBytes(module)})
}
func verifyIntegrity(dir string, manifest Manifest, module []byte) error {
	receipt, e := ReadPrivate[Release](filepath.Join(dir, ".official.json"), 256<<10)
	if e != nil || !reflect.DeepEqual(receipt.Manifest, manifest) {
		return errors.New("插件缺少匹配的官方安装凭据，请从市场重新安装")
	}
	hashes, e := ReadPrivate[integrity](filepath.Join(dir, ".integrity.json"), 1024)
	if e != nil {
		return errors.New("插件缺少完整性记录，请从市场重新安装")
	}
	b, _ := json.Marshal(manifest)
	if hashes.Manifest != digestBytes(b) || hashes.Module != digestBytes(module) {
		return errors.New("插件文件已变化，业务执行已停止")
	}
	return nil
}

// Dispatch invokes a declared export on a fresh instance. Public render hooks
// cannot write persistent data. Administrative actions require caller auth.
func (m *Manager) Dispatch(ctx context.Context, slug string, in BusinessInput, read ContentReader) (BusinessResult, error) {
	if in.Event == "task.execute.v1" || in.Event == "event.deliver.v1" {
		return BusinessResult{}, ErrInactive
	}
	return m.dispatch(ctx, slug, in, read, "")
}

func (m *Manager) dispatch(ctx context.Context, slug string, in BusinessInput, read ContentReader, taskEpoch string, enqueuers ...TaskEnqueuer) (BusinessResult, error) {
	return m.dispatchAuthorized(ctx, slug, in, read, taskEpoch, nil, enqueuers...)
}

func (m *Manager) dispatchAuthorized(ctx context.Context, slug string, in BusinessInput, read ContentReader, taskEpoch string, authorize func(context.Context) error, enqueuers ...TaskEnqueuer) (BusinessResult, error) {
	return m.dispatchAuthorizedEffects(ctx, slug, in, read, taskEpoch, authorize, nil, enqueuers...)
}

func (m *Manager) dispatchAuthorizedEffects(ctx context.Context, slug string, in BusinessInput, read ContentReader, taskEpoch string, authorize func(context.Context) error, effects *taskEffectInvocation, enqueuers ...TaskEnqueuer) (BusinessResult, error) {
	if ctx == nil {
		return BusinessResult{}, errors.New("context required")
	}
	if err := ctx.Err(); err != nil {
		return BusinessResult{}, err
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		return BusinessResult{}, ctx.Err()
	default:
		return BusinessResult{}, errors.New("插件正在处理其他请求")
	}
	var manifest Manifest
	var config Configuration
	var module []byte
	var generation uint64
	var compilation *businessCompilationEntry
	var capturedControl TaskControl
	e := m.WithData(slug, true, 0, func(dir string, g uint64) error {
		var e error
		manifest, e = readManifest(filepath.Join(filepath.Dir(dir), "plugin.json"))
		if e != nil {
			return e
		}
		if manifest.Runtime != "wasm/core-v1" || len(manifest.Hooks[in.Event]) != 1 {
			return errors.New("插件没有此业务接口")
		}
		if time.Now().Before(m.execution[slug].BlockedUntil) {
			return errors.New("插件连续执行失败，冷却 30 秒后重试")
		}
		config, e = m.configurationForData(ctx, dir, manifest)
		if e != nil {
			return e
		}
		generation = g
		if taskEpoch != "" && m.taskControls == nil {
			epoch, err := readTaskEpoch(dir)
			if err != nil || epoch.Token != taskEpoch {
				return ErrInactive
			}
		}
		module, _, e = readModule(filepath.Join(filepath.Dir(dir), filepath.FromSlash(manifest.Entry)))
		if e != nil {
			return e
		}
		if e = verifyIntegrity(filepath.Dir(dir), manifest, module); e != nil {
			return e
		}
		if m.taskControls != nil {
			capturedControl, e = m.activeTaskControl(ctx, manifest, module)
			if e != nil {
				return e
			}
			if taskEpoch != "" && capturedControl.Epoch.Token != taskEpoch {
				return ErrInactive
			}
			config = capturedControl.Configuration
		}
		compilation = businessCompilations.acquire(m.root, slug, generation, module)
		return nil
	})
	if e != nil {
		return BusinessResult{}, e
	}
	defer businessCompilations.release(compilation)
	active, e := m.ActiveContext(slug, generation)
	if e != nil {
		return BusinessResult{}, e
	}
	call, cancel := context.WithTimeoutCause(ctx, defaultCallTimeout, errBusinessCallTimeout)
	defer cancel()
	stop := context.AfterFunc(active, cancel)
	defer stop()
	in.Schema = 1
	in.Now = time.Now().Unix()
	if !slices.Contains(config.Grants, "content.read") {
		in.Post = nil
	}
	if in.Event == "content.render" && !slices.Contains(config.Grants, "render.fragment") {
		return BusinessResult{}, errors.New("页面展示权限尚未授予")
	}
	payload, e := json.Marshal(in)
	if e != nil || len(payload) > businessBytes {
		return BusinessResult{}, errors.New("plugin input too large")
	}
	calls := 0
	var authorityFailure error
	var effectFailure error
	host := func(ctx context.Context, request []byte) (any, error) {
		if authorityFailure != nil {
			return nil, authorityFailure
		}
		if effectFailure != nil {
			return nil, effectFailure
		}
		calls++
		if calls > 32 {
			if taskEpoch != "" && (slices.Contains(manifest.Capabilities, "content.write") || slices.Contains(manifest.Capabilities, "http.send")) {
				effectFailure = errors.New("host call budget exceeded")
			}
			return nil, errors.New("host call budget exceeded")
		}
		var q struct {
			Method string          `json:"method"`
			Key    string          `json:"key,omitempty"`
			Value  json.RawMessage `json:"value,omitempty"`
			URL    string          `json:"url,omitempty"`
			ID     int64           `json:"id,omitempty"`
		}
		decoded, e := packagefs.DecodeJSON[struct {
			Method string          `json:"method"`
			Key    string          `json:"key,omitempty"`
			Value  json.RawMessage `json:"value,omitempty"`
			URL    string          `json:"url,omitempty"`
			ID     int64           `json:"id,omitempty"`
		}](bytes.NewReader(request), businessBytes, "host request")
		if e != nil {
			if taskEpoch != "" && (slices.Contains(manifest.Capabilities, "content.write") || slices.Contains(manifest.Capabilities, "http.send")) {
				effectFailure = e
			}
			return nil, e
		}
		q = decoded
		if q.Method == "content.update" && (taskEpoch == "" || m.taskControls == nil || effects == nil || effects.store == nil) {
			effectFailure = errors.New("background content writer unavailable")
			return nil, effectFailure
		}
		if q.Method == "http.enqueue" && (taskEpoch == "" || m.taskControls == nil || effects == nil || effects.webhooks == nil) {
			effectFailure = errors.New("background webhook queue unavailable")
			return nil, effectFailure
		}
		// Tasks only admit durable webhooks; guest execution performs no network I/O.
		if taskEpoch != "" && q.Method != "settings.get" && q.Method != "cache.get" && q.Method != "content.get" && q.Method != "content.update" && q.Method != "http.enqueue" {
			return nil, errors.New("background host operation unavailable")
		}
		cap := map[string]string{"settings.get": "settings.read", "settings.set": "settings.write", "cache.get": "cache", "cache.set": "cache", "cache.delete": "cache", "content.get": "content.read", "content.update": "content.write", "http.get": "http.outbound", "http.enqueue": "http.send", "jobs.enqueue": "jobs.enqueue"}[q.Method]
		if cap == "" || !slices.Contains(manifest.Capabilities, cap) || !slices.Contains(config.Grants, cap) {
			if q.Method == "content.update" || q.Method == "http.enqueue" {
				effectFailure = errors.New("background effect capability denied")
				return nil, effectFailure
			}
			return nil, errors.New("capability denied")
		}
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if m.taskControls != nil {
			if e := m.checkCapturedTaskControl(ctx, capturedControl); e != nil {
				authorityFailure = e
				return nil, e
			}
		}
		if authorize != nil {
			if e := authorize(ctx); e != nil {
				authorityFailure = e
				return nil, e
			}
		}
		switch q.Method {
		case "http.enqueue":
			if q.ID != 0 {
				effectFailure = errors.New("unexpected webhook content ID")
				return nil, effectFailure
			}
			result, err := effects.enqueueWebhook(ctx, capturedControl, q.Key, q.URL, q.Value)
			if err != nil {
				effectFailure = err
			}
			return result, err
		case "content.update":
			// This service receives the verified package and real private claim;
			// neither may be supplied or replaced by guest JSON.
			result, err := effects.writeContent(ctx, capturedControl, q.Key, q.ID, q.Value)
			if err != nil {
				effectFailure = err
			}
			return result, err
		case "jobs.enqueue":
			if in.Event != "admin.action" || len(enqueuers) != 1 || enqueuers[0] == nil || len(manifest.Hooks["task.execute.v1"]) != 1 {
				return nil, errors.New("task enqueue unavailable")
			}
			// Check the current generation before invoking the host callback.
			if err := m.WithData(slug, true, generation, func(string, uint64) error { return ctx.Err() }); err != nil {
				return nil, err
			}
			return enqueuers[0](ctx, q.Key, q.Value)
		case "settings.get":
			return config.Values, nil
		case "settings.set":
			if in.Event != "admin.action" {
				return nil, errors.New("render hooks are read only")
			}
			var values map[string]any
			if e = json.Unmarshal(q.Value, &values); e != nil {
				return nil, e
			}
			next := config
			next.Values = values
			err := m.WithData(slug, true, generation, func(dir string, _ uint64) error {
				if e := ctx.Err(); e != nil {
					return e
				}
				if e := validateConfiguration(manifest, next); e != nil {
					return e
				}
				old, e := m.configurationForData(ctx, dir, manifest)
				if e != nil {
					return e
				}
				if old.Version != next.Version {
					return ErrConfigurationConflict
				}
				next.Version++
				if m.taskControls != nil {
					return m.writeSharedTaskValues(ctx, manifest, module, capturedControl.Epoch.Token, next)
				}
				return writeConfiguration(filepath.Join(dir, "runtime.json"), next)
			})
			if err != nil {
				if m.taskControls != nil && (errors.Is(err, ErrInactive) || errors.Is(err, ErrTaskControlUnavailable) || errors.Is(err, ErrTaskControlCorrupt)) {
					authorityFailure = err
				}
				return nil, err
			}
			config = next
			return true, nil
		case "content.get":
			if read == nil || q.ID < 1 {
				return nil, errors.New("content unavailable")
			}
			return read(ctx, q.ID)
		case "http.get":
			u, e := safehttp.ValidateURL(q.URL)
			if e != nil || !slices.Contains(manifest.OutboundHosts, strings.ToLower(u.Hostname())) {
				return nil, errors.New("outbound host denied")
			}
			client := safehttp.Client(defaultCallTimeout)
			defer client.CloseIdleConnections()
			req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
			if e != nil {
				return nil, e
			}
			res, e := client.Do(req)
			if e != nil {
				return nil, errors.New("outbound request failed")
			}
			defer res.Body.Close()
			b, e := io.ReadAll(io.LimitReader(res.Body, 32<<10+1))
			if e != nil || len(b) > 32<<10 {
				return nil, errors.New("outbound response too large")
			}
			return map[string]any{"status": res.StatusCode, "body": string(b)}, nil
		default:
			if !exportNamePattern.MatchString(q.Key) {
				return nil, errors.New("invalid namespace key")
			}
			var result any
			e := m.WithData(slug, true, generation, func(dir string, _ uint64) error {
				if e := ctx.Err(); e != nil {
					return e
				}
				path := filepath.Join(dir, "kv.json")
				values, e := ReadPrivate[map[string]json.RawMessage](path, businessBytes)
				if os.IsNotExist(e) {
					values = map[string]json.RawMessage{}
				} else if e != nil {
					return e
				}
				if q.Method == "cache.get" {
					result = values[q.Key]
					return nil
				}
				if in.Event != "admin.action" {
					return errors.New("render hooks are read only")
				}
				if q.Method == "cache.delete" {
					delete(values, q.Key)
				} else {
					if !json.Valid(q.Value) {
						return errors.New("invalid value")
					}
					values[q.Key] = q.Value
				}
				b, _ := json.Marshal(values)
				if len(b) > businessBytes || len(values) > 128 {
					return errors.New("plugin namespace quota exceeded")
				}
				result = true
				return WritePrivate(path, values)
			})
			return result, e
		}
	}
	started := time.Now()
	out, e := executeBusinessCached(call, module, manifest, manifest.Hooks[in.Event][0], payload, host, compilation)
	if e == nil && authorityFailure == nil && m.taskControls != nil {
		authorityFailure = m.checkCapturedTaskControl(ctx, capturedControl)
	}
	// Guests may encode or ignore a failed host response. The host's revoked or
	// unavailable authority still terminates the invocation and remains retryable
	// where appropriate; it is not evidence of an unhealthy plugin.
	if authorityFailure != nil {
		e = authorityFailure
	} else if effectFailure != nil {
		e = effectFailure
	}
	if e == nil && out.Fragment != nil {
		f := out.Fragment
		if !slices.Contains(config.Grants, "render.fragment") || !validManifestText(f.Title, 120, false) || !validManifestText(f.Text, 2000, true) || !slices.Contains([]string{"info", "warning", "success"}, f.Tone) {
			e = errors.New("invalid or unauthorized fragment")
		}
	}
	// A disconnected caller or an exhausted outer request budget says nothing
	// about this plugin's health. Preserve a local sandbox deadline that fired
	// first, even if the caller is cancelled before execution unwinds.
	callerInterrupted := ctx.Err() != nil && context.Cause(call) != errBusinessCallTimeout &&
		(e == nil || errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded))
	if callerInterrupted && e == nil {
		e = ctx.Err()
	}
	m.mu.Lock()
	if m.generationID(slug) != generation {
		m.mu.Unlock()
		return BusinessResult{}, ErrInactive
	}
	st := m.execution[slug]
	st.Calls++
	st.LastRun = time.Now().UTC()
	st.LastDurationMS = time.Since(started).Milliseconds()
	switch {
	case callerInterrupted || authorityFailure != nil || effectFailure != nil:
		// Neither increment nor clear an existing genuine failure streak.
	case e != nil:
		st.Failures++
		st.Consecutive++
		st.LastError = "业务执行失败：超时、接口或返回数据无效"
		if st.Consecutive >= 3 {
			st.BlockedUntil = time.Now().Add(30 * time.Second)
		}
	default:
		st.Consecutive = 0
		st.LastError = ""
		st.BlockedUntil = time.Time{}
	}
	m.execution[slug] = st
	m.mu.Unlock()
	if e != nil {
		return BusinessResult{}, e
	}
	return out, nil
}

type hostCall func(context.Context, []byte) (any, error)

func registerHost(ctx context.Context, r wazero.Runtime, host hostCall) error {
	_, e := r.NewHostModuleBuilder("newpress").NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, p, n, out, capacity uint32) uint32 {
		if n > businessBytes || capacity > businessBytes || m.Memory() == nil {
			return ^uint32(0)
		}
		raw, ok := m.Memory().Read(p, n)
		if !ok {
			return ^uint32(0)
		}
		if capacity == 0 {
			return ^uint32(0)
		}
		if _, ok := m.Memory().Read(out, capacity); !ok {
			return ^uint32(0)
		}
		var value any
		var err error
		if host == nil {
			err = errors.New("host unavailable during validation")
		} else {
			value, err = host(ctx, append([]byte(nil), raw...))
		}
		response := map[string]any{"ok": err == nil, "data": value}
		if err != nil {
			response["error"] = err.Error()
		}
		b, e := json.Marshal(response)
		if e != nil || len(b) > businessBytes || uint32(len(b)) > capacity {
			return ^uint32(0)
		}
		if !m.Memory().Write(out, b) {
			return ^uint32(0)
		}
		return uint32(len(b))
	}).Export("host_call").Instantiate(ctx)
	return e
}
func signature(m api.Module, name string, params, results []api.ValueType) bool {
	f := m.ExportedFunction(name)
	return f != nil && slices.Equal(f.Definition().ParamTypes(), params) && slices.Equal(f.Definition().ResultTypes(), results)
}
func executeBusiness(ctx context.Context, code []byte, manifest Manifest, export string, input []byte, host hostCall) (BusinessResult, error) {
	return executeBusinessCached(ctx, code, manifest, export, input, host, nil)
}

func executeBusinessCached(ctx context.Context, code []byte, manifest Manifest, export string, input []byte, host hostCall, compilation *businessCompilationEntry) (BusinessResult, error) {
	if len(input) > businessBytes {
		return BusinessResult{}, errors.New("plugin input too large")
	}
	if e := ctx.Err(); e != nil {
		return BusinessResult{}, e
	}
	config := wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(defaultMemoryMiB * 16).WithCloseOnContextDone(true)
	if compilation != nil {
		config = config.WithCompilationCache(compilation.cache)
	}
	r := wazero.NewRuntimeWithConfig(ctx, config)
	var compiled wazero.CompiledModule
	var closeCompiled bool
	defer func() {
		// Closing the runtime also discards its request-specific host closure.
		_ = r.Close(context.Background())
		if closeCompiled {
			_ = compiled.Close(context.Background())
		}
	}()
	if e := registerHost(ctx, r, host); e != nil {
		return BusinessResult{}, fmt.Errorf("register plugin host: %w", e)
	}
	var e error
	if compilation == nil {
		compiled, e = r.CompileModule(ctx, code)
	} else {
		compiled, closeCompiled, e = compilation.compile(ctx, r, code)
	}
	if e != nil {
		return BusinessResult{}, fmt.Errorf("compile WebAssembly plugin: %w", e)
	}
	m, e := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(manifest.Slug).WithStartFunctions())
	if e != nil {
		return BusinessResult{}, fmt.Errorf("instantiate WebAssembly plugin: %w", e)
	}
	i32, i64 := api.ValueTypeI32, api.ValueTypeI64
	if m.Memory() == nil || !signature(m, "np_api_version", nil, []api.ValueType{i64}) || !signature(m, "np_alloc", []api.ValueType{i32}, []api.ValueType{i32}) || !signature(m, "np_free", []api.ValueType{i32, i32}, nil) {
		return BusinessResult{}, errors.New("business ABI requires memory, np_api_version, np_alloc and np_free")
	}
	version, e := m.ExportedFunction("np_api_version").Call(ctx)
	if e != nil {
		return BusinessResult{}, fmt.Errorf("call np_api_version: %w", e)
	}
	if len(version) != 1 || version[0] != ABIVersion {
		return BusinessResult{}, errors.New("unsupported business ABI")
	}
	for _, functions := range manifest.Hooks {
		for _, name := range functions {
			if !signature(m, name, []api.ValueType{i32, i32}, []api.ValueType{i64}) {
				return BusinessResult{}, errors.New("invalid business export signature")
			}
		}
	}
	if export == "" {
		return BusinessResult{}, nil
	}
	allocated, e := m.ExportedFunction("np_alloc").Call(ctx, uint64(len(input)))
	if e != nil {
		return BusinessResult{}, fmt.Errorf("call np_alloc: %w", e)
	}
	if len(allocated) != 1 {
		return BusinessResult{}, errors.New("input allocation failed")
	}
	pointer := uint32(allocated[0])
	if pointer == 0 || !m.Memory().Write(pointer, input) {
		return BusinessResult{}, errors.New("input memory out of bounds")
	}
	result, e := m.ExportedFunction(export).Call(ctx, uint64(pointer), uint64(len(input)))
	if e != nil {
		return BusinessResult{}, fmt.Errorf("business call failed (%s): %w", export, e)
	}
	if len(result) != 1 {
		return BusinessResult{}, errors.New("business call failed")
	}
	address, size := uint32(result[0]>>32), uint32(result[0])
	if size > businessBytes || size == 0 {
		return BusinessResult{}, errors.New("invalid response length")
	}
	b, ok := m.Memory().Read(address, size)
	if !ok {
		return BusinessResult{}, errors.New("output memory out of bounds")
	}
	b = append([]byte(nil), b...)
	if _, e = m.ExportedFunction("np_free").Call(ctx, uint64(address), uint64(size)); e != nil {
		return BusinessResult{}, fmt.Errorf("call np_free for response: %w", e)
	}
	if _, e = m.ExportedFunction("np_free").Call(ctx, uint64(pointer), uint64(len(input))); e != nil {
		return BusinessResult{}, fmt.Errorf("call np_free for input: %w", e)
	}
	return packagefs.DecodeJSON[BusinessResult](bytes.NewReader(b), businessBytes, "plugin response")
}
