package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/messaging"
)

func TestPostgreSQLDiscoveryMessagingAndIdempotency(t *testing.T) {
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
	hash, err := auth.HashPassword("Integration identity password 2026")
	if err != nil {
		t.Fatal(err)
	}
	stamp := fmt.Sprintf("flow-%d", time.Now().UnixNano())
	var userIDs, postIDs, termIDs []int64
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM np_posts WHERE id=ANY($1::bigint[])`, postIDs)
		_, _ = pool.Exec(cleanup, `DELETE FROM np_terms WHERE id=ANY($1::bigint[])`, termIDs)
		_, _ = pool.Exec(cleanup, `DELETE FROM np_message_notifications WHERE user_id=ANY($1::bigint[]) OR peer_id=ANY($1::bigint[])`, userIDs)
		_, _ = pool.Exec(cleanup, `DELETE FROM np_messages WHERE sender_id=ANY($1::bigint[]) OR recipient_id=ANY($1::bigint[])`, userIDs)
		_, _ = pool.Exec(cleanup, `DELETE FROM np_users WHERE id=ANY($1::bigint[])`, userIDs)
	}()
	createUser := func(suffix string) auth.User {
		t.Helper()
		username := stamp + suffix
		user, err := users.CreateUser(ctx, auth.UserMutation{Actor: stamp}, auth.UserInput{Username: username, Email: username + "@example.test", DisplayName: "集成创作者", Status: auth.UserStatusActive, Locale: "zh-CN", RoleSlugs: []string{"author"}}, hash)
		if err != nil {
			t.Fatal(err)
		}
		userIDs = append(userIDs, user.ID)
		return user
	}
	alice, bob, other := createUser("-alice"), createUser("-bob"), createUser("-other")
	// Exercise authentication, not only the user lookup that previously failed
	// because its SELECT projection referenced an undeclared table alias.
	manager, err := auth.NewWithContext(ctx, auth.Config{Users: users, AbsoluteTTL: time.Hour, IdleTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, principal, err := manager.AuthenticateContext(ctx, stamp, strings.ToUpper(alice.Username), "Integration identity password 2026")
	if err != nil || principal.UserID != alice.ID {
		t.Fatalf("persistent login = %#v, %v", principal, err)
	}
	posts := NewPostRepository(pool)
	category, err := posts.CreateTerm(ctx, content.TermInput{Taxonomy: content.TaxonomyCategory, Slug: stamp, Name: stamp})
	if err != nil {
		t.Fatal(err)
	}
	termIDs = append(termIDs, category.ID)
	for i := 0; i < 27; i++ {
		input := content.PostInput{AuthorID: alice.ID, Title: fmt.Sprintf("%s %02d", stamp, i), Slug: fmt.Sprintf("%s-%02d", stamp, i), Status: content.StatusPublish, CategoryIDs: []int64{category.ID}, PublishedAt: time.Now().Add(-time.Duration(100-i) * time.Hour)}
		if i == 0 {
			input.Title += " 100%_literal"
		}
		post, err := posts.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		postIDs = append(postIDs, post.ID)
	}
	for _, status := range []string{content.StatusPrivate, content.StatusDraft, content.StatusFuture} {
		post, err := posts.Create(ctx, content.PostInput{AuthorID: alice.ID, Title: stamp + " private 100%_literal", Slug: stamp + "-" + status, Status: status, CategoryIDs: []int64{category.ID}, PublishedAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		postIDs = append(postIDs, post.ID)
	}
	result, err := posts.SearchPublished(ctx, content.PublishedQuery{AuthorID: alice.ID, Search: "100%_literal", Limit: 6})
	if err != nil || result.Total != 1 || result.Items[0].ID != postIDs[0] {
		t.Fatalf("literal old-content search = %#v, %v", result, err)
	}
	result, err = posts.SearchPublished(ctx, content.PublishedQuery{AuthorID: alice.ID, Category: stamp, Sort: "oldest", Limit: 6, Offset: 6})
	if err != nil || result.Total != 27 || len(result.Items) != 6 || result.Items[0].ID != postIDs[6] {
		t.Fatalf("database discovery page = %#v, %v", result, err)
	}
	for _, query := range []content.PublishedQuery{{AuthorID: bob.ID}, {Category: stamp + "-missing"}, {LegacyAuthor: alice.DisplayName, Search: stamp}} {
		result, err = posts.SearchPublished(ctx, query)
		if err != nil || result.Total != 0 {
			t.Fatalf("author/category isolation = %#v, %v", result, err)
		}
	}
	// Older seeds belong to a hidden system account. They retain their
	// metadata byline and must not link to an unavailable account profile.
	systemAuthor := createUser("-system")
	if _, err := pool.Exec(ctx, `UPDATE np_users SET status='system' WHERE id=$1`, systemAuthor.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := posts.Create(ctx, content.PostInput{AuthorID: systemAuthor.ID, AuthorName: "NewPress Team", Title: stamp + " legacy", Slug: stamp + "-legacy", Status: content.StatusPublish, CategoryIDs: []int64{category.ID}})
	if err != nil {
		t.Fatal(err)
	}
	postIDs = append(postIDs, legacy.ID)
	legacy, err = posts.GetPublishedBySlug(ctx, legacy.Slug)
	if err != nil || legacy.AuthorID != 0 || legacy.AuthorName != "NewPress Team" || legacy.AuthorURL() != "/authors/newpress-team/" {
		t.Fatalf("legacy system byline = %#v, %v", legacy, err)
	}
	for _, query := range []content.PublishedQuery{{LegacyAuthor: "NewPress Team", Search: stamp}, {Search: "NewPress Team", Category: stamp}} {
		result, err = posts.SearchPublished(ctx, query)
		if err != nil || result.Total != 1 || len(result.Items) != 1 || result.Items[0].ID != legacy.ID {
			t.Fatalf("legacy discovery = %#v, %v", result, err)
		}
	}
	result, err = posts.SearchPublished(ctx, content.PublishedQuery{AuthorID: systemAuthor.ID})
	if err != nil || result.Total != 0 {
		t.Fatalf("system account profile exposed = %#v, %v", result, err)
	}
	messages := NewMessagingRepository(pool)
	input := messaging.Input{SenderID: alice.ID, RecipientID: bob.ID, Content: "私信 [微笑] <script>literal</script>", Nonce: stamp + "-nonce"}
	sent, err := messages.Send(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := NewMessagingRepository(pool).Send(ctx, input)
	if err != nil || replay.ID != sent.ID {
		t.Fatalf("message replay = %#v, %v", replay, err)
	}
	input.Content = "different"
	if _, err := messages.Send(ctx, input); !errors.Is(err, messaging.ErrConflict) {
		t.Fatalf("nonce conflict = %v", err)
	}
	inbox, err := messages.Conversations(ctx, bob.ID, 0)
	if err != nil || inbox.Unread != 1 || len(inbox.Items) != 1 {
		t.Fatalf("inbox = %#v, %v", inbox, err)
	}
	notices, err := messages.Notices(ctx, bob.ID, messaging.NoticeQuery{UnreadOnly: true})
	if err != nil || notices.Unread != 1 || notices.Through != sent.ID {
		t.Fatalf("notices = %#v, %v", notices, err)
	}
	foreign, err := messages.Messages(ctx, other.ID, alice.ID, messaging.MessageQuery{})
	if err != nil || len(foreign.Items) != 0 {
		t.Fatalf("foreign messages = %#v, %v", foreign, err)
	}
	if err := messages.Read(ctx, bob.ID, alice.ID, sent.ID); err != nil {
		t.Fatal(err)
	}
	history, err := messages.Messages(ctx, alice.ID, bob.ID, messaging.MessageQuery{})
	if err != nil || history.ReadThrough != sent.ID || history.Items[0].ReadAt == nil {
		t.Fatalf("read receipt = %#v, %v", history, err)
	}
	newMessage, err := messages.Send(ctx, messaging.Input{SenderID: alice.ID, RecipientID: bob.ID, Content: "New arrival", Nonce: stamp + "-next"})
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.UpdateNotices(ctx, bob.ID, notices.Through, "clear"); err != nil {
		t.Fatal(err)
	}
	notices, err = messages.Notices(ctx, bob.ID, messaging.NoticeQuery{})
	if err != nil || len(notices.Items) != 1 || notices.Items[0].ID != newMessage.ID {
		t.Fatalf("snapshot notification clear = %#v, %v", notices, err)
	}
	history, err = messages.Messages(ctx, bob.ID, alice.ID, messaging.MessageQuery{})
	if err != nil || len(history.Items) != 2 {
		t.Fatalf("clearing notices removed messages: %#v, %v", history, err)
	}

}
