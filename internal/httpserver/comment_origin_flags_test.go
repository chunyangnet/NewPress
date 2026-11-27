package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/plugin"
)

func TestCommentOriginFlagAssets(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	s.registerPluginRoutes(mux)
	for _, name := range []string{"us.svg", "cn.svg", "jp.svg", "zz.svg", "US.svg", "LICENSE", "settings.json"} {
		r := httptest.NewRequest(http.MethodGet, "/plugin-assets/comment-origin/flags/"+name, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if name != "us.svg" && name != "cn.svg" && name != "jp.svg" {
			if w.Code != http.StatusNotFound {
				t.Errorf("%s: %d", name, w.Code)
			}
			continue
		}
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(w.Body.String(), "<svg") || !strings.Contains(w.Header().Get("Cache-Control"), "public") {
			t.Fatalf("asset %s: %d %v", name, w.Code, w.Header())
		}
		r.Header.Set("If-None-Match", w.Header().Get("ETag"))
		cached := httptest.NewRecorder()
		mux.ServeHTTP(cached, r)
		if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
			t.Fatal("flag cache ignored")
		}
	}
}

func TestCommentOriginFlagsInHTMLAndPublicProjection(t *testing.T) {
	s, handler, comments, _, _ := newCommentHTTPTestServer(t)
	if err := s.plugins.InstallBuiltin(); err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.SetEnabled(plugin.CommentOrigin, true); err != nil {
		t.Fatal(err)
	}
	c, err := comments.Create(t.Context(), comment.CreateInput{PostID: 1, AuthorName: "读者", AuthorEmail: "reader@example.test", ContentSource: "国旗展示", Status: comment.StatusApproved})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://example.test/posts/welcome-to-newpress", nil)
	if err := s.commentOrigin.Capture(c.ID, "8.8.8.8", "Windows NT 10.0"); err != nil {
		t.Fatal(err)
	}
	record, _ := s.commentOrigin.PrivateRecord(c.ID)
	record.Status, record.Location = "resolved", commentorigin.Location{Country: "日本", CountryCode: "JP", City: "东京", Precision: "city"}
	if err := s.plugins.WithData(plugin.CommentOrigin, true, 0, func(dir string, _ uint64) error {
		return plugin.WritePrivate(filepath.Join(dir, "records", "1.json"), record)
	}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"all", "off", "foreign"} {
		config, _ := s.commentOrigin.Settings()
		config.FlagMode = mode
		if _, err := s.commentOrigin.Save(config, false); err != nil {
			t.Fatal(err)
		}
		view := s.publicCommentWithOrigin(r, c)
		data, _ := json.Marshal(view)
		page := httptest.NewRecorder()
		handler.ServeHTTP(page, r)
		want := mode != "off"
		if page.Code != 200 || strings.Contains(page.Body.String(), "/plugin-assets/comment-origin/flags/jp.svg") != want || (view.Origin.Flag == "jp") != want {
			t.Fatalf("mode %s: HTML=%d, projection=%s", mode, page.Code, data)
		}
		if strings.Contains(string(data), "country_code") || strings.Contains(string(data), "8.8.8.8") || strings.Contains(page.Body.String(), "8.8.8.8") {
			t.Fatal("private source leaked")
		}
	}
}
