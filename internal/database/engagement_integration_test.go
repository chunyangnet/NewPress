package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
)

func TestPostgreSQLEngagement(t *testing.T) {
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
	hash, err := auth.HashPassword("Account engagement fixture password")
	if err != nil {
		t.Fatal(err)
	}
	stamp := fmt.Sprintf("engagement-%d", time.Now().UnixNano())
	userIDs := []int64{}
	postIDs := []int64{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=ANY($1::bigint[])`, postIDs)
		pool.Exec(cleanup, `DELETE FROM np_users WHERE id=ANY($1::bigint[])`, userIDs)
	}()
	for _, suffix := range []string{"reader", "writer"} {
		u, err := users.CreateUser(ctx, auth.UserMutation{Actor: "integration"}, auth.UserInput{Username: stamp + suffix, Email: stamp + suffix + "@example.test", DisplayName: suffix, Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"subscriber"}}, hash)
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, u.ID)
	}
	posts := NewPostRepository(pool)
	for i := 0; i < 3; i++ {
		status := content.StatusPublish
		if i == 2 {
			status = content.StatusPrivate
		}
		p, err := posts.Create(ctx, content.PostInput{Title: stamp + fmt.Sprint(i), Slug: stamp + fmt.Sprint(i), AuthorID: userIDs[1], Status: status, PublishedAt: time.Now().Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		postIDs = append(postIDs, p.ID)
	}
	s := NewEngagementRepository(pool)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			if err := s.Set(ctx, engagement.Input{UserID: userIDs[0], TargetID: postIDs[0], Kind: engagement.Bookmark, Collection: "技术", Active: true}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	states, err := s.States(ctx, userIDs[0], engagement.Bookmark, []int64{postIDs[0]})
	if err != nil || len(states) != 1 || states[0].Count != 1 || !states[0].Active || states[0].Collection != "技术" {
		t.Fatalf("states %#v %v", states, err)
	}
	entries, err := s.Entries(ctx, userIDs[1], engagement.Bookmark)
	if err != nil || len(entries) != 0 {
		t.Fatal("account isolation failed", entries, err)
	}
	page, err := posts.SearchPublished(ctx, content.PublishedQuery{PostIDs: []int64{postIDs[2], postIDs[1], postIDs[0]}, Sort: "selection", Limit: 1, Offset: 1})
	if err != nil || page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != postIDs[0] {
		t.Fatalf("selected public page %#v %v", page, err)
	}
	for _, query := range []content.PublishedQuery{{PostIDs: []int64{}}, {AuthorIDs: []int64{}}} {
		page, err = posts.SearchPublished(ctx, query)
		if err != nil || page.Total != 0 {
			t.Fatalf("empty selection %#v %v", page, err)
		}
	}
	page, err = posts.SearchPublished(ctx, content.PublishedQuery{AuthorIDs: []int64{userIDs[1]}})
	if err != nil || page.Total != 2 {
		t.Fatalf("follow feed %#v %v", page, err)
	}
	if err := s.Set(ctx, engagement.Input{UserID: userIDs[0], TargetID: postIDs[0], Kind: engagement.Bookmark, Active: false}); err != nil {
		t.Fatal(err)
	}
	states, err = s.States(ctx, userIDs[0], engagement.Bookmark, []int64{postIDs[0]})
	if err != nil || states[0].Count != 0 || states[0].Active {
		t.Fatal(states, err)
	}
}
