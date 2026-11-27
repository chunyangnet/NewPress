package httpserver

import (
	"newpress.local/newpress/internal/aieditor"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/contenthtml"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/update"
)

func (view pageView) AILanguages() []aieditor.OutputLanguage { return aieditor.OutputLanguages() }

type siteView = presentation.Site

// SiteTimezone lets the theme engine bind its date helpers to the configured
// site timezone. See theme.Manager.Render.
func (view pageView) SiteTimezone() string { return view.Site.Timezone }
func (view pageView) SiteLocale() string   { return view.Site.Locale }

func (view pageView) ThemeAssetBase() string { return view.CDNAssetBase }
func (view pageView) CoreAssetBase() string  { return view.CoreCDNAssetBase }

type pageView struct {
	contentPaths        contentPathBuilder
	Status              presentation.StatusPage
	Flash               flashPageView
	ShopProductKey      string
	ShopDescriptionHTML string
	Recognition         presentation.Recognition
	RelatedTopics       []aiRelatedTopic
	UIAssetVersion      string
	Kind                string
	LoginProviders      []loginProviderView
	CodeLoginEnabled    bool
	VerificationEnabled bool
	ModuleOverrides     map[string]bool
	AppearanceFields    []layoutField
	LayoutFields        []layoutField
	LayoutOptions       []theme.SettingField
	LayoutComponents    []theme.SettingGroup
	HomeModuleSettings  map[string]homeModuleSettings
	HomeCustomBlocks    string
	Modules             modulePage
	FeatureStatus       *adminFeatureStatus
	Site                siteView
	Integrations        siteIntegrationsView
	PageTitle           string
	Description         string
	Canonical           string
	BodyClass           string
	ActiveNav           string
	SearchQuery         string
	Category            string
	CategorySlug        string
	Categories          []content.Term
	Posts               []content.Post
	Featured            []content.Post
	Home                homePageView
	HomeEditor          []homeRegionEditor
	Discovery           discoveryView
	Author              publicAuthorView
	Post                content.Post
	Plugins             []plugin.Installed
	PluginAdmin         pluginAdminView
	PluginRuntime       pluginRuntimeView
	PluginSharedReview  pluginSharedReviewView
	PluginWebhooks      pluginWebhookView
	AIProMax            aiProMaxView
	ObjectStorage       storageAdminView
	CDN                 cdnAdminView
	CDNAssetBase        string
	CoreCDNAssetBase    string
	ExtensionUpdates    extensionUpdateView
	Themes              []theme.Manifest
	Version             string
	GoVersion           string
	Now                 time.Time
	IsAdmin             bool
	IsPreview           bool
	resourcePreviews    map[string]string
	ThemeValues         map[string]any
	ThemeClass          string
	ThemeCSSURL         string
	Admin               adminView
	Dashboard           adminDashboardView
	Content             adminContentView
	Editor              adminEditorView
	Revision            adminRevisionCompareView
	Media               adminMediaView
	Taxonomy            adminTaxonomyView
	Users               adminUsersView
	Sessions            adminSessionsView
	Comments            publicCommentsView
	CommentAdmin        adminCommentsView
	CommentEditor       adminCommentEditorView
	Navigation          navigationView
	MenuAdmin           adminNavigationView
	Tools               adminToolsView
	Updates             update.Snapshot
	Archive             taxonomyArchiveView
	Community           communityView
	Account             accountView
	Currency            currencyAdminView
	Preview             contentPreviewView
	Unsubscribe         unsubscribeView
	Series              seriesPageView
	Snippets            snippetPageView
	ReadingSeries       []seriesReadingView
	Analytics           analyticsView
	SystemStatus        systemStatusView
	Images              map[string]*responsiveImageView
	BodyImages          map[string]contenthtml.ResponsiveImage
	SEO                 pageSEOView
	Redirects           redirectPageView
	Questions           questionsView
	Circles             circlesPageView
	LinkPages           linkPagesView
	Announcements       announcementAdminView
	AnnouncementPage    announcementPublicView
	PluginFragments     []pluginFragment
	Feedback            feedbackPageView
	Features            featureAdminView
}

type communityView struct {
	Total       int
	Page        int
	Pages       int
	HasPrevious bool
	HasNext     bool
	PreviousURL string
	NextURL     string
}

type adminView struct {
	LayoutAvailable         bool
	Current                 string
	DetailTitle             string
	PluginSlug              string
	PluginSettings          []plugin.SettingsPage
	Configured              bool
	Authenticated           bool
	Preview                 bool
	ReadOnly                bool
	Principal               auth.Principal
	Username                string
	Login                   string
	RoleName                string
	PersistentUsers         bool
	CanManageCircles        bool
	CSRFToken               string
	Next                    string
	Notice                  string
	NoticeTone              string
	FieldErrors             map[string]string
	Settings                theme.SettingsSnapshot
	Uptime                  string
	StartedAt               time.Time
	PostCount               int
	PluginCount             int
	ThemeCount              int
	MediaCount              int
	CommentCount            int
	PendingComments         int
	TermCount               int
	CategoryCount           int
	TagCount                int
	NavigationMenuCount     int
	NavigationItemCount     int
	NavigationLocationCount int
	UserCount               int
	SessionCount            int
	OwnSessionCount         int
	MediaBytes              int64
	MediaSize               string
	Checks                  []adminCheck
	SiteSettings            site.SettingsSnapshot
	Runtime                 []adminRuntimeSetting
}

// Can is intended for read/navigation decisions in admin templates. The
// loopback-only unconfigured preview is deliberately allowed to inspect every
// surface; an authenticated user's page-level ReadOnly state must never expand
// that user's capabilities.
func (view adminView) Can(capability string) bool {
	return view.Preview || view.Principal.Can(capability)
}

func (view adminView) CanWrite(capability string) bool {
	return !view.Preview && !view.ReadOnly && view.Principal.Can(capability)
}

type adminRuntimeSetting struct {
	Label  string
	Value  string
	Detail string
	Status string
}

type adminCheck struct {
	Label  string
	Detail string
	Status string
}

type publicPostResponse struct {
	Permalink    string                   `json:"permalink"`
	Pinned       bool                     `json:"pinned"`
	ArticleBadge content.ArticleBadge     `json:"article_badge"`
	ID           int64                    `json:"id"`
	AuthorID     int64                    `json:"author_id"`
	Type         string                   `json:"type"`
	Status       string                   `json:"status"`
	Slug         string                   `json:"slug"`
	Title        string                   `json:"title"`
	Excerpt      string                   `json:"excerpt"`
	ContentHTML  string                   `json:"content_html,omitempty"`
	CoverURL     string                   `json:"cover_url,omitempty"`
	AuthorName   string                   `json:"author_name"`
	Category     string                   `json:"category"`
	Categories   []contentTermAPIResponse `json:"categories"`
	Tags         []contentTermAPIResponse `json:"tags"`
	PublishedAt  time.Time                `json:"published_at"`
	UpdatedAt    time.Time                `json:"updated_at"`
	Version      int64                    `json:"version"`
}
