package httpserver

import (
	"net"
	"net/http"
	"newpress.local/newpress/internal/content"
	"os"
	"strings"
	"testing"
	"time"
)

func seedDailyWorkbench(t *testing.T, s *Server, authorID int64) {
	t.Helper()
	for _, item := range []struct {
		title, status string
		when          time.Time
	}{
		{"待审核：站点使用指南", content.StatusPending, time.Time{}},
		{"草稿：整理本周内容", content.StatusDraft, time.Time{}},
		{"明日发布：社区精选", content.StatusFuture, time.Now().Add(24 * time.Hour)},
		{"下月发布内容", content.StatusFuture, time.Now().Add(30 * 24 * time.Hour)},
	} {
		_, err := s.repository.Create(t.Context(), content.PostInput{Title: item.title, Status: item.status, Type: content.TypePost, AuthorID: authorID, AuthorName: "编辑", PublishedAt: item.when, ContentSource: "<p>工作台测试内容</p>", Actor: "workbench-test"})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkbenchDashboard(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	seedDailyWorkbench(t, s, owner.UserID)
	r := serveCookieRequest(h, http.MethodGet, "https://example.test/admin/", cookie, nil)
	if r.Code != 200 {
		t.Fatalf("status %d", r.Code)
	}
	body := r.Body.String()
	for _, text := range []string{"工作台", "待审核文章", "daily-scheduled", "data-daily-workspace", "data-daily-tabs hidden", "data-daily-panel", "继续编辑", "daily-quick-note", "data-quick-draft"} {
		if !strings.Contains(body, text) {
			t.Errorf("missing %s", text)
		}
	}
	if !strings.Contains(body, "明日发布：社区精选") || strings.Contains(body, "下月发布内容") {
		t.Fatal("schedule window incorrect")
	}
}

// Isolated opt-in fixture; never reads or modifies the running site's data.
func TestWorkbenchBrowserFixture(t *testing.T) {
	addr := os.Getenv("NEWPRESS_WORKBENCH_BROWSER_ADDR")
	if addr == "" {
		t.Skip("isolated browser fixture")
	}
	s, h, users, _, cookie, owner := newPersistentAdminTestServer(t)
	seedDailyWorkbench(t, s, owner.UserID)
	_, authorCookie, _ := createAndLoginTestUser(t, users, owner, "workbench-author", "author@example.test", "作者", "author")
	_, readerCookie, _ := createAndLoginTestUser(t, users, owner, "workbench-reader", "reader@example.test", "读者", "subscriber")
	s.config.PublicURL = "http://localhost:19366"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		c := *cookie
		switch r.PathValue("role") {
		case "author":
			c = *authorCookie
		case "reader":
			c = *readerCookie
		}
		http.SetCookie(w, &c)
		http.Redirect(w, r, "/admin/", 303)
	})
	mux.Handle("/", h)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Log("workbench fixture ready", addr)
	if err := http.Serve(listener, mux); err != nil {
		t.Fatal(err)
	}
}
