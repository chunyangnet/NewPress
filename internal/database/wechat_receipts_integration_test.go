package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/wechat"
)

type receiptTransport func(*http.Request) (*http.Response, error)

func (f receiptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPostgreSQLWeChatReceiptRecovery(t *testing.T) {
	dsn := os.Getenv("NEWPRESS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NEWPRESS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := pgx.Identifier{fmt.Sprintf("wechat_receipt_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := admin.Exec(cleanup, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	poolConfig := admin.Config()
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	posts := NewPostRepository(pool)
	post, err := posts.Create(ctx, content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "回执验证", Slug: "receipt-verification", ContentSource: "仅用于隔离回执测试"})
	if err != nil {
		t.Fatal(err)
	}
	svc := &wechat.Service{Store: NewWeChatRepository(pool), Content: posts, Client: wechat.NewClient(), PublicURL: "https://example.test"}
	cfg := wechat.Defaults()
	cfg.AppID, cfg.Secret, cfg.ThumbMediaID = "wx0123456789abcdef", strings.Repeat("t", 32), "fixture-cover"
	cfg.Enabled, cfg.AutoPublish = true, true
	if _, err = svc.SaveConfig(ctx, cfg, false); err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	pollReply := `{"publish_id":"wrong-task","publish_status":0,"article_id":"wrong-article"}`
	svc.Client.HTTP = &http.Client{Transport: receiptTransport(func(r *http.Request) (*http.Response, error) {
		calls[r.URL.Path]++
		var payload string
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			payload = `{"access_token":"test-token","expires_in":7200}`
		case "/cgi-bin/draft/add":
			payload = `{"media_id":"saved-draft"}`
		case "/cgi-bin/freepublish/submit":
			payload = `{"publish_id":null}`
		case "/cgi-bin/freepublish/get":
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["publish_id"] != "verified-task" {
				t.Fatal("query identity changed", input, err)
			}
			payload = pollReply
		default:
			return nil, errors.New("unexpected remote operation")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
	})}
	if _, err = svc.Queue(ctx, post.ID, post.Version, "operator"); err != nil {
		t.Fatal(err)
	}
	if err = svc.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A separate pool observes the committed task and performs reconciliation.
	reopened, err := pgxpool.NewWithConfig(ctx, poolConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	svc.Store = NewWeChatRepository(reopened)
	readJob := func() wechat.Job {
		t.Helper()
		state, err := svc.Store.View(ctx)
		if err != nil || len(state.Jobs) != 1 {
			t.Fatal(state, err)
		}
		return state.Jobs[0]
	}
	job := readJob()
	if job.Status != "uncertain" || job.Stage != "publish" || job.MediaID != "saved-draft" || job.PublishID != "" {
		t.Fatal("malformed receipt persisted as success", job)
	}
	if err = svc.Action(ctx, job.ID, "retry", job.UpdatedAt); err == nil {
		t.Fatal("uncertain task could repeat")
	}
	if err = svc.Tick(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = svc.Resolve(ctx, job.ID, job.UpdatedAt, "", "verified-task"); err != nil {
		t.Fatal(err)
	}
	if err = svc.Tick(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	job = readJob()
	if job.Status != "publishing" || job.PublishID != "verified-task" || job.ArticleID != "" || job.Error == "" {
		t.Fatal("wrong response adopted", job)
	}
	pollReply = `{"publish_id":"verified-task","publish_status":0,"article_id":"verified-article"}`
	svc.Store = NewWeChatRepository(pool)
	if err = svc.Tick(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	job = readJob()
	if job.Status != "published" || job.ArticleID != "verified-article" || job.Error != "" || calls["/cgi-bin/draft/add"] != 1 || calls["/cgi-bin/freepublish/submit"] != 1 {
		t.Fatal("reconciliation repeated a write", job, calls)
	}
}
