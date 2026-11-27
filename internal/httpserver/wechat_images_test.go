package httpserver

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/wechat"
)

func TestWeChatCollectionStoresOrientedPictures(t *testing.T) {
	for _, orientation := range []int{6, 9} {
		t.Run(fmt.Sprint(orientation), func(t *testing.T) {
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
			img := image.NewRGBA(image.Rect(0, 0, 48, 80))
			for y := 0; y < 80; y++ {
				for x := 0; x < 48; x++ {
					c := color.RGBA{R: 220, A: 255}
					if y >= 40 {
						c = color.RGBA{B: 220, A: 255}
					}
					img.SetRGBA(x, y, c)
				}
			}
			var encoded bytes.Buffer
			if err = jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 95}); err != nil {
				t.Fatal(err)
			}
			metadata := append([]byte("Exif\x00\x00"), []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, byte(orientation), 0, 0, 0, 0, 0, 0, 0}...)
			metadata = append(metadata, []byte("private-camera-location")...)
			segment := []byte{0xff, 0xe1, 0, 0}
			binary.BigEndian.PutUint16(segment[2:], uint16(len(metadata)+2))
			picture := append(append(append(append([]byte{}, encoded.Bytes()[:2]...), segment...), metadata...), encoded.Bytes()[2:]...)
			downloads := 0
			s.wechat.Client.HTTP = &http.Client{Transport: wechatTransport(func(r *http.Request) (*http.Response, error) {
				var body io.Reader
				if r.URL.Hostname() == "mp.weixin.qq.com" {
					body = strings.NewReader(`<meta property="og:title" content="照片方向"><meta property="og:image" content="https://mmbiz.qpic.cn/portrait"><div id="js_content"><p>导入照片</p><img data-src="https://mmbiz.qpic.cn/portrait" alt="竖拍照片"></div>`)
				} else if r.URL.Hostname() == "mmbiz.qpic.cn" {
					downloads++
					body = bytes.NewReader(picture)
				} else {
					t.Fatal("unexpected remote host", r.URL.Hostname())
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(body), Request: r}, nil
			})}
			r := serveContentAPI(h, cookie, "POST", base+"preview", `{"urls":["https://mp.weixin.qq.com/s/oriented-photo"]}`, headers)
			var preview wechatSelectionPreview
			if err := json.Unmarshal(r.Body.Bytes(), &preview); err != nil || r.Code != 200 || downloads != 0 || len(preview.Articles) != 1 {
				t.Fatal("preview fetched or lost the image", r.Code, downloads, r.Body.String(), err)
			}
			body := wechatJSON(t, map[string]any{"articles": preview.Articles, "receipts": preview.Receipts})
			r = serveContentAPI(h, cookie, "POST", base+"apply", body, headers)
			if orientation == 9 {
				items, err := library.List(media.Query{})
				if r.Code != 422 || !strings.Contains(r.Body.String(), "方向信息") || items.Total != 0 || err != nil {
					t.Fatal("damaged image imported", r.Code, items.Total, r.Body.String(), err)
				}
				plan, err := s.repository.PreviewImport(t.Context(), wechat.Batch(preview.Articles, nil, owner.UserID, owner.Label(), owner.Username))
				if err != nil || plan.New != 1 || plan.Existing != 0 {
					t.Fatal("failed image still created an article", plan, err)
				}
				return
			}
			var result wechatSelectionPreview
			if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil || r.Code != 200 || len(result.Plan.Items) != 1 || downloads != 1 {
				t.Fatal("oriented image import failed", r.Code, downloads, r.Body.String(), err)
			}
			post, err := s.repository.GetByID(t.Context(), result.Plan.Items[0].PostID)
			if err != nil || post.Status != content.StatusDraft || !strings.Contains(post.ContentHTML, `alt="竖拍照片"`) {
				t.Fatal(post, err)
			}
			reopened, err := media.NewManager(root)
			if err != nil {
				t.Fatal(err)
			}
			items, err := reopened.List(media.Query{})
			if err != nil || items.Total != 1 || items.Items[0].Width != 80 || items.Items[0].Height != 48 || post.CoverURL != items.Items[0].PublicURL {
				t.Fatal("stored display size or cover differs", items, err)
			}
			_, stream, err := reopened.Open(items.Items[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			data, err := io.ReadAll(stream)
			if err != nil || bytes.Contains(data, []byte("private-camera-location")) || bytes.Contains(data, []byte("Exif")) {
				t.Fatal("private metadata retained", err)
			}
			decoded, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			left := color.NRGBAModel.Convert(decoded.At(20, 24)).(color.NRGBA)
			right := color.NRGBAModel.Convert(decoded.At(60, 24)).(color.NRGBA)
			if left.B < 200 || left.R > 20 || right.R < 200 || right.B > 20 {
				t.Fatal("pixels did not follow clockwise rotation", left, right)
			}
			if replay := serveContentAPI(h, cookie, "POST", base+"apply", body, headers); replay.Code != 200 || downloads != 1 {
				t.Fatal("replay downloaded or imported again", replay.Code, downloads)
			}
		})
	}
}
