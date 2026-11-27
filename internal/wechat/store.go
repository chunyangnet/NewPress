// Package wechat owns the optional official-account connection and durable sync jobs.
package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

var ErrConflict = errors.New("公众号配置或任务已变化，请刷新后重试")
var ErrDisabled = errors.New("请先配置并开启公众号同步")
var ErrBusy = errors.New("同一篇文章已有任务正在处理，请先查看同步记录")

const MaxJobs = 5000

type Config struct {
	Version        int64     `json:"version"`
	Enabled        bool      `json:"enabled"`
	CollectEnabled bool      `json:"collect_enabled"`
	CollectDrafts  bool      `json:"collect_drafts,omitempty"`
	AppID          string    `json:"app_id"`
	Secret         string    `json:"app_secret,omitempty"`
	HasSecret      bool      `json:"has_secret,omitempty"`
	AutoSync       bool      `json:"auto_sync"`
	AutoPublish    bool      `json:"auto_publish"`
	IncludeSource  bool      `json:"include_source"`
	Author         string    `json:"author"`
	ThumbMediaID   string    `json:"thumb_media_id"`
	OpenComment    bool      `json:"open_comment"`
	FansComment    bool      `json:"fans_comment"`
	Since          time.Time `json:"since"`
}

func Defaults() Config          { return Config{Version: 1, CollectEnabled: true, IncludeSource: true} }
func (c Config) Public() Config { c.HasSecret = c.Secret != ""; c.Secret = ""; return c }
func (c Config) Validate() error {
	if c.Version < 1 || (c.AppID != "" && !regexp.MustCompile(`^wx[0-9a-fA-F]{16}$`).MatchString(c.AppID)) {
		return errors.New("AppID 应为 wx 开头的 18 位标识")
	}
	if len(c.Secret) > 128 || (c.Secret != "" && len(c.Secret) < 16) || strings.ContainsAny(c.Secret, "\r\n\x00 ") {
		return errors.New("AppSecret 格式不正确")
	}
	if utf8.RuneCountInString(c.Author) > 16 || len(c.ThumbMediaID) > 128 || strings.ContainsAny(c.ThumbMediaID, "\r\n\x00") {
		return errors.New("作者最多 16 字，封面素材 ID 最多 128 字节")
	}
	if c.Enabled && (c.AppID == "" || c.Secret == "") {
		return errors.New("开启同步前请填写 AppID 和 AppSecret")
	}
	return nil
}

type Job struct {
	ID          string            `json:"id"`
	AppID       string            `json:"app_id"`
	PostID      int64             `json:"post_id"`
	Version     int64             `json:"version"`
	Title       string            `json:"title"`
	Actor       string            `json:"actor"`
	AutoPublish bool              `json:"auto_publish"`
	Status      string            `json:"status"`
	Stage       string            `json:"stage"`
	MediaID     string            `json:"media_id,omitempty"`
	PublishID   string            `json:"publish_id,omitempty"`
	ArticleID   string            `json:"article_id,omitempty"`
	ArticleURL  string            `json:"article_url,omitempty"`
	Error       string            `json:"error,omitempty"`
	Attempts    int               `json:"attempts"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	NextAt      time.Time         `json:"next_at"`
	LeaseUntil  time.Time         `json:"lease_until"`
	Images      map[string]string `json:"images,omitempty"`
}

func (j Job) Public() Job { j.Images = nil; return j }

type State struct {
	Schema int    `json:"schema"`
	Config Config `json:"config"`
	Jobs   []Job  `json:"jobs"`
}

func NewState() State { return State{Schema: 1, Config: Defaults(), Jobs: []Job{}} }
func Decode(b []byte) (State, error) {
	var s State
	if len(b) > 16<<20 {
		return s, errors.New("公众号存储超过容量限制")
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	return s, Validate(s)
}
func Validate(s State) error {
	if s.Schema != 1 || len(s.Jobs) > MaxJobs {
		return errors.New("公众号存储格式或容量无效")
	}
	if err := s.Config.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, j := range s.Jobs {
		if len(j.ID) != 64 || seen[j.ID] || j.PostID <= 0 || j.Version < 1 || j.AppID == "" || j.CreatedAt.IsZero() || len(j.Images) > 45 {
			return errors.New("公众号任务记录无效")
		}
		switch j.Status {
		case "queued", "working", "draft", "publishing", "published", "failed", "uncertain", "skipped":
		default:
			return errors.New("公众号任务状态无效")
		}
		switch j.Stage {
		case "prepare", "draft", "publish", "poll":
		default:
			return errors.New("公众号任务阶段无效")
		}
		seen[j.ID] = true
	}
	return nil
}

type Store interface {
	ViewConfig(context.Context) (Config, error)
	View(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}

// ReadConfig avoids copying the full job history at repeated operation guards.
func ReadConfig(ctx context.Context, store Store) (Config, error) {
	return store.ViewConfig(ctx)
}

type FileStore struct {
	mu    sync.Mutex
	path  string
	state State
}

// The application holds its existing data-directory lock for this store's lifetime.
func NewFileStore(path string) (*FileStore, error) {
	f := &FileStore{path: path, state: NewState()}
	if path == "" {
		return f, nil
	}
	if info, err := packagefs.LstatNoSymlink(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if err == nil && (!info.Mode().IsRegular() || info.Size() > 16<<20) {
		return nil, errors.New("公众号存储文件无效或过大")
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	f.state, err = Decode(b)
	return f, err
}
func clone(s State) State { b, _ := json.Marshal(s); var c State; _ = json.Unmarshal(b, &c); return c }
func (f *FileStore) ViewConfig(ctx context.Context) (Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	return f.state.Config, nil
}

func (f *FileStore) View(ctx context.Context) (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	return clone(f.state), nil
}
func (f *FileStore) Update(ctx context.Context, fn func(*State) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	candidate := clone(f.state)
	if err := fn(&candidate); err != nil {
		return err
	}
	if err := Validate(candidate); err != nil {
		return err
	}
	if reflect.DeepEqual(candidate, f.state) {
		return nil
	}
	if f.path != "" {
		if err := packagefs.MkdirAllNoSymlink(filepath.Dir(f.path), 0700); err != nil {
			return err
		}
		if _, err := packagefs.LstatNoSymlink(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b, err := json.Marshal(candidate)
		if err != nil {
			return err
		}
		if len(b) > 16<<20 {
			return fmt.Errorf("公众号记录容量已满")
		}
		tmp, err := os.CreateTemp(filepath.Dir(f.path), ".wechat-*")
		if err != nil {
			return err
		}
		name := tmp.Name()
		defer os.Remove(name)
		if err = tmp.Chmod(0600); err == nil {
			_, err = tmp.Write(b)
		}
		if err == nil {
			err = tmp.Sync()
		}
		closeErr := tmp.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(name, f.path); err != nil {
			return err
		}
	}
	f.state = candidate
	return nil
}
