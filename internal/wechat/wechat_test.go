package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}
}
func fixture(t *testing.T) (*Service, content.Post) {
	t.Helper()
	store, err := NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	repo := content.NewMemoryRepository(false)
	post, err := repo.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: "同步测试", Slug: "wechat-test", ContentSource: "这是正文", AuthorName: "作者"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: store, Client: NewClient(), Content: repo, PublicURL: "https://example.test"}
	cfg := Defaults()
	cfg.Enabled = true
	cfg.AppID = "wx0123456789abcdef"
	cfg.Secret = strings.Repeat("a", 32)
	cfg.ThumbMediaID = "cover"
	if _, err = s.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	return s, post
}
func stateJob(t *testing.T, s *Service) Job {
	t.Helper()
	v, e := s.Store.View(t.Context())
	if e != nil || len(v.Jobs) != 1 {
		t.Fatalf("jobs: %v %#v", e, v.Jobs)
	}
	return v.Jobs[0]
}

func TestConfigSecretVersionAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wechat.json")
	f, _ := NewFileStore(path)
	s := &Service{Store: f}
	cfg := Defaults()
	cfg.AppID = "wx0123456789abcdef"
	cfg.Secret = strings.Repeat("s", 32)
	cfg.Enabled = true
	public, e := s.SaveConfig(t.Context(), cfg, false)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(public)
	if strings.Contains(string(b), "app_secret") || public.Secret != "" || !public.HasSecret {
		t.Fatal("secret leaked")
	}
	if _, e = s.SaveConfig(t.Context(), cfg, false); !errors.Is(e, ErrConflict) {
		t.Fatal("stale accepted", e)
	}
	if _, e = s.SaveConfig(t.Context(), public, true); e == nil {
		t.Fatal("enabled secret clearing allowed")
	}
	public.Author = "新作者"
	public, e = s.SaveConfig(t.Context(), public, false)
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	state, e := reopened.View(t.Context())
	if e != nil || state.Config.Secret != cfg.Secret {
		t.Fatal("secret not preserved", e)
	}
	// View is detached from the durable state.
	state.Config.Secret = "changed"
	again, _ := f.View(t.Context())
	if again.Config.Secret != cfg.Secret {
		t.Fatal("view aliases state")
	}
	public.Enabled = false
	public.Secret = "ignored-but-long-secret"
	public, e = s.SaveConfig(t.Context(), public, true)
	if e != nil || public.HasSecret {
		t.Fatal("clear failed", e)
	}
	for _, bad := range []Config{{Version: 1, AppID: "invalid"}, {Version: 1, Secret: "short"}, {Version: 1, Secret: strings.Repeat("x", 20) + "\n"}, {Version: 1, Author: strings.Repeat("字", 17)}} {
		if bad.Validate() == nil {
			t.Fatal("invalid accepted")
		}
	}
}

func TestNetworkAllowlistAndArticleURLs(t *testing.T) {
	for _, raw := range []string{"http://api.weixin.qq.com/x", "https://mp.weixin.qq.com.evil.test/s/abcde", "https://user@mp.weixin.qq.com/s/abcde", "https://127.0.0.1/", "https://mp.weixin.qq.com:444/s/abcde"} {
		u, _ := url.Parse(raw)
		if allowedURL(u) {
			t.Fatal("allowed", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "::1", "::ffff:127.0.0.1", "2001:db8::1", "198.18.0.1"} {
		if publicIP(net.ParseIP(raw)) {
			t.Fatal("public", raw)
		}
	}
	if !publicIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public IPv4 rejected")
	}
	raw := "http://mp.weixin.qq.com/s?__biz=abc&amp;mid=123&amp;idx=1&amp;sn=xyz&amp;scene=1#read"
	got, e := ArticleURL(raw)
	if e != nil || strings.Contains(got, "scene") || strings.Contains(got, "#") || !strings.HasPrefix(got, "https:") {
		t.Fatal(got, e)
	}
	for _, raw := range []string{"https://mp.weixin.qq.com/s?__biz=a&mid=1&idx=1&sn=a&sn=b", "https://mp.weixin.qq.com/cgi-bin/home", "https://evil.test/s/abcde", "file:///s/abcde"} {
		if _, e = ArticleURL(raw); e == nil {
			t.Fatal("invalid article", raw)
		}
	}
	for _, raw := range []string{"http://mmbiz.qpic.cn/a", "//mmbiz.qlogo.cn/a"} {
		if got, e = ImageURL(raw); e != nil || !strings.HasPrefix(got, "https:") {
			t.Fatal(got, e)
		}
	}
	if _, e = ImageURL("https://api.weixin.qq.com/anything"); e == nil {
		t.Fatal("API accepted as image")
	}
	client := safeHTTP()
	u, _ := url.Parse("https://mp.weixin.qq.com/s/abcde")
	v, _ := url.Parse("https://mmbiz.qpic.cn/a")
	if client.CheckRedirect(&http.Request{URL: v}, []*http.Request{{URL: u}}) == nil {
		t.Fatal("cross host redirect")
	}
}

func TestParseCollectionSanitizationAndRepeatImport(t *testing.T) {
	source := `<html><head><meta property="og:title" content="公众号测试"><meta name="description" content="摘要"><meta property="og:image" content="https://mmbiz.qpic.cn/cover"></head><body><span id="js_name">公众号作者</span><div id="js_content" style="display:none"><p onclick="evil()">正文<script>evil()</script><img data-src="http://mmbiz.qpic.cn/image" onerror="evil()"><img src="http://127.0.0.1/private"><iframe src="https://evil.test"></iframe><a href="javascript:evil()">安全链接</a></p></div></body></html>`
	article, e := ParseArticle("https://mp.weixin.qq.com/s/abcdef", source)
	if e != nil {
		t.Fatal(e)
	}
	if len(article.Images) != 2 || article.Author != "公众号作者" || strings.Contains(article.Preview, "<img") {
		t.Fatalf("bad extraction %#v", article)
	}
	for _, bad := range []string{"<script", "onerror", "onclick", "<iframe", "javascript:", "127.0.0.1"} {
		if strings.Contains(article.Body, bad) {
			t.Fatal("unsafe", bad)
		}
	}
	repo := content.NewMemoryRepository(false)
	batch := Batch([]Collected{article}, map[string]string{"https://mmbiz.qpic.cn/image": "/media/1/example.jpg"}, 1, "本站作者", "owner")
	plan, e := repo.PreviewImport(t.Context(), batch)
	if e != nil || plan.New != 1 {
		t.Fatal(plan, e)
	}
	applied, e := repo.ApplyImport(t.Context(), batch, plan.Revision)
	if e != nil {
		t.Fatal(e)
	}
	post, e := repo.GetByID(t.Context(), applied.Items[0].PostID)
	if e != nil || post.Status != content.StatusDraft || !strings.Contains(post.ContentHTML, "公众号作者") || !strings.Contains(post.ContentHTML, "/media/1/example.jpg") {
		t.Fatal(post, e)
	}
	plan, e = repo.PreviewImport(t.Context(), batch)
	if e != nil || plan.New != 0 || plan.Existing != 1 {
		t.Fatal("not idempotent", plan, e)
	}
	if _, e = ParseArticle(article.URL, "<h1>请完成验证</h1>"); e == nil {
		t.Fatal("challenge accepted")
	}
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	jpegData, e := JPEG(pngData.Bytes())
	if e != nil || !bytes.HasPrefix(jpegData, []byte{0xff, 0xd8}) {
		t.Fatal("image conversion", e)
	}
	if _, e = JPEG([]byte("not image")); e == nil {
		t.Fatal("non image")
	}
}

func TestDraftThenPublishAndPoll(t *testing.T) {
	s, p := fixture(t)
	calls := map[string]int{}
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			return reply(r, `{"access_token":"token","expires_in":7200}`), nil
		case "/cgi-bin/draft/add":
			var body struct {
				Articles []Article `json:"articles"`
			}
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil || len(body.Articles) != 1 || body.Articles[0].Thumb != "cover" {
				t.Fatal(body, e)
			}
			return reply(r, `{"media_id":"draft-id"}`), nil
		case "/cgi-bin/freepublish/submit":
			return reply(r, `{"publish_id":100000001}`), nil
		case "/cgi-bin/freepublish/get":
			return reply(r, `{"publish_id":"100000001","publish_status":0,"article_id":"article-id","article_detail":{"item":[{"article_url":"https://mp.weixin.qq.com/s/abcdef"}]}}`), nil
		}
		t.Fatal(r.URL.Path)
		return nil, errors.New("unexpected")
	})}
	j, e := s.Queue(t.Context(), p.ID, p.Version, "owner")
	if e != nil {
		t.Fatal(e)
	}
	duplicate, e := s.Queue(t.Context(), p.ID, p.Version, "owner")
	if e != nil || duplicate.ID != j.ID {
		t.Fatal("duplicate queue", e)
	}
	if e = s.Tick(t.Context(), time.Now()); e != nil {
		t.Fatal(e)
	}
	j = stateJob(t, s)
	if j.Status != "draft" || calls["/cgi-bin/freepublish/submit"] != 0 {
		t.Fatal("not draft default", j)
	}
	if e = s.Action(t.Context(), j.ID, "publish", j.CreatedAt.Add(-time.Hour)); !errors.Is(e, ErrConflict) {
		t.Fatal("stale action", e)
	}
	if e = s.Action(t.Context(), j.ID, "publish", j.UpdatedAt); e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(t.Context(), time.Now()); e != nil {
		t.Fatal(e)
	}
	j = stateJob(t, s)
	if j.Status != "publishing" || j.PublishID != "100000001" {
		t.Fatal(j)
	}
	if e = s.Tick(t.Context(), time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	j = stateJob(t, s)
	if j.Status != "published" || j.ArticleURL == "" {
		t.Fatal(j)
	}
	if calls["/cgi-bin/draft/add"] != 1 || calls["/cgi-bin/freepublish/submit"] != 1 || calls["/cgi-bin/stable_token"] != 1 {
		t.Fatal(calls)
	}
}

func TestUncertainMutationNeverBlindlyRetries(t *testing.T) {
	for _, stage := range []string{"draft", "publish"} {
		t.Run(stage, func(t *testing.T) {
			s, p := fixture(t)
			v, _ := s.Store.View(t.Context())
			cfg := v.Config
			cfg.AutoPublish = stage == "publish"
			if _, e := s.SaveConfig(t.Context(), cfg, false); e != nil {
				t.Fatal(e)
			}
			writes := 0
			s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "stable_token") {
					return reply(r, `{"access_token":"token","expires_in":7200}`), nil
				}
				writes++
				if stage == "publish" && strings.Contains(r.URL.Path, "draft/add") {
					return reply(r, `{"media_id":"draft-id"}`), nil
				}
				return nil, errors.New("simulated response loss")
			})}
			_, e := s.Queue(t.Context(), p.ID, p.Version, "owner")
			if e != nil {
				t.Fatal(e)
			}
			if e = s.Tick(t.Context(), time.Now()); e != nil {
				t.Fatal(e)
			}
			j := stateJob(t, s)
			if j.Status != "uncertain" || j.Stage != stage {
				t.Fatal(j)
			}
			for _, action := range []string{"retry", "publish", "cancel", "resume", "draft_only"} {
				if e = s.Action(t.Context(), j.ID, action, j.UpdatedAt); e == nil {
					t.Fatal("uncertain action allowed", action)
				}
			}
			before := writes
			if e = s.Tick(t.Context(), time.Now().Add(time.Hour)); e != nil || writes != before {
				t.Fatal("automatic resubmit", e)
			}
			if stage == "publish" {
				if e = s.Resolve(t.Context(), j.ID, j.UpdatedAt, "wrong-draft", ""); e == nil {
					t.Fatal("publish downgraded")
				}
				e = s.Resolve(t.Context(), j.ID, j.UpdatedAt, "", "verified-publish")
			} else {
				e = s.Resolve(t.Context(), j.ID, j.UpdatedAt, "verified-draft", "")
			}
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestRestartLeasesAndAutomaticPublication(t *testing.T) {
	s, p := fixture(t)
	_, e := s.Queue(t.Context(), p.ID, p.Version, "owner")
	if e != nil {
		t.Fatal(e)
	}
	e = s.Store.Update(t.Context(), func(v *State) error {
		v.Config.Enabled = false
		v.Jobs[0].Status = "working"
		v.Jobs[0].Stage = "publish"
		v.Jobs[0].LeaseUntil = time.Now().Add(-time.Minute)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Tick(t.Context(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if j := stateJob(t, s); j.Status != "uncertain" {
		t.Fatal(j)
	}
	s, _ = fixture(t)
	v, _ := s.Store.View(t.Context())
	cfg := v.Config
	cfg.AutoSync = true
	cfg, e = s.SaveConfig(t.Context(), cfg, false)
	if e != nil {
		t.Fatal(e)
	}
	post, e := s.Content.Create(t.Context(), content.PostInput{Type: content.TypePost, Status: content.StatusPublish, Title: "自动同步", Slug: "automatic"})
	if e != nil {
		t.Fatal(e)
	}
	event := activity.Event{Kind: activity.Publication, PostID: post.ID, At: time.Now()}
	if e = s.Observe(t.Context(), event); e != nil {
		t.Fatal(e)
	}
	if e = s.Observe(t.Context(), event); e != nil {
		t.Fatal(e)
	}
	_ = stateJob(t, s)
}

func TestTokenRefreshAndPermissionFailure(t *testing.T) {
	c := NewClient()
	tokens, requests := 0, 0
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "stable_token") {
			tokens++
			return reply(r, `{"access_token":"token","expires_in":7200}`), nil
		}
		requests++
		if requests == 1 {
			return reply(r, `{"errcode":42001}`), nil
		}
		return reply(r, `{"errcode":48001,"errmsg":"secret must not leak"}`), nil
	})}
	_, e := c.Draft(context.Background(), Config{AppID: "wx0123456789abcdef", Secret: strings.Repeat("s", 32)}, Article{})
	var api *APIError
	if !errors.As(e, &api) || api.Uncertain || api.Code != 48001 || tokens != 2 || requests != 2 || strings.Contains(e.Error(), "secret") {
		t.Fatal(e, tokens, requests)
	}
}

func TestImageUploadIsReusedAfterDraftFailure(t *testing.T) {
	s, p := fixture(t)
	library, err := media.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Media = library
	var picture bytes.Buffer
	_ = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	item, err := library.Upload(t.Context(), bytes.NewReader(picture.Bytes()), media.UploadInput{Filename: "wechat.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	post, err := s.Content.Update(t.Context(), p.ID, content.Mutation{ExpectedVersion: p.Version}, content.PostInput{Type: content.TypePost, Status: content.StatusDraft, Title: p.Title, Slug: p.Slug, CoverURL: item.PublicURL, ContentSource: `<p>图片正文</p><img src="` + item.PublicURL + `">`})
	if err != nil {
		t.Fatal(err)
	}
	uploads, drafts := 0, 0
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			return reply(r, `{"access_token":"token","expires_in":7200}`), nil
		case "/cgi-bin/material/add_material", "/cgi-bin/media/uploadimg":
			uploads++
			reader, e := r.MultipartReader()
			if e != nil {
				t.Fatal(e)
			}
			part, e := reader.NextPart()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(part)
			if e != nil || !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
				t.Fatal("upload is not JPEG", e)
			}
			if r.URL.Query().Get("type") == "image" {
				return reply(r, `{"media_id":"cover-id"}`), nil
			}
			return reply(r, `{"url":"https://mmbiz.qpic.cn/uploaded"}`), nil
		case "/cgi-bin/draft/add":
			drafts++
			var body struct {
				Articles []Article `json:"articles"`
			}
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Fatal(e)
			}
			if body.Articles[0].Thumb != "cover-id" || !strings.Contains(body.Articles[0].Content, "https://mmbiz.qpic.cn/uploaded") {
				t.Fatal(body)
			}
			if drafts == 1 {
				return reply(r, `{"errcode":48001}`), nil
			}
			return reply(r, `{"media_id":"draft-id"}`), nil
		}
		t.Fatal(r.URL.Path)
		return nil, errors.New("unexpected endpoint")
	})}
	if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	j := stateJob(t, s)
	if j.Status != "failed" || len(j.Images) != 2 {
		t.Fatal(j)
	}
	if err = s.Action(t.Context(), j.ID, "retry", j.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	j = stateJob(t, s)
	if j.Status != "draft" || uploads != 2 || drafts != 2 {
		t.Fatal(j, uploads, drafts)
	}
	processed, err := library.ProcessImage(t.Context(), item.ID, item.Version, media.ImageInput{FocalX: 50, FocalY: 50})
	if err != nil {
		t.Fatal(err)
	}
	variant := processed.Image.Variants[0].Name
	data, err := s.localImage(processed.VariantURL(variant, "native"))
	if err != nil || len(data) == 0 {
		t.Fatal("managed variant not supported", err)
	}
}
