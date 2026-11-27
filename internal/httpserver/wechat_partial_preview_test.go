package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/wechat"
)

type partialWeChatPreview struct {
	wechatSelectionPreview
	Failures []wechat.CollectFailure `json:"failures"`
}

func TestWeChatCollectionIsolatesInvalidArticlesAndRetries(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	path := filepath.Join(t.TempDir(), "content.json")
	repo, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	reads := &collectionCheckpointRepository{Repository: repo}
	s.repository = reads
	const base = "https://example.test/api/v1/admin/wechat/collect/"
	headers := authenticatedAPIHeaders(owner)
	var picture bytes.Buffer
	if err = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 12, 12))); err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{
		"/s/valid1":  collectionImageMarkup("正常文章", 0, 1),
		"/s/title1":  `<meta property="og:title" content="异常&#127;标题"><div id="js_content"><p>正文</p></div>`,
		"/s/excerpt": `<meta property="og:title" content="异常摘要"><meta name="description" content="摘要&#127;内容"><div id="js_content"><p>正文</p></div>`,
		// Escaping can make a readable source exceed the stored content limit.
		"/s/large1": `<meta property="og:title" content="膨胀正文"><div id="js_content"><p>` + strings.Repeat("&", content.MaxContentBytes/5+1) + `</p></div>`,
	}
	for address, source := range sources {
		if _, err := wechat.ParseArticle("https://mp.weixin.qq.com"+address, source); err != nil {
			t.Fatal("fixture must reach import validation", address, err)
		}
	}
	var downloads atomic.Int32
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		var body io.Reader
		if req.URL.Hostname() == "mp.weixin.qq.com" {
			body = strings.NewReader(sources[req.URL.Path])
		} else {
			downloads.Add(1)
			body = bytes.NewReader(picture.Bytes())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: req}, nil
	})}
	urls := []string{"https://mp.weixin.qq.com/s/valid1", "https://mp.weixin.qq.com/s/title1", "https://mp.weixin.qq.com/s/excerpt", "https://mp.weixin.qq.com/s/large1"}
	r := serveContentAPI(handler, cookie, "POST", base+"preview", wechatJSON(t, map[string]any{"urls": urls}), headers)
	var preview partialWeChatPreview
	if err = json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Articles) != 1 || len(preview.Failures) != 3 || preview.Plan.New != 1 || len(preview.Receipts) != 1 {
		t.Fatalf("invalid article discarded the valid preview: status=%d articles=%d failures=%d err=%v response=%.1200s", r.Code, len(preview.Articles), len(preview.Failures), err, r.Body.String())
	}
	if downloads.Load() != 0 || preview.Articles[0].URL != urls[0] {
		t.Fatal("preview fetched media or changed input order", downloads.Load())
	}
	if reads.previews != 5 {
		t.Fatal("per-article plans were redundantly read", reads.previews)
	}
	wantErrors := map[string]string{urls[1]: "标题不能包含控制字符", urls[2]: "摘要不能包含控制字符", urls[3]: "正文不能超过 2 MiB"}
	for _, failure := range preview.Failures {
		message, ok := wantErrors[failure.URL]
		if !ok || !strings.Contains(failure.Message, message) {
			t.Fatal("missing per-article validation detail", failure)
		}
		delete(wantErrors, failure.URL)
	}
	if len(wantErrors) != 0 {
		t.Fatal(wantErrors)
	}
	// A failed article cannot be appended using the remaining article's receipt.
	rejected, err := wechat.ParseArticle(urls[1], sources["/s/title1"])
	if err != nil {
		t.Fatal(err)
	}
	tampered := append(append([]wechat.Collected{}, preview.Articles...), rejected)
	r = serveContentAPI(handler, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": tampered, "receipts": preview.Receipts}), headers)
	if r.Code != 409 || downloads.Load() != 0 || reads.applies != 0 {
		t.Fatal("failed article acquired confirmation", r.Code, downloads.Load(), reads.applies)
	}
	request := wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts})
	r = serveContentAPI(handler, cookie, "POST", base+"apply", request, headers)
	var result wechatSelectionPreview
	if err = json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || result.Plan.New != 1 || len(result.Plan.Items) != 1 || downloads.Load() != 1 {
		t.Fatal("valid selection did not import", r.Code, r.Body.String(), err, downloads.Load())
	}
	post, err := repo.GetByID(t.Context(), result.Plan.Items[0].PostID)
	if err != nil || post.Status != content.StatusDraft || !strings.Contains(post.ContentHTML, "/media/") {
		t.Fatal(post, err)
	}
	post, err = repo.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: "保留人工修改", Slug: post.Slug, ContentSource: "<p>人工正文</p>", AuthorID: owner.UserID})
	if err != nil {
		t.Fatal(err)
	}
	// Repeated receipt retries must not duplicate or replace a draft.
	for range 2 {
		r = serveContentAPI(handler, cookie, "POST", base+"apply", request, headers)
		if r.Code != 200 || downloads.Load() != 1 {
			t.Fatal("retry duplicated work", r.Code, r.Body.String(), downloads.Load())
		}
	}
	sources["/s/title1"] = `<meta property="og:title" content="已修正标题"><div id="js_content"><p>修正后的正文</p></div>`
	r = serveContentAPI(handler, cookie, "POST", base+"preview", wechatJSON(t, map[string]any{"urls": []string{urls[1]}}), headers)
	var retry partialWeChatPreview
	if err = json.Unmarshal(r.Body.Bytes(), &retry); err != nil || r.Code != 200 || len(retry.Articles) != 1 || len(retry.Failures) != 0 || len(retry.Receipts) != 1 {
		t.Fatal("corrected link cannot be retried", r.Code, r.Body.String(), err)
	}
	r = serveContentAPI(handler, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": retry.Articles, "receipts": retry.Receipts}), headers)
	if r.Code != 200 {
		t.Fatal("corrected article cannot be imported", r.Code, r.Body.String())
	}
	reopened, err := content.NewFileRepository(path, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.GetByID(t.Context(), post.ID)
	if err != nil || wechatJSON(t, after) != wechatJSON(t, post) {
		t.Fatal("retry changed saved article", wechatJSON(t, after), wechatJSON(t, post), err)
	}
	list, err := reopened.ListAdmin(t.Context(), content.AdminQuery{Limit: 100})
	library, mediaErr := s.media.List(media.Query{})
	if err != nil || list.Total != 2 || mediaErr != nil || library.Total != 1 || downloads.Load() != 1 {
		t.Fatal("failed rows leaked drafts or media", list.Total, library.Total, downloads.Load(), err, mediaErr)
	}
}

func TestWeChatCollectionAllInvalidReturnsActionableFailures(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`<meta property="og:title" content="标题&#127;异常"><div id="js_content">正文</div>`)), Request: req}, nil
	})}
	r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/wechat/collect/preview", `{"urls":["https://mp.weixin.qq.com/s/badone","https://mp.weixin.qq.com/s/badtwo"]}`, authenticatedAPIHeaders(owner))
	var result partialWeChatPreview
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || result.Articles == nil || len(result.Articles) != 0 || len(result.Failures) != 2 || len(result.Receipts) != 0 {
		t.Fatal("all-invalid response lost retry details", r.Code, r.Body.String(), err)
	}
	for _, failure := range result.Failures {
		if !strings.Contains(failure.Message, "标题不能包含控制字符") {
			t.Fatal(failure)
		}
	}
}

type partialPreviewRepository struct {
	content.Repository
	onSingle func(content.ImportBatch) error
}

func (r *partialPreviewRepository) PreviewImport(ctx context.Context, batch content.ImportBatch) (content.ImportPlan, error) {
	if len(batch.Items) == 1 && r.onSingle != nil {
		if err := r.onSingle(batch); err != nil {
			return content.ImportPlan{}, err
		}
	}
	return r.Repository.PreviewImport(ctx, batch)
}

func TestWeChatCollectionPreviewInfrastructureFailureDoesNotBecomeArticleFailure(t *testing.T) {
	for _, failure := range []error{errors.New("private storage detail"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
			s.repository = &partialPreviewRepository{Repository: s.repository, onSingle: func(batch content.ImportBatch) error {
				if batch.Items[0].Input.Title == "second" {
					return failure
				}
				return nil
			}}
			s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
				title := "firstx"
				if strings.HasSuffix(req.URL.Path, "second") {
					title = "second"
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(collectionImageMarkup(title, 0, 0))), Request: req}, nil
			})}
			r := serveContentAPI(handler, cookie, "POST", "https://example.test/api/v1/admin/wechat/collect/preview", `{"urls":["https://mp.weixin.qq.com/s/firstx","https://mp.weixin.qq.com/s/second"]}`, authenticatedAPIHeaders(owner))
			if r.Code != 503 || strings.Contains(r.Body.String(), "private storage detail") || strings.Contains(r.Body.String(), `"receipts"`) || strings.Contains(r.Body.String(), `"articles"`) {
				t.Fatal("infrastructure failure became a usable partial preview", r.Code, r.Body.String())
			}
		})
	}
}

func TestWeChatCollectionPartialPreviewKeepsConflictsAndModuleChecks(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "conflicting-target", true: "disabled-during-validation"}[disabled], func(t *testing.T) {
			s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
			const base = "https://example.test/api/v1/admin/wechat/collect/"
			const validURL = "https://mp.weixin.qq.com/s/valid1"
			validSource := collectionImageMarkup("正常文章", 0, 0)
			valid, err := wechat.ParseArticle(validURL, validSource)
			if err != nil {
				t.Fatal(err)
			}
			if !disabled {
				_, err = s.repository.Create(t.Context(), content.PostInput{Title: "占用路径", Slug: "wechat-" + valid.Key[:20], ContentSource: "保留原文", AuthorID: owner.UserID})
				if err != nil {
					t.Fatal(err)
				}
			}
			reads := &collectionCheckpointRepository{Repository: s.repository}
			s.repository = reads
			if disabled {
				reads.afterPreview = func(calls int) {
					if calls != 2 {
						return
					}
					cfg, err := wechat.ReadConfig(t.Context(), s.wechat.Store)
					if err != nil {
						t.Fatal(err)
					}
					cfg.CollectEnabled = false
					if _, err = s.wechat.SaveConfig(t.Context(), cfg, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(req *http.Request) (*http.Response, error) {
				source := validSource
				if req.URL.Path != "/s/valid1" {
					source = `<meta property="og:title" content="标题&#127;异常"><div id="js_content">正文</div>`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(source)), Request: req}, nil
			})}
			r := serveContentAPI(handler, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/valid1","https://mp.weixin.qq.com/s/badone"]}`, authenticatedAPIHeaders(owner))
			if disabled {
				if r.Code != 422 || !strings.Contains(r.Body.String(), "采集模块已关闭") || strings.Contains(r.Body.String(), `"receipts"`) {
					t.Fatal("disabled preview returned confirmations", r.Code, r.Body.String())
				}
				return
			}
			var preview partialWeChatPreview
			if err = json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Articles) != 1 || len(preview.Failures) != 1 || preview.Plan.Conflicts != 1 || len(preview.Receipts) != 0 {
				t.Fatal("target conflict was hidden or signed", r.Code, r.Body.String(), err)
			}
		})
	}
}
