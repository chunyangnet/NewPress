package database

import (
	"context"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgreSQLAccountProfiles(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("NEWPRESS_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepository(pool)
	if err := users.EnsureRoles(ctx, auth.BuiltInRoles()); err != nil {
		t.Fatal(err)
	}
	if err := users.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	stamp := fmt.Sprintf("profile-%d", time.Now().UnixNano())
	hash, _ := auth.HashPassword("Postgres profile test password")
	ids := []int64{}
	var postID int64
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Exec(clean, `DELETE FROM np_comments WHERE post_id=$1`, postID)
		pool.Exec(clean, `DELETE FROM np_posts WHERE id=$1`, postID)
		pool.Exec(clean, `DELETE FROM np_users WHERE id=ANY($1::bigint[])`, ids)
	}()
	for _, suffix := range []string{"a", "b"} {
		u, err := users.CreateUser(ctx, auth.UserMutation{}, auth.UserInput{Username: stamp + suffix, Email: stamp + suffix + "@example.test", DisplayName: "同名", Locale: "zh-CN", RoleSlugs: []string{"subscriber"}}, hash)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	u, _ := users.GetUser(ctx, ids[0])
	input := auth.ProfileInput{DisplayName: "新名字", Locale: "en-US", Bio: "第一行\n第二行", AvatarMediaID: 42}
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			_, err := users.UpdateProfile(ctx, u.ID, auth.UserMutation{ExpectedVersion: u.Version}, input)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, auth.ErrUserVersionConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("optimistic profile updates", won.Load())
	}
	updated, err := users.GetUser(ctx, u.ID)
	if err != nil || updated.Bio != input.Bio || updated.AvatarMediaID != 42 || updated.PasswordHash != u.PasswordHash || updated.Email != u.Email || !updated.HasRole("subscriber") {
		t.Fatal("profile failed isolation", err)
	}
	refs, err := users.ListUsers(ctx, auth.UserQuery{AvatarMediaID: 42})
	if err != nil || refs.Total != 1 || refs.Items[0].ID != u.ID {
		t.Fatal(refs, err)
	}
	posts := NewPostRepository(pool)
	post, err := posts.Create(ctx, content.PostInput{Title: stamp, Slug: stamp, AuthorID: ids[1], Status: content.StatusPublish})
	if err != nil {
		t.Fatal(err)
	}
	postID = post.ID
	comments := NewCommentRepository(pool)
	for _, c := range []struct {
		user         int64
		status, text string
	}{{u.ID, comment.StatusPending, "自己的待审核100%"}, {u.ID, comment.StatusApproved, "自己的公开评论"}, {u.ID, comment.StatusTrash, "已删除的评论"}, {ids[1], comment.StatusApproved, "其他账号评论"}} {
		if _, err := comments.Create(ctx, comment.CreateInput{PostID: post.ID, UserID: c.user, AuthorName: "同名", AuthorEmail: u.Email, ContentSource: c.text, Status: c.status}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := comments.ListAdmin(ctx, comment.Query{UserID: u.ID, OwnOnly: true, Limit: 1})
	if err != nil || list.Total != 2 || list.StatusCounts[comment.StatusApproved] != 1 || list.StatusCounts[comment.StatusTrash] != 0 {
		t.Fatal(list, err)
	}
	list, err = comments.ListAdmin(ctx, comment.Query{UserID: u.ID, OwnOnly: true, Search: "%"})
	if err != nil || list.Total != 1 {
		t.Fatal("literal search parity", list, err)
	}
	s := NewEngagementRepository(pool)
	if err := s.Set(ctx, engagement.Input{UserID: u.ID, TargetID: post.ID, Kind: engagement.Bookmark, Collection: "原收藏夹", Active: true}); err != nil {
		t.Fatal(err)
	}
	imports := []engagement.Input{{UserID: u.ID, TargetID: post.ID, Kind: engagement.Bookmark, Collection: "本机收藏", Active: true}, {UserID: u.ID, TargetID: post.ID, Kind: engagement.Like, Active: true}}
	r, err := s.Merge(ctx, u.ID, imports, true)
	if err != nil || r.Added != 1 || r.Existing != 1 {
		t.Fatal(r, err)
	}
	before, _ := s.Entries(ctx, u.ID, engagement.Like)
	if len(before) != 0 {
		t.Fatal("preview wrote rows")
	}
	for range 8 {
		wg.Go(func() {
			if _, err := s.Merge(ctx, u.ID, imports, false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	r, err = s.Merge(ctx, u.ID, imports, false)
	if err != nil || r.Added != 0 || r.Existing != 2 {
		t.Fatal(r, err)
	}
	bookmarks, _ := s.Entries(ctx, u.ID, engagement.Bookmark)
	if len(bookmarks) != 1 || bookmarks[0].Collection != "原收藏夹" {
		t.Fatal(bookmarks)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO np_engagement(user_id,kind,target_id) SELECT $1,'bookmark',n FROM generate_series(1000001,1000999) n`, u.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Merge(ctx, u.ID, []engagement.Input{{UserID: u.ID, TargetID: 9000001, Kind: engagement.Like, Active: true}, {UserID: u.ID, TargetID: 9000002, Kind: engagement.Bookmark, Active: true}}, false)
	if !errors.Is(err, engagement.ErrLimit) {
		t.Fatal(err)
	}
	after, _ := s.Entries(ctx, u.ID, engagement.Like)
	if len(after) != 1 {
		t.Fatal("failed merge partially committed")
	}
}
