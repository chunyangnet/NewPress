package httpserver

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestHomeCreativeComponentsSavePreviewAndRender(t *testing.T) {
	s, handler, _, cookie, principal := newAuthenticatedContentServer(t)
	initial := s.themes.SettingsSnapshot()
	const animationKey = "home_carousel_animation"
	if initial.Values[animationKey] != "slide" {
		t.Fatalf("default carousel animation = %v, want slide", initial.Values[animationKey])
	}
	form := url.Values{"csrf_token": {principal.CSRFToken}, "revision": {initial.Revision}}
	for key, value := range initial.Values {
		form.Set(key, fmt.Sprint(value))
	}
	for _, key := range []string{"carousel", "search_banner"} {
		form.Set("home_"+key+"_visibility", "both")
	}
	form.Set("home_top_order", "search_banner,carousel,custom,hero,notice,channels,categories")
	form.Set("home_custom_blocks", `[{"key":"custom","title":"自定义模块示例","surface":true,"visibility":"both","html":"<h3>保留图文内容</h3><p><a href='/explore/'>发现内容</a></p><script>alert('home-sentinel')</script><img src='/example.webp' onerror='alert(1)'><p style='position:fixed' onclick='alert(1)'>正文</p>"}]`)
	form.Set("home_search_terms", "主题设计,Go & HTML,主题设计")
	form.Set("home_search_background", "video")
	form.Set("home_search_video", "/media/background.webm")
	form.Set(animationKey, "invalid")
	invalid := serveContentForm(handler, cookie, "https://example.test/admin/appearance/layout/", "https://example.test", form)
	if invalid.Code != 422 || s.themes.SettingsSnapshot().Revision != initial.Revision {
		t.Fatalf("invalid carousel animation changed settings or was not rejected: status %d", invalid.Code)
	}
	form.Set(animationKey, "stack")
	preview := serveContentForm(handler, cookie, "https://example.test/admin/appearance/preview/?page=home", "https://example.test", form)
	if preview.Code != 200 {
		t.Fatalf("preview %d: %s", preview.Code, preview.Body.String())
	}
	if s.themes.SettingsSnapshot().Revision != initial.Revision {
		t.Fatal("preview persisted settings")
	}
	var document struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`data-home-module="carousel"`, `data-carousel-effect="stack"`, `data-autoplay="true"`, `data-autohide-controls="true"`, `data-home-module="search_banner"`, `data-home-module="custom"`, `data-bg-src="/media/background.webm"`, `保留图文内容`, `name="q"`, `chunnel-home-blocks.css`, `chunnel-home-blocks.js`} {
		if !strings.Contains(document.HTML, fragment) {
			t.Errorf("missing rendered component %s", fragment)
		}
	}
	for _, fragment := range []string{"home-sentinel", `style="position:fixed"`, `onerror=`, `onclick=`} {
		if strings.Contains(document.HTML, fragment) {
			t.Errorf("unsafe HTML survived: %s", fragment)
		}
	}
	if !strings.Contains(document.HTML, `/explore/?q=Go&#43;%26&#43;HTML`) && !strings.Contains(document.HTML, `/explore/?q=Go+%26+HTML`) {
		t.Fatal("search term encoding lost")
	}
	saved := serveContentForm(handler, cookie, "https://example.test/admin/appearance/layout/", "https://example.test", form)
	if saved.Code != 303 {
		t.Fatalf("save %d: %s", saved.Code, saved.Body.String())
	}
	if err := s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	public := serveContentAPI(handler, nil, "GET", "https://example.test/", "", nil)
	if public.Code != 200 || !strings.Contains(public.Body.String(), `保留图文内容`) || !strings.Contains(public.Body.String(), `data-carousel-effect="stack"`) {
		t.Fatalf("public render %d", public.Code)
	}
	canvas := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/appearance/layout/", "", nil)
	if canvas.Code != 200 {
		t.Fatalf("canvas %d: %s", canvas.Code, canvas.Body.String())
	}
	for _, key := range []string{"home_custom_blocks", "home_search_video", "home_slide_1_title"} {
		if count := strings.Count(canvas.Body.String(), `name="`+key+`"`); count != 1 {
			t.Errorf("field %s occurs %d times", key, count)
		}
	}
	appearance := serveContentAPI(handler, cookie, "GET", "https://example.test/admin/appearance/", "", nil)
	if appearance.Code != 200 {
		t.Fatalf("appearance %d: %s", appearance.Code, appearance.Body.String())
	}
	for _, body := range []string{canvas.Body.String(), appearance.Body.String()} {
		_, options, found := strings.Cut(body, `<select id="setting-home_carousel_animation" name="home_carousel_animation">`)
		if !found {
			t.Fatal("carousel animation select is missing")
		}
		options, _, _ = strings.Cut(options, "</select>")
		for _, option := range []string{
			`value="slide">横向滑动</option>`,
			`value="fade">淡入式</option>`,
			`value="rotate-3d">3D 旋转</option>`,
			`value="stack" selected>卡片堆叠</option>`,
			`value="accordion">手风琴画廊</option>`,
			`value="page-turn">翻页式</option>`,
		} {
			if !strings.Contains(options, option) {
				t.Errorf("missing carousel option %s", option)
			}
		}
	}
}

func TestHomeCreativeEmptySlidesAndMediaValidation(t *testing.T) {
	s, _, _, _, _ := newAuthenticatedContentServer(t)
	values := s.themes.SettingsSnapshot().Values
	values["home_slide_1_title"] = ""
	values["home_slide_2_image"] = ""
	values["home_search_terms"] = "Go,Go,中文, third ,four,five,six,seven,eight"
	slides, terms := s.homeCreativeContent(values)
	if len(slides) != 1 || slides[0].Index != 1 || len(terms) != 6 {
		t.Fatalf("empty slides or search limit: %#v %#v", slides, terms)
	}
	for _, bad := range []string{"//foreign.test/video.mp4", "https://foreign.test/video.mp4", "javascript:alert(1)", "/\\foreign.test/video.mp4"} {
		values["home_search_video"] = bad
		_, _, _, fields := s.themes.PreviewSettings(values)
		if fields["home_search_video"] == "" {
			t.Errorf("non-local media accepted: %s", bad)
		}
	}
}
