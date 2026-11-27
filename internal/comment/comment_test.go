package comment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileStoreLifecycleModerationPersistenceAndCascade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "comments.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.Create(ctx, CreateInput{
		PostID: 1, AuthorName: " Alice ", AuthorEmail: "ALICE@example.test", Status: StatusApproved,
		ContentSource: "第一段 <script>alert(1)</script>\n\n第二段", AuthorIPHash: strings.Repeat("a", 64),
		UserAgent: "Test Browser/1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if root.ID != 1 || root.Version != 1 || root.AuthorName != "Alice" || root.AuthorEmail != "alice@example.test" {
		t.Fatalf("root comment = %#v", root)
	}
	if strings.Contains(root.ContentHTML, "<script>") || !strings.Contains(root.ContentHTML, "&lt;script&gt;") || !strings.Contains(root.ContentHTML, "</p><p>") {
		t.Fatalf("rendered comment HTML = %q", root.ContentHTML)
	}

	child, err := store.Create(ctx, CreateInput{
		PostID: 1, ParentID: root.ID, AuthorName: "Bob", AuthorEmail: "bob@example.test",
		Status: StatusPending, ContentSource: "等待审核的回复",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, CreateInput{
		PostID: 2, ParentID: root.ID, AuthorName: "Cross", AuthorEmail: "cross@example.test",
		Status: StatusPending, ContentSource: "跨文章回复",
	}); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("cross-post parent error = %v", err)
	}
	public, total, err := store.ListApproved(ctx, 1, 50, 0)
	if err != nil || total != 1 || len(public) != 1 || public[0].ID != root.ID {
		t.Fatalf("approved list = %#v total=%d err=%v", public, total, err)
	}
	approvedChild, err := store.ChangeStatus(ctx, child.ID, Mutation{ExpectedVersion: child.Version, Actor: "qaadmin"}, StatusApproved)
	if err != nil || approvedChild.Version != 2 || approvedChild.ModeratedBy != "qaadmin" || approvedChild.ModeratedAt.IsZero() {
		t.Fatalf("approved child = %#v, %v", approvedChild, err)
	}
	if _, err := store.ChangeStatus(ctx, child.ID, Mutation{ExpectedVersion: child.Version, Actor: "stale"}, StatusSpam); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale moderation error = %v", err)
	}

	adminList, err := store.ListAdmin(ctx, Query{Search: "等待审核", Limit: 20})
	if err != nil || adminList.Total != 1 || adminList.Items[0].ID != child.ID || adminList.StatusCounts[StatusApproved] != 2 {
		t.Fatalf("admin comment list = %#v, %v", adminList, err)
	}
	reloaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reloaded.Get(ctx, child.ID)
	if err != nil || loaded.Version != approvedChild.Version || loaded.Status != StatusApproved {
		t.Fatalf("reloaded child = %#v, %v", loaded, err)
	}
	if err := reloaded.Delete(ctx, root.ID, Mutation{ExpectedVersion: root.Version, Actor: "qaadmin"}); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := reloaded.Snapshot(ctx); err != nil || snapshot.Total != 0 {
		t.Fatalf("snapshot after cascade = %#v, %v", snapshot, err)
	}
	if _, err := reloaded.Get(ctx, child.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cascaded child error = %v", err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("comment document permissions = %v", info.Mode().Perm())
	}
}

func TestFileStoreThreadDepthAndSeedFixtures(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileStore("", false)
	if err != nil {
		t.Fatal(err)
	}
	parentID := int64(0)
	for depth := 1; depth <= MaxThreadDepth; depth++ {
		item, err := store.Create(ctx, CreateInput{
			PostID: 1, ParentID: parentID, AuthorName: "Depth", AuthorEmail: "depth@example.test",
			Status: StatusApproved, ContentSource: "层级回复",
		})
		if err != nil {
			t.Fatalf("create depth %d: %v", depth, err)
		}
		parentID = item.ID
	}
	if _, err := store.Create(ctx, CreateInput{
		PostID: 1, ParentID: parentID, AuthorName: "Too deep", AuthorEmail: "deep@example.test",
		Status: StatusApproved, ContentSource: "超过层级",
	}); !errors.Is(err, ErrInvalidParent) {
		t.Fatalf("deep parent error = %v", err)
	}

	seeded, err := NewFileStore("", true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := seeded.Snapshot(ctx)
	if err != nil || snapshot.Total != 4 || snapshot.StatusCounts[StatusApproved] != 4 {
		t.Fatalf("seed snapshot = %#v, %v", snapshot, err)
	}
	postOne, total, err := seeded.ListApproved(ctx, 1, 50, 0)
	if err != nil || total != 2 || postOne[1].ParentID != postOne[0].ID {
		t.Fatalf("seeded post comments = %#v total=%d err=%v", postOne, total, err)
	}
}

func TestFileStoreSafeEditStaffReplyRevisionAndReload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "comments.json")
	legacy := `{"schema":3,"kind":"newpress/comments","next_id":2,"updated_at":"2025-08-12T00:00:00Z","items":[{"id":1,"post_id":1,"version":1,"author_name":"原作者","author_email":"before@example.test","status":"approved","content_source":"原始正文","content_html":"<p>原始正文</p>","created_at":"2025-08-12T00:00:00Z","updated_at":"2025-08-12T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := store.Edit(ctx, 1, Mutation{ExpectedVersion: 1, Actor: "chief-editor"}, EditInput{
		AuthorName: "修订作者", AuthorEmail: "AFTER@example.test", ContentSource: "修订正文 <script>alert(1)</script>",
	})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Version != 2 || edited.AuthorEmail != "after@example.test" || edited.EditedBy != "chief-editor" || edited.EditedAt.IsZero() ||
		strings.Contains(edited.ContentHTML, "<script>") || !strings.Contains(edited.ContentHTML, "&lt;script&gt;") {
		t.Fatalf("edited comment = %#v", edited)
	}
	if _, err := store.Edit(ctx, 1, Mutation{ExpectedVersion: 1, Actor: "stale-editor"}, EditInput{
		AuthorName: "冲突", AuthorEmail: "conflict@example.test", ContentSource: "不能覆盖",
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale edit error = %v", err)
	}
	revisions, err := store.ListRevisions(ctx, 1, 20)
	if err != nil || len(revisions) != 1 || revisions[0].Version != 1 || revisions[0].Actor != "chief-editor" || revisions[0].ContentSource != "原始正文" {
		t.Fatalf("revisions = %#v, err=%v", revisions, err)
	}
	unchanged, err := store.Edit(ctx, 1, Mutation{ExpectedVersion: 2, Actor: "chief-editor"}, EditInput{
		AuthorName: edited.AuthorName, AuthorEmail: edited.AuthorEmail, ContentSource: edited.ContentSource,
	})
	if err != nil || unchanged.Version != 2 {
		t.Fatalf("idempotent edit = %#v, err=%v", unchanged, err)
	}
	if revisionsAfter, err := store.ListRevisions(ctx, 1, 20); err != nil || len(revisionsAfter) != 1 {
		t.Fatalf("idempotent edit revisions = %#v, err=%v", revisionsAfter, err)
	}

	if _, err := store.Reply(ctx, ReplyInput{
		ParentID: 1, ExpectedParentVersion: 1, AuthorName: "站方", AuthorEmail: "staff@example.test",
		ContentSource: "过期回复", Actor: "chief-editor",
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale staff reply error = %v", err)
	}
	reply, err := store.Reply(ctx, ReplyInput{
		ParentID: 1, ExpectedParentVersion: 2, UserID: 7, AuthorName: "站方编辑部", AuthorEmail: "staff@example.test",
		ContentSource: "这是站方回复。", Actor: "chief-editor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ParentID != 1 || reply.PostID != 1 || reply.Status != StatusApproved || !reply.StaffReply || reply.CreatedBy != "chief-editor" ||
		reply.ModeratedBy != "chief-editor" || reply.UserID != 7 || reply.Version != 1 {
		t.Fatalf("staff reply = %#v", reply)
	}

	reloaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reloaded.Get(ctx, reply.ID)
	if err != nil || !loaded.StaffReply || loaded.CreatedBy != "chief-editor" {
		t.Fatalf("reloaded staff reply = %#v, err=%v", loaded, err)
	}
	loadedRevisions, err := reloaded.ListRevisions(ctx, 1, 20)
	if err != nil || len(loadedRevisions) != 1 || loadedRevisions[0].Version != 1 {
		t.Fatalf("reloaded revisions = %#v, err=%v", loadedRevisions, err)
	}
	payload, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(payload), `"schema": 3`) || !strings.Contains(string(payload), `"revisions"`) {
		t.Fatalf("upgraded document = %s, err=%v", payload, err)
	}
	if err := reloaded.Delete(ctx, 1, Mutation{ExpectedVersion: 2, Actor: "chief-editor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.ListRevisions(ctx, 1, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted revision owner error = %v", err)
	}
}

func TestFileStoreRestoreRevisionPreservesModerationAndOptimisticLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "comments.json")
	store, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(ctx, CreateInput{
		PostID: 1, AuthorName: "原始称呼", AuthorEmail: "original@example.test", Status: StatusApproved,
		ContentSource: "原始正文 <script>restore()</script>",
	})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := store.Edit(ctx, created.ID, Mutation{ExpectedVersion: created.Version, Actor: "editor"}, EditInput{
		AuthorName: "当前称呼", AuthorEmail: "current@example.test", ContentSource: "当前正文",
	})
	if err != nil || edited.Version != 2 {
		t.Fatalf("edited = %#v, err=%v", edited, err)
	}
	pending, err := store.ChangeStatus(ctx, created.ID, Mutation{ExpectedVersion: edited.Version, Actor: "moderator"}, StatusPending)
	if err != nil || pending.Version != 3 || pending.Status != StatusPending {
		t.Fatalf("pending = %#v, err=%v", pending, err)
	}
	if _, err := store.RestoreRevision(ctx, created.ID, 1, Mutation{ExpectedVersion: 2, Actor: "stale-restorer"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale restore error = %v", err)
	}
	if _, err := store.RestoreRevision(ctx, created.ID, 2, Mutation{ExpectedVersion: pending.Version, Actor: "restorer"}); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("missing restore source error = %v", err)
	}
	restored, err := store.RestoreRevision(ctx, created.ID, 1, Mutation{ExpectedVersion: pending.Version, Actor: "restorer"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Version != 4 || restored.Status != StatusPending || restored.AuthorName != "原始称呼" ||
		restored.AuthorEmail != "original@example.test" || restored.ContentSource != "原始正文 <script>restore()</script>" ||
		restored.EditedBy != "restorer" || strings.Contains(restored.ContentHTML, "<script>") ||
		!strings.Contains(restored.ContentHTML, "&lt;script&gt;") {
		t.Fatalf("restored = %#v", restored)
	}
	revisions, err := store.ListRevisions(ctx, created.ID, 20)
	if err != nil || len(revisions) != 2 || revisions[0].Version != 3 || revisions[0].Actor != "restorer" ||
		revisions[0].Status != StatusPending || revisions[0].ContentSource != "当前正文" || revisions[1].Version != 1 {
		t.Fatalf("restore revisions = %#v, err=%v", revisions, err)
	}
	unchanged, err := store.RestoreRevision(ctx, created.ID, 1, Mutation{ExpectedVersion: restored.Version, Actor: "restorer"})
	if err != nil || unchanged.Version != restored.Version {
		t.Fatalf("idempotent restore = %#v, err=%v", unchanged, err)
	}
	if revisionsAfter, err := store.ListRevisions(ctx, created.ID, 20); err != nil || len(revisionsAfter) != 2 {
		t.Fatalf("idempotent restore revisions = %#v, err=%v", revisionsAfter, err)
	}
	reloaded, err := NewFileStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	reloadedItem, err := reloaded.Get(ctx, created.ID)
	if err != nil || reloadedItem.Version != 4 || reloadedItem.Status != StatusPending || reloadedItem.AuthorName != "原始称呼" {
		t.Fatalf("reloaded restored comment = %#v, err=%v", reloadedItem, err)
	}
}

func TestCommentValidationAndMalformedDocuments(t *testing.T) {
	input, fields := NormalizeCreateInput(CreateInput{
		PostID: -1, ParentID: -1, UserID: -1, AuthorName: "\x00", AuthorEmail: "not-email",
		Status: "published", ContentSource: "\x00", AuthorIPHash: "raw-ip", UserAgent: "bad\nagent",
	})
	_ = input
	for _, field := range []string{"post_id", "parent_id", "user_id", "author_name", "author_email", "status", "content_source", "author_ip_hash", "user_agent"} {
		if fields[field] == "" {
			t.Fatalf("missing %s validation in %#v", field, fields)
		}
	}
	if _, err := NormalizeQuery(Query{Search: strings.Repeat("搜", MaxSearchRunes+1)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("long search error = %v", err)
	}

	for name, payload := range map[string]string{
		"unknown":   `{"schema":1,"kind":"newpress/comments","next_id":1,"updated_at":"2026-08-11T00:00:00Z","items":[],"extra":true}`,
		"duplicate": `{"schema":1,"schema":1,"kind":"newpress/comments","next_id":1,"updated_at":"2026-08-11T00:00:00Z","items":[]}`,
		"trailing":  `{"schema":1,"kind":"newpress/comments","next_id":1,"updated_at":"2026-08-11T00:00:00Z","items":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "comments.json")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFileStore(path, false); err == nil {
				t.Fatal("accepted malformed comment document")
			}
		})
	}
}
