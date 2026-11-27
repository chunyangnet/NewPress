package httpserver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/virtualcurrency"
)

// An opt-in local fixture with an in-memory repository and temporary settings.
// No normal site data, credentials or saved themes are modified.
func TestArticleWidgetsBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_ARTICLE_WIDGETS_BROWSER") != "1" {
		t.Skip("opt-in browser fixture")
	}
	s, _, accounts, repository, cookie, owner := newPersistentAdminTestServer(t)
	slug := os.Getenv("NEWPRESS_ARTICLE_WIDGETS_THEME")
	if slug == "" {
		slug = "chunnel"
	}
	ports := map[string]int{"chunnel": 19428, "nexora": 19429, "docshub": 19430, "starter": 19431}
	port, ok := ports[slug]
	if !ok {
		t.Fatalf("unsupported article widget fixture theme %q; choose chunnel, nexora, docshub or starter", slug)
	}
	if override := os.Getenv("NEWPRESS_ARTICLE_WIDGETS_PORT"); override != "" {
		var err error
		port, err = strconv.Atoi(override)
		if err != nil || port < 1024 || port > 65535 {
			t.Fatal("NEWPRESS_ARTICLE_WIDGETS_PORT must be between 1024 and 65535")
		}
	}
	themeRoot := t.TempDir()
	if err := extension.CopyTree(t.Context(), filepath.Join("..", "..", "themes", slug), filepath.Join(themeRoot, slug)); err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(themeRoot, slug, theme.WithSettingsDir(t.TempDir()))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	if got := themes.Active().Slug; got != slug {
		t.Fatalf("fixture theme = %q, want %q", got, slug)
	}
	s.themes, s.config.ActiveTheme = themes, slug
	s.config.ThemesDir = themeRoot
	address := fmt.Sprintf("127.0.0.1:%d", port)
	s.config.PublicURL = "http://" + address
	titles := []string{"把阅读留在自己的站点", "给独立博客设计一张安静的文章卡片", "没有封面，也值得被认真阅读", "从文字出发：为不同主题保留各自的颜色、字体与留白，让一张站内文章卡片自然融入阅读的节奏", "周末的纸上花园", "写作是一种缓慢整理生活的方式", "给未来的自己留下一篇笔记", "在城市漫游中收集光与影", "把喜欢的文章串成一条阅读路径", "一份关于长期创作的小小清单"}
	var posts []content.Post
	for i, title := range titles {
		cover := ""
		if i%2 == 0 {
			cover = "/__widget_fixture_cover.svg"
		}
		posts = append(posts, createWidgetPost(t, repository, content.PostInput{AuthorID: owner.UserID, Title: title, Slug: fmt.Sprintf("widget-reading-%d", i+1), Status: content.StatusPublish, PublishedAt: time.Now().Add(-time.Duration(i+1) * time.Hour), Excerpt: "让信息与页面自然相处。通过克制的排版、清楚的层级和跟随主题的颜色，为读者留下一段连续、舒适的阅读体验。", AuthorName: "编辑部", Category: "阅读与设计", CoverURL: cover}))
	}
	createWidgetPost(t, repository, content.PostInput{AuthorID: owner.UserID, Title: "不应出现在文章选择器中的草稿", Slug: "widget-draft-hidden", Status: content.StatusDraft})
	compact := strings.Replace(widgetSource(posts[1].ID), `data-np-layout="card"`, `data-np-layout="compact"`, 1)
	source := "<p>文章卡片将另一篇文章放回阅读的语境，让内容之间的连接清楚而自然。</p>\n<h2>继续阅读</h2>\n" + widgetSource(posts[0].ID) + "\n<p>卡片的字体、颜色和圆角跟随当前主题；引用保留文章身份，标题和链接使用最新的公开版本。</p>\n" + compact + "\n<h2>没有封面的文章</h2>\n" + widgetSource(posts[3].ID) + "\n<p>这是正文的结尾，所有数据均为本地临时验证内容。</p>"
	article := createWidgetPost(t, repository, content.PostInput{AuthorID: owner.UserID, Title: "让文章卡片融入每一种主题", Slug: "article-widget-fixture", Status: content.StatusPublish, ContentSource: source, AuthorName: "编辑部", Category: "设计手记"})
	var components strings.Builder
	components.WriteString(`<p>这里汇集了所有文章组件。可以点击红包、切换标签页、拖动图片对比，或参与投票和评分。</p><h2>本站文章卡片</h2>` + widgetSource(posts[0].ID))
	componentNames := map[string]string{
		"callout": "提示块", "quote": "引用", "code": "高亮代码", "download": "网盘下载",
		"gift": "礼物红包", "redpacket": "积分红包", "gallery": "图片网格与灯箱", "image-card": "图片卡片",
		"audio": "网易云音频", "video": "视频播放器", "table": "表格", "accordion": "折叠面板",
		"tabs": "标签页", "card": "内容卡片", "badges": "徽章标签", "progress": "进度条",
		"counter": "数字计数", "poll": "投票", "rating": "评分", "compare": "图片对比",
		"carousel": "图片轮播", "annotation": "高亮批注", "glossary": "词汇释义", "ai": "AI 声明内容块",
		"mention": "用户卡片", "chart": "数据图表",
	}
	for i, kind := range articlewidgets.Kinds {
		c := articlewidgets.Config{Version: 1, ID: fmt.Sprintf("fixture_component_%02d", i), Kind: kind, Title: componentNames[kind], Tone: "theme", Style: "soft", Width: "reading", Density: "normal", Mode: "both", Max: 100, Value: 75, Columns: 3, Items: []articlewidgets.Item{{Title: "设计", Text: "清楚的层级、舒适的留白，让阅读有自己的节奏。", Value: 85}, {Title: "性能", Text: "图片按需加载，复杂交互在需要时启动，长文章也能轻快浏览。", Value: 90}, {Title: "兼容性", Text: "组件跟随主题的文字与背景颜色，在手机和深色模式下也保持自然。", Value: 80}}}
		switch kind {
		case "callout":
			c.Title = "提示"
			c.Tone = "blue"
			c.Text = "这是一条提示信息，帮助你更好地理解接下来的内容。"
		case "quote":
			c.Text = "生活不止眼前的苟且，还有诗和远方。"
			c.Caption = "高晓松"
			c.Tone = "amber"
		case "code":
			c.Title = ""
			c.Language = "python"
			c.Text = "def hello_world():\n    print(\"Hello, World!\")\n\n    # 这是一个优雅的代码块\n    return True"
		case "rating":
			c.Max = 5
			c.Title = "这篇内容对你有帮助吗？"
			c.Text = "留下你的评分，帮助我们写出更好的内容。"
		case "progress":
			c.Title = "项目进度"
			c.Text = "已完成视觉设计与内容整理，正在进行最后的体验打磨。"
		case "counter":
			c.Title = "一起记录生活"
			c.Value = 1280
			c.Unit = "篇文章"
			c.Text = "每一次认真书写，都值得被看见。"
		case "poll":
			c.Title = "你最希望看到哪类内容？"
			c.Text = "选择一个感兴趣的方向，结果会实时汇总。"
		case "audio":
			c.Provider = "netease"
			c.UID = "186016"
			c.Title = "晴天"
			c.Text = "周杰伦 · 叶惠美"
			c.Image = "/__widget_fixture_cover.svg"
		case "video":
			c.Provider = "youtube"
			c.UID = "dQw4w9WgXcQ"
			c.Title = "Never Gonna Give You Up"
			c.Text = "Rick Astley · 音乐视频"
			c.Image = "/__component_fixture_mountains.webp"
		case "mention":
			c.Provider = "site"
			c.UID = fmt.Sprint(owner.UserID)
			c.Title = "站点作者"
			c.Text = "记录设计、技术与日常生活。"
		case "redpacket":
			c.Title = "新年快乐，恭喜发财"
			c.Text = ""
			c.Amount = "1.00"
			c.Count = 2
			c.Currency = "main"
			c.Mode = "login"
		case "gift":
			c.Title = "新年快乐，恭喜发财"
			c.Sender = "星"
			c.Caption = "HELLO-NEWPRESS"
			c.Image = "/__widget_fixture_cover.svg"
		case "chart":
			c.Layout = "radar"
			c.Title = "阅读体验评估"
			c.Items = append(c.Items, articlewidgets.Item{Title: "易用性", Value: 92}, articlewidgets.Item{Title: "可读性", Value: 88})
		case "compare":
			c.Title = "城市与自然"
			c.Items = []articlewidgets.Item{{Title: "城市", Image: "/__component_fixture_architecture.webp"}, {Title: "自然", Image: "/__component_fixture_mountains.webp"}}
		case "table":
			c.Title = ""
			c.Rows = [][]string{{"功能", "描述", "状态"}, {"🚀 性能优化", "提升页面加载速度", "✓ 已完成"}, {"🎨 界面升级", "全新视觉风格", "◷ 进行中"}, {"🛡 安全加固", "增强系统安全性", "● 待开始"}}
		case "image-card":
			c.Title = "远山与晨光"
			c.Text = "一场与自然的邂逅"
			c.Image = "/__component_fixture_mountains.webp"
		case "gallery", "carousel":
			c.Title = "光影之间"
			c.Items = []articlewidgets.Item{{Title: "山间晨光", Text: "让视线走得更远", Image: "/__component_fixture_mountains.webp"}, {Title: "城市线条", Text: "光与建筑的对话", Image: "/__component_fixture_architecture.webp"}, {Title: "纸上时光", Text: "翻过日常的一页", Image: "/__widget_fixture_cover.svg"}}
		case "card":
			c.Title = "给忙碌的日常，留一点阅读时间"
			c.Text = "从一页书、一段文字开始，收集生活里那些微小而珍贵的发现。"
			c.Image = "/__widget_fixture_cover.svg"
			c.URL = "/posts/article-widget-fixture"
		case "badges":
			c.Items = []articlewidgets.Item{{Title: "★ 推荐"}, {Title: "技术"}, {Title: "设计"}, {Title: "生活"}, {Title: "效率"}, {Title: "学习"}, {Title: "随笔"}}
		case "annotation":
			c.Title = "让设计服务于阅读"
			c.Text = "清楚的层级和适度的留白，可以帮助读者更轻松地理解内容。"
		case "glossary":
			c.Title = "渐进增强"
			c.Text = "先提供可阅读、可访问的基础内容，再根据浏览器能力添加交互和视觉效果。"
		case "ai":
			c.Title = "关于这段内容"
			c.Text = "这段内容使用 AI 辅助整理，并由作者核对与编辑。观点与最终表达由作者负责。"
		case "download":
			c.Title = "文章排版与设计素材"
			c.Text = "图标、配色与排版参考 · 24.6 MB · ZIP"
			c.URL = "https://example.com/download"
			c.Caption = "8y28"
		}
		components.WriteString("<h2>" + componentNames[kind] + "</h2>" + componentSource(c))
	}
	for _, tone := range []string{"amber", "green", "red", "purple"} {
		components.WriteString(componentSource(articlewidgets.Config{Version: 1, ID: "fixture_tone_" + tone, Kind: "callout", Title: map[string]string{"amber": "小贴士", "green": "操作成功", "red": "注意事项", "purple": "延伸阅读"}[tone], Text: map[string]string{"amber": "试着多使用快捷键，能让你的工作效率提升不少哦！", "green": "你的更改已保存，可以继续编辑。", "red": "发布前记得检查链接与图片是否正确。", "purple": "从相关资料出发，继续探索这个话题。"}[tone], Tone: tone, Style: "soft", Width: "reading"}))
	}
	for _, layout := range []string{"bar", "line"} {
		components.WriteString(componentSource(articlewidgets.Config{Version: 1, ID: "fixture_chart_" + layout, Kind: "chart", Title: map[string]string{"bar": "每周阅读量", "line": "创作记录"}[layout], Layout: layout, Max: 100, Unit: "篇", Width: "reading", Items: []articlewidgets.Item{{Title: "周一", Value: 36}, {Title: "周二", Value: 58}, {Title: "周三", Value: 45}, {Title: "周四", Value: 82}, {Title: "周五", Value: 64}}}))
	}
	components.WriteString(`<h2>原生代码块增强</h2><pre><code class="language-go">func main() { println("Hello") }</code></pre><h2>本地音频</h2>`)
	components.WriteString(componentSource(articlewidgets.Config{Version: 1, ID: "fixture_audio_local", Kind: "audio", Title: "本站音频示例", Provider: "local", URL: "/__component_fixture_audio.wav", Width: "reading"}))
	componentPost := createWidgetPost(t, repository, content.PostInput{AuthorID: owner.UserID, Title: "给文字更多表达的方式", Slug: "component-gallery-fixture", Status: content.StatusPublish, ContentSource: components.String(), AuthorName: "编辑部", Category: "组件设计"})
	ledger, ledgerSettings := currencyHTTPStore(t, s, false)
	if _, err := ledger.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: owner.UserID, UserID: owner.UserID, Delta: 10000, ExpectedVersion: ledgerSettings.Version, RequestID: "fixture-widget-balance", Reason: "组件临时演示"}); err != nil {
		t.Fatal(err)
	}
	_, readerCookie, _ := createAndLoginTestUser(t, accounts, owner, "widget-reader", "widget-reader@example.test", "组件读者", "subscriber")
	handler := s.routes()
	fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slug == "nexora" && strings.HasPrefix(r.URL.Path, "/__nexora_visual/") {
			serveNexoraVisualFixture(w, r, themes)
			return
		}
		switch r.URL.Path {
		case "/__component_fixture_mountains.webp", "/__component_fixture_architecture.webp":
			name := "innovation-mountains.webp"
			if strings.Contains(r.URL.Path, "architecture") {
				name = "about-architecture.webp"
			}
			http.ServeFile(w, r, filepath.Join("..", "..", "themes", "nexora", "assets", "images", name))
		case "/__component_fixture_audio.wav":
			wav := make([]byte, 44+16000)
			copy(wav, "RIFF")
			binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
			copy(wav[8:], "WAVEfmt ")
			binary.LittleEndian.PutUint32(wav[16:], 16)
			binary.LittleEndian.PutUint16(wav[20:], 1)
			binary.LittleEndian.PutUint16(wav[22:], 1)
			binary.LittleEndian.PutUint32(wav[24:], 8000)
			binary.LittleEndian.PutUint32(wav[28:], 16000)
			binary.LittleEndian.PutUint16(wav[32:], 2)
			binary.LittleEndian.PutUint16(wav[34:], 16)
			copy(wav[36:], "data")
			binary.LittleEndian.PutUint32(wav[40:], 16000)
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(wav)
		case "/__widget_fixture_cover.svg":
			w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 720 480" width="720" height="480"><rect width="720" height="480" fill="#e9e0d3"/><rect x="66" y="56" width="244" height="310" rx="122" fill="#b5c5b9"/><rect y="348" width="720" height="132" fill="#cdb99f"/><ellipse cx="434" cy="380" rx="176" ry="24" fill="#b39a7d"/><path d="M260 188Q336 162 432 199V372Q344 338 260 361Z" fill="#faf6e9"/><path d="M432 199Q516 162 600 188V361Q514 338 432 372Z" fill="#f2ead8"/><path d="M432 200V371" stroke="#d8ccb7" stroke-width="4"/><path d="M290 232Q352 219 406 239M290 262Q352 249 406 269M290 292Q352 279 406 299M460 239Q520 219 570 232M460 269Q520 249 570 262M460 299Q520 279 570 292" fill="none" stroke="#d1c3aa" stroke-width="5" stroke-linecap="round"/></svg>`)
		case "/__widget_fixture_login":
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, fmt.Sprintf("/admin/content/%d/edit/", article.ID), http.StatusSeeOther)
		case "/__component_fixture_login":
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, fmt.Sprintf("/admin/content/%d/edit/", componentPost.ID), http.StatusSeeOther)
		case "/__component_fixture_reader":
			http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: readerCookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, "/posts/"+componentPost.Slug, http.StatusSeeOther)
		case "/__widget_fixture_article":
			http.Redirect(w, r, "/posts/"+article.Slug, http.StatusSeeOther)
		default:
			handler.ServeHTTP(w, r)
		}
	})
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fixture, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = server.Close() })
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	t.Logf("article widget fixture ready (%s): %s/__widget_fixture_login ; public article: /posts/%s", slug, s.config.PublicURL, article.Slug)
	var expiry <-chan time.Time
	if os.Getenv("NEWPRESS_ARTICLE_WIDGETS_KEEP_ALIVE") != "1" {
		expiry = time.After(45 * time.Minute)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatal(err)
		}
	case <-expiry:
	case <-t.Context().Done():
	}
}
