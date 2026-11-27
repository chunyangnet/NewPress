package httpserver

import (
	"maps"
	"net/url"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/module"
	"newpress.local/newpress/internal/presentation"
)

func publicTerm(t content.Term) presentation.Term {
	prefix := "/categories/"
	if t.Taxonomy == content.TaxonomyTag {
		prefix = "/tags/"
	}
	return presentation.Term{ID: t.ID, Name: t.Name, Slug: t.Slug, Description: t.Description, URL: prefix + url.PathEscape(t.Slug) + "/", Count: t.Count}
}
func publicTerms(terms []content.Term) []presentation.Term {
	result := make([]presentation.Term, 0, len(terms))
	for _, t := range terms {
		result = append(result, publicTerm(t))
	}
	return result
}
func publicPost(p content.Post, paths ...contentPathBuilder) presentation.Post {
	return presentation.Post{Pinned: p.IsPinned(), ArticleBadge: presentation.ArticleBadge(p.ArticleBadge()), ID: p.ID, AuthorID: p.AuthorID, Type: p.Type, Slug: p.Slug, Title: p.Title, Excerpt: p.Excerpt,
		ContentHTML: p.ContentHTML, CoverURL: p.CoverURL, URL: resolvedContentPath(p, paths...), AuthorName: p.AuthorName, AuthorURL: p.AuthorURL(),
		Category: p.Category, CommentStatus: p.CommentStatus, Categories: publicTerms(p.Categories), Tags: publicTerms(p.Tags), PublishedAt: p.PublishedAt, UpdatedAt: p.UpdatedAt}
}
func publicPosts(posts []content.Post, paths ...contentPathBuilder) []presentation.Post {
	result := make([]presentation.Post, 0, len(posts))
	for _, p := range posts {
		result = append(result, publicPost(p, paths...))
	}
	return result
}
func publicLinks(items []navigationItemView) []presentation.Link {
	result := make([]presentation.Link, 0, len(items))
	for _, p := range items {
		result = append(result, presentation.Link{ID: p.ID, Label: p.Label, URL: p.URL, Description: p.Description, Rel: p.Rel, Icon: p.Icon,
			Badge:        presentation.NavigationBadge{Text: p.Badge.Text, Style: p.Badge.Style, Placement: p.Badge.Placement, Background: p.Badge.Background, Color: p.Badge.Color, BorderColor: p.Badge.BorderColor, GradientTo: p.Badge.GradientTo, FontSize: p.Badge.FontSize, Radius: p.Badge.Radius, OffsetX: p.Badge.OffsetX, OffsetY: p.Badge.OffsetY, HideOnMobile: p.Badge.HideOnMobile},
			Presentation: p.Presentation, NewWindow: p.NewWindow, Active: p.Active, Current: p.Current, Children: publicLinks(p.Children)})
	}
	return result
}
func publicCommentItems(items []*publicCommentNode) []presentation.Comment {
	result := make([]presentation.Comment, 0, len(items))
	for _, p := range items {
		result = append(result, presentation.Comment{ID: p.ID, ParentID: p.ParentID, AuthorName: p.AuthorName, ContentHTML: p.ContentHTML,
			Badges: p.Badges, Support: presentation.CommentSupport(p.Support), CreatedAt: p.CreatedAt, EditedAt: p.EditedAt, StaffReply: p.StaffReply, CanReply: p.CanReply, Avatar: p.Avatar, Depth: p.Depth, Children: publicCommentItems(p.Children)})
	}
	return result
}
func publicBadges(items []progressionBadge) []presentation.Badge {
	result := make([]presentation.Badge, 0, len(items))
	for _, item := range items {
		result = append(result, presentation.Badge(item))
	}
	return result
}
func publicPage(data pageView, paths ...contentPathBuilder) presentation.Page {
	enabled := map[string]bool{}
	for _, m := range module.Catalog() {
		enabled[m.ID] = module.Allowed(data.ModuleOverrides, m.ID)
	}
	p := presentation.Page{Status: data.Status, Recognition: data.Recognition, Contract: presentation.Version, Kind: data.Kind, Site: presentation.Site(data.Site),
		PageTitle: data.PageTitle, Description: data.Description, Canonical: data.Canonical, BodyClass: data.BodyClass, IsPreview: data.IsPreview, Now: data.Now,
		Post: publicPost(data.Post, paths...), Posts: publicPosts(data.Posts, paths...), Featured: publicPosts(data.Featured, paths...), Categories: publicTerms(data.Categories),
		Archive: publicTerm(data.Archive.Term), Settings: maps.Clone(data.ThemeValues), ThemeCSSURL: data.ThemeCSSURL, ThemeClass: data.ThemeClass,
		SearchQuery: data.SearchQuery, Category: data.Category, CategorySlug: data.CategorySlug, Modules: enabled, CDNAssetBase: data.CDNAssetBase,
		Links:            map[string]string{"home": "/", "explore": "/explore/", "account": "/account/", "access": "/account/access/", "session": "/api/v1/social/session", "client": "/core-assets/js/newpress-client.js"},
		NavigationCSSURL: data.Navigation.CSSURL,
		Navigation: map[string]presentation.Menu{
			"explore":       {Items: publicLinks(data.Navigation.Explore), Configured: data.Navigation.ExploreConfigured},
			"primary":       {Items: publicLinks(data.Navigation.Primary), Configured: data.Navigation.PrimaryConfigured},
			"utility":       {Items: publicLinks(data.Navigation.Utility), Configured: data.Navigation.UtilityConfigured},
			"mobile":        {Items: publicLinks(data.Navigation.Mobile), Configured: data.Navigation.MobileConfigured},
			"mobile_action": {Items: publicLinks(data.Navigation.MobileAction), Configured: data.Navigation.MobileActionConfigured},
			"footer":        {Items: publicLinks(data.Navigation.Footer), Configured: data.Navigation.FooterConfigured},
		},
		Account: presentation.Account{Authenticated: data.Account.Authenticated, CanWrite: data.Account.CanWrite, ID: data.Account.ID, Name: data.Account.Name, Username: data.Account.Username,
			AvatarURL: data.Account.AvatarURL, Initial: data.Account.Initial, Bio: data.Account.Bio, RoleName: data.Account.RoleName, Joined: data.Account.Joined, Badges: publicBadges(data.Account.Badges)},
		Author: presentation.Author{ID: data.Author.ID, Name: data.Author.Name, AvatarURL: data.Author.AvatarURL, Initial: data.Author.Initial, Description: data.Author.Description, Joined: data.Author.Joined, Badges: publicBadges(data.Author.Badges)},
	}
	d := data.Discovery
	p.Pagination = presentation.Pagination{Total: d.Total, Page: d.Page, Pages: d.Pages, From: d.From, To: d.To, HasPrevious: d.HasPrevious, HasNext: d.HasNext, PreviousURL: d.PreviousURL, NextURL: d.NextURL}
	c := data.Comments
	p.Comments = presentation.Comments{FormURL: c.FormURL, APIURL: c.APIURL, Items: publicCommentItems(c.Items), Total: c.Total, Page: c.Page, Pages: c.Pages, MaxLength: c.MaxLength, Open: c.Open, LoginRequired: c.LoginRequired,
		PerPage: c.PerPage, MaxDepth: c.MaxDepth, RequireLogin: c.RequireLogin, AllowReplies: c.AllowReplies, AccountName: c.AccountName,
		FormToken: c.FormToken, ClosedReason: c.ClosedReason, PreviousURL: c.PreviousURL, NextURL: c.NextURL, Notice: c.Notice, NoticeTone: c.NoticeTone, PrivacyNotice: c.PrivacyNotice, ModerationNotice: c.ModerationNotice,
		FieldErrors: maps.Clone(c.FieldErrors), Form: presentation.CommentForm(c.Form)}
	i := data.Integrations
	p.Integrations = presentation.Integrations{ICPNumber: i.ICPNumber, PoliceNumber: i.PoliceNumber, PoliceURL: i.PoliceURL,
		FooterCopyright: i.FooterCopyright, FooterHTML: i.FooterHTML, FooterHideCredit: i.FooterHideCredit,
		AnalyticsConfig: i.AnalyticsConfig, ScriptURL: i.ScriptURL}
	for _, v := range i.Verification {
		p.Integrations.Verification = append(p.Integrations.Verification, presentation.Verification{Name: v.Name, Content: v.Content})
	}
	fillPublicComponents(&p, data, paths...)
	return p
}

// Chunnel receives its explicitly versioned public view. Administration and storage
// fields are deliberately absent, even when the caller holds an admin view.
type chunnelPublicView struct {
	Status                                                                                             presentation.StatusPage
	Flash                                                                                              flashPageView
	ShopProductKey, ShopDescriptionHTML                                                                string
	Recognition                                                                                        presentation.Recognition
	Site                                                                                               siteView
	Kind, PageTitle, Description, Canonical, BodyClass, ActiveNav, SearchQuery, Category, CategorySlug string
	IsAdmin, IsPreview, VerificationEnabled                                                            bool
	Now                                                                                                time.Time
	ThemeValues                                                                                        map[string]any
	ThemeClass, ThemeCSSURL, CDNAssetBase                                                              string
	ModuleOverrides                                                                                    map[string]bool
	LoginProviders                                                                                     []loginProviderView
	CodeLoginEnabled                                                                                   bool
	Posts, Featured                                                                                    []presentation.Post
	Post                                                                                               presentation.Post
	Categories                                                                                         []content.Term
	Home                                                                                               chunnelHomeView
	Discovery                                                                                          discoveryView
	Author                                                                                             publicAuthorView
	Comments                                                                                           publicCommentsView
	Navigation                                                                                         navigationView
	Archive                                                                                            taxonomyArchiveView
	Community                                                                                          communityView
	Account                                                                                            accountView
	Preview                                                                                            contentPreviewView
	Unsubscribe                                                                                        unsubscribeView
	Series                                                                                             presentation.SeriesPage
	ReadingSeries                                                                                      []presentation.SeriesReading
	Images                                                                                             map[string]*responsiveImageView
	BodyImages                                                                                         map[string]contenthtml.ResponsiveImage
	SEO                                                                                                pageSEOView
	Questions                                                                                          questionsView
	AnnouncementPage                                                                                   announcementPublicView
	Circles                                                                                            circlesPageView
	LinkPages                                                                                          linkPagesView
	PluginFragments                                                                                    []pluginFragment
	Feedback                                                                                           feedbackPageView
	Integrations                                                                                       siteIntegrationsView
}
type chunnelHomeView struct {
	presentation.Home
	Currency currencySettingsDTO
}

func (v chunnelPublicView) SiteTimezone() string   { return v.Site.Timezone }
func (v chunnelPublicView) SiteLocale() string     { return v.Site.Locale }
func (v chunnelPublicView) ThemeAssetBase() string { return v.CDNAssetBase }
func (v chunnelPublicView) ModuleEnabled(id string) bool {
	return module.Allowed(v.ModuleOverrides, id)
}
func chunnelPage(d pageView, paths ...contentPathBuilder) chunnelPublicView {
	return chunnelPublicView{Status: d.Status, Flash: d.Flash, ShopProductKey: d.ShopProductKey, ShopDescriptionHTML: d.ShopDescriptionHTML, Recognition: d.Recognition, Site: d.Site, Kind: d.Kind, PageTitle: d.PageTitle, Description: d.Description, Canonical: d.Canonical, BodyClass: d.BodyClass, ActiveNav: d.ActiveNav,
		SearchQuery: d.SearchQuery, Category: d.Category, CategorySlug: d.CategorySlug, IsPreview: d.IsPreview, VerificationEnabled: d.VerificationEnabled, Now: d.Now,
		ThemeValues: d.ThemeValues, ThemeClass: d.ThemeClass, ThemeCSSURL: d.ThemeCSSURL, CDNAssetBase: d.CDNAssetBase, ModuleOverrides: d.ModuleOverrides, LoginProviders: d.LoginProviders, CodeLoginEnabled: d.CodeLoginEnabled,
		Posts: publicPosts(d.Posts, paths...), Featured: publicPosts(d.Featured, paths...), Post: publicPost(d.Post, paths...), Categories: d.Categories, Home: chunnelHomeView{Home: publicHome(d.Home, paths...), Currency: d.Home.Currency},
		Discovery: d.Discovery, Author: d.Author, Comments: d.Comments, Navigation: d.Navigation, Archive: d.Archive, Community: d.Community, Account: d.Account, Preview: d.Preview,
		Unsubscribe: d.Unsubscribe, Series: publicSeriesPage(d.Series, paths...), ReadingSeries: publicReadingSeries(d.ReadingSeries, paths...), Images: d.Images, BodyImages: d.BodyImages, SEO: d.SEO, Circles: d.Circles, Questions: d.Questions, LinkPages: d.LinkPages,
		PluginFragments: d.PluginFragments, Feedback: d.Feedback, Integrations: d.Integrations, AnnouncementPage: d.AnnouncementPage}
}
