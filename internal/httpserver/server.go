package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"newpress.local/newpress/internal/announcement"
	"sync"
	"time"

	"newpress.local/newpress/internal/accessconfig"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/articlewidgets"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/cdn"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/maildelivery"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/mediausage"
	"newpress.local/newpress/internal/messaging"
	"newpress.local/newpress/internal/navigation"
	"newpress.local/newpress/internal/notification"
	"newpress.local/newpress/internal/objectstorage"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/plugindispatch"
	"newpress.local/newpress/internal/pluginjobs"
	"newpress.local/newpress/internal/progression"
	"newpress.local/newpress/internal/questions"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/resources"
	"newpress.local/newpress/internal/shop"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/update"
	"newpress.local/newpress/internal/verification"
	"newpress.local/newpress/internal/virtualcurrency"
	"newpress.local/newpress/internal/wechat"
	"newpress.local/newpress/internal/writingtemplate"
)

type Server struct {
	resources         *resources.Store
	moduleRoutes      []moduleRoute
	config            config.Config
	repository        content.Repository
	repositoryKind    string
	themes            *theme.Manager
	plugins           *plugin.Manager
	pluginTasks       pluginjobs.ManagedStore
	pluginWebhooks    plugindispatch.Reviewer
	pluginMarket      *plugin.Market
	commentOrigin     *commentorigin.Service
	objectStorage     *objectstorage.Service
	cdn               *cdn.Service
	media             *media.Manager
	mediaUsage        *mediausage.Service
	comments          comment.Store
	messages          messaging.Store
	engagements       engagement.Store
	navigation        navigation.Store
	auditEvents       audit.Reader
	commentGuard      *commentGuard
	clientIPs         clientIPResolver
	siteSettings      *site.Manager
	auth              *auth.Manager
	previewSigningKey [32]byte
	http              *http.Server
	startedAt         time.Time
	systemStatus      systemStatusMonitor
	requests          requestLifetime
	updates           *update.Manager
	extensions        *extensionupdate.Service
	versionMu         sync.Mutex
	mailSender        maildelivery.Sender
	accessRate        commentRateLimiter
	registration      registrationRuntime
	accessSettings    *accessconfig.Store
	notifications     *notification.Service
	analytics         *analytics.Collector
	importMu          sync.Mutex
	questions         questions.Store
	circles           community.Store
	circleFiles       *community.Files
	feedback          feedback.Store
	linkPages         linkpage.Store
	announcements     *announcement.Store
	wechat            *wechat.Service
	currency          virtualcurrency.Store
	currencyCatalog   virtualcurrency.Catalog
	currencyRewards   *virtualcurrency.Service
	progression       *progression.Service
	recognition       *recognition.Service
	commerce          *commerce.Service
	shop              *shop.Service
	aiClient          *aieditor.Client
	aiImages          *aieditor.ImageClient
	aiSettings        *aieditor.SettingsStore
	writingTemplates  *writingtemplate.Service
	aiTelemetry       *aieditor.TelemetryStore
	aiBalances        *aieditor.BalanceService
	aiState           aiEditorState
	aiJobs            *aijobs.Queue
	aiReports         *aiworkbench.Store
	aiPreviews        aiworkbench.Previews
	verification      *verification.Service
	setupRate         setupRateLimiter
	articleComponents *articlewidgets.Store
	componentProfiles componentProfileCache
}

// SetAuditReader attaches the read-only audit projection during application
// assembly. It intentionally does not accept a writer or expose storage
// internals to the HTTP layer.
func (s *Server) SetAuditReader(reader audit.Reader) {
	if s != nil {
		s.auditEvents = reader
	}
}

func (s *Server) SetMessagingStore(store messaging.Store) {
	if store != nil {
		s.messages = store
	}
}

func (s *Server) SetEngagementStore(store engagement.Store) {
	if store != nil {
		s.engagements = store
		if s.notifications != nil {
			s.notifications.Engagement = store
		}
	}
}

func (s *Server) SetCurrencyStore(store virtualcurrency.Store) {
	if s != nil {
		s.currency = store
		if store != nil {
			s.currencyCatalog = virtualcurrency.NewMemoryCatalog(store)
			s.currencyRewards = &virtualcurrency.Service{Store: store, Catalog: s.currencyCatalog, Users: s.auth, Content: s.repository}
		} else {
			s.currencyCatalog = nil
			s.currencyRewards = nil
		}
	}
}

func (s *Server) SetCurrencyCatalog(ctx context.Context, catalog virtualcurrency.Catalog) error {
	if catalog == nil {
		s.SetCurrencyStore(nil)
		return nil
	}
	store, err := catalog.Store(ctx, virtualcurrency.DefaultCurrencyKey)
	if err != nil {
		return err
	}
	s.currency, s.currencyCatalog = store, catalog
	s.currencyRewards = &virtualcurrency.Service{Store: store, Catalog: catalog, Users: s.auth, Content: s.repository}
	return nil
}

func (s *Server) SetNotificationService(service *notification.Service) {
	if service != nil {
		s.notifications = service
		service.FlashEnabled = func() bool { return s.moduleEnabled("flash") }
		s.bindCircleNotifications()
		s.bindQuestionNotifications()
	}
}

func New(cfg config.Config, repository content.Repository, themes *theme.Manager, plugins *plugin.Manager, administrators ...*auth.Manager) *Server {
	settings, err := site.NewManager("", site.DefaultSettings(cfg.SiteTitle, cfg.SiteTagline))
	if err != nil {
		settings, _ = site.NewManager("", site.DefaultSettings("NewPress", "文章、资源与社区"))
	}
	return NewWithSiteSettings(cfg, repository, themes, plugins, settings, administrators...)
}

func NewWithSiteSettings(cfg config.Config, repository content.Repository, themes *theme.Manager, plugins *plugin.Manager, settings *site.Manager, administrators ...*auth.Manager) *Server {
	library, _ := media.NewManager("")
	return NewWithSiteSettingsAndMedia(cfg, repository, themes, plugins, settings, library, administrators...)
}

func NewWithSiteSettingsAndMedia(cfg config.Config, repository content.Repository, themes *theme.Manager, plugins *plugin.Manager, settings *site.Manager, library *media.Manager, administrators ...*auth.Manager) *Server {
	comments, _ := comment.NewFileStore("", cfg.SeedDemo)
	return NewWithSiteSettingsMediaAndComments(cfg, repository, themes, plugins, settings, library, comments, administrators...)
}

func NewWithSiteSettingsMediaAndComments(cfg config.Config, repository content.Repository, themes *theme.Manager, plugins *plugin.Manager, settings *site.Manager, library *media.Manager, comments comment.Store, administrators ...*auth.Manager) *Server {
	navigationStore, _ := navigation.NewFileStore("", cfg.SeedDemo)
	return NewWithSiteSettingsMediaCommentsAndNavigation(cfg, repository, themes, plugins, settings, library, comments, navigationStore, administrators...)
}

func NewWithSiteSettingsMediaCommentsAndNavigation(cfg config.Config, repository content.Repository, themes *theme.Manager, plugins *plugin.Manager, settings *site.Manager, library *media.Manager, comments comment.Store, navigationStore navigation.Store, administrators ...*auth.Manager) *Server {
	adminAuth, _ := auth.New(auth.Config{})
	if len(administrators) > 0 && administrators[0] != nil {
		adminAuth = administrators[0]
	}
	if settings == nil {
		var err error
		settings, err = site.NewManager("", site.DefaultSettings(cfg.SiteTitle, cfg.SiteTagline))
		if err != nil {
			settings, _ = site.NewManager("", site.DefaultSettings("NewPress", "文章、资源与社区"))
		}
	}
	if library == nil {
		library, _ = media.NewManager("")
	}
	if comments == nil {
		comments, _ = comment.NewFileStore("", cfg.SeedDemo)
	}
	if navigationStore == nil {
		navigationStore, _ = navigation.NewFileStore("", cfg.SeedDemo)
	}
	usage := mediausage.New(library, repository, navigationStore, settings, themes, cfg.PublicURL)
	usage.SetProfiles(adminAuth)
	server := &Server{
		config:            cfg,
		articleComponents: articlewidgets.NewStore(componentDataPath(cfg.DataDir)),
		repository:        usage.Content(),
		repositoryKind:    statusStorageLabel(repository, cfg.DatabaseURL != ""),
		themes:            themes,
		plugins:           plugins,
		media:             library,
		mediaUsage:        usage,
		comments:          comments,
		navigation:        usage.Navigation(),
		commentGuard:      newCommentGuard(),
		clientIPs:         newClientIPResolver(cfg.TrustedProxyCIDRs, cfg.CDNProxies...),
		siteSettings:      settings,
		auth:              adminAuth,
		previewSigningKey: newPreviewSigningKey(),
		startedAt:         time.Now(),
		aiClient:          aieditor.NewClient(),
		aiJobs:            aijobs.New(plugins),
		aiImages:          aieditor.NewImageClient(),
	}
	server.repository = &aiContentEvents{Repository: server.repository, s: server}
	resourceBackend, _ := resources.NewFileBackend("")
	server.resources = &resources.Store{Backend: resourceBackend}
	server.feedback, _ = feedback.NewFileStore("")
	server.aiSettings, _ = aieditor.NewSettingsStore("")
	server.writingTemplates, _ = writingtemplate.New(context.Background(), &writingtemplate.MemoryStorage{})
	server.aiReports, _ = aiworkbench.NewStore("")
	server.aiTelemetry, _ = aieditor.NewTelemetryStore("", func() aieditor.TelemetryConfig { return server.aiSettings.Operations() })
	server.aiBalances = aieditor.NewBalanceService()
	server.accessSettings, _ = accessconfig.NewStore("")
	verificationStore, _ := verification.NewStore("")
	server.SetVerification(verificationStore)
	server.linkPages, _ = linkpage.NewFileStore("")
	usage.SetFeatures(server.settingMediaReferences)
	server.announcements, _ = announcement.NewFileStore("")
	if plugins != nil {
		server.commentOrigin = commentorigin.New(plugins, func(ctx context.Context, id int64) (bool, error) {
			_, err := server.comments.Get(ctx, id)
			if errors.Is(err, comment.ErrNotFound) {
				return false, nil
			}
			return err == nil, err
		})
		server.pluginMarket, _ = plugin.NewMarket(plugins, plugin.MarketSource{})
	}
	server.messages, _ = messaging.NewFileStore("")
	wechatStore, _ := wechat.NewFileStore("")
	server.wechat = &wechat.Service{Store: wechatStore, Client: wechat.NewClient(), Content: server.repository, Media: library, PublicURL: cfg.PublicURL}
	if cfg.SMTP.Enabled() {
		if sender, err := maildelivery.NewSMTP(cfg.SMTP); err == nil {
			server.mailSender = sender
		}
	}
	server.engagements, _ = engagement.NewFileStore("")
	statistics, _ := analytics.NewFileStore("")
	server.SetAnalytics(analytics.NewCollector(statistics))
	server.SetPluginServices(server.pluginMarket, server.commentOrigin)
	notificationStore, _ := notification.NewFileStore("")
	server.notifications = &notification.Service{FlashEnabled: func() bool { return server.moduleEnabled("flash") }, Store: notificationStore, Users: adminAuth, Content: server.repository, Comments: comments, Engagement: server.engagements, Settings: func() notification.Settings {
		current := settings.Snapshot().Settings
		return notification.Settings{Title: current.Identity.Title, PublicURL: cfg.PublicURL, Timezone: current.Identity.Timezone}
	}}
	if sender, ok := server.mailSender.(maildelivery.MessageSender); ok {
		server.notifications.Sender = sender
	}
	server.http = &http.Server{
		Addr:              cfg.Address,
		Handler:           server.requests.track(server.routes()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
	return server
}

func (s *Server) Run(ctx context.Context) error {
	errorChannel := make(chan error, 1)
	go func() {
		slog.Info("newpress listening", "address", s.config.Address, "theme", s.themes.Active().Slug)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorChannel <- err
			return
		}
		errorChannel <- nil
	}()

	select {
	case err := <-errorChannel:
		s.requests.stop()
		s.requests.active.Wait()
		return err
	case <-ctx.Done():
		if err := s.shutdown(); err != nil {
			return err
		}
		return <-errorChannel
	}
}
