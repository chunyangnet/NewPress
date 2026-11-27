package content

import (
	"newpress.local/newpress/internal/activity"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicationOutboxSurvivesEditsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.json")
	s, err := NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	input := PostInput{AuthorID: 2, Title: "通知测试", Slug: "durable-publication", Status: StatusFuture, PublishedAt: time.Now().Add(time.Hour)}
	p, err := s.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingActivity(t.Context(), time.Now(), 50)
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	input.Title = "发布前更新标题"
	p, err = s.Update(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	pending, err = s.PendingActivity(t.Context(), input.PublishedAt.Add(time.Second), 50)
	if err != nil || len(pending) != 2 || pending[0].Kind != activity.Publication || pending[1].Kind != activity.ContentUpdated || pending[1].Version != p.Version {
		t.Fatal(pending, err)
	}
	if err = s.AckActivity(t.Context(), pending[0].Key); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.PendingActivity(t.Context(), input.PublishedAt.Add(time.Second), 50)
	if err != nil || len(remaining) != 1 || remaining[0].Key != pending[1].Key {
		t.Fatal("publication ACK removed update event", remaining, err)
	}
	if err = s.AckActivity(t.Context(), pending[1].Key); err != nil {
		t.Fatal(err)
	}
	s, err = NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingActivity(t.Context(), input.PublishedAt.Add(time.Second), 50)
	if len(pending) != 0 {
		t.Fatal("ack lost")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("changed externally"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeStatus(t.Context(), p.ID, Mutation{ExpectedVersion: p.Version}, StatusPublish); err == nil {
		t.Fatal("write should fail")
	}
	pending, _ = s.PendingActivity(t.Context(), time.Now().Add(2*time.Hour), 50)
	if len(pending) != 0 {
		t.Fatal("event escaped failed transaction")
	}
	if err = os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
}
