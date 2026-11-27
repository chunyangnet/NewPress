package httpserver

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/announcement"
	"newpress.local/newpress/internal/content"
)

func TestAnnouncementsAdminCRUDPublicVisibilityAndConflict(t *testing.T) {
	s, handler, _, cookie, p := newAuthenticatedContentServer(t)
	state, _ := s.announcements.Snapshot(t.Context())
	form := url.Values{"csrf_token": {p.CSRFToken}, "expected_user_id": {fmt.Sprint(p.UserID)}, "revision": {state.Revision}, "action": {"save"}, "id": {"0"}, "title": {"站点更新公告"}, "body": {"第一段\n<script>alert('notice-xss')</script>"}, "url": {""}, "date": {"2026-01-20"}, "order": {"0"}, "published": {"true"}, "pinned": {"true"}}
	bad := cloneValues(form)
	bad.Set("csrf_token", "invalid")
	if response := serveContentForm(handler, cookie, "https://example.test/admin/announcements/", "https://example.test", bad); response.Code != 403 {
		t.Fatalf("CSRF: %d", response.Code)
	}
	bad = cloneValues(form)
	bad.Set("url", "javascript:alert(1)")
	if response := serveContentForm(handler, cookie, "https://example.test/admin/announcements/", "https://example.test", bad); response.Code != 422 {
		t.Fatalf("URL: %d %s", response.Code, response.Body)
	}
	if response := serveContentForm(handler, cookie, "https://example.test/admin/announcements/", "https://example.test", form); response.Code != 303 {
		t.Fatalf("create: %d %s", response.Code, response.Body)
	}
	if response := serveContentForm(handler, cookie, "https://example.test/admin/announcements/", "https://example.test", form); response.Code != 409 {
		t.Fatalf("conflict: %d", response.Code)
	}
	state, _ = s.announcements.Snapshot(t.Context())
	if len(state.Items) != 1 {
		t.Fatal(state)
	}
	for _, path := range []string{"/admin/announcements/", "/admin/announcements/?edit=1", "/announcements/", "/announcements/1/"} {
		response := serveContentAPI(handler, cookie, "GET", "https://example.test"+path, "", nil)
		if response.Code != 200 || !strings.Contains(response.Body.String(), "站点更新公告") {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body)
		}
		if strings.Contains(response.Body.String(), "<script>alert('notice-xss')</script>") {
			t.Fatal("unescaped notice body")
		}
	}
	form.Set("revision", state.Revision)
	form.Set("id", "1")
	form.Del("published")
	if response := serveContentForm(handler, cookie, "https://example.test/admin/announcements/", "https://example.test", form); response.Code != 303 {
		t.Fatalf("hide: %d", response.Code)
	}
	if response := serveContentAPI(handler, nil, "GET", "https://example.test/announcements/1/", "", nil); response.Code != 404 {
		t.Fatalf("hidden detail: %d", response.Code)
	}
	state, _ = s.announcements.Snapshot(t.Context())
	deleteForm := url.Values{"csrf_token": {p.CSRFToken}, "expected_user_id": {fmt.Sprint(p.UserID)}, "revision": {state.Revision}, "action": {"delete"}, "id": {"1"}}
	if response := serveContentForm(handler, cookie, "https://example.test/admin/announcements/", "https://example.test", deleteForm); response.Code != 303 {
		t.Fatalf("delete: %d", response.Code)
	}
}

func TestHomeSidebarModulesLayoutAndPublicSelections(t *testing.T) {
	s, handler, repository, cookie, p := newAuthenticatedContentServer(t)
	now := time.Now().Add(-time.Hour)
	for index, status := range []string{content.StatusPublish, content.StatusPublish, content.StatusPublish, content.StatusDraft, content.StatusPrivate} {
		_, err := repository.Create(t.Context(), content.PostInput{Type: content.TypePost, Title: fmt.Sprintf("模块文章-%d-%s", index, status), Slug: fmt.Sprintf("sidebar-%d", index), Status: status, AuthorID: p.UserID, AuthorName: p.Label(), PublishedAt: now, CoverURL: "/theme-assets/images/editorial/cover-print-small.webp"})
		if err != nil {
			t.Fatal(err)
		}
	}
	state, _ := s.announcements.Snapshot(t.Context())
	for _, item := range []announcement.Item{{Title: "公开公告", Date: now, Published: true}, {Title: "隐藏公告哨兵", Date: now}} {
		var err error
		state, err = s.announcements.Save(t.Context(), state.Revision, "save", item)
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot := s.themes.SettingsSnapshot()
	form := url.Values{"csrf_token": {p.CSRFToken}, "revision": {snapshot.Revision}}
	for key, value := range snapshot.Values {
		form.Set(key, fmt.Sprint(value))
	}
	const order = "focus_list,topic_banner,entry_cards,image_grid,notice_list"
	form.Set("home_aside_order", order)
	for _, key := range strings.Split(order, ",") {
		form.Set("home_"+key+"_visibility", "both")
	}
	response := serveContentForm(handler, cookie, "https://example.test/admin/appearance/layout/", "https://example.test", form)
	if response.Code != 303 {
		t.Fatalf("layout: %d %s", response.Code, response.Body)
	}
	public := serveContentAPI(handler, nil, "GET", "https://example.test/", "", nil)
	if public.Code != 200 {
		t.Fatalf("home: %d %s", public.Code, public.Body)
	}
	for _, key := range strings.Split(order, ",") {
		if !strings.Contains(public.Body.String(), `data-home-module="`+key+`"`) {
			t.Errorf("missing %s", key)
		}
	}
	for _, bad := range []string{"隐藏公告哨兵", "模块文章-3-draft", "模块文章-4-private"} {
		if strings.Contains(public.Body.String(), bad) {
			t.Errorf("leaked %s", bad)
		}
	}
	if !strings.Contains(public.Body.String(), "chunnel-home-sidebar.css") || !strings.Contains(public.Body.String(), "公开公告") {
		t.Fatal("missing module assets or notices")
	}
	canvas := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/appearance/layout/", "", nil)
	if canvas.Code != 200 {
		t.Fatalf("canvas: %d", canvas.Code)
	}
	for _, key := range []string{"home_topic_title", "home_entry_1_url", "home_notice_list_count"} {
		if count := strings.Count(canvas.Body.String(), `name="`+key+`"`); count != 1 {
			t.Errorf("%s appears %d times", key, count)
		}
	}
	values := s.themes.SettingsSnapshot().Values
	values["home_aside_order"] = ""
	values["home_main_order"] = order + ",feed"
	if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) != 0 {
		t.Fatal(fields, err)
	}
	if response := serveContentAPI(handler, nil, "GET", "https://example.test/", "", nil); response.Code != 200 {
		t.Fatalf("main placement: %d", response.Code)
	}
}
