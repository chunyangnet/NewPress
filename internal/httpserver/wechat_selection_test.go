package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/wechat"
)

type wechatSelectionPreview struct {
	Articles []wechat.Collected `json:"articles"`
	Receipts map[string]string  `json:"receipts"`
	Plan     content.ImportPlan `json:"plan"`
}

func collectionImageMarkup(key string, start, count int) string {
	var body strings.Builder
	fmt.Fprintf(&body, `<meta property="og:title" content="%s"><div id="js_content"><p>待导入正文</p>`, key)
	for i := start; i < start+count; i++ {
		fmt.Fprintf(&body, `<img data-src="https://mmbiz.qpic.cn/budget-%d" alt="图片 %d">`, i, i)
	}
	body.WriteString("</div>")
	return body.String()
}

func TestWeChatCollectionRejectsOversizedSelectionBeforeDownloads(t *testing.T) {
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	const base = "https://example.test/api/v1/admin/wechat/collect/"
	headers := authenticatedAPIHeaders(owner)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 12, 12))); err != nil {
		t.Fatal(err)
	}
	downloads := 0
	s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
		var body io.Reader
		if r.URL.Hostname() == "mp.weixin.qq.com" {
			start := 0
			if r.URL.Path == "/s/second" {
				start = 26
			}
			body = strings.NewReader(collectionImageMarkup(r.URL.Path, start, 26))
		} else {
			downloads++
			body = bytes.NewReader(picture.Bytes())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: r}, nil
	})}
	r := serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/firstx","https://mp.weixin.qq.com/s/second"]}`, headers)
	var preview wechatSelectionPreview
	if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Articles) != 2 || len(preview.Receipts) != 2 || downloads != 0 {
		t.Fatal(r.Code, r.Body.String(), err, downloads)
	}
	r = serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts}), headers)
	library, err := s.media.List(media.Query{})
	if r.Code != 422 || downloads != 0 || err != nil || library.Total != 0 {
		t.Fatalf("oversized selection did work before rejecting: status=%d downloads=%d media=%d err=%v response=%s", r.Code, downloads, library.Total, err, r.Body.String())
	}
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Images int `json:"images"`
				Limit  int `json:"limit"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &failure); err != nil || failure.Error.Code != "wechat.image_limit" || failure.Error.Details.Images != 52 || failure.Error.Details.Limit != 50 {
		t.Fatal("image limit response lost its selection details", r.Body.String(), err)
	}
	// The same signed preview can still be imported in smaller, explicit batches.
	for _, article := range preview.Articles {
		r = serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": []wechat.Collected{article}, "receipts": map[string]string{article.Key: preview.Receipts[article.Key]}}), headers)
		var applied wechatSelectionPreview
		if err := json.Unmarshal(r.Body.Bytes(), &applied); err != nil || r.Code != 200 || applied.Plan.New != 1 {
			t.Fatal("split import failed", r.Code, r.Body.String(), err)
		}
	}
	if downloads != 52 {
		t.Fatal("unexpected image downloads", downloads)
	}
	r = serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts}), headers)
	if r.Code != 200 || downloads != 52 {
		t.Fatal("existing images were counted or downloaded on replay", r.Code, downloads, r.Body.String())
	}
}

func TestWeChatCollectionCountsSharedImagesAndIndependentCover(t *testing.T) {
	for _, independentCover := range []bool{false, true} {
		t.Run(fmt.Sprintf("independent-cover-%t", independentCover), func(t *testing.T) {
			s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
			const base = "https://example.test/api/v1/admin/wechat/collect/"
			headers := authenticatedAPIHeaders(owner)
			var picture bytes.Buffer
			if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 12, 12))); err != nil {
				t.Fatal(err)
			}
			downloads := 0
			s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
				var body io.Reader
				if r.URL.Hostname() == "mp.weixin.qq.com" {
					start, cover := 0, 0
					if r.URL.Path == "/s/second" {
						start = 24
					}
					if independentCover {
						cover = 50
					}
					markup := fmt.Sprintf(`<meta property="og:image" content="https://mmbiz.qpic.cn/budget-%d">`, cover) + collectionImageMarkup(r.URL.Path, start, 26)
					body = strings.NewReader(markup)
				} else {
					downloads++
					body = bytes.NewReader(picture.Bytes())
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: r}, nil
			})}
			r := serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/firstx","https://mp.weixin.qq.com/s/second"]}`, headers)
			var preview wechatSelectionPreview
			if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Articles) != 2 {
				t.Fatal(r.Code, r.Body.String(), err)
			}
			r = serveContentAPI(h, cookie, "POST", base+"apply", wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts}), headers)
			if independentCover {
				if r.Code != 422 || downloads != 0 || !strings.Contains(r.Body.String(), `"images":51`) {
					t.Fatal("independent cover omitted from budget", r.Code, downloads, r.Body.String())
				}
			} else if r.Code != 200 || downloads != 50 {
				t.Fatal("shared image or cover counted twice", r.Code, downloads, r.Body.String())
			}
		})
	}
}

type collectionCheckpointRepository struct {
	content.Repository
	afterPreview func(int)
	previews     int
	applies      int
}

func (r *collectionCheckpointRepository) PreviewImport(ctx context.Context, batch content.ImportBatch) (content.ImportPlan, error) {
	plan, err := r.Repository.PreviewImport(ctx, batch)
	r.previews++
	if r.afterPreview != nil {
		r.afterPreview(r.previews)
	}
	return plan, err
}

func (r *collectionCheckpointRepository) ApplyImport(ctx context.Context, batch content.ImportBatch, revision string) (content.ImportPlan, error) {
	r.applies++
	return r.Repository.ApplyImport(ctx, batch, revision)
}

func TestWeChatCollectionModuleDisableStopsAtCheckpoints(t *testing.T) {
	for _, phase := range []string{"preview", "first-image", "later-image", "commit-without-images"} {
		t.Run(phase, func(t *testing.T) {
			s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
			const base = "https://example.test/api/v1/admin/wechat/collect/"
			headers := authenticatedAPIHeaders(owner)
			wrapped := &collectionCheckpointRepository{Repository: s.repository}
			s.repository = wrapped
			var picture bytes.Buffer
			if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 12, 12))); err != nil {
				t.Fatal(err)
			}
			setEnabled := func(enabled bool) error {
				state, err := s.wechat.Store.View(t.Context())
				if err != nil {
					return err
				}
				cfg := state.Config
				cfg.CollectEnabled = enabled
				_, err = s.wechat.SaveConfig(t.Context(), cfg, false)
				return err
			}
			downloads := 0
			armed := true
			s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
				var body io.Reader
				if r.URL.Hostname() == "mp.weixin.qq.com" {
					count := 2
					if phase == "commit-without-images" {
						count = 0
					}
					body = strings.NewReader(collectionImageMarkup("检查模块开关", 0, count))
					if phase == "preview" && armed {
						if err := setEnabled(false); err != nil {
							return nil, err
						}
					}
				} else {
					downloads++
					body = bytes.NewReader(picture.Bytes())
					if armed && (phase == "first-image" && downloads == 1 || phase == "later-image" && downloads == 2) {
						if err := setEnabled(false); err != nil {
							return nil, err
						}
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: r}, nil
			})}
			r := serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/firstx"]}`, headers)
			if phase == "preview" {
				if r.Code != 422 || !strings.Contains(r.Body.String(), "采集模块已关闭") || downloads != 0 || wrapped.applies != 0 {
					t.Fatal("disabled preview continued", r.Code, downloads, r.Body.String())
				}
				return
			}
			var preview wechatSelectionPreview
			if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || len(preview.Articles) != 1 {
				t.Fatal(r.Code, r.Body.String(), err)
			}
			wrapped.previews = 0
			if phase == "commit-without-images" {
				wrapped.afterPreview = func(calls int) {
					if calls == 3 {
						if err := setEnabled(false); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			request := wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts})
			r = serveContentAPI(h, cookie, "POST", base+"apply", request, headers)
			library, err := s.media.List(media.Query{})
			wantMedia := 0
			if phase == "later-image" {
				wantMedia = 1
			}
			if r.Code != 422 || !strings.Contains(r.Body.String(), "采集模块已关闭") || wrapped.applies != 0 || err != nil || library.Total != wantMedia {
				t.Fatal("disabled import continued", r.Code, wrapped.applies, library.Total, err, r.Body.String())
			}
			// Re-enabling never imports automatically; the same explicit request can resume.
			armed = false
			wrapped.afterPreview = nil
			if err = setEnabled(true); err != nil {
				t.Fatal(err)
			}
			r = serveContentAPI(h, cookie, "POST", base+"apply", request, headers)
			if r.Code != 200 || wrapped.applies != 1 {
				t.Fatal("re-enabled explicit import failed", r.Code, wrapped.applies, r.Body.String())
			}
		})
	}
}
