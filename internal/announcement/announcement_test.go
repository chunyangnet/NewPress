package announcement

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentNoticesConflictPublicationAndOrdering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "announcements.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.Snapshot(t.Context())
	now := time.Now().UTC()
	first := state.Revision
	for _, item := range []Item{
		{Title: "普通公告", Body: "第一段\n第二段", Date: now.Add(-time.Hour), Published: true},
		{Title: "置顶公告", Date: now.Add(-48 * time.Hour), Published: true, Pinned: true, Order: 4},
		{Title: "隐藏哨兵", Date: now.Add(-time.Hour)},
		{Title: "未来哨兵", Date: now.Add(48 * time.Hour), Published: true},
	} {
		state, err = s.Save(t.Context(), state.Revision, "save", item)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Save(t.Context(), first, "delete", Item{ID: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	loaded, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loaded.Snapshot(t.Context())
	if err != nil || snapshot.Revision != state.Revision || len(snapshot.Items) != 4 {
		t.Fatal(snapshot, err)
	}
	public := Public(snapshot, now)
	if len(public) != 2 || public[0].Title != "置顶公告" || public[1].Title != "普通公告" {
		t.Fatal(public)
	}
	snapshot.Items[0].Title = "修改副本"
	fresh, _ := loaded.Snapshot(t.Context())
	if fresh.Items[0].Title == "修改副本" {
		t.Fatal("snapshot aliases store")
	}
	state, err = loaded.Save(t.Context(), fresh.Revision, "delete", Item{ID: 1})
	if err != nil || len(state.Items) != 3 {
		t.Fatal(state, err)
	}
	if _, err = loaded.Save(t.Context(), state.Revision, "delete", Item{ID: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestNoticeValidationAndCorruptSnapshot(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "//evil.test/x", "/\\evil.test", "https://user:secret@example.test/", "data:text/html,x", "/a\nHeader:x", "/%2f/evil.test"} {
		if ValidURL(raw) {
			t.Errorf("accepted unsafe URL %q", raw)
		}
	}
	for _, raw := range []string{"", "/announcements/", "/pages/notice/?q=a&b=2", "https://example.test/notice"} {
		if !ValidURL(raw) {
			t.Errorf("rejected URL %q", raw)
		}
	}
	path := filepath.Join(t.TempDir(), "announcements.json")
	s, _ := NewFileStore(path)
	state, _ := s.Snapshot(t.Context())
	for _, i := range []Item{{Title: "", Date: time.Now()}, {Title: "超出排序", Date: time.Now(), Order: -1}, {Title: "缺少日期"}} {
		if _, err := s.Save(t.Context(), state.Revision, "save", i); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"schema":1,"schema":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err == nil {
		t.Fatal("accepted corrupt or duplicate document")
	}
}

type documentRepository struct{ raw []byte }

func (r *documentRepository) Load(context.Context) ([]byte, bool, error) {
	return bytes.Clone(r.raw), r.raw != nil, nil
}
func (r *documentRepository) Create(_ context.Context, _ string, raw []byte) (bool, error) {
	if r.raw != nil {
		return false, nil
	}
	r.raw = bytes.Clone(raw)
	return true, nil
}
func (r *documentRepository) CompareAndSwap(_ context.Context, expected, _ string, raw []byte) (bool, error) {
	s, err := decode(r.raw)
	if err != nil {
		return false, err
	}
	if s.Revision != expected {
		return false, nil
	}
	r.raw = bytes.Clone(raw)
	return true, nil
}

func TestRepositoryInstancesObserveCommittedWrites(t *testing.T) {
	repository := &documentRepository{}
	a, _ := NewRepositoryStore(t.Context(), repository)
	b, _ := NewRepositoryStore(t.Context(), repository)
	initial, _ := a.Snapshot(t.Context())
	next, err := a.Save(t.Context(), initial.Revision, "save", Item{Title: "数据库公告", Date: time.Now(), Published: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Save(t.Context(), initial.Revision, "delete", Item{ID: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	current, err := b.Snapshot(t.Context())
	if err != nil || current.Revision != next.Revision || len(current.Items) != 1 {
		t.Fatal(current, err)
	}
}
