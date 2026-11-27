package httpserver

import (
	"bytes"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatCollectionStoresWebPDisplayColors(t *testing.T) {
	for _, name := range []string{"palette", "alpha-rotated"} {
		t.Run(name, func(t *testing.T) {
			read := func(extension string) []byte {
				data, err := os.ReadFile(filepath.Join("..", "media", "testdata", "webp-color", name+extension))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			original := read(".webp")
			reference, err := wechat.JPEG(read(".png"))
			if err != nil {
				t.Fatal(err)
			}
			want, _, err := image.Decode(bytes.NewReader(reference))
			if err != nil {
				t.Fatal(err)
			}
			s, _, manager, repository, cookie, owner := newPersistentAdminTestServer(t)
			root := t.TempDir()
			library, err := media.NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			s = NewWithSiteSettingsAndMedia(s.config, repository, s.themes, s.plugins, s.siteSettings, library, manager)
			h := s.routes()
			const base = "https://example.test/api/v1/admin/wechat/collect/"
			headers := authenticatedAPIHeaders(owner)
			downloads := 0
			s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
				var body io.Reader
				switch r.URL.Hostname() {
				case "mp.weixin.qq.com":
					body = strings.NewReader(`<meta property="og:title" content="色彩采集"><meta property="og:image" content="https://mmbiz.qpic.cn/colors"><div id="js_content"><p>颜色与透明度</p><img data-src="https://mmbiz.qpic.cn/colors" alt="颜色样张"></div>`)
				case "mmbiz.qpic.cn":
					downloads++
					body = bytes.NewReader(original)
				default:
					t.Fatal("unexpected remote host", r.URL.Hostname())
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: r}, nil
			})}
			r := serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/color-test"]}`, headers)
			var preview wechatSelectionPreview
			if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || downloads != 0 || len(preview.Articles) != 1 {
				t.Fatal("invalid collection preview", r.Code, downloads, r.Body.String(), err)
			}
			body := wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts})
			r = serveContentAPI(h, cookie, "POST", base+"apply", body, headers)
			var result wechatSelectionPreview
			if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || len(result.Plan.Items) != 1 || downloads != 1 {
				t.Fatal("WebP import failed", r.Code, downloads, r.Body.String(), err)
			}
			post, err := s.repository.GetByID(t.Context(), result.Plan.Items[0].PostID)
			if err != nil || post.Status != content.StatusDraft || !strings.Contains(post.ContentHTML, `alt="颜色样张"`) {
				t.Fatal(post, err)
			}
			reopened, err := media.NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			items, err := reopened.List(media.Query{})
			if err != nil || items.Total != 1 || items.Items[0].Width != want.Bounds().Dx() || items.Items[0].Height != want.Bounds().Dy() || post.CoverURL != items.Items[0].PublicURL {
				t.Fatal("stored dimensions or cover differs", items, err)
			}
			_, stream, err := reopened.Open(items.Items[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			got, format, err := image.Decode(stream)
			stream.Close()
			if err != nil || format != "jpeg" || got.Bounds() != want.Bounds() {
				t.Fatal("invalid stored image", format, err)
			}
			var sums [3]uint64
			for y := 0; y < want.Bounds().Dy(); y++ {
				for x := 0; x < want.Bounds().Dx(); x++ {
					gr, gg, gb, _ := got.At(x, y).RGBA()
					wr, wg, wb, _ := want.At(x, y).RGBA()
					for channel, pair := range [][2]uint32{{gr, wr}, {gg, wg}, {gb, wb}} {
						a, b := pair[0], pair[1]
						if a > b {
							a, b = b, a
						}
						sums[channel] += uint64(b - a)
					}
				}
			}
			for channel, sum := range sums {
				if mean := float64(sum) / float64(want.Bounds().Dx()*want.Bounds().Dy()) / 257; mean > 2.5 {
					t.Errorf("stored channel %d mean error %.3f exceeds 2.5", channel, mean)
				}
			}
			if replay := serveContentAPI(h, cookie, "POST", base+"apply", body, headers); replay.Code != 200 || downloads != 1 {
				t.Fatal("replay downloaded or imported again", replay.Code, downloads)
			}
		})
	}
}
