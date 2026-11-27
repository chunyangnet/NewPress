package presentation

import "time"

// Fixtures are deterministic, non-sensitive examples for independent theme
// checks. They are data, not live account credentials or repository objects.
func Fixtures() map[string]Page {
	empty := Page{Contract: Version, Kind: "home", Site: Site{Title: "示例站点", Tagline: "独立主题", Locale: "zh-CN", Timezone: "Asia/Shanghai", IconURL: "/favicon.ico"},
		PageTitle: "示例站点", Description: "公共展示契约夹具", Navigation: map[string]Menu{"primary": {}, "utility": {}, "mobile": {}, "mobile_action": {}, "footer": {}, "explore": {}}, Settings: map[string]any{}, Modules: map[string]bool{},
		Links: map[string]string{"account": "/account/", "access": "/account/access/", "client": "/core-assets/js/newpress-client.js"}, ThemeCSSURL: "/theme-settings.css"}
	article := empty
	article.Kind = "single"
	article.Post = Post{ID: 1, Slug: "hello", Type: "post", Title: "Hello <world>", Excerpt: "示例摘要", ContentHTML: "<p>经过清洗的正文。</p>", URL: "/posts/hello", AuthorName: "示例作者", AuthorURL: "/authors/1/", PublishedAt: time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC), CommentStatus: "open"}
	article.Posts = []Post{article.Post}
	article.Comments = Comments{Open: true, MaxLength: 2000, FormToken: "fixture-only", Items: []Comment{{ID: 1, AuthorName: "读者", ContentHTML: "<p>示例评论</p>"}}}
	preview := article
	preview.IsPreview = true
	preview.Comments = Comments{}
	disabled := empty
	disabled.Modules = map[string]bool{"community": false, "series": false, "comments": false, "feedback": false}
	rich := article
	rich.Account = Account{Authenticated: true, CanWrite: true, ID: 1, Name: "示例作者", Username: "example", Badges: []Badge{{Name: "作者", Color: "#2563eb"}}}
	rich.Author = Author{ID: 1, Name: "示例作者", Badges: rich.Account.Badges}
	rich.Pagination = Pagination{Total: 21, Page: 2, Pages: 3, From: 11, To: 20, HasPrevious: true, HasNext: true, PreviousURL: "/?page=1", NextURL: "/?page=3"}
	rich.Categories = []Term{{ID: 1, Name: "示例分类", Slug: "example", URL: "/categories/example/", Count: 1}}
	rich.Archive = rich.Categories[0]
	rich.Navigation = map[string]Menu{"primary": {Configured: true, Items: []Link{{Label: "示例菜单", URL: "/", Children: []Link{{Label: "发现", URL: "/explore/"}}}}}, "utility": {}, "mobile": {}, "mobile_action": {}, "footer": {}, "explore": {}}
	rich.Modules = map[string]bool{"community": true, "series": true, "comments": true, "feedback": true, "links": true}
	series := Series{ID: 1, Kind: "series", Title: "示例系列", Slug: "example", URL: "/series/example/", Count: 1}
	circle := Circle{Slug: "example", Name: "示例圈子", URL: "/circles/example/", CanRead: true, CanJoin: true, TopicCount: 1}
	rich.Series = SeriesPage{Items: []Series{series}, Current: series, Chapters: rich.Posts, Total: 1, Page: 1, Pages: 1}
	rich.ReadingSeries = []SeriesReading{{Series: series, Next: rich.Post, Related: rich.Posts, Position: 1}}
	rich.Home = Home{Top: []HomeBlock{{Key: "hero", Title: "欢迎", Visibility: "both"}}, Main: []HomeBlock{{Key: "feed", Title: "最新文章", Visibility: "both"}}, HasDesktopMain: true, Recent: rich.Posts, Series: []Series{series}, Circles: []Circle{circle}, Categories: []HomeCategory{{Term: rich.Categories[0]}}, Tags: rich.Categories, Authors: []HomeAuthor{{ID: 1, Name: "示例作者", URL: "/authors/1/"}}}
	rich.Circles = Circles{Directory: true, SignedIn: true, Items: []Circle{circle}, Circle: circle, Total: 1, Page: 1, Pages: 1, Topics: []CircleTopic{{ID: 1, Title: "示例话题", Body: "话题正文", URL: "/circles/example/1"}}}
	feedback := FeedbackItem{ID: 1, Title: "示例反馈", Body: "希望增加的能力", URL: "/feedback/example/1", Status: "open", StatusLabel: "待处理"}
	rich.Feedback = Feedback{CanRead: true, Items: []FeedbackItem{feedback}, Item: feedback, Total: 1, Page: 1, Pages: 1, Settings: FeedbackSettings{Name: "反馈", Enabled: true}, Boards: []FeedbackBoard{{Slug: "example", Name: "示例反馈", URL: "/feedback/example/"}}}
	rich.LinkPages = LinkDirectory{Available: true, Board: LinkBoard{Slug: "example", Name: "示例链接", URL: "/links/example/"}, Items: []DirectoryItem{{ID: 1, Name: "NewPress", URL: "/", Host: "example.test"}}}
	rich.Modules["questions"], rich.Modules["flash"] = true, true
	question := Question{ID: 1, Version: 2, Title: "如何开始学习？", HTML: "<p>示例问题。</p>", URL: "/questions/1/", Author: "读者", CanEdit: true, Body: "示例问题。", CanAccept: true, Count: 1, Tags: []string{"入门"}, AnswersView: []QuestionAnswer{{ID: 1, HTML: "<p>请阅读入门文档。</p>", Author: "作者", Accepted: true}}, AcceptedID: 1}
	rich.Questions = Questions{Question: question, Items: []Question{question}, Featured: []Question{question}, SignedIn: true, Total: 1, PublicTotal: 1, SolvedTotal: 1, Page: 1, Pages: 1, AllURL: "/questions/", Topics: []QuestionTopic{{Name: "入门", Count: 1}}}
	flash := FlashItem{ID: 2, Version: 1, Title: "文档更新", URL: "/flash/update/", HTML: "<p>新增学习指南。</p>", Body: "新增学习指南。", Slug: "update", Status: "published", SourceName: "文档团队", SourceURL: "/pages/about"}
	rich.Flash = Flash{Items: []FlashItem{flash}, Current: flash, Total: 1, Authenticated: true, Editing: true, Endpoint: "/api/v1/flash/2/"}
	notice := AnnouncementItem{ID: 1, Title: "学习指南公告", URL: "/announcements/1/", Pinned: true, Date: article.Post.PublishedAt}
	rich.AnnouncementPage = AnnouncementPage{Items: []AnnouncementItem{notice}, Current: notice, Total: 1, Results: 1, Page: 1, Pages: 1, Paragraphs: []AnnouncementParagraph{{Text: "新增内容", Heading: true}, {Text: "欢迎阅读更新后的文档。"}}}
	rich.Comments.AllowReplies, rich.Comments.MaxDepth = true, 5
	rich.Comments.Items[0].Children = []Comment{{ID: 2, ParentID: 1, AuthorName: "回复者", ContentHTML: "<p>示例回复</p>"}}
	loginRequired := article
	loginRequired.Comments.LoginRequired, loginRequired.Comments.RequireLogin = true, true
	return map[string]Page{"empty": empty, "article": article, "preview": preview, "modules-disabled": disabled, "components": rich, "login-required": loginRequired}
}
