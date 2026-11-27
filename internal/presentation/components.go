package presentation

import "time"

// Homepage is populated only for the explicitly declared home-layout/v1
// capability. All display links/counts are values, not domain object methods.
type Home struct {
	HasSidebarModules                                         bool
	FocusPosts                                                []Post
	EntryCards                                                []HomeLink
	Announcements                                             []Announcement
	TopicURL                                                  string
	GuideLinks                                                []HomeLink
	Updated, SpotlightDesktop, SpotlightMobile                []Post
	Top, Main, Aside                                          []HomeBlock
	HasDesktopSidebar, HasDesktopMain, HasCustomBlocks        bool
	Slides                                                    []Slide
	SearchTerms, Channels, Filters, Sorts                     []HomeLink
	NoticeURL, ListURL, GridURL, ResetURL, DateLabel, Weekday string
	Categories                                                []HomeCategory
	Tags                                                      []Term
	Authors                                                   []HomeAuthor
	Recent                                                    []Post
	Series                                                    []Series
	Circles                                                   []Circle
	Initials                                                  map[string]string
	CheckIn                                                   CheckIn
	TotalPosts, TotalCategories, TotalSeries                  int
}

type Announcement struct {
	Title, URL string
	Date       time.Time
	Pinned     bool
}
type HomeBlock struct {
	Key, Title, Description, Icon, Visibility, Requires string
	Custom                                              *CustomBlock
}
type CustomBlock struct {
	Key, Title, HTML, Visibility string
	Surface                      bool
}
type Slide struct {
	Index                      int
	Title, Summary, Image, URL string
}
type HomeLink struct {
	Title, Description, URL, Icon string
	Active                        bool
}
type HomeCategory struct {
	Term
	CoverURL string
}
type HomeAuthor struct {
	ID                                    int64
	Name, Initial, AvatarURL, URL, Latest string
}
type CheckIn struct {
	Mode, Timezone                   string
	Enabled                          bool
	Source, Reward, Unit, AccountURL string
	IntervalHours                    int
}
type Series struct {
	ID                                                   int64
	Kind, Title, Slug, Description, CoverURL, URL, Label string
	Count                                                int
}
type SeriesPage struct {
	Items                []Series
	Current              Series
	Chapters             []Post
	Search, Kind         string
	Total, Page, Pages   int
	PreviousURL, NextURL string
}
type SeriesReading struct {
	Series         Series
	Previous, Next Post
	Related        []Post
	Position       int
}
type Unsubscribe struct {
	UserID int64
	Token  string
	Done   bool
}
type Preview struct {
	EditorURL, SavedLabel, ExpiresLabel, StatusLabel string
	SourceVersion                                    int64
}
type Image struct {
	Src, SrcSet, CardSrc, CardSrcSet string
	Width, Height, FocalX, FocalY    int
}
type SEO struct {
	Kind, Published, Modified, ImageAlt string
	ImageWidth, ImageHeight             int
}
type LoginProvider struct{ ID, Name, Mark, URL string }
type Badge struct {
	Name, Color, Kind string
	Level             int
}
type PluginFragment struct{ Name, Title, Text, Tone string }

// Link directory projections exclude icon bytes, write receipts and counters.
type LinkBoard struct {
	Slug, Name, Description, URL, Style, Accent string
	Search, Switcher, NewTab                    bool
}
type DirectoryItem struct {
	ID                                                    int64
	Name, URL, Description, Category, Host, Icon, Initial string
	Featured                                              bool
}
type LinkDirectory struct {
	Board                   LinkBoard
	Boards                  []LinkBoard
	Items                   []DirectoryItem
	Categories              []string
	Style, Search, Category string
	Available               bool
}
