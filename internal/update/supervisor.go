package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/version"
)

type childProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	exitedAt time.Time
	err      error
	instance string
}

func startChild(root, seed string, cfg config.Config, r Release) (*childProcess, error) {
	if err := validateRelease(root, seed, r, false); err != nil {
		return nil, err
	}
	c := &childProcess{done: make(chan struct{}), instance: randomID()}
	c.cmd = exec.Command(executable(root, r, seed), "serve")
	c.cmd.Stdout, c.cmd.Stderr = os.Stdout, os.Stderr
	values := map[string]string{"NEWPRESS_UPDATE_MANAGED": "1", "NEWPRESS_UPDATE_POLICY_PROTOCOL": policyProtocol, "NEWPRESS_UPDATE_LAUNCHER": LauncherGeneration, "NEWPRESS_UPDATE_INSTANCE": c.instance, "NEWPRESS_UPDATE_DIR": root, "NEWPRESS_THEMES_DIR": releaseThemes(root, r, cfg.ThemesDir), "NEWPRESS_AUTO_MIGRATE": fmt.Sprint(cfg.AutoMigrate)}
	if r.ID != "" {
		values["NEWPRESS_AUTO_MIGRATE"] = "false"
		values["NEWPRESS_SEED_DEMO"] = "false"
	}
	c.cmd.Env = replaceEnvironment(os.Environ(), values)
	var err error
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err = c.cmd.Start(); err != nil {
		c.stdin.Close()
		return nil, errors.New("无法启动 NewPress 程序，请检查可执行权限和平台")
	}
	go func() {
		c.err = c.cmd.Wait()
		c.exitedAt = time.Now()
		close(c.done)
	}()
	return c, nil
}

func replaceEnvironment(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	for _, e := range base {
		key, _, _ := strings.Cut(e, "=")
		replaced := false
		for k := range values {
			if strings.EqualFold(k, key) {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, e)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func (c *childProcess) stop() {
	if c != nil {
		c.stdin.Close()
		<-c.done
	}
}

// A candidate that never becomes ready may not reach its stdin shutdown loop.
// Bound cleanup for that failed candidate; the live old process still drains
// normally before any update is attempted.
func (c *childProcess) abort(timeout time.Duration) {
	if c == nil {
		return
	}
	_ = c.stdin.Close()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.done:
	case <-timer.C:
		_ = c.cmd.Process.Kill()
		<-c.done
	}
}

func validateRelease(root, seed string, r Release, candidate bool) error {
	if r.ID == "" {
		if seed == "" {
			return errors.New("缺少初始程序")
		}
		return nil
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(r.ID) || !ValidVersion(r.Version) || !digestPattern.MatchString(r.BinaryHash) {
		return errors.New("发布记录无效")
	}
	h, err := hashFile(executable(root, r, seed))
	if err != nil || h != r.BinaryHash {
		return errors.New("已准备程序的 SHA-256 校验失败")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable(root, r, seed), "update-info").Output()
	if ctx.Err() != nil {
		return errors.New("更新程序版本自检超时，请检查主机负载与可执行权限")
	}
	var info struct {
		Generation string `json:"launcher_generation"`
		Protocol   int    `json:"protocol"`
		Version    string `json:"version"`
	}
	if err != nil || len(output) > 4096 || json.Unmarshal(output, &info) != nil || info.Protocol != Protocol || info.Version != r.Version || (candidate && info.Generation != LauncherGeneration) {
		return errors.New("更新程序的版本或托管协议与清单不一致")
	}
	return nil
}

func readyURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health/ready", nil
}

func awaitReady(ctx context.Context, c *childProcess, address string, timeout time.Duration) error {
	endpoint, err := readyURL(address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	passed := 0
	for {
		select {
		case <-ctx.Done():
			return errors.New("新进程未通过启动健康检查")
		case <-c.done:
			return errors.New("NewPress 进程在启动检查期间退出")
		case <-ticker.C:
			r, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			response, err := client.Do(r)
			if err == nil {
				ok := response.StatusCode == http.StatusOK && response.Header.Get("X-NewPress-Instance") == c.instance
				response.Body.Close()
				if ok {
					passed++
				} else {
					passed = 0
				}
			} else {
				passed = 0
			}
			if passed >= 3 {
				return nil
			}
		}
	}
}

// Run owns the serving process without a shell, Docker daemon, or root access.
// The immutable launcher stays alive while verified binaries live on a volume.
func Run(ctx context.Context, cfg config.Config) error {
	root, err := filepath.Abs(Directory(cfg.DataDir))
	if err != nil {
		return err
	}
	if err = ValidateDirectory(root, cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir); err != nil {
		return err
	}
	m, err := New(root, version.Version, cfg.ThemesDir, true)
	if err != nil {
		return err
	}
	defer m.Close()
	lock, err := dirlock.Acquire(filepath.Join(root, "supervisor"))
	if err != nil {
		return errors.New("已有托管进程使用此更新目录")
	}
	defer lock.Close()
	seed, err := os.Executable()
	if err != nil {
		return err
	}
	active := Release{Version: version.Version}
	if err = readJSON(filepath.Join(root, "active.json"), &active); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("无法读取当前发布记录")
	}
	var child *childProcess
	var pending Transaction
	err = readJSON(filepath.Join(root, "transaction.json"), &pending)
	switch {
	case err == nil:
		active = pending.Previous
		child, err = restorePrevious(ctx, m, seed, cfg, pending, "上次更新切换被中断，正在恢复上一可用版本")
		if err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
		state, err := m.state()
		if err != nil {
			return err
		}
		// On a clean boot, reject invalid deployment settings unless a valid site override supplies them.
		// Recovery uses the existing journal or guard and must not depend on a new policy.
		if _, envErr := deploymentPolicy(); envErr != nil && !state.BlocksChanges() {
			if _, err := m.policy(); err != nil {
				return err
			}
		}
		if state.Busy() {
			state.step("failed", state.Percent, "上次更新任务被中断，当前版本未切换，请重新检查更新")
			if err = m.save(state); err != nil {
				return err
			}
			if err = removeRecord(root, "request.json"); err != nil {
				return err
			}
		}
		// Evaluate the guard at startup, even if probing/cleaning up the child takes past the deadline.
		started := time.Now()
		child, err = startReady(ctx, root, seed, cfg, active)
		if err != nil {
			original := err
			child, active, err = recoverRecentExit(ctx, m, seed, cfg, active, started)
			if err != nil {
				return err
			}
			if child == nil {
				return original
			}
		}
	default:
		return errors.New("无法读取更新恢复日志")
	}
	defer func() { child.stop() }()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-child.done:
			previous, restored, err := recoverRecentExit(ctx, m, seed, cfg, active, child.exitedAt)
			if err != nil {
				return err
			}
			if previous != nil {
				child, active = previous, restored
				continue
			}
			if child.err != nil {
				return fmt.Errorf("serving process exited: %w", child.err)
			}
			return nil
		case <-ticker.C:
			var tx Transaction
			if err = readJSON(filepath.Join(root, "request.json"), &tx); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return errors.New("无法读取更新切换请求")
			}
			tx.Previous = active
			tx.State.Previous = active.Version
			switch {
			case tx.Generation != LauncherGeneration || tx.State.ID != tx.Candidate.ID:
				err = errors.New("旧安装请求已失效，请使用已升级的托管程序重新检查")
			case !tx.Expires.After(time.Now()):
				err = errors.New("发布授权已过期，请重新检查")
			}
			var policy PolicySnapshot
			if err == nil {
				policy, err = m.policy()
			}
			if err == nil {
				err = validateRelease(root, seed, tx.Candidate, true)
			}
			// Recheck just before cutover: self-testing the executable can take time.
			if err == nil && !tx.Expires.After(time.Now()) {
				err = errors.New("发布授权已过期，请重新检查")
			}
			if err == nil && cfg.DatabaseURL != "" {
				err = verifyBackupReceipt(cfg.PublicURL, active.Version, tx.Candidate.Version)
			}
			if err != nil {
				tx.State.step("failed", 82, err.Error())
				if err = m.save(tx.State); err != nil {
					return err
				}
				if err = removeRecord(root, "request.json"); err != nil {
					return err
				}
				continue
			}
			if !newer(tx.Candidate.Version, active.Version) {
				tx.State.step("failed", 82, "目标版本不高于当前版本")
				if err = m.save(tx.State); err != nil {
					return err
				}
				if err = removeRecord(root, "request.json"); err != nil {
					return err
				}
				continue
			}
			window := policy.window()
			if err = writeJSON(filepath.Join(root, "transaction.json"), tx); err != nil {
				return err
			}
			if err = removeRecord(root, "request.json"); err != nil {
				return err
			}
			tx.State.step("stopping", 85, "正在等待请求完成并停止旧进程")
			if err = m.save(tx.State); err != nil {
				return err
			}
			child.stop()
			if ctx.Err() != nil {
				return nil
			}
			var installErr error
			if cfg.DatabaseURL == "" {
				tx.State.step("backup", 88, "正在创建离线站点备份，保留更新前恢复点")
				if err = m.save(tx.State); err != nil {
					return err
				}
				backupCfg := cfg
				backupCfg.ThemesDir = releaseThemes(root, active, cfg.ThemesDir)
				// Use the running release's backup implementation and version stamp.
				backupCommand := exec.CommandContext(ctx, executable(root, active, seed), "backup", "create", filepath.Join(root, "backup-"+tx.State.ID+".zip"))
				backupCommand.Env = replaceEnvironment(os.Environ(), map[string]string{"NEWPRESS_THEMES_DIR": backupCfg.ThemesDir, "NEWPRESS_DATA_DIR": cfg.DataDir, "NEWPRESS_PLUGINS_DIR": cfg.PluginsDir, "NEWPRESS_UPDATE_DIR": root})
				backupCommand.Stdout, backupCommand.Stderr = os.Stdout, os.Stderr
				installErr = backupCommand.Run()
				if installErr != nil {
					installErr = errors.New("离线备份失败，已停止更新并恢复旧版本")
				}
			}
			if installErr == nil && !tx.Expires.After(time.Now()) {
				installErr = errors.New("发布授权在备份期间过期")
			}
			if installErr == nil {
				tx.State.step("starting", 94, "正在启动新版本并检查服务；数据库自动迁移已关闭")
				if err = m.save(tx.State); err != nil {
					return err
				}
				candidate, startErr := startChild(root, seed, cfg, tx.Candidate)
				if startErr == nil {
					child = candidate
					startErr = awaitReady(ctx, child, cfg.Address, 60*time.Second)
					if startErr == nil {
						tx.State.step("observing", 96, "新进程已就绪，正在观察首页、登录页和服务稳定性")
						startErr = m.save(tx.State)
						if startErr == nil {
							startErr = observe(ctx, child, cfg.Address, window)
						}
					}
					if startErr != nil {
						child.abort(15 * time.Second)
					}
				}
				installErr = startErr
			}
			if ctx.Err() != nil {
				return nil
			} // Leave the journal for restart recovery.
			if installErr == nil {
				installErr = writeJSON(filepath.Join(root, "previous.json"), active)
			}
			completed := tx.State
			if installErr == nil {
				completed.step("completed", 100, "更新完成，新版本已通过稳定性观察，请重新登录后台")
				completed.ProtectedUntil = completed.UpdatedAt.Add(window)
				guard := restartGuard{Candidate: tx.Candidate, Previous: active, Until: completed.ProtectedUntil, State: completed}
				installErr = writeJSON(filepath.Join(root, "guard.json"), guard)
			}
			if installErr == nil {
				installErr = writeJSON(filepath.Join(root, "active.json"), tx.Candidate)
			}
			if installErr != nil {
				child.stop()
				child, err = restorePrevious(ctx, m, seed, cfg, tx, "新版本未能完成切换，正在恢复原版本")
				if err != nil {
					return err
				}
				continue
			}
			active, tx.State = tx.Candidate, completed
			// Keep the recovery journal until both active pointer and result are durable.
			if err = m.save(tx.State); err != nil {
				return err
			}
			if err = removeRecord(root, "transaction.json"); err != nil {
				return err
			}
		}
	}
}

func Info() any {
	return struct {
		Generation     string `json:"launcher_generation"`
		PolicyProtocol string `json:"policy_protocol"`
		Protocol       int    `json:"protocol"`
		Version        string `json:"version"`
	}{LauncherGeneration, policyProtocol, Protocol, version.Version}
}

// WaitForParent makes EOF a portable shutdown signal, including on Windows.
func WaitForParent(cancel context.CancelFunc) { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }
