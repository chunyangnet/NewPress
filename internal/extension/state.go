// Package extension contains the shared on-disk version transaction contract.
// It does not load plugins, render themes, or choose a network trust root.
package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/version"
)

const (
	HistoryFile = ".versions.json"
	RollbackDir = ".rollback"
	MaxHistory  = 20
)

var (
	ErrConflict = errors.New("安装版本已变化，请刷新后重新选择")
	ErrBusy     = errors.New("已有更新任务正在运行，请稍后重试")
)

type Metadata struct {
	Channel        string    `json:"channel"`
	PublishedAt    time.Time `json:"published_at"`
	DataCompatible bool      `json:"data_compatible"`
}

func (m *Metadata) Validate(value string) error {
	v, err := version.Parse(value)
	if err != nil {
		return err
	}
	if m == nil || (m.Channel != "stable" && m.Channel != "preview") || m.PublishedAt.IsZero() || m.PublishedAt.After(time.Now().Add(5*time.Minute)) {
		return errors.New("发行信息需要通道和有效发布时间")
	}
	if m.Channel == "stable" && v.Pre != "" {
		return errors.New("稳定通道不能包含预发布版本")
	}
	return nil
}

type Event struct {
	Action string    `json:"action"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	SHA256 string    `json:"sha256,omitempty"`
	At     time.Time `json:"at"`
}

type history struct {
	Schema         int     `json:"schema"`
	Revision       int64   `json:"revision"`
	Previous       string  `json:"previous"`
	PreviousDigest string  `json:"previous_digest"`
	Events         []Event `json:"events"`
}

type VersionState struct {
	Revision       string  `json:"revision"`
	Previous       string  `json:"previous,omitempty"`
	History        []Event `json:"history"`
	ReviewRequired bool    `json:"review_required"`
}

func readHistory(directory string) (history, error) {
	h, err := ReadJSON[history](filepath.Join(directory, HistoryFile), 32<<10)
	if errors.Is(err, os.ErrNotExist) {
		return history{Schema: 1, Events: []Event{}}, nil
	}
	if err != nil {
		return history{}, err
	}
	if h.Schema != 1 || h.Revision < 1 || len(h.Events) == 0 || len(h.Events) > MaxHistory {
		return history{}, errors.New("版本记录无效")
	}
	for _, event := range h.Events {
		if (event.Action != "update" && event.Action != "rollback") || len(event.From) > 64 || len(event.To) > 64 || event.At.IsZero() {
			return history{}, errors.New("版本记录无效")
		}
	}
	return h, nil
}

func State(directory, current string) (VersionState, error) {
	h, err := readHistory(directory)
	if err != nil {
		return VersionState{}, err
	}
	data, _ := json.Marshal(struct {
		Version string
		History history
	}{current, h})
	sum := sha256.Sum256(data)
	return VersionState{Revision: hex.EncodeToString(sum[:]), Previous: h.Previous, History: h.Events}, nil
}

func Record(before, after, from, to, action, digest string) error {
	h, err := readHistory(before)
	if err != nil {
		return err
	}
	h.Revision++
	h.Previous = from
	h.PreviousDigest, err = treeDigest(filepath.Join(after, RollbackDir))
	if err != nil {
		return err
	}
	h.Events = append(h.Events, Event{Action: action, From: from, To: to, SHA256: digest, At: time.Now().UTC()})
	if len(h.Events) > MaxHistory {
		h.Events = h.Events[len(h.Events)-MaxHistory:]
	}
	return WriteJSON(filepath.Join(after, HistoryFile), h)
}

func VerifyRollback(directory string) error {
	h, err := readHistory(directory)
	if err != nil {
		return err
	}
	if h.Previous == "" || len(h.PreviousDigest) != 64 {
		return errors.New("没有可用的回退快照")
	}
	actual, err := treeDigest(filepath.Join(directory, RollbackDir))
	if err != nil || actual != h.PreviousDigest {
		return errors.New("回退快照完整性校验失败")
	}
	return nil
}

func treeDigest(root string) (string, error) {
	h := sha256.New()
	var total int64
	count := 0
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := packagefs.LstatNoSymlink(name)
		if err != nil {
			return err
		}
		count++
		total += info.Size()
		if count > 20000 || total > 512<<20 {
			return errors.New("回退快照超出限制")
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			fmt.Fprintf(h, "dir:%s\n", filepath.ToSlash(rel))
			return nil
		}
		f, _, err := packagefs.OpenRegularNoSymlink(name)
		if err != nil {
			return err
		}
		defer f.Close()
		fileHash := sha256.New()
		n, err := io.Copy(fileHash, io.LimitReader(f, info.Size()+1))
		if err != nil || n != info.Size() {
			return errors.New("回退快照在校验时发生变化")
		}
		fmt.Fprintf(h, "file:%s:%x\n", filepath.ToSlash(rel), fileHash.Sum(nil))
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

type Progress func(phase string, received, total int64)

func (p Progress) Report(phase string, received, total int64) {
	if p != nil {
		p(phase, received, total)
	}
}

func ReadJSON[T any](name string, limit int64) (T, error) {
	var zero T
	f, _, err := packagefs.OpenRegularNoSymlink(name)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	return packagefs.DecodeJSON[T](f, limit, "extension state")
}

func WriteJSON(name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(name)
	if err := packagefs.ValidateRegularFileTargetNoSymlink(name); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = packagefs.ReplaceRegularFileNoSymlink(temporary, name); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil // Windows does not support fsync on directory handles.
	}
	f, err := packagefs.OpenDirectoryNoSymlink(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// CopyTree rejects links and special files and bounds even locally supplied
// state. Exclusions apply only at the source root, never to nested user data.
func CopyTree(ctx context.Context, source, target string, exclude ...string) error {
	if info, err := packagefs.LstatNoSymlink(source); err != nil || !info.IsDir() {
		return errors.New("扩展快照目录不可用")
	}
	if err := packagefs.MkdirAllNoSymlink(target, 0700); err != nil {
		return err
	}
	skip := map[string]bool{}
	for _, name := range exclude {
		skip[name] = true
	}
	var total int64
	files := 0
	return filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, name)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("扩展快照路径无效")
		}
		if rel == "." {
			return nil
		}
		if skip[rel] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := packagefs.LstatNoSymlink(name)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if info.IsDir() {
			files++
			if files > 20000 {
				return errors.New("扩展快照条目超过限制")
			}
			return packagefs.MkdirAllNoSymlink(destination, 0700)
		}
		files++
		total += info.Size()
		if !info.Mode().IsRegular() || files > 20000 || total > 512<<20 {
			return errors.New("扩展快照包含特殊文件或超过 512 MiB 限制")
		}
		in, _, err := packagefs.OpenRegularNoSymlink(name)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		written, err := io.Copy(out, io.LimitReader(in, info.Size()+1))
		if err == nil && written != info.Size() {
			err = errors.New("扩展快照文件在复制时发生变化")
		}
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}

type journal struct {
	Schema int    `json:"schema"`
	Slug   string `json:"slug"`
}

type Transaction struct {
	root, dir, slug string
	Stage           string
	committed       bool
}

func Begin(root, slug string) (*Transaction, error) {
	if err := packagefs.ValidateSlug(slug); err != nil {
		return nil, err
	}
	if info, err := packagefs.LstatNoSymlink(root); err != nil || !info.IsDir() {
		return nil, errors.New("扩展目录不可用")
	}
	dir := filepath.Join(root, ".ota-"+slug)
	if err := os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("无法准备更新，可能存在待恢复的事务: %w", err)
	}
	t := &Transaction{root: root, dir: dir, slug: slug, Stage: filepath.Join(dir, "new")}
	if err := WriteJSON(filepath.Join(dir, "transaction.json"), journal{1, slug}); err != nil {
		t.Close()
		return nil, err
	}
	if err := os.Mkdir(t.Stage, 0700); err != nil {
		t.Close()
		return nil, err
	}
	return t, nil
}

// Commit is called under the owning manager's lifecycle lock. Everything
// needed by the next version, including the rollback snapshot, is staged first.
func (t *Transaction) Commit() error {
	target, old := filepath.Join(t.root, t.slug), filepath.Join(t.dir, "old")
	if info, err := packagefs.LstatNoSymlink(target); err != nil || !info.IsDir() {
		return errors.New("原扩展目录不可用")
	}
	if err := syncTree(t.Stage); err != nil {
		return err
	}
	if err := os.Rename(target, old); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if err := t.restore(); err != nil {
			return fmt.Errorf("更新中断且恢复待完成，请重启服务: %v (原错误: %w)", err, cause)
		}
		return cause
	}
	if err := syncDir(t.root); err != nil {
		return rollback(err)
	}
	if err := syncDir(t.dir); err != nil {
		return rollback(err)
	}
	if err := os.Rename(t.Stage, target); err != nil {
		return rollback(err)
	}
	if err := syncDir(t.root); err != nil {
		return rollback(err)
	}
	if err := WriteJSON(filepath.Join(t.dir, "committed.json"), journal{1, t.slug}); err != nil {
		return rollback(err)
	}
	t.committed = true
	// The durable commit is authoritative even if cleanup must wait for startup.
	_ = os.RemoveAll(t.dir)
	_ = syncDir(t.root)
	return nil
}

func (t *Transaction) restore() error {
	old, target := filepath.Join(t.dir, "old"), filepath.Join(t.root, t.slug)
	if info, err := packagefs.LstatNoSymlink(old); err != nil || !info.IsDir() {
		return errors.New("更新恢复快照不可用")
	}
	if info, err := packagefs.LstatNoSymlink(target); err == nil {
		if !info.IsDir() {
			return errors.New("扩展恢复目标不是目录")
		}
		failed := filepath.Join(t.dir, "failed")
		if _, err := os.Lstat(failed); !os.IsNotExist(err) {
			return errors.New("更新恢复目标已占用")
		}
		if err := os.Rename(target, failed); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(old, target); err != nil {
		return err
	}
	if err := syncDir(t.root); err != nil {
		return err
	}
	return os.RemoveAll(t.dir)
}

func (t *Transaction) Close() {
	if t.committed {
		return
	}
	// Never delete the only old installation after an interrupted rename.
	if _, err := os.Lstat(filepath.Join(t.dir, "old")); os.IsNotExist(err) {
		_ = os.RemoveAll(t.dir)
	}
}

func Recover(root string) error {
	if info, err := packagefs.LstatNoSymlink(root); err != nil || !info.IsDir() {
		return errors.New("扩展根目录不可用")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".ota-") {
			continue
		}
		slug := strings.TrimPrefix(entry.Name(), ".ota-")
		dir := filepath.Join(root, entry.Name())
		if packagefs.ValidateSlug(slug) != nil {
			return errors.New("更新事务目录名称无效")
		}
		if info, err := packagefs.LstatNoSymlink(dir); err != nil || !info.IsDir() {
			return errors.New("更新事务目录不可用")
		}
		t := &Transaction{root: root, dir: dir, slug: slug}
		j, journalErr := ReadJSON[journal](filepath.Join(dir, "transaction.json"), 1024)
		oldInfo, oldErr := packagefs.LstatNoSymlink(filepath.Join(dir, "old"))
		if os.IsNotExist(oldErr) {
			// A crash before the first rename left the active installation intact.
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			continue
		}
		if oldErr != nil || !oldInfo.IsDir() || journalErr != nil || j.Schema != 1 || j.Slug != slug {
			return errors.New("更新恢复记录损坏，已保留原始目录")
		}
		commit, commitErr := ReadJSON[journal](filepath.Join(dir, "committed.json"), 1024)
		if commitErr == nil && commit.Schema == 1 && commit.Slug == slug {
			if info, err := packagefs.LstatNoSymlink(filepath.Join(root, slug)); err != nil || !info.IsDir() {
				return errors.New("已提交扩展目录不可用，已保留恢复快照")
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		} else if errors.Is(commitErr, os.ErrNotExist) {
			if err := t.restore(); err != nil {
				return err
			}
		} else {
			return errors.New("更新提交记录损坏，已保留恢复快照")
		}
	}
	return syncDir(root)
}

func syncTree(root string) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := packagefs.LstatNoSymlink(name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return syncDir(name)
		}
		if !info.Mode().IsRegular() {
			return errors.New("更新包含特殊文件")
		}
		f, err := os.OpenFile(name, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		actual, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, actual) {
			f.Close()
			return errors.New("更新文件在同步时发生变化")
		}
		err = f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
