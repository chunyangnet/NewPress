package httpserver

import (
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/announcement"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/layout"
	"newpress.local/newpress/internal/theme"
)

var errHomeQuery = errors.New("invalid homepage query")

type homeModule = layout.Block
type homeRegionEditor = layout.Region

func (s *Server) homeLayoutEditor(values map[string]any) []homeRegionEditor {
	if !s.themes.Active().Supports(theme.HomeLayoutV1) {
		return nil
	}
	regions := layout.Resolve(values)
	for i := range regions {
		for j := range regions[i].Modules {
			block := &regions[i].Modules[j]
			if block.Requires != "" && !s.moduleEnabled(block.Requires) {
				block.Unavailable = "对应业务模块已暂停，可在模块中心重新启用。"
			}
			if block.Requires == "community" && (!s.config.CommunityEnabled || s.circles == nil) {
				block.Unavailable = "圈子存储尚未启用，实际页面暂不显示。"
			}
		}
	}
	return regions
}

type homeCategory struct {
	content.Term
	CoverURL string
}

type homeAuthor struct {
	ID                                    int64
	Name, Initial, AvatarURL, URL, Latest string
}

type homeLink struct {
	Title, Description, URL, Icon string
	Active                        bool
}

type homeCheckInView struct {
	Mode, Timezone string
	Enabled        bool
	Source         string
	Reward         string
	Unit           string
	IntervalHours  int
	AccountURL     string
}

type homePageView struct {
	GuideLinks                                     []homeLink
	Updated                                        []content.Post
	SpotlightDesktop, SpotlightMobile              []content.Post
	Slides                                         []homeSlide
	SearchTerms                                    []homeLink
	HasCustomBlocks                                bool
	HasSidebarModules                              bool
	FocusPosts                                     []content.Post
	EntryCards                                     []homeLink
	Announcements                                  []homeAnnouncement
	TopicURL                                       string
	NoticeURL                                      string
	Top, Main, Aside                               []homeModule
	HasDesktopSidebar, HasDesktopMain              bool
	Categories                                     []homeCategory
	Tags                                           []content.Term
	Authors                                        []homeAuthor
	Circles                                        []circleCard
	Recent                                         []content.Post
	Initials                                       map[string]string
	Channels, Filters, Sorts                       []homeLink
	Series                                         []content.Series
	Currency                                       currencySettingsDTO
	CheckIn                                        homeCheckInView
	TotalPosts, TotalCategories, TotalSeries       int
	ListURL, GridURL, ResetURL, DateLabel, Weekday string
}

func homeString(values map[string]any, key, fallback string) string {
	if v, ok := values[key].(string); ok {
		return v
	}
	return fallback
}

func homeEnabled(values map[string]any, key string) bool {
	v, ok := values[key].(bool)
	return !ok || v
}

func homeLimit(values map[string]any, key string, fallback, maximum int) int {
	v, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strconvValue(values[key]), "+"), ".0")))
	if err != nil || v < 1 {
		return fallback
	}
	return min(v, maximum)
}

func strconvValue(value any) string {
	switch v := value.(type) {
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

func homeEditor(values map[string]any) []homeRegionEditor  { return layout.Resolve(values) }
func homeRequested(values map[string]any, key string) bool { return layout.Requested(values, key) }

func homeQueryLink(values url.Values, key, value string) string {
	copy := maps.Clone(values)
	copy.Del("page")
	if value == "" {
		copy.Del(key)
	} else {
		copy.Set(key, value)
	}
	return discoveryURL("/", copy, 1) + "#latest"
}

func (s *Server) homePage(request *http.Request, themeValues map[string]any) (pageView, error) {
	settings := s.siteSnapshot(request.Context()).Settings
	hasLayout := s.themes.Active().Supports(theme.HomeLayoutV1)
	defaultSort := "published"
	if hasLayout {
		defaultSort = homeString(themeValues, "home_feed_sort", "published")
	}
	values, query, page, err := discoveryQuery(request, defaultSort, settings.Reading.HomePosts)
	if err != nil {
		return pageView{}, errHomeQuery
	}
	if hasLayout && values.Get("view") == "" {
		values.Set("view", homeString(themeValues, "feed_columns", "list"))
	}
	result, err := s.repository.SearchPublished(request.Context(), query)
	if err != nil {
		return pageView{}, err
	}
	data := s.baseViewContext(request.Context(), "首页", settings.Identity.Description, discoveryURL("/", values, page))
	data.BodyClass, data.ActiveNav, data.ThemeValues = "is-home", "home", themeValues
	data.Posts, data.SearchQuery = result.Items, query.Search
	data.Category, data.CategorySlug = query.Category, query.Category
	data.Discovery = newDiscoveryView("/", values, query, page, result.Total)
	if !hasLayout {
		return data, nil
	}
	for i := range data.Discovery.Links {
		data.Discovery.Links[i].URL += "#latest"
	}
	data.Discovery.PreviousURL += "#latest"
	data.Discovery.NextURL += "#latest"
	h := &data.Home
	h.Slides, h.SearchTerms = s.homeCreativeContent(themeValues)
	h.NoticeURL = homeString(themeValues, "home_notice_url", "")
	if !s.moduleURLAvailable(h.NoticeURL) {
		h.NoticeURL = ""
	}
	h.Initials = map[string]string{}
	h.DateLabel = data.Now.Format("01月02日")
	h.Weekday = []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}[data.Now.Weekday()]
	h.ListURL, h.GridURL = homeQueryLink(values, "view", "list"), homeQueryLink(values, "view", "grid")
	h.ResetURL = "/#latest"
	h.Filters = []homeLink{{Title: "全部", URL: homeQueryLink(values, "category", ""), Active: query.Category == ""}}
	for _, sort := range []struct{ key, title string }{{"published", "最新发布"}, {"updated", "最近更新"}, {"oldest", "最早发布"}} {
		h.Sorts = append(h.Sorts, homeLink{Title: sort.title, URL: homeQueryLink(values, "sort", sort.key), Active: query.Sort == sort.key})
	}

	recent := content.PublishedPage{}
	needRecent := homeRequested(themeValues, "recent") || homeRequested(themeValues, "authors") || homeRequested(themeValues, "hero") || homeRequested(themeValues, "spotlight") || homeRequested(themeValues, "categories") || homeRequested(themeValues, "focus_list") || homeRequested(themeValues, "image_grid")
	if needRecent {
		recent, err = s.repository.SearchPublished(request.Context(), content.PublishedQuery{Sort: "published", Limit: 24})
		if err != nil {
			return pageView{}, err
		}
	}
	h.TotalPosts = result.Total
	h.Recent = recent.Items[:min(5, len(recent.Items))]
	data.Featured = recent.Items[:min(6, len(recent.Items))]
	if raw := strings.TrimSpace(homeString(themeValues, "home_featured_ids", "")); needRecent && raw != "" {
		ids := []int64{}
		for _, value := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil && id > 0 && !slices.Contains(ids, id) && len(ids) < 12 {
				ids = append(ids, id)
			}
		}
		selected, err := s.repository.SearchPublished(request.Context(), content.PublishedQuery{PostIDs: ids, Sort: "selection", Limit: 6})
		if err != nil {
			return pageView{}, err
		}
		data.Featured = selected.Items
	}
	h.SpotlightDesktop, h.SpotlightMobile = homeSpotlight(themeValues, data.Featured)
	h.FocusPosts = data.Featured[:min(5, len(data.Featured))]
	h.EntryCards = s.homeEntryCards(themeValues)
	h.TopicURL = homeString(themeValues, "home_topic_url", "")
	if !s.moduleURLAvailable(h.TopicURL) {
		h.TopicURL = ""
	}
	if homeRequested(themeValues, "notice_list") {
		state, readErr := s.announcements.Snapshot(request.Context())
		if readErr != nil {
			slog.Warn("homepage announcements unavailable", "error", readErr)
		} else {
			for _, item := range announcement.Public(state, time.Now()) {
				if len(h.Announcements) >= homeLimit(themeValues, "home_notice_list_count", 4, 8) {
					break
				}
				if !s.moduleURLAvailable(announcementURL(item)) {
					continue
				}
				h.Announcements = append(h.Announcements, homeAnnouncement{Title: item.Title, URL: announcementURL(item), Date: item.Date, Pinned: item.Pinned})
			}
		}
	}
	if homeRequested(themeValues, "reading_guide") {
		h.GuideLinks = s.homeGuideLinks(themeValues)
	}
	if homeRequested(themeValues, "updated") {
		updated, queryErr := s.repository.SearchPublished(request.Context(), content.PublishedQuery{Sort: "updated", Limit: max(3, homeLimit(themeValues, "home_updated_count", 4, 5))})
		if queryErr != nil {
			slog.Warn("homepage updates unavailable", "error", queryErr)
		} else {
			h.Updated = updated.Items
		}
	}
	categories := content.TermList{}
	if homeRequested(themeValues, "categories") || homeRequested(themeValues, "feed") || homeRequested(themeValues, "stats") {
		categories, err = s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyCategory, Limit: 500})
		if err != nil {
			return pageView{}, err
		}
	}
	for _, category := range categories.Items {
		if category.Count <= 0 {
			continue
		}
		data.Categories = append(data.Categories, category)
		active := category.Slug == content.NormalizeTermSlug(query.Category) || strings.EqualFold(category.Name, query.Category)
		if active {
			data.Category, data.CategorySlug = category.Name, category.Slug
		}
		h.Filters = append(h.Filters, homeLink{Title: category.Name, URL: homeQueryLink(values, "category", category.Slug), Active: active})
		if len(h.Categories) < homeLimit(themeValues, "home_category_count", 4, 8) {
			card := homeCategory{Term: category}
			for _, post := range recent.Items {
				for _, term := range post.Categories {
					if term.ID == category.ID && post.CoverURL != "" {
						card.CoverURL = post.CoverURL
						break
					}
				}
				if card.CoverURL != "" {
					break
				}
			}
			h.Categories = append(h.Categories, card)
		}
	}
	h.TotalCategories = len(data.Categories)

	if s.moduleEnabled("series") && (homeRequested(themeValues, "series") || homeRequested(themeValues, "stats")) {
		limit := homeLimit(themeValues, "home_series_count", 4, 8)
		for offset := 0; offset < content.MaxSeries; offset += 100 {
			series, err := s.repository.ListSeries(request.Context(), content.SeriesQuery{PublicOnly: true, Limit: 100, Offset: offset})
			if err != nil {
				slog.Warn("homepage series unavailable", "error", err)
				break
			}
			h.TotalSeries = series.Total
			for _, item := range series.Items {
				if item.Count() > 0 && len(h.Series) < limit {
					h.Series = append(h.Series, item)
				}
			}
			if len(h.Series) == limit || offset+len(series.Items) >= series.Total || len(series.Items) == 0 || !homeRequested(themeValues, "series") {
				break
			}
		}
	}
	if homeRequested(themeValues, "tags") {
		tags, err := s.repository.ListTerms(request.Context(), content.TermQuery{Taxonomy: content.TaxonomyTag, Limit: 100})
		if err != nil {
			slog.Warn("homepage tags unavailable", "error", err)
		} else {
			slices.SortStableFunc(tags.Items, func(a, b content.Term) int { return b.Count - a.Count })
			for _, tag := range tags.Items {
				if tag.Count > 0 && len(h.Tags) < 12 {
					h.Tags = append(h.Tags, tag)
				}
			}
		}
	}
	seenAuthors := map[string]bool{}
	for _, post := range append(append(slices.Clone(recent.Items), data.Posts...), data.Featured...) {
		initial, _ := utf8.DecodeRuneInString(post.AuthorName)
		h.Initials[post.AuthorName] = string(initial)
		key := post.AuthorURL()
		if !homeRequested(themeValues, "authors") || len(h.Authors) >= 4 || seenAuthors[key] {
			continue
		}
		seenAuthors[key] = true
		author := homeAuthor{ID: post.AuthorID, Name: post.AuthorName, Initial: string(initial), URL: key, Latest: post.Title}
		if post.AuthorID > 0 {
			user, err := s.auth.GetUser(request.Context(), post.AuthorID)
			if err != nil || user.Status != auth.UserStatusActive {
				continue
			}
			author.Name, author.AvatarURL = user.DisplayName, s.avatarURL(user.AvatarMediaID)
		}
		h.Authors = append(h.Authors, author)
	}
	if s.moduleEnabled("community") && homeRequested(themeValues, "community") && s.config.CommunityEnabled && s.circles != nil {
		items, err := s.circles.List(request.Context())
		if err != nil {
			slog.Warn("homepage circles unavailable", "error", err)
		} else {
			for _, item := range items {
				if item.Visibility == "public" && len(h.Circles) < 4 {
					h.Circles = append(h.Circles, s.circleCard(request.Context(), item, community.Actor{}))
				}
			}
		}
	}
	if homeRequested(themeValues, "checkin") {
		chosen, coin, _ := s.resolveHomeCheckIn(request.Context())
		h.CheckIn = chosen
		if chosen.Source == "wallet" {
			h.Currency = coin
		}
	}
	h.Channels = []homeLink{
		{Title: "发现内容", Description: "浏览站点里的全部内容", URL: "/explore/", Icon: "compass"},
		{Title: "专题合集", Description: "沿着主题继续深入", URL: "/series/", Icon: "library"},
		{Title: "我的收藏", Description: "留住值得再看的内容", URL: "/account/", Icon: "bookmark"},
	}
	if s.moduleEnabled("community") && homeEnabled(themeValues, "community_nav") {
		h.Channels = append(h.Channels, homeLink{Title: "交流圈子", Description: "加入正在发生的讨论", URL: "/community/", Icon: "orbit"})
	}
	if !s.moduleEnabled("series") {
		h.Channels = slices.DeleteFunc(h.Channels, func(link homeLink) bool { return link.URL == "/series/" })
	}
	if data.Site.PublicAPIEnabled {
		h.Channels = append(h.Channels, homeLink{Title: "订阅更新", Description: "用 RSS 跟踪站点更新", URL: "/feed.xml", Icon: "rss"})
	}
	available := map[string]bool{
		"focus_list": len(h.FocusPosts) > 0, "image_grid": len(h.FocusPosts) > 0,
		"topic_banner": strings.TrimSpace(homeString(themeValues, "home_topic_title", "")) != "", "entry_cards": len(h.EntryCards) > 0, "notice_list": len(h.Announcements) > 0,
		"carousel": len(h.Slides) > 0, "search_banner": true,
		"hero": true, "notice": strings.TrimSpace(homeString(themeValues, "home_notice_text", "")) != "", "channels": true,
		"categories": len(h.Categories) > 0, "feed": true, "series": len(h.Series) > 0, "spotlight": len(h.SpotlightDesktop)+len(h.SpotlightMobile) > 0,
		"reading_guide": len(h.GuideLinks) > 0, "updated": len(h.Updated) > 0,
		"community": len(h.Circles) > 0, "welcome": true, "checkin": h.CheckIn.Enabled,
		"recent": len(h.Recent) > 0, "authors": len(h.Authors) > 0, "tags": len(h.Tags) > 0, "stats": true, "subscribe": data.Site.PublicAPIEnabled,
	}
	for _, region := range homeEditor(themeValues) {
		if region.Key == "aside" && !homeEnabled(themeValues, "sidebar_enabled") {
			continue
		}
		for _, item := range region.Modules {
			if item.Custom != nil {
				available[item.Key] = strings.TrimSpace(item.Custom.HTML) != ""
			}
			if item.Visibility == "hidden" || !available[item.Key] {
				continue
			}
			h.HasCustomBlocks = h.HasCustomBlocks || item.Key == "carousel" || item.Key == "search_banner" || item.Custom != nil
			h.HasSidebarModules = h.HasSidebarModules || slices.Contains([]string{"focus_list", "topic_banner", "entry_cards", "image_grid", "notice_list"}, item.Key)
			switch region.Key {
			case "top":
				h.Top = append(h.Top, item)
			case "main":
				h.Main = append(h.Main, item)
				h.HasDesktopMain = h.HasDesktopMain || item.Visibility != "mobile"
			case "aside":
				h.Aside = append(h.Aside, item)
				h.HasDesktopSidebar = h.HasDesktopSidebar || item.Visibility != "mobile"
			}
		}
	}
	return data, nil
}
