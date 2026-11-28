package aiworkbench

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

type Report struct {
	ID        string    `json:"id"`
	Owner     int64     `json:"owner"`
	Task      string    `json:"task"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Content   string    `json:"content,omitempty"`
	Source    *Preview  `json:"source,omitempty"`
}

type reportDocument struct {
	Version int      `json:"version"`
	Reports []Report `json:"reports"`
}
type Store struct {
	mu      sync.Mutex
	path    string
	reports []Report
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, reports: []Report{}}
	if path == "" {
		return s, nil
	}
	f, _, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, errors.New("无法读取 AI 报告")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return nil, errors.New("AI 报告文件过大或读取失败")
	}
	var doc reportDocument
	if json.Unmarshal(data, &doc) != nil || doc.Version != 1 || len(doc.Reports) > MaxReports {
		return nil, errors.New("AI 报告格式无效")
	}
	seen := map[string]bool{}
	for _, report := range doc.Reports {
		if !validReport(report) || seen[report.ID] {
			return nil, errors.New("AI 报告校验失败")
		}
		seen[report.ID] = true
	}
	s.reports = doc.Reports
	return s, nil
}

func validReport(r Report) bool {
	_, taskOK := FindTask(r.Task)
	_, idErr := hex.DecodeString(r.ID)
	return taskOK && idErr == nil && len(r.ID) == 32 && r.Owner >= 0 && !r.CreatedAt.IsZero() && len(r.Title) <= 500 && len(r.Model) <= 200 && len(r.Provider) <= 300 && strings.TrimSpace(r.Content) != "" && utf8.ValidString(r.Content) && len(r.Content) <= MaxReportBytes && r.Source != nil && len(r.Source.Context) <= MaxContextBytes && utf8.ValidString(r.Source.Context) && r.Source.Task == r.Task
}

func cloneReport(r Report) Report {
	if r.Source != nil {
		source := r.Source.WithSourceCheck(nil)
		r.Source = &source
	}
	return r
}
func retained(r Report, now time.Time) bool {
	return r.CreatedAt.After(now.AddDate(0, 0, -RetentionDays))
}

func (s *Store) Save(r Report, now time.Time) error {
	return s.SaveChecked(r, now, nil)
}

// SaveChecked refreshes authority after waiting for the lock and after encoding,
// before persistence starts. The callback must not call back into this Store.
func (s *Store) SaveChecked(r Report, now time.Time, check func() error) error {
	if !validReport(r) {
		return errors.New("AI 报告内容无效或过长")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	next := []Report{cloneReport(r)}
	for _, old := range s.reports {
		if old.ID == r.ID {
			return errors.New("AI 报告已保存")
		}
		if retained(old, now) && len(next) < MaxReports {
			next = append(next, old)
		}
	}
	if err := s.persist(next, check); err != nil {
		return err
	}
	s.reports = next
	return nil
}

func (s *Store) List(owner int64, now time.Time) []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []Report{}
	for _, r := range s.reports {
		if r.Owner == owner && retained(r, now) {
			r.Content, r.Source = "", nil
			result = append(result, r)
		}
	}
	return result
}

func (s *Store) Get(owner int64, id string, now time.Time) (Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.reports {
		if r.ID == id && r.Owner == owner && retained(r, now) {
			return cloneReport(r), nil
		}
	}
	return Report{}, ErrNotFound
}

func (s *Store) Delete(owner int64, id string, now time.Time) error {
	return s.DeleteChecked(owner, id, now, nil)
}

// DeleteChecked has the same callback/locking contract as SaveChecked.
func (s *Store) DeleteChecked(owner int64, id string, now time.Time, check func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	found := false
	next := []Report{}
	for _, r := range s.reports {
		if r.ID == id && r.Owner == owner && retained(r, now) {
			found = true
			continue
		}
		if retained(r, now) {
			next = append(next, r)
		}
	}
	if !found {
		return ErrNotFound
	}
	if err := s.persist(next, check); err != nil {
		return err
	}
	s.reports = next
	return nil
}

func (s *Store) persist(reports []Report, check func() error) error {
	if s.path == "" {
		if check != nil {
			return check()
		}
		return nil
	}
	data, err := json.Marshal(reportDocument{1, reports})
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("AI 报告存储已达上限")
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	if err := packagefs.MkdirAllNoSymlink(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".ai-reports-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return packagefs.ReplaceRegularFileNoSymlink(f.Name(), s.path)
}
