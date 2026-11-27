// Package presentation defines the versioned, read-only public theme contract.
// It imports no repositories, identities, configuration stores or HTTP handlers.
package presentation

import "time"

const Version = "public/v1"

type Site struct {
	Title, Tagline, Description, PublicURL, Locale, Timezone       string
	IconURL, SocialImageURL, Generator, Robots, MaintenanceMessage string
	ActiveTheme, ThemeVersion                                      string
	PublicAPIEnabled                                               bool
}

type Term struct {
	ID                           int64
	Name, Slug, Description, URL string
	Count                        int
}
type Post struct {
	Pinned                                              bool
	ArticleBadge                                        ArticleBadge
	ID, AuthorID                                        int64
	Type, Slug, Title, Excerpt, ContentHTML, CoverURL   string
	URL, AuthorName, AuthorURL, Category, CommentStatus string
	Categories, Tags                                    []Term
	PublishedAt, UpdatedAt                              time.Time
}

// ArticleBadge is a read-only visual hint with validated style, color and icon IDs.
type ArticleBadge struct {
	Kind, Text, Style, Tone, Icon, Placement string
	HideOnMobile                             bool
}

// NavigationBadge contains public style hints; themes choose the rendering.
type NavigationBadge struct {
	Text, Style, Placement, Background, Color, BorderColor, GradientTo string
	FontSize, Radius, OffsetX, OffsetY                                 int
	HideOnMobile                                                       bool
}

type Link struct {
	Badge                                            NavigationBadge
	ID                                               int64
	Label, URL, Description, Rel, Icon, Presentation string
	NewWindow, Active, Current                       bool
	Children                                         []Link
}
type Menu struct {
	Items      []Link
	Configured bool
}
type Pagination struct {
	Total, Page, Pages, From, To int
	HasPrevious, HasNext         bool
	PreviousURL, NextURL         string
}
type Account struct {
	Authenticated, CanWrite                                   bool
	ID                                                        int64
	Name, Username, AvatarURL, Initial, Bio, RoleName, Joined string
	Badges                                                    []Badge
}
type Author struct {
	ID                                            int64
	Name, AvatarURL, Initial, Description, Joined string
	Badges                                        []Badge
}
type Comment struct {
	Badges                  []IdentityBadge
	Support                 CommentSupport
	ID, ParentID            int64
	AuthorName, ContentHTML string
	Avatar                  string
	Depth                   int
	CreatedAt, EditedAt     time.Time
	StaffReply, CanReply    bool
	Children                []Comment
}
type Comments struct {
	FormURL, APIURL                                     string
	Items                                               []Comment
	Total, Page, Pages, PerPage, MaxLength, MaxDepth    int
	Open, LoginRequired, RequireLogin, AllowReplies     bool
	AccountName                                         string
	FormToken, ClosedReason, PreviousURL, NextURL       string
	Notice, NoticeTone, PrivacyNotice, ModerationNotice string
	FieldErrors                                         map[string]string
	Form                                                CommentForm
}
type CommentForm struct {
	ParentID                         int64
	AuthorName, AuthorEmail, Content string
}
type Verification struct{ Name, Content string }
type Integrations struct {
	ICPNumber, PoliceNumber, PoliceURL string
	FooterCopyright, FooterHTML        string
	FooterHideCredit                   bool
	Verification                       []Verification
	AnalyticsConfig, ScriptURL         string
}

type StatusPage struct {
	Code                                int
	Kind, Title, Message, Service, Icon string
}

type Page struct {
	Status                                       StatusPage
	Recognition                                  Recognition
	Contract, Kind                               string
	Site                                         Site
	PageTitle, Description, Canonical, BodyClass string
	IsPreview                                    bool
	Now                                          time.Time
	Post                                         Post
	Posts, Featured                              []Post
	Categories                                   []Term
	Archive                                      Term
	Author                                       Author
	Account                                      Account
	Comments                                     Comments
	Pagination                                   Pagination
	Navigation                                   map[string]Menu
	NavigationCSSURL                             string
	Modules                                      map[string]bool
	Links                                        map[string]string
	Settings                                     map[string]any
	ThemeCSSURL, ThemeClass                      string
	SearchQuery, Category, CategorySlug          string
	Integrations                                 Integrations
	CDNAssetBase                                 string
	Home                                         Home
	Series                                       SeriesPage
	ReadingSeries                                []SeriesReading
	Questions                                    Questions
	AnnouncementPage                             AnnouncementPage
	Flash                                        Flash
	ShopProductKey, ShopDescriptionHTML          string
	Circles                                      Circles
	LinkPages                                    LinkDirectory
	Feedback                                     Feedback
	Unsubscribe                                  Unsubscribe
	Preview                                      Preview
	Images                                       map[string]Image
	SEO                                          SEO
	LoginProviders                               []LoginProvider
	CodeLoginEnabled, VerificationEnabled        bool
	PluginFragments                              []PluginFragment
}

// These methods are renderer metadata only; page values contain no services.
func (p Page) SiteTimezone() string   { return p.Site.Timezone }
func (p Page) SiteLocale() string     { return p.Site.Locale }
func (p Page) ThemeAssetBase() string { return p.CDNAssetBase }
