package database

import (
	"context"
	"errors"
	"fmt"
	"newpress.local/newpress/internal/community"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgreSQLCommunity(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pool, e := Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	if e = Migrate(ctx, pool); e != nil {
		t.Fatal(e)
	}
	slug := fmt.Sprintf("community-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM np_community_circles WHERE slug=$1`, slug)
	s := NewCommunityRepository(pool)
	now := time.Now().UTC()
	admin := community.Actor{ID: 991, Admin: true}
	reader := community.Actor{ID: 992}
	create := community.Command{Action: "create", Key: "community-create-key", Name: "真实数据库圈子", JoinPolicy: "approval", Visibility: "members", OwnerID: admin.ID}
	first, e := s.Apply(ctx, slug, admin, create, now)
	if e != nil {
		t.Fatal(e)
	}
	join := community.Command{Action: "join", Key: "community-reader-join", Version: first.Version}
	joined, e := s.Apply(ctx, slug, reader, join, now)
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.Get(ctx, slug)
	if e != nil || community.CanRead(c, reader) {
		t.Fatal("pending disclosure", e)
	}
	accepted, e := s.Apply(ctx, slug, admin, community.Command{Action: "member", Key: "community-member-yes", Version: joined.Version, UserID: reader.ID, Role: "member"}, now)
	if e != nil {
		t.Fatal(e)
	}
	topic := community.Command{Action: "topic", Key: "community-new-topic", Version: accepted.Version, Title: "提问", Body: "内容", Kind: "question"}
	created, e := s.Apply(ctx, slug, reader, topic, now)
	if e != nil {
		t.Fatal(e)
	}
	reply, e := s.Apply(ctx, slug, admin, community.Command{Action: "reply", Key: "community-new-reply", Version: created.Version, TopicID: created.TopicID, Body: "可采纳的答复"}, now)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Apply(ctx, slug, reader, community.Command{Action: "accept", Key: "community-accept-reply", Version: reply.Version, TopicID: created.TopicID, ReplyID: reply.ReplyID}, now)
	if e != nil {
		t.Fatal(e)
	}
	c, e = s.Get(ctx, slug)
	if e != nil || c.Topics[0].AcceptedID != reply.ReplyID {
		t.Fatal("accept not persisted", e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := NewCommunityRepository(pool).Apply(ctx, slug, reader, community.Command{Action: "reply", Key: fmt.Sprintf("community-concurrent-%d", i), Version: c.Version, TopicID: created.TopicID, Body: "并发答复"}, now)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	wins := 0
	for e := range errs {
		if e == nil {
			wins++
		} else if !errors.Is(e, community.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("concurrent winners", wins)
	}
	repeated, e := NewCommunityRepository(pool).Apply(ctx, slug, reader, topic, now)
	if e != nil || !repeated.Replay || repeated.TopicID != created.TopicID {
		t.Fatal("durable replay", repeated, e)
	}
	c, e = s.Get(ctx, slug)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Apply(ctx, slug, reader, community.Command{Action: "member", Key: "community-illegal-edit", Version: c.Version, UserID: admin.ID, Role: "banned"}, now)
	if !errors.Is(e, community.ErrForbidden) {
		t.Fatal(e)
	}
	after, _ := s.Get(ctx, slug)
	if after.Version != c.Version {
		t.Fatal("failure committed")
	}
	directory, e := s.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, h := range directory {
		if len(h.Topics) != 0 || len(h.Receipts) != 0 {
			t.Fatal("directory contains bodies")
		}
	}
}

func TestPostgreSQLCommunityRichLifecycle(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("rich-%d", time.Now().UnixNano())
	defer pool.Exec(context.Background(), `DELETE FROM np_community_circles WHERE slug=$1`, slug)
	defer pool.Exec(context.Background(), `DELETE FROM np_audit_events WHERE object_id=$1 OR object_id LIKE $2`, slug, slug+":%")
	store := NewCommunityRepository(pool)
	now := time.Now().UTC()
	owner, member := community.Actor{ID: 9981, Admin: true}, community.Actor{ID: 9982}
	seq := 0
	apply := func(a community.Actor, cmd community.Command) community.Result {
		t.Helper()
		seq++
		cmd.Key = fmt.Sprintf("rich-command-key-%06d", seq)
		if c, err := store.Get(ctx, slug); err == nil {
			cmd.Version = c.Version
		}
		result, err := store.Apply(ctx, slug, a, cmd, now)
		if err != nil {
			t.Fatalf("%s: %v", cmd.Action, err)
		}
		return result
	}
	apply(owner, community.Command{Action: "create", Name: "完整链路", JoinPolicy: "open", Visibility: "members", OwnerID: owner.ID})
	apply(member, community.Command{Action: "join"})
	files, _ := community.NewFiles("")
	file, err := files.Stage(ctx, slug, "rich-upload-key-123", member.ID, "note.txt", strings.NewReader("postgres attachment"))
	if err != nil {
		t.Fatal(err)
	}
	apply(member, community.Command{Action: "add_attachment", Attachment: &file})
	topic := apply(member, community.Command{Action: "topic", Title: "图文", Body: "**内容**", Format: "markdown", Kind: "question", Attachments: []string{file.ID}})
	reply := apply(owner, community.Command{Action: "reply", TopicID: topic.TopicID, Body: "回应"})
	apply(member, community.Command{Action: "accept", TopicID: topic.TopicID, ReplyID: reply.ReplyID})
	report := apply(member, community.Command{Action: "report", TopicID: topic.TopicID, Category: "privacy", Reason: "需要核对"})
	reopened := NewCommunityRepository(pool)
	c, err := reopened.Get(ctx, slug)
	if err != nil || len(c.Reports) != 1 || len(c.Attachments) != 1 || c.Topics[0].Format != "markdown" || c.Topics[0].AcceptedID != reply.ReplyID {
		t.Fatal("rich facts lost", c, err)
	}
	version := c.Version
	events, err := reopened.PendingActivity(ctx, now, 500)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Circle == slug {
			count++
			if err = reopened.AckActivity(ctx, e.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	c, err = reopened.Get(ctx, slug)
	if err != nil || count == 0 || len(c.Activities) != 0 || c.Version != version {
		t.Fatal("outbox ack", count, c.Version, err)
	}
	yes := true
	apply(owner, community.Command{Action: "review_report", ReportID: report.ReportID, Status: "resolved", Note: "已处理", Enabled: &yes})
	apply(owner, community.Command{Action: "delete_topic", TopicID: topic.TopicID})
	apply(owner, community.Command{Action: "archive_circle", Enabled: &yes})
	c, err = reopened.Get(ctx, slug)
	if err != nil || !c.Archived || len(c.Topics) != 0 || len(c.Attachments) != 0 || c.Reports[0].Status != "resolved" {
		t.Fatal("lifecycle lost", c, err)
	}
	directory, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range directory {
		if len(h.Attachments)+len(h.Reports)+len(h.Changes)+len(h.Activities) != 0 {
			t.Fatal("private directory facts")
		}
	}
	var audits int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM np_audit_events WHERE object_id=$1 OR object_id LIKE $2`, slug, slug+":%").Scan(&audits); err != nil || audits != seq {
		t.Fatal("transactional audit", audits, seq, err)
	}
}
