package wechat

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const collectionPageFixture = `<meta property="og:title" content="公开文章"><span id="js_name">来源公众号</span><div id="js_content"><p>正文保留完整。</p><img data-src="https://mmbiz.qpic.cn/fixture" alt="正文图片"></div>`

func TestCollectLargePageKeepsOnlyArticle(t *testing.T) {
	const address = "https://mp.weixin.qq.com/s/large1"
	want, err := ParseArticle(address, collectionPageFixture)
	if err != nil {
		t.Fatal(err)
	}
	script := `<script>` + strings.Repeat("/* page runtime */", 200000) + `</script>`
	for name, source := range map[string]string{"before-body": script + collectionPageFixture, "after-body": collectionPageFixture + script} {
		t.Run(name, func(t *testing.T) {
			c := &Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return reply(r, source), nil })}}
			got, err := c.FetchArticle(t.Context(), address)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("large page did not preserve the article: err=%v body_bytes=%d", err, len(got.Body))
			}
		})
	}
}

type articlePageReader struct {
	io.Reader
	read   int
	closed bool
}

func (r *articlePageReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func (r *articlePageReader) Close() error { r.closed = true; return nil }

func TestCollectPageSizeBoundary(t *testing.T) {
	const limit = 8 << 20
	const address = "https://mp.weixin.qq.com/s/large1"
	for _, size := range []int{limit, limit + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			source := `<script>` + strings.Repeat(" ", size-len(collectionPageFixture)-len(`<script></script>`)) + `</script>` + collectionPageFixture
			_, err := ParseArticle(address, source)
			check := func(err error) {
				t.Helper()
				if size <= limit && err != nil {
					t.Fatal("page at limit was rejected", err)
				}
				if size > limit && (err == nil || !strings.Contains(err.Error(), "文章页面超过 8 MiB")) {
					t.Fatal("oversized page needs a precise error", err)
				}
			}
			check(err)
			for _, length := range []int64{-1, int64(size)} {
				t.Run(fmt.Sprintf("content-length-%d", length), func(t *testing.T) {
					reader := &articlePageReader{Reader: strings.NewReader(source)}
					c := &Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
						response := reply(r, "")
						response.ContentLength, response.Body = length, reader
						return response, nil
					})}}
					_, err := c.FetchArticle(t.Context(), address)
					check(err)
					if !reader.closed || reader.read > limit+1 || size <= limit && reader.read != size {
						t.Fatalf("unexpected bounded read: closed=%v bytes=%d", reader.closed, reader.read)
					}
					if length > limit && reader.read != 0 {
						t.Fatal("downloaded an already oversized response", reader.read)
					}
				})
			}
		})
	}
}

type articleReadFailure struct{}

func (articleReadFailure) Read([]byte) (int, error) { return 0, errors.New("private network detail") }

func TestCollectPageReadFailureDoesNotImportPartialContent(t *testing.T) {
	reader := &articlePageReader{Reader: io.MultiReader(strings.NewReader(collectionPageFixture), articleReadFailure{})}
	c := &Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		response := reply(r, "")
		response.Body = reader
		return response, nil
	})}}
	article, err := c.FetchArticle(t.Context(), "https://mp.weixin.qq.com/s/large1")
	if err == nil || !strings.Contains(err.Error(), "读取中断") || strings.Contains(err.Error(), "MiB") || strings.Contains(err.Error(), "private") || article.Body != "" || !reader.closed {
		t.Fatal("partial page was accepted or failure was misreported", err, reader.closed)
	}
}

func TestCollectPageRejectsInvalidUTF8(t *testing.T) {
	_, err := ParseArticle("https://mp.weixin.qq.com/s/large1", collectionPageFixture+"\xff")
	if err == nil || !strings.Contains(err.Error(), "UTF-8") || strings.Contains(err.Error(), "过大") {
		t.Fatal("invalid encoding was confused with page size", err)
	}
}

func TestCollectPageLimitAppliesAfterDecompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = io.WriteString(writer, collectionPageFixture+strings.Repeat(" ", 8<<20))
		_ = writer.Close()
	}))
	defer server.Close()
	// Exercise net/http's real automatic decompression with a local fixture.
	transport := server.Client().Transport
	c := &Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		local := r.Clone(r.Context())
		local.URL.Scheme, local.URL.Host = "http", server.Listener.Addr().String()
		response, err := transport.RoundTrip(local)
		if response != nil {
			response.Request = r
		}
		return response, err
	})}}
	_, err := c.FetchArticle(t.Context(), "https://mp.weixin.qq.com/s/large1")
	if err == nil || !strings.Contains(err.Error(), "文章页面超过 8 MiB") {
		t.Fatal("decompressed page bypassed limit or was misreported", err)
	}
}
