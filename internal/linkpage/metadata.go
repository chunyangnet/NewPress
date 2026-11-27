package linkpage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"newpress.local/newpress/internal/safehttp"
)

type Metadata struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Logo        string `json:"logo"`
	Notice      string `json:"notice"`
}

var urlIcon = url.URL{Path: "/favicon.ico"}

func cleanText(s string, n int) string {
	v := []rune(strings.Join(strings.Fields(s), " "))
	if len(v) > n {
		v = v[:n]
	}
	return string(v)
}
func parseMetadata(doc *html.Node, raw string) (Metadata, []string) {
	m := Metadata{URL: raw, Title: Host(raw)}
	var title, ogTitle, description, ogDescription string
	var icons []string
	var walk func(*html.Node)
	var words func(*html.Node) string
	words = func(n *html.Node) string {
		if n.Type == html.TextNode {
			return n.Data
		}
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			b.WriteString(words(c))
		}
		return b.String()
	}
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			a := map[string]string{}
			for _, v := range n.Attr {
				a[strings.ToLower(v.Key)] = v.Val
			}
			switch n.Data {
			case "title":
				if title == "" {
					title = words(n)
				}
			case "meta":
				switch strings.ToLower(a["property"]) {
				case "og:title":
					ogTitle = a["content"]
				case "og:description":
					ogDescription = a["content"]
				}
				if strings.EqualFold(a["name"], "description") {
					description = a["content"]
				}
			case "link":
				for _, rel := range strings.Fields(strings.ToLower(a["rel"])) {
					if (rel == "icon" || rel == "apple-touch-icon") && a["href"] != "" && len(icons) < 4 {
						icons = append(icons, a["href"])
						break
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if ogTitle != "" {
		title = ogTitle
	}
	if ogDescription != "" {
		description = ogDescription
	}
	if title = cleanText(title, 100); title != "" {
		m.Title = title
	}
	m.Description = cleanText(description, 500)
	return m, icons
}
func DecodeLogo(s string) ([]byte, error) {
	if len(s) > 96<<10 {
		return nil, ErrInvalid
	}
	b, e := base64.StdEncoding.DecodeString(s)
	if e != nil {
		return nil, ErrInvalid
	}
	c, f, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || f != "png" || c.Width < 1 || c.Height < 1 || c.Width > 128 || c.Height > 128 {
		return nil, ErrInvalid
	}
	return b, nil
}
func NormalizeLogo(b []byte) (string, error) {
	if len(b) > 2<<20 {
		return "", ErrInvalid
	}
	if len(b) > 6 && bytes.Equal(b[:4], []byte{0, 0, 1, 0}) {
		converted, e := icoPNG(b)
		if e != nil {
			return "", e
		}
		b = converted
	}
	c, _, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || c.Width < 1 || c.Height < 1 || c.Width > 4096 || c.Height > 4096 || int64(c.Width)*int64(c.Height) > 4_000_000 {
		return "", ErrInvalid
	}
	img, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		return "", ErrInvalid
	}
	w, h := c.Width, c.Height
	if w > 128 || h > 128 {
		if w >= h {
			h = max(1, h*128/w)
			w = 128
		} else {
			w = max(1, w*128/h)
			h = 128
		}
	}
	target := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(target, target.Bounds(), img, img.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	if e = png.Encode(&out, target); e != nil {
		return "", e
	}
	if out.Len() > 72<<10 {
		return "", ErrInvalid
	}
	return base64.StdEncoding.EncodeToString(out.Bytes()), nil
}

// Decode PNG-backed or uncompressed 32-bit ICO frames, then discard metadata.
// The AND mask is retained for older favicons without an alpha channel.
func icoPNG(b []byte) ([]byte, error) {
	count := int(binary.LittleEndian.Uint16(b[4:6]))
	if count < 1 || count > 32 || len(b) < 6+count*16 {
		return nil, ErrInvalid
	}
	var best []byte
	bestArea := 0
	for i := 0; i < count; i++ {
		entry := b[6+i*16 : 6+(i+1)*16]
		size, off := uint64(binary.LittleEndian.Uint32(entry[8:12])), uint64(binary.LittleEndian.Uint32(entry[12:16]))
		if off < uint64(6+count*16) || size < 8 || off+size > uint64(len(b)) {
			continue
		}
		data := b[off : off+size]
		if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			cfg, _, e := image.DecodeConfig(bytes.NewReader(data))
			if e == nil && cfg.Width <= 256 && cfg.Height <= 256 && cfg.Width*cfg.Height > bestArea {
				best = append([]byte(nil), data...)
				bestArea = cfg.Width * cfg.Height
			}
			continue
		}
		if len(data) < 40 || binary.LittleEndian.Uint32(data[:4]) != 40 || binary.LittleEndian.Uint16(data[14:16]) != 32 || binary.LittleEndian.Uint32(data[16:20]) != 0 {
			continue
		}
		w, h := int(binary.LittleEndian.Uint32(data[4:8])), int(binary.LittleEndian.Uint32(data[8:12]))/2
		if w < 1 || h < 1 || w > 256 || h > 256 || 40+w*h*4 > len(data) || w*h <= bestArea {
			continue
		}
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		hasAlpha := false
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				s := 40 + ((h-1-y)*w+x)*4
				d := y*img.Stride + x*4
				img.Pix[d], img.Pix[d+1], img.Pix[d+2], img.Pix[d+3] = data[s+2], data[s+1], data[s], data[s+3]
				hasAlpha = hasAlpha || data[s+3] != 0
			}
		}
		if !hasAlpha {
			stride := ((w + 31) / 32) * 4
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					alpha := byte(255)
					index := 40 + w*h*4 + (h-1-y)*stride + x/8
					if index < len(data) && data[index]&(1<<uint(7-x%8)) != 0 {
						alpha = 0
					}
					img.Pix[y*img.Stride+x*4+3] = alpha
				}
			}
		}
		var out bytes.Buffer
		if png.Encode(&out, img) == nil {
			best = out.Bytes()
			bestArea = w * h
		}
	}
	if best == nil {
		return nil, ErrInvalid
	}
	return best, nil
}

// Each hop uses safehttp's DNS-pinned transport. No cookies, credentials, proxy,
// insecure fallback or browser script execution is involved.
func fetch(ctx context.Context, client *http.Client, raw string, limit int64) ([]byte, string, string, error) {
	for hop := 0; hop < 4; hop++ {
		u, e := safehttp.ValidateURL(raw)
		if e != nil {
			return nil, "", "", e
		}
		req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if e != nil {
			return nil, "", "", e
		}
		req.Header.Set("User-Agent", "NewPress-LinkPreview/1.0")
		req.Header.Set("Accept", "text/html,image/png,image/jpeg,image/webp;q=0.9,*/*;q=0.1")
		res, e := client.Do(req)
		if e != nil {
			return nil, "", "", errors.New("站点暂时无法访问，可手动填写")
		}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			res.Body.Close()
			loc, e := res.Location()
			if e != nil {
				return nil, "", "", ErrInvalid
			}
			raw = loc.String()
			continue
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			return nil, "", "", errors.New("站点拒绝了信息读取，可手动填写")
		}
		data, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
		res.Body.Close()
		if e != nil || int64(len(data)) > limit {
			return nil, "", "", errors.New("站点内容过大或读取失败")
		}
		return data, res.Header.Get("Content-Type"), u.String(), nil
	}
	return nil, "", "", errors.New("站点跳转次数过多")
}
func Extract(ctx context.Context, raw string) (Metadata, error) {
	raw, e := NormalizeURL(raw)
	if e != nil {
		return Metadata{}, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := safehttp.Client(6 * time.Second)
	defer client.CloseIdleConnections()
	b, typ, final, e := fetch(ctx, client, raw, 1<<20)
	if e != nil {
		return Metadata{}, e
	}
	if typ != "" && !strings.Contains(strings.ToLower(typ), "html") {
		return Metadata{}, errors.New("该地址不是网页，请输入站点首页")
	}
	reader, e := charset.NewReader(bytes.NewReader(b), typ)
	if e != nil {
		return Metadata{}, e
	}
	doc, e := html.Parse(io.LimitReader(reader, 2<<20))
	if e != nil {
		return Metadata{}, e
	}
	m, icons := parseMetadata(doc, final)
	origin, _ := safehttp.ValidateURL(final)
	icons = append(icons, origin.ResolveReference(&urlIcon).String())
	for _, ref := range icons[:min(len(icons), 5)] {
		u, e := origin.Parse(ref)
		if e != nil {
			continue
		}
		data, _, _, e := fetch(ctx, client, u.String(), 2<<20)
		if e != nil {
			continue
		}
		m.Logo, e = NormalizeLogo(data)
		if e == nil {
			break
		}
	}
	if m.Logo == "" {
		m.Notice = "已提取文字信息；未找到可用的 PNG、JPEG 或 WebP 图标，可自行上传。"
	}
	return m, nil
}
