package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/navigation"
)

// TestPostgreSQLIntegration is deliberately opt-in so ordinary unit tests do
// not require a local database. CI supplies NEWPRESS_TEST_DATABASE_URL and
// runs this test against a disposable PostgreSQL 18 service.
func TestPostgreSQLIntegration(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// A second run is part of the contract: deployed processes and an explicit
	// `migrate up` command must both be safe after the schema is current.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
	assertMigrations(t, ctx, pool)

	stamp := time.Now().UTC().UnixNano()
	username := fmt.Sprintf("pg-it-%d", stamp)
	email := fmt.Sprintf("pg-it-%d@example.test", stamp)
	postSlug := fmt.Sprintf("pg-integration-%d", stamp)
	navigationSlug := fmt.Sprintf("pg-navigation-%d", stamp)
	categoryName := fmt.Sprintf("PG 集成分类 %d", stamp)
	tagName := fmt.Sprintf("PG 集成标签 %d", stamp)

	var userID, postID, categoryID, tagID, commentID, menuID int64
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		// Fixture names are unique, and every statement is best-effort so a
		// failed assertion cannot hide the original failure with cleanup noise.
		if menuID > 0 {
			_, _ = pool.Exec(cleanupContext, `DELETE FROM np_navigation_menus WHERE id=$1`, menuID)
		}
		if commentID > 0 {
			_, _ = pool.Exec(cleanupContext, `DELETE FROM np_comments WHERE id=$1`, commentID)
		}
		if postID > 0 {
			_, _ = pool.Exec(cleanupContext, `DELETE FROM np_posts WHERE id=$1`, postID)
		}
		if categoryID > 0 || tagID > 0 {
			_, _ = pool.Exec(cleanupContext, `DELETE FROM np_terms WHERE id=ANY($1::bigint[])`, []int64{categoryID, tagID})
		}
		if userID > 0 {
			_, _ = pool.Exec(cleanupContext, `DELETE FROM np_audit_events WHERE actor_label=$1`, username)
			_, _ = pool.Exec(cleanupContext, `DELETE FROM np_users WHERE id=$1`, userID)
		}
	}()

	users := NewUserRepository(pool)
	if err := users.Ready(ctx); err != nil {
		t.Fatalf("user repository readiness: %v", err)
	}
	passwordHash, err := auth.HashPassword("PostgreSQL integration password 2026")
	if err != nil {
		t.Fatal(err)
	}
	user, err := users.CreateUser(ctx, auth.UserMutation{Actor: username}, auth.UserInput{
		Username: username, Email: email, DisplayName: "PG 集成作者", Status: auth.UserStatusActive,
		Locale: "zh-CN", RoleSlugs: []string{"author"},
	}, passwordHash)
	if err != nil {
		t.Fatalf("create persistent author: %v", err)
	}
	userID = user.ID
	if user.Version != 1 || !user.HasRole("author") || !user.Can(auth.CapabilityContentWriteOwn) {
		t.Fatalf("created author projection = %#v", user)
	}
	loadedUser, err := users.FindUserByLogin(ctx, strings.ToUpper(username))
	if err != nil || loadedUser.ID != user.ID {
		t.Fatalf("case-insensitive user lookup = %#v, %v", loadedUser, err)
	}

	posts := NewPostRepository(pool)
	previousPage, err := posts.ListPublishedPage(ctx, 1, 0)
	if err != nil {
		t.Fatalf("list initial published page: %v", err)
	}
	post, err := posts.Create(ctx, content.PostInput{
		AuthorID: user.ID, Type: content.TypePost, Status: content.StatusPublish, Slug: postSlug,
		Title: "PostgreSQL 集成文章", ContentSource: "第一版正文", Category: categoryName,
		TagNames: []string{tagName}, CommentStatus: content.CommentOpen, AuthorName: user.DisplayName,
		Actor: username,
	})
	if err != nil {
		t.Fatalf("create persistent post: %v", err)
	}
	postID = post.ID
	if post.Version != 1 || post.Status != content.StatusPublish || len(post.Categories) != 1 || len(post.Tags) != 1 {
		t.Fatalf("created post projection = %#v", post)
	}
	categoryID = post.Categories[0].ID
	tagID = post.Tags[0].ID
	if post.Categories[0].Name != categoryName || post.Tags[0].Name != tagName {
		t.Fatalf("created taxonomy projection = categories:%#v tags:%#v", post.Categories, post.Tags)
	}
	if _, err := posts.GetPublishedBySlug(ctx, postSlug); err != nil {
		t.Fatalf("published post lookup: %v", err)
	}
	publishedPage, err := posts.ListPublishedPage(ctx, 1, 0)
	if err != nil || publishedPage.Total != previousPage.Total+1 || len(publishedPage.Items) != 1 || publishedPage.Items[0].ID != post.ID {
		t.Fatalf("published page = %#v, %v", publishedPage, err)
	}
	if len(publishedPage.Items[0].Categories) != 1 || publishedPage.Items[0].Categories[0].ID != categoryID {
		t.Fatalf("published page lost taxonomy: %#v", publishedPage.Items[0])
	}
	if previousPage.Total > 0 {
		secondPage, err := posts.ListPublishedPage(ctx, 1, 1)
		if err != nil || secondPage.Total != publishedPage.Total || len(secondPage.Items) != 1 || secondPage.Items[0].ID != previousPage.Items[0].ID {
			t.Fatalf("second published page = %#v, %v", secondPage, err)
		}
	}
	if empty, err := posts.ListPublishedPage(ctx, 8, publishedPage.Total); err != nil || empty.Total != publishedPage.Total || len(empty.Items) != 0 {
		t.Fatalf("past final published page = %#v, %v", empty, err)
	}

	autosave := content.Autosave{
		PostID: post.ID, OwnerKey: fmt.Sprintf("user:%d", user.ID), SourceVersion: post.Version,
		Snapshot: post, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if _, err := posts.SaveAutosave(ctx, autosave); err != nil {
		t.Fatalf("save autosave: %v", err)
	}
	if loaded, err := posts.GetAutosave(ctx, post.ID, autosave.OwnerKey); err != nil || loaded.Snapshot.Title != post.Title {
		t.Fatalf("load autosave = %#v, %v", loaded, err)
	}
	updatedPost, err := posts.Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: username}, content.PostInput{
		AuthorID: user.ID, Type: content.TypePost, Status: content.StatusPublish, Slug: postSlug,
		Title: "PostgreSQL 集成文章（更新）", ContentSource: "第二版正文", CategoryIDs: []int64{categoryID},
		TagNames: []string{tagName}, CommentStatus: content.CommentOpen, AuthorName: user.DisplayName, Actor: username,
	})
	if err != nil {
		t.Fatalf("update persistent post: %v", err)
	}
	if updatedPost.Version != 2 || updatedPost.Title == post.Title {
		t.Fatalf("updated post projection = %#v", updatedPost)
	}
	if _, err := posts.GetAutosave(ctx, post.ID, autosave.OwnerKey); !errors.Is(err, content.ErrAutosaveNotFound) {
		t.Fatalf("formal post update did not clear autosave: %v", err)
	}
	if _, err := posts.Update(ctx, post.ID, content.Mutation{ExpectedVersion: post.Version, Actor: username}, content.PostInput{
		AuthorID: user.ID, Type: content.TypePost, Status: content.StatusPublish, Slug: postSlug,
		Title: "stale", ContentSource: "stale", CategoryIDs: []int64{categoryID}, TagNames: []string{tagName},
		CommentStatus: content.CommentOpen, AuthorName: user.DisplayName,
	}); !errors.Is(err, content.ErrVersionConflict) {
		t.Fatalf("stale post update = %v", err)
	}

	comments := NewCommentRepository(pool)
	item, err := comments.Create(ctx, comment.CreateInput{
		PostID: post.ID, AuthorName: "PG 访客", AuthorEmail: "guest@example.test", Status: comment.StatusApproved,
		ContentSource: "原始评论", AuthorIPHash: strings.Repeat("a", 64), UserAgent: "newpress-integration",
	})
	if err != nil {
		t.Fatalf("create persistent comment: %v", err)
	}
	commentID = item.ID
	if item.Version != 1 || item.Status != comment.StatusApproved {
		t.Fatalf("created comment projection = %#v", item)
	}
	edited, err := comments.Edit(ctx, item.ID, comment.Mutation{ExpectedVersion: item.Version, Actor: username}, comment.EditInput{
		AuthorName: "PG 访客（编辑）", AuthorEmail: item.AuthorEmail, ContentSource: "编辑后评论",
	})
	if err != nil || edited.Version != 2 {
		t.Fatalf("edit persistent comment = %#v, %v", edited, err)
	}
	revisions, err := comments.ListRevisions(ctx, item.ID, 20)
	if err != nil || len(revisions) != 1 || revisions[0].Version != 1 {
		t.Fatalf("comment revisions = %#v, %v", revisions, err)
	}
	restored, err := comments.RestoreRevision(ctx, item.ID, revisions[0].Version, comment.Mutation{ExpectedVersion: edited.Version, Actor: username})
	if err != nil || restored.Version != 3 || restored.Status != comment.StatusApproved || restored.ContentSource != item.ContentSource {
		t.Fatalf("restore persistent comment = %#v, %v", restored, err)
	}
	reply, err := comments.Reply(ctx, comment.ReplyInput{
		ParentID: item.ID, ExpectedParentVersion: restored.Version, UserID: user.ID,
		AuthorName: "站方", AuthorEmail: "staff@example.test", ContentSource: "感谢反馈", Actor: username,
	})
	if err != nil || !reply.StaffReply || reply.Status != comment.StatusApproved {
		t.Fatalf("staff reply = %#v, %v", reply, err)
	}
	moderated, err := comments.ChangeStatus(ctx, reply.ID, comment.Mutation{ExpectedVersion: reply.Version, Actor: username}, comment.StatusPending)
	if err != nil || moderated.Version != 2 || moderated.Status != comment.StatusPending {
		t.Fatalf("comment moderation = %#v, %v", moderated, err)
	}

	navigationStore := NewNavigationRepository(pool)
	menu, err := navigationStore.Create(ctx, navigation.MenuInput{
		Name: "PG 集成菜单", Slug: navigationSlug, Locations: []string{navigation.LocationPrimary, navigation.LocationMobile},
	}, username)
	if err != nil {
		t.Fatalf("create persistent menu: %v", err)
	}
	menuID = menu.ID
	menu, err = navigationStore.ReplaceItems(ctx, menu.ID, navigation.Mutation{ExpectedVersion: menu.Version, Actor: username}, []navigation.ItemInput{
		{ID: -1, Type: navigation.TypeCustom, Label: "首页", URL: "/"},
		{ID: -2, ParentID: -1, Type: navigation.TypeCustom, Label: "文章", URL: "/explore/"},
	})
	if err != nil || menu.Version != 2 || len(menu.Items) != 2 || menu.Items[1].ParentID != menu.Items[0].ID {
		t.Fatalf("replace persistent menu items = %#v, %v", menu, err)
	}
	if _, err := navigationStore.ReplaceItems(ctx, menu.ID, navigation.Mutation{ExpectedVersion: 1, Actor: username}, nil); !errors.Is(err, navigation.ErrVersionConflict) {
		t.Fatalf("stale navigation replacement = %v", err)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM np_audit_events WHERE actor_label=$1`, username).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount < 5 {
		t.Fatalf("audit events for integration actor = %d, want at least 5", auditCount)
	}
	auditPage, err := NewAuditReader(pool).List(ctx, audit.Query{Limit: 3, ObjectType: "comment"})
	if err != nil {
		t.Fatalf("list PostgreSQL audit events: %v", err)
	}
	if auditPage.Total < 1 || len(auditPage.Events) < 1 || auditPage.Events[0].Actor != "operator" {
		t.Fatalf("PostgreSQL audit projection = %#v", auditPage)
	}
}

func assertMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM newpress_schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 10 {
		t.Fatalf("migration count = %d, want at least 10", count)
	}
	var missing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (VALUES ('0001_core.sql'),('0002_content_workflow.sql'),('0003_taxonomy_runtime.sql'),('0004_identity_runtime.sql'),('0005_session_capabilities.sql'),('0006_comment_runtime.sql'),('0007_navigation_runtime.sql'),('0008_content_autosave.sql'),('0009_comment_editorial.sql'),('0010_audit_query.sql')) AS required(version) LEFT JOIN newpress_schema_migrations applied USING (version) WHERE applied.version IS NULL`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatalf("missing migration records = %d", missing)
	}
	var emptyChecksums int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM newpress_schema_migrations WHERE checksum=''`).Scan(&emptyChecksums); err != nil {
		t.Fatal(err)
	}
	if emptyChecksums != 0 {
		t.Fatalf("migration records with empty checksums = %d", emptyChecksums)
	}
}
