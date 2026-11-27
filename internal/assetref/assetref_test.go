package assetref

import (
	"reflect"
	"testing"

	"newpress.local/newpress/internal/articlewidgets"
)

func TestManagedURLResolution(t *testing.T) {
	r := New("https://example.test")
	for _, test := range []struct{ value, want string }{
		{"/media/1/cover.png", "/media/1/cover.png"},
		{"/media/1/cover.png?download=1#preview", "/media/1/cover.png"},
		{"https://EXAMPLE.test:443/media/1/cover.png", "/media/1/cover.png"},
		{"//example.test/media/1/cover.png", "/media/1/cover.png"},
		{"../media/1/cover.png", "/media/1/cover.png"},
		{"/posts/%2e%2e/media/1/cover.png", "/media/1/cover.png"},
		{"/media/1/%E5%9B%BE.png", "/media/1/%E5%9B%BE.png"},
		{"/media/1/图.png", "/media/1/%E5%9B%BE.png"},
		{"/media/1/a%2520b.png", "/media/1/a%2520b.png"},
		{"/media/11/cover.png", "/media/11/cover.png"},
		{"https://external.test/media/1/cover.png", ""},
		{"https://example.test:444/media/1/cover.png", ""},
		{"http://example.test/media/1/cover.png", ""},
		{"https://example.test@external.test/media/1/cover.png", ""},
		{"data:image/png,/media/1/cover.png", ""},
		{"/other?image=/media/1/cover.png", ""},
		{"#cover", ""}, {"/media/01/cover.png", ""}, {"/media/-1/cover.png", ""},
		{"/media/1/", ""}, {"/media/1/a/b.png", ""}, {"/media/1/%xx.png", ""},
	} {
		t.Run(test.value, func(t *testing.T) {
			link, ok := r.URL(test.value, "/posts/example")
			if ok != (test.want != "") || link.Path != test.want {
				t.Fatalf("URL(%q) = %#v, %v; want %q", test.value, link, ok, test.want)
			}
		})
	}
}

func TestComponentEncodedMediaReferencesAreProtected(t *testing.T) {
	c := articlewidgets.Config{Version: 1, ID: "component_media_test", Kind: "audio", Provider: "local", Image: "/media/1/cover.png", URL: "/media/2/audio.mp3", Items: []articlewidgets.Item{{URL: "/media/3/alternate.ogg"}}}
	source := `<aside data-np-widget="audio" data-np-config="` + c.Encode() + `"><p>播放器</p></aside>`
	got := New("https://example.test").HTML(source, "/posts/media")
	want := []Link{{1, "/media/1/cover.png"}, {2, "/media/2/audio.mp3"}, {3, "/media/3/alternate.ogg"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("encoded media references = %#v", got)
	}
}

func TestHTMLReferencesUseAttributesAndExactPaths(t *testing.T) {
	r := New("https://example.test")
	source := `<p>/media/9/text.png</p><!-- <img src="/media/9/comment.png"> -->
	<code>&lt;img src="/media/9/escaped.png"&gt;</code>
	<img SRC="&#47;media/1/a.png?x=1&amp;y=2"><a href='../media/2/b.pdf#page=2'>attachment</a>
	<img srcset="/media/3/c.png 1x, /media/4/d.png 2x"><video poster="/media/5/e.png"></video>
	<img src="https://other.test/media/6/external.png" alt="/media/9/alt.png">
	<img src="/media/1/a.png"><script>var x='<img src="/media/9/script.png">';</script>`
	got := r.HTML(source, "/posts/example")
	want := []Link{{1, "/media/1/a.png"}, {2, "/media/2/b.pdf"}, {3, "/media/3/c.png"}, {4, "/media/4/d.png"}, {5, "/media/5/e.png"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTML links = %#v", got)
	}
	fields := r.Content("/media/1/a.png", source, source, "/posts/example")
	if got := fields.Matching("/media/1/a.png"); !reflect.DeepEqual(got, []string{"content_source", "cover_url"}) {
		t.Fatalf("matching fields = %#v", got)
	}
	if got := fields.Matching("/media/1/a.png-extra"); len(got) != 0 {
		t.Fatalf("prefix collision = %#v", got)
	}
}
