package community

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/audit"
)

func TestCircleRichLifecycleReportsArchiveAndFiles(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			root := t.TempDir()
			path := ""
			if disk {
				path = filepath.Join(root, "community.json")
			}
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			files, _ := NewFiles(filepath.Join(root, "files"))
			owner := Actor{ID: 1, Admin: true}
			member := Actor{ID: 2}
			other := Actor{ID: 3}
			now := time.Now().UTC()
			sequence := 0
			apply := func(a Actor, cmd Command, want error) Result {
				t.Helper()
				sequence++
				cmd.Key = fmt.Sprintf("features-key-%016d", sequence)
				if c, e := store.Get(t.Context(), "flow"); e == nil {
					cmd.Version = c.Version
				}
				result, e := store.Apply(t.Context(), "flow", a, cmd, now)
				if !errors.Is(e, want) {
					t.Fatalf("%s: %v want %v", cmd.Action, e, want)
				}
				return result
			}
			apply(owner, Command{Action: "create", Name: "图文圈子", JoinPolicy: "open", Visibility: "members", OwnerID: 1}, nil)
			apply(member, Command{Action: "join"}, nil)
			file, err := files.Stage(t.Context(), "flow", "attachment-test-key", 2, "hello.txt", strings.NewReader("hello attachment"))
			if err != nil {
				t.Fatal(err)
			}
			added := apply(member, Command{Action: "add_attachment", Attachment: &file}, nil)
			topic := apply(member, Command{Action: "topic", Title: "正文", Body: "**Markdown**", Kind: "question", Format: "markdown", Attachments: []string{added.AttachmentID}}, nil)
			c, _ := store.Get(t.Context(), "flow")
			bound, _ := AttachmentByID(c, file.ID)
			if !CanReadAttachment(c, bound, member) || CanReadAttachment(c, bound, other) {
				t.Fatal("attachment permission")
			}
			apply(member, Command{Action: "remove_attachment", AttachmentID: file.ID}, ErrConflict)
			apply(other, Command{Action: "report", TopicID: topic.TopicID, Category: "spam", Reason: "广告"}, ErrForbidden)
			report := apply(member, Command{Action: "report", TopicID: topic.TopicID, Category: "spam", Reason: "请检查此内容"}, nil)
			apply(member, Command{Action: "report", TopicID: topic.TopicID, Category: "spam", Reason: "重复举报"}, ErrConflict)
			yes := true
			apply(owner, Command{Action: "archive_topic", TopicID: topic.TopicID, Enabled: &yes}, nil)
			apply(member, Command{Action: "reply", TopicID: topic.TopicID, Body: "归档后回复"}, ErrForbidden)
			apply(owner, Command{Action: "delete_topic", TopicID: topic.TopicID}, ErrConflict)
			apply(member, Command{Action: "review_report", ReportID: report.ReportID, Status: "resolved", Note: "无权审核"}, ErrForbidden)
			apply(owner, Command{Action: "review_report", ReportID: report.ReportID, Status: "resolved", Note: "已核实，隐藏内容", Enabled: &yes}, nil)
			c, _ = store.Get(t.Context(), "flow")
			if CanReadAttachment(c, bound, member) || !CanReadAttachment(c, bound, owner) {
				t.Fatal("hidden file permission")
			}
			apply(owner, Command{Action: "delete_topic", TopicID: topic.TopicID}, nil)
			c, _ = store.Get(t.Context(), "flow")
			if len(c.Topics) != 0 || len(c.Attachments) != 0 || len(c.Reports) != 1 || c.Reports[0].Status != "resolved" {
				t.Fatal(c)
			}
			apply(owner, Command{Action: "clear_reports"}, nil)
			apply(owner, Command{Action: "archive_circle", Enabled: &yes}, nil)
			apply(other, Command{Action: "join"}, ErrForbidden)
			apply(owner, Command{Action: "topic", Title: "归档", Body: "不能发布", Kind: "discussion"}, ErrForbidden)
			c, _ = store.Get(t.Context(), "flow")
			if len(c.Changes) == 0 || len(c.Activities) == 0 {
				t.Fatal("missing durable history/outbox")
			}
			if disk {
				store, err = NewFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				c, _ = store.Get(t.Context(), "flow")
				if !c.Archived || len(c.Activities) == 0 {
					t.Fatal("restart lost features")
				}
			}
			events, err := store.PendingActivity(t.Context(), now, 100)
			if err != nil || len(events) == 0 {
				t.Fatal(events, err)
			}
			version := c.Version
			for _, event := range events {
				if err = store.AckActivity(t.Context(), event.Key); err != nil {
					t.Fatal(err)
				}
			}
			c, _ = store.Get(t.Context(), "flow")
			if c.Version != version || len(c.Activities) != 0 {
				t.Fatal("ack changed business version")
			}
		})
	}
}

type uncertainUploadStore struct {
	Store
	fail bool
}

func (s *uncertainUploadStore) Apply(ctx context.Context, slug string, a Actor, cmd Command, now time.Time) (Result, error) {
	result, err := s.Store.Apply(ctx, slug, a, cmd, now)
	if err == nil && s.fail {
		s.fail = false
		return Result{}, errors.New("lost commit acknowledgement")
	}
	return result, err
}

func TestCircleUploadFailureRecoveryAndOrphanCollection(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store, _ := NewFileStore(filepath.Join(root, "community.json"))
	files, _ := NewFiles(filepath.Join(root, "objects"))
	owner := Actor{ID: 1, Admin: true}
	now := time.Now().UTC()
	created, err := store.Apply(ctx, "files", owner, Command{Action: "create", Key: "files-create-key-123", Name: "附件恢复", JoinPolicy: "open", Visibility: "members", OwnerID: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	rejected, _, err := files.Upload(ctx, store, "files", owner, "files-conflict-key-123", created.Version+1, "draft.txt", strings.NewReader("draft"), now)
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = files.Read(ctx, rejected); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected upload left object", err)
	}
	ambiguous := &uncertainUploadStore{Store: store, fail: true}
	file, _, err := files.Upload(ctx, ambiguous, "files", owner, "files-uncertain-key-123", created.Version, "draft.txt", strings.NewReader("draft"), now)
	if err == nil {
		t.Fatal("expected lost acknowledgement")
	}
	if _, err = files.Read(ctx, file); err != nil {
		t.Fatal("committed object removed", err)
	}
	_, _, err = files.Upload(ctx, ambiguous, "files", owner, "files-another-key-123", created.Version, "new.txt", strings.NewReader("new"), now)
	if err == nil {
		t.Fatal("unresolved commit allowed unbounded staging")
	}
	file, receipt, err := files.Upload(ctx, ambiguous, "files", owner, "files-uncertain-key-123", created.Version, "draft.txt", strings.NewReader("draft"), now)
	if err != nil || !receipt.Replay {
		t.Fatal("retry", receipt, err)
	}
	orphan, err := files.Stage(ctx, "files", "files-orphan-key-123", 1, "orphan.txt", strings.NewReader("orphan"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, _ := NewFiles(filepath.Join(root, "objects"))
	if err = reopened.Reconcile(ctx, store); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Read(ctx, file); err != nil {
		t.Fatal("referenced object collected", err)
	}
	if _, err = reopened.Read(ctx, orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan retained", err)
	}
	_, err = store.Apply(ctx, "files", owner, Command{Action: "remove_attachment", Key: "files-remove-key-123", Version: receipt.Version, AttachmentID: file.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Reconcile(ctx, store); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Read(ctx, file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed deletion not recovered", err)
	}
}

func TestCircleAttachmentsRejectUnsafeTypesAndMarkdown(t *testing.T) {
	f, _ := NewFiles("")
	for _, payload := range []string{"<svg onload=alert(1)></svg>", "<!DOCTYPE html><script>alert(1)</script>", ""} {
		if _, err := f.Stage(t.Context(), "safe", "safe-upload-key-123", 1, "unsafe.svg", strings.NewReader(payload)); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	file, err := f.Stage(t.Context(), "safe", "safe-upload-key-123", 1, "valid.txt", strings.NewReader("plain"))
	if err != nil {
		t.Fatal(err)
	}
	file.CreatedAt = time.Now()
	data, err := f.Read(t.Context(), file)
	if err != nil || !bytes.Equal(data, []byte("plain")) {
		t.Fatal(err)
	}
	if _, err = f.Stage(t.Context(), "safe", "safe-upload-key-123", 1, "valid.txt", strings.NewReader("different")); !errors.Is(err, ErrConflict) {
		t.Fatal("key reused", err)
	}
	file.MIME = "image/png"
	source := "**bold**\n\n![allowed](attachment:" + file.ID + ")\n\n![tracking](https://evil.test/t.png)\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))"
	output := RenderBody("safe", source, "markdown", []Attachment{file})
	if !strings.Contains(output, "<strong>bold</strong>") || !strings.Contains(output, "/attachments/"+file.ID) || strings.Contains(output, "evil.test") || strings.Contains(output, "<script") || strings.Contains(output, "javascript:") {
		t.Fatal(output)
	}
	if got := RenderBody("safe", "<b>plain</b>", "", nil); strings.Contains(got, "<b>") {
		t.Fatal(got)
	}
}

func TestCircleAuditedOutboxSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	ledger, err := audit.Open(filepath.Join(root, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "community.json")
	if err = ledger.EnsureBaselinePath(audit.FactCommunity, path); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path, ledger)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Apply(t.Context(), "audit", Actor{ID: 1, Admin: true}, Command{Action: "create", Key: "audit-create-key-123", Name: "审计", JoinPolicy: "open", Visibility: "public", OwnerID: 1}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = ledger.ResolvePaths(map[string]string{audit.FactCommunity: path}); err != nil {
		t.Fatal(err)
	}
	page, err := ledger.List(t.Context(), audit.Query{Action: "community.create"})
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
}
