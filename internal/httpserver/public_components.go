package httpserver

import (
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/layout"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/presentation"
)

func publicSeries(s content.Series) presentation.Series {
	return presentation.Series{ID: s.ID, Kind: s.Kind, Title: s.Title, Slug: s.Slug, Description: s.Description, CoverURL: s.CoverURL, URL: s.URL(), Label: s.Label(), Count: s.Count()}
}
func publicSeriesList(items []content.Series) []presentation.Series {
	result := make([]presentation.Series, 0, len(items))
	for _, s := range items {
		result = append(result, publicSeries(s))
	}
	return result
}
func publicSeriesPage(s seriesPageView, paths ...contentPathBuilder) presentation.SeriesPage {
	return presentation.SeriesPage{Items: publicSeriesList(s.Items), Current: publicSeries(s.Current), Chapters: publicPosts(s.Chapters, paths...), Search: s.Search, Kind: s.Kind, Total: s.Total, Page: s.Page, Pages: s.Pages, PreviousURL: s.PreviousURL, NextURL: s.NextURL}
}
func publicReadingSeries(items []seriesReadingView, paths ...contentPathBuilder) []presentation.SeriesReading {
	result := make([]presentation.SeriesReading, 0, len(items))
	for _, s := range items {
		result = append(result, presentation.SeriesReading{Series: publicSeries(s.Series), Previous: publicPost(s.Previous, paths...), Next: publicPost(s.Next, paths...), Related: publicPosts(s.Related, paths...), Position: s.Position})
	}
	return result
}
func publicHomeBlocks(items []layout.Block) []presentation.HomeBlock {
	result := make([]presentation.HomeBlock, 0, len(items))
	for _, b := range items {
		block := presentation.HomeBlock{Key: b.Key, Title: b.Title, Description: b.Description, Icon: b.Icon, Visibility: b.Visibility, Requires: b.Requires}
		if b.Custom != nil {
			block.Custom = &presentation.CustomBlock{Key: b.Custom.Key, Title: b.Custom.Title, HTML: contenthtml.Sanitize(b.Custom.HTML), Visibility: b.Custom.Visibility, Surface: b.Custom.Surface}
		}
		result = append(result, block)
	}
	return result
}
func publicHomeLinks(items []homeLink) []presentation.HomeLink {
	result := make([]presentation.HomeLink, 0, len(items))
	for _, l := range items {
		result = append(result, presentation.HomeLink(l))
	}
	return result
}
func publicHome(h homePageView, paths ...contentPathBuilder) presentation.Home {
	p := presentation.Home{GuideLinks: publicHomeLinks(h.GuideLinks), Updated: publicPosts(h.Updated, paths...), SpotlightDesktop: publicPosts(h.SpotlightDesktop, paths...), SpotlightMobile: publicPosts(h.SpotlightMobile, paths...), Top: publicHomeBlocks(h.Top), Main: publicHomeBlocks(h.Main), Aside: publicHomeBlocks(h.Aside), HasDesktopSidebar: h.HasDesktopSidebar, HasDesktopMain: h.HasDesktopMain, HasCustomBlocks: h.HasCustomBlocks,
		SearchTerms: publicHomeLinks(h.SearchTerms), Channels: publicHomeLinks(h.Channels), Filters: publicHomeLinks(h.Filters), Sorts: publicHomeLinks(h.Sorts),
		NoticeURL: h.NoticeURL, ListURL: h.ListURL, GridURL: h.GridURL, ResetURL: h.ResetURL, DateLabel: h.DateLabel, Weekday: h.Weekday, Tags: publicTerms(h.Tags), Recent: publicPosts(h.Recent, paths...),
		Series: publicSeriesList(h.Series), Circles: h.Circles, Initials: h.Initials, TotalPosts: h.TotalPosts, TotalCategories: h.TotalCategories, TotalSeries: h.TotalSeries,
		CheckIn: presentation.CheckIn{Mode: h.CheckIn.Mode, Timezone: h.CheckIn.Timezone, Enabled: h.CheckIn.Enabled, Source: h.CheckIn.Source, Reward: h.CheckIn.Reward, Unit: h.CheckIn.Unit, AccountURL: h.CheckIn.AccountURL, IntervalHours: h.CheckIn.IntervalHours}}
	p.HasSidebarModules, p.FocusPosts, p.EntryCards, p.TopicURL = h.HasSidebarModules, publicPosts(h.FocusPosts, paths...), publicHomeLinks(h.EntryCards), h.TopicURL
	for _, item := range h.Announcements {
		p.Announcements = append(p.Announcements, presentation.Announcement(item))
	}
	for _, s := range h.Slides {
		p.Slides = append(p.Slides, presentation.Slide(s))
	}
	for _, a := range h.Authors {
		p.Authors = append(p.Authors, presentation.HomeAuthor(a))
	}
	for _, c := range h.Categories {
		p.Categories = append(p.Categories, presentation.HomeCategory{Term: publicTerm(c.Term), CoverURL: c.CoverURL})
	}
	return p
}
func publicLinkBoard(b linkpage.Board) presentation.LinkBoard {
	return presentation.LinkBoard{Slug: b.Slug, Name: b.Settings.Name, Description: b.Settings.Description, URL: "/links/" + b.Slug + "/", Style: b.Settings.Style, Accent: b.Settings.Accent, Search: b.Settings.Search, Switcher: b.Settings.Switcher, NewTab: b.Settings.NewTab}
}
func publicLinkDirectory(v linkPagesView) presentation.LinkDirectory {
	p := presentation.LinkDirectory{Board: publicLinkBoard(v.Board), Categories: v.Categories, Style: v.Style, Search: v.Search, Category: v.Category, Available: v.Available}
	for _, b := range v.Boards {
		p.Boards = append(p.Boards, publicLinkBoard(b))
	}
	for _, i := range v.Items {
		p.Items = append(p.Items, presentation.DirectoryItem{ID: i.ID, Name: i.Name, URL: i.URL, Description: i.Description, Category: i.Category, Host: i.Host, Icon: i.Icon, Initial: i.Initial, Featured: i.Featured})
	}
	return p
}

func fillPublicComponents(p *presentation.Page, d pageView, paths ...contentPathBuilder) {
	p.Questions = publicQuestions(d.Questions)
	p.AnnouncementPage = publicAnnouncements(d.AnnouncementPage)
	p.Flash = publicFlash(d.Flash)
	p.ShopProductKey = d.ShopProductKey
	p.ShopDescriptionHTML = d.ShopDescriptionHTML
	p.Home = publicHome(d.Home, paths...)
	p.Series = publicSeriesPage(d.Series, paths...)
	p.ReadingSeries = publicReadingSeries(d.ReadingSeries, paths...)
	p.Circles = d.Circles
	p.LinkPages = publicLinkDirectory(d.LinkPages)
	p.Feedback = publicFeedback(d.Feedback)
	p.Unsubscribe = presentation.Unsubscribe(d.Unsubscribe)
	p.Preview = presentation.Preview{EditorURL: d.Preview.EditorURL, SavedLabel: d.Preview.SavedLabel, ExpiresLabel: d.Preview.ExpiresLabel, StatusLabel: d.Preview.StatusLabel, SourceVersion: d.Preview.SourceVersion}
	p.Images = map[string]presentation.Image{}
	for key, v := range d.Images {
		if v != nil {
			p.Images[key] = presentation.Image(*v)
		}
	}
	p.SEO = presentation.SEO{Kind: d.SEO.Kind, Published: d.SEO.Published, Modified: d.SEO.Modified, ImageAlt: d.SEO.ImageAlt, ImageWidth: d.SEO.ImageWidth, ImageHeight: d.SEO.ImageHeight}
	p.CodeLoginEnabled = d.CodeLoginEnabled
	p.VerificationEnabled = d.VerificationEnabled
	for _, v := range d.LoginProviders {
		p.LoginProviders = append(p.LoginProviders, presentation.LoginProvider{ID: v.ID, Name: v.Name, Mark: v.Mark, URL: "/auth/" + v.ID + "/start"})
	}
	for _, v := range d.PluginFragments {
		p.PluginFragments = append(p.PluginFragments, presentation.PluginFragment{Name: v.Name, Title: v.Title, Text: v.Text, Tone: v.Tone})
	}
	if d.Kind == "archive" {
		a := d.Archive
		p.Pagination = presentation.Pagination{Total: a.Total, Page: a.Page, Pages: a.Pages, HasPrevious: a.HasPrevious, HasNext: a.HasNext, PreviousURL: a.PreviousURL, NextURL: a.NextURL}
	}
	if d.Kind == "community" {
		c := d.Community
		p.Pagination = presentation.Pagination{Total: c.Total, Page: c.Page, Pages: c.Pages, HasPrevious: c.HasPrevious, HasNext: c.HasNext, PreviousURL: c.PreviousURL, NextURL: c.NextURL}
	}
}

func publicFeedback(v feedbackPageView) presentation.Feedback {
	return presentation.Feedback{Directory: v.Directory, SignedIn: v.SignedIn, CanRead: v.CanRead, CanSubmit: v.CanSubmit, Manage: v.Manage, Configure: v.Configure, Roadmap: v.Roadmap, Slug: v.Slug, URL: v.URL, API: v.API, CSRF: v.CSRF, Search: v.Search, Status: v.Status, Kind: v.Kind, Category: v.Category, Sort: v.Sort, Scope: v.Scope, View: v.View, Previous: v.Previous, Next: v.Next, ListURL: v.ListURL, RoadmapURL: v.RoadmapURL, UserID: v.UserID, Version: v.Version, Boards: v.Boards, Items: v.Items, Item: v.Item, Columns: v.Columns, Kinds: v.Kinds, Statuses: v.Statuses, Categories: v.Categories, Page: v.Page, Pages: v.Pages, Total: v.Total, AllCount: v.AllCount, DoneCount: v.DoneCount, ActiveCount: v.ActiveCount, PendingCount: v.PendingCount, Settings: presentation.FeedbackSettings{Name: v.Settings.Name, Description: v.Settings.Description, Guidelines: v.Settings.Guidelines, SubmitLabel: v.Settings.SubmitLabel, Enabled: v.Settings.Enabled, Submissions: v.Settings.Submissions, Voting: v.Settings.Voting, Comments: v.Settings.Comments, OfficialReplies: v.Settings.OfficialReplies, Roadmap: v.Settings.Roadmap, History: v.Settings.History, Moderation: v.Settings.Moderation, PrivateSubmissions: v.Settings.PrivateSubmissions, DefaultView: v.Settings.DefaultView, Accent: v.Settings.Accent, PageSize: v.Settings.PageSize, Kinds: v.Settings.Kinds, Categories: v.Settings.Categories}}
}
