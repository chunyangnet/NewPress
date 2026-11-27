package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"newpress.local/newpress/internal/announcement"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"newpress.local/newpress/internal/accessconfig"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/aiworkbench"
	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/cdn"
	"newpress.local/newpress/internal/comment"
	"newpress.local/newpress/internal/commentorigin"
	"newpress.local/newpress/internal/commerce"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/database"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/extensionupdate"
	"newpress.local/newpress/internal/feedback"
	"newpress.local/newpress/internal/httpserver"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/maildelivery"
	"newpress.local/newpress/internal/media"
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
	"newpress.local/newpress/internal/version"
	"newpress.local/newpress/internal/virtualcurrency"
	"newpress.local/newpress/internal/wechat"
)

type App struct {
	config        config.Config
	pool          *pgxpool.Pool
	server        *httpserver.Server
	sessions      auth.SessionStore
	themes        *theme.Manager
	plugins       *plugin.Manager
	pluginWorker  *pluginjobs.Worker
	webhookWorker *plugindispatch.Worker
	commentOrigin *commentorigin.Service
	objectStorage *objectstorage.Service
	cdn           *cdn.Service
	media         *media.Manager
	comments      comment.Store
	navigation    navigation.Store
	settings      *site.Manager
	locks         *dirlock.Set
	updates       *update.Manager
	extensions    *extensionupdate.Service
	notifications *notification.Service
	analytics     *analytics.Collector
	aiTelemetry   *aieditor.TelemetryStore
	wechat        *wechat.Service
	commerce      *commerce.Service
	shop          *shop.Service
	circles       community.Store
	circleFiles   *community.Files
}

func New(ctx context.Context, cfg config.Config) (*App, error) {
	locks, err := dirlock.Acquire(cfg.DataDir, cfg.ThemesDir, cfg.PluginsDir)
	if err != nil {
		return nil, err
	}
	initialized := false
	var pool *pgxpool.Pool
	defer func() {
		if !initialized {
			if pool != nil {
				pool.Close()
			}
			_ = locks.Close()
		}
	}()
	if cfg.DatabaseURL != "" {
		pool, err = database.OpenWithPoolLimits(ctx, cfg.DatabaseURL, cfg.DBMinConns, cfg.DBMaxConns)
		if err != nil {
			return nil, err
		}
		if cfg.AutoMigrate {
			if err := database.Migrate(ctx, pool); err != nil {
				return nil, err
			}
		}
	}
	themeOptions := []theme.ManagerOption{theme.WithSettingsDir(filepath.Join(cfg.DataDir, "theme-settings"))}
	if pool != nil {
		themeOptions = append(themeOptions, theme.WithSettingsRepository(database.NewThemeSettingsRepository(pool)))
	}
	themes := theme.NewManager(cfg.ThemesDir, cfg.ActiveTheme, themeOptions...)
	if err := themes.Recover(); err != nil {
		return nil, fmt.Errorf("recover theme updates: %w", err)
	}
	if err := themes.Load(); err != nil {
		return nil, fmt.Errorf("load active theme %q: %w", cfg.ActiveTheme, err)
	}
	aiSettings, err := aieditor.NewSettingsStore(filepath.Join(cfg.DataDir, "ai-settings.json"))
	if err != nil {
		return nil, fmt.Errorf("load AI settings: %w", err)
	}
	aiTelemetry, err := aieditor.NewTelemetryStore(filepath.Join(cfg.DataDir, "ai-usage.json"), aiSettings.Operations)
	if err != nil {
		return nil, fmt.Errorf("load AI usage: %w", err)
	}
	aiReports, err := aiworkbench.NewStore(filepath.Join(cfg.DataDir, "ai-reports.json"))
	if err != nil {
		return nil, fmt.Errorf("load AI reports: %w", err)
	}
	accessSettings, err := accessconfig.NewStore(filepath.Join(cfg.DataDir, "access-settings.json"))
	if err != nil {
		return nil, fmt.Errorf("load login and SMS settings: %w", err)
	}
	verificationSettings, err := verification.NewStore(filepath.Join(cfg.DataDir, "verification-settings.json"))
	if err != nil {
		return nil, fmt.Errorf("load verification settings: %w", err)
	}
	mediaLibrary, err := media.NewManager(filepath.Join(cfg.DataDir, "media"))
	if err != nil {
		return nil, fmt.Errorf("load media library: %w", err)
	}

	var pluginOptions []plugin.ManagerOption
	if pool != nil {
		controls := database.NewPluginTaskControlRepository(pool)
		if err := controls.Ready(ctx); err != nil {
			return nil, fmt.Errorf("validate shared plugin authorization: %w", err)
		}
		pluginOptions = append(pluginOptions, plugin.WithTaskControlStore(controls))
	}
	plugins := plugin.NewManager(cfg.PluginsDir, pluginOptions...)
	if err := plugins.Recover(); err != nil {
		return nil, fmt.Errorf("recover plugin operations: %w", err)
	}
	market, err := plugin.NewMarket(plugins, plugin.EnvironmentSource())
	if err != nil {
		return nil, fmt.Errorf("initialize official plugin market: %w", err)
	}
	origin := commentorigin.New(plugins)
	storage := objectstorage.New(plugins, mediaLibrary)
	cdnService := cdn.New(plugins, cfg.PublicURL)
	storage.OnDeliveryChange = func(raw string) { cdnService.PurgeURLs("storage", []string{raw}) }
	var (
		siteSettings      *site.Manager
		repository        content.Repository
		users             auth.UserStore
		comments          comment.Store
		messages          messaging.Store
		engagements       engagement.Store
		statistics        analytics.Store
		notices           notification.Store
		wechatStore       wechat.Store
		circles           community.Store
		feedbackStore     feedback.Store
		linkPageStore     linkpage.Store
		announcementStore *announcement.Store
		activitySources   []activity.Source
		navigationStore   navigation.Store
		auditReader       audit.Reader
	)
	var currencyCatalog virtualcurrency.Catalog
	var progressionStore *progression.Store

	if cfg.DatabaseURL == "" {
		usersPath := filepath.Join(cfg.DataDir, "users.json")
		commentsPath := filepath.Join(cfg.DataDir, "comments.json")
		navigationPath := filepath.Join(cfg.DataDir, "navigation.json")
		ledger, ledgerErr := audit.Open(filepath.Join(cfg.DataDir, audit.FileName))
		if ledgerErr != nil {
			return nil, fmt.Errorf("open local audit ledger: %w", ledgerErr)
		}
		auditReader = ledger
		facts := map[string]string{audit.FactUsers: usersPath, audit.FactComments: commentsPath, audit.FactNavigation: navigationPath, audit.FactCommunity: filepath.Join(cfg.DataDir, "community.json")}
		if ledgerErr := ledger.ResolvePaths(facts); ledgerErr != nil {
			return nil, fmt.Errorf("recover local audit ledger: %w", ledgerErr)
		}
		repository, err = content.NewFileRepository(filepath.Join(cfg.DataDir, "content.json"), cfg.SeedDemo)
		if err != nil {
			return nil, fmt.Errorf("load persistent content: %w", err)
		}
		feedbackStore, err = feedback.NewFileStore(filepath.Join(cfg.DataDir, "feedback.json"))
		if err != nil {
			return nil, fmt.Errorf("load feedback pages: %w", err)
		}
		linkPageStore, err = linkpage.NewFileStore(filepath.Join(cfg.DataDir, "link-pages.json"))
		if err != nil {
			return nil, fmt.Errorf("load link pages: %w", err)
		}
		announcementStore, err = announcement.NewFileStore(filepath.Join(cfg.DataDir, "announcements.json"))
		if err != nil {
			return nil, fmt.Errorf("load announcements: %w", err)
		}
		messages, err = messaging.NewFileStore(filepath.Join(cfg.DataDir, "messages.json"))
		if err != nil {
			return nil, fmt.Errorf("load messages: %w", err)
		}
		wechatStore, err = wechat.NewFileStore(filepath.Join(cfg.DataDir, "wechat.json"))
		if err != nil {
			return nil, fmt.Errorf("load WeChat integration: %w", err)
		}
		// Validate retained extension facts even while it is disabled, including
		// during the offline backup/restore application validation.
		communityPath := filepath.Join(cfg.DataDir, "community.json")
		_, communityFileErr := os.Lstat(communityPath)
		if cfg.CommunityEnabled || !os.IsNotExist(communityFileErr) {
			circles, err = community.NewFileStore(communityPath, ledger)
			if err != nil {
				return nil, fmt.Errorf("load community: %w", err)
			}
			if cfg.CommunityEnabled {
				if err = ledger.EnsureBaselinePath(audit.FactCommunity, communityPath); err != nil {
					return nil, err
				}
			}
		}
		engagements, err = engagement.NewFileStore(filepath.Join(cfg.DataDir, "engagement.json"))
		if err != nil {
			return nil, fmt.Errorf("load account engagement: %w", err)
		}
		currencyCatalog, err = virtualcurrency.NewFileCatalog(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("load virtual currency ledger: %w", err)
		}
		progressionStore, err = progression.NewFileStore(filepath.Join(cfg.DataDir, "progression.json"))
		if err != nil {
			return nil, fmt.Errorf("load progression ledger: %w", err)
		}
		statistics, err = analytics.NewFileStore(filepath.Join(cfg.DataDir, "analytics.json"))
		if err != nil {
			return nil, fmt.Errorf("load analytics: %w", err)
		}
		notices, err = notification.NewFileStore(filepath.Join(cfg.DataDir, "notifications.json"))
		if err != nil {
			return nil, fmt.Errorf("load notifications: %w", err)
		}
		users, err = auth.NewFileUserStore(usersPath, ledger)
		if err != nil {
			return nil, fmt.Errorf("load persistent users: %w", err)
		}
		comments, err = comment.NewFileStore(commentsPath, cfg.SeedDemo, ledger)
		if err != nil {
			return nil, fmt.Errorf("load persistent comments: %w", err)
		}
		activitySources = []activity.Source{repository.(activity.Source), comments.(activity.Source)}
		navigationStore, err = navigation.NewFileStore(navigationPath, cfg.SeedDemo, ledger)
		if err != nil {
			return nil, fmt.Errorf("load persistent navigation: %w", err)
		}
		for _, fact := range []struct{ key, path string }{
			{audit.FactUsers, usersPath}, {audit.FactComments, commentsPath}, {audit.FactNavigation, navigationPath},
		} {
			if err := ledger.EnsureBaselinePath(fact.key, fact.path); err != nil {
				return nil, fmt.Errorf("anchor local %s audit baseline: %w", fact.key, err)
			}
		}
	} else {
		if cfg.SeedDemo {
			if err := database.SeedDemo(ctx, pool); err != nil {
				return nil, err
			}
		}
		repository = database.NewPostRepository(pool)
		if err := database.PreparePublicSearch(ctx, pool); err != nil {
			return nil, fmt.Errorf("prepare public search: %w", err)
		}
		if err := database.PrepareSnippetReferences(ctx, pool); err != nil {
			return nil, fmt.Errorf("prepare snippet references: %w", err)
		}
		feedbackStore = database.NewFeedbackRepository(pool)
		linkPageStore = database.NewLinkPageRepository(pool)
		announcementStore, err = announcement.NewRepositoryStore(ctx, database.NewAnnouncementRepository(pool))
		if err != nil {
			return nil, fmt.Errorf("load announcements: %w", err)
		}
		messages = database.NewMessagingRepository(pool)
		engagements = database.NewEngagementRepository(pool)
		currencyCatalog = database.NewCurrencyCatalog(pool)
		progressionStore = database.NewProgressionStore(pool)
		statistics = database.NewAnalyticsRepository(pool)
		notices = database.NewNotificationRepository(pool)
		wechatStore = database.NewWeChatRepository(pool)
		if _, err = wechatStore.View(ctx); err != nil {
			return nil, fmt.Errorf("load WeChat integration: %w", err)
		}
		if cfg.CommunityEnabled {
			circles = database.NewCommunityRepository(pool)
		}
		activitySources = []activity.Source{database.NewActivitySource(pool, "notification.post"), database.NewActivitySource(pool, "notification.comment")}
		users = database.NewUserRepository(pool)
		comments = database.NewCommentRepository(pool)
		navigationStore = database.NewNavigationRepository(pool)
		auditReader = database.NewAuditReader(pool)
	}
	if pool == nil {
		siteSettings, err = site.NewManager(
			filepath.Join(cfg.DataDir, "site-settings.json"),
			site.DefaultSettings(cfg.SiteTitle, cfg.SiteTagline),
		)
	} else {
		siteSettings, err = site.NewManagerWithRepository(
			ctx, database.NewSiteSettingsRepository(pool),
			filepath.Join(cfg.DataDir, "site-settings.json"),
			site.DefaultSettings(cfg.SiteTitle, cfg.SiteTagline),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("load persistent site settings: %w", err)
	}

	var sessions auth.SessionStore
	if pool == nil {
		sessions, err = auth.NewFileSessionStore(filepath.Join(cfg.DataDir, auth.SessionDirectory, "sessions.json"))
		if err != nil {
			return nil, fmt.Errorf("load persistent sessions: %w", err)
		}
	} else {
		sessions = database.NewSessionRepository(pool)
	}

	adminAuth, err := auth.NewWithContext(ctx, auth.Config{
		Username:             cfg.AdminUsername,
		PasswordHash:         cfg.AdminPassHash,
		BootstrapEmail:       cfg.AdminEmail,
		BootstrapDisplayName: cfg.AdminDisplayName,
		BootstrapLocale:      cfg.AdminLocale,
		Users:                users,
		Sessions:             sessions,
		AbsoluteTTL:          cfg.AdminSessionTTL,
		IdleTTL:              cfg.AdminIdleTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("configure administrator authentication: %w", err)
	}

	repository = cdn.WrapRepository(repository, cdnService)
	comments = cdn.WrapComments(comments, repository, cdnService)
	server := httpserver.NewWithSiteSettingsMediaCommentsAndNavigation(cfg, repository, themes, plugins, siteSettings, mediaLibrary, comments, navigationStore, adminAuth)
	var taskStore pluginjobs.ManagedStore
	if pool == nil {
		backend, loadErr := pluginjobs.NewFileBackend(filepath.Join(cfg.DataDir, "plugin-jobs.json"))
		if loadErr != nil {
			return nil, fmt.Errorf("load plugin tasks: %w", loadErr)
		}
		taskStore = &pluginjobs.Queue{Backend: backend}
	} else {
		taskStore = database.NewPluginJobRepository(pool)
	}
	server.SetPluginTaskStore(taskStore)
	pluginExecutor := server.PluginTaskExecutor()
	var webhookWorker *plugindispatch.Worker
	if pool != nil {
		effects := database.NewPluginContentEffectRepository(pool)
		if err := effects.Ready(ctx); err != nil {
			return nil, fmt.Errorf("load plugin content receipts: %w", err)
		}
		pluginExecutor.Effects = effects
		webhooks := database.NewPluginWebhookRepository(pool, cfg.PluginWebhooksEnabled)
		if err := webhooks.Ready(ctx); err != nil {
			return nil, fmt.Errorf("load plugin webhook intents: %w", err)
		}
		pluginExecutor.Webhooks = webhooks
		server.SetPluginWebhookStore(webhooks)
		webhookWorker = &plugindispatch.Worker{Store: webhooks, Sender: plugindispatch.PublicSender{}}
	}
	pluginWorker := &pluginjobs.Worker{Queue: taskStore, Executor: pluginExecutor}
	var questionStore questions.Store
	if pool == nil {
		questionStore, err = questions.NewFileStore(filepath.Join(cfg.DataDir, "questions.json"))
	} else {
		questionStore = database.NewQuestionsRepository(pool)
	}
	if err != nil {
		return nil, fmt.Errorf("load questions: %w", err)
	}
	if err = questionStore.Ready(ctx); err != nil {
		return nil, fmt.Errorf("validate questions: %w", err)
	}
	server.SetQuestionsStore(questionStore)
	server.SetAuditReader(auditReader)
	server.SetAISettings(aiSettings)
	server.SetAITelemetry(aiTelemetry)
	server.SetAIReports(aiReports)
	server.SetAccessSettings(accessSettings)
	server.SetVerification(verificationSettings)
	server.SetFeedbackStore(feedbackStore)
	server.SetLinkPageStore(linkPageStore)
	storage.SetLinkPages(linkPageStore)
	server.SetAnnouncementStore(announcementStore)
	server.SetMessagingStore(messages)
	server.SetEngagementStore(engagements)
	if err = server.SetCurrencyCatalog(ctx, currencyCatalog); err != nil {
		return nil, fmt.Errorf("load currency catalog: %w", err)
	}
	var circleFiles *community.Files
	if circles != nil {
		files, fileErr := community.NewFiles(filepath.Join(cfg.DataDir, "community-files"))
		if fileErr != nil {
			return nil, fileErr
		}
		if fileErr = files.ValidateStore(ctx, circles); fileErr != nil {
			return nil, fileErr
		}
		server.SetCommunityFiles(files)
		circleFiles = files
		storage.SetCommunity(circles, files)
	}
	if cfg.CommunityEnabled {
		server.SetCommunityStore(circles)
	}
	progressionService := &progression.Service{Store: progressionStore, Users: adminAuth, Content: repository, Currencies: currencyCatalog}
	if err = progressionService.ValidatePayments(ctx); err != nil {
		return nil, fmt.Errorf("validate membership payments: %w", err)
	}
	server.SetProgressionService(progressionService)
	var recognitionBackend recognition.Backend
	if pool == nil {
		recognitionBackend, err = recognition.NewFileBackend(filepath.Join(cfg.DataDir, "recognition.json"))
	} else {
		recognitionBackend = database.NewRecognitionBackend(pool)
	}
	if err != nil {
		return nil, fmt.Errorf("load community identities: %w", err)
	}
	recognitionService := &recognition.Service{Backend: recognitionBackend, Users: adminAuth, Content: repository, Currencies: currencyCatalog}
	if err = recognitionService.Ready(ctx); err != nil {
		return nil, fmt.Errorf("validate article tips: %w", err)
	}
	server.SetRecognitionService(recognitionService)
	var commerceBackend commerce.Backend
	if pool == nil {
		commerceBackend, err = commerce.NewFileBackend(filepath.Join(cfg.DataDir, "commerce.json"))
	} else {
		commerceBackend = database.NewCommerceBackend(pool)
	}
	if err != nil {
		return nil, fmt.Errorf("load payment orders: %w", err)
	}
	commerceState, err := commerceBackend.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("load payment state: %w", err)
	}
	keyPath := filepath.Join(cfg.DataDir, "payment-secrets.key")
	if _, keyErr := os.Stat(keyPath); errors.Is(keyErr, os.ErrNotExist) && (len(commerceState.Settings.Channels) > 0 || len(commerceState.Orders) > 0) {
		return nil, errors.New("payment encryption key is missing; restore payment-secrets.key with the payment data")
	}
	commerceVault, err := commerce.OpenVault(keyPath)
	if err != nil {
		return nil, fmt.Errorf("open payment encryption key: %w", err)
	}
	commerceService := &commerce.Service{Backend: commerceBackend, Vault: commerceVault, Currencies: currencyCatalog, Members: progressionStore, Users: adminAuth, PublicURL: cfg.PublicURL}
	if err = commerceService.Ready(ctx); err != nil {
		return nil, fmt.Errorf("validate payment state: %w", err)
	}
	server.SetCommerceService(commerceService)
	var resourceBackend resources.Backend
	if pool == nil {
		resourceBackend, err = resources.NewFileBackend(filepath.Join(cfg.DataDir, "resources.json"))
	} else {
		resourceBackend = database.NewResourceBackend(pool)
	}
	if err != nil {
		return nil, fmt.Errorf("load article resources: %w", err)
	}
	server.SetResourceStore(&resources.Store{Backend: resourceBackend})
	var shopBackend shop.Backend
	if pool == nil {
		shopBackend, err = shop.NewFileBackend(filepath.Join(cfg.DataDir, "shop.json"))
	} else {
		shopBackend = database.NewShopBackend(pool)
	}
	if err != nil {
		return nil, fmt.Errorf("load shop orders: %w", err)
	}
	shopService := &shop.Service{Backend: shopBackend, Currencies: currencyCatalog, Members: progressionStore, Users: adminAuth}
	if err = shopService.Ready(ctx); err != nil {
		return nil, fmt.Errorf("validate shop state: %w", err)
	}
	if err = shopService.ValidateWallet(ctx); err != nil {
		return nil, fmt.Errorf("validate shop wallet receipts: %w", err)
	}
	if err = shopService.RecoverWallet(ctx); err != nil {
		return nil, fmt.Errorf("recover shop wallet receipts: %w", err)
	}
	commerceService.SettleShop = func(ctx context.Context, receipt commerce.ShopSettlement) error {
		_, err := shopService.MarkPaid(ctx, shop.PaymentReceipt{OrderID: receipt.OrderID, UserID: receipt.UserID, Index: receipt.Installment - 1, Reference: receipt.PaymentID, Amount: receipt.Amount, Currency: receipt.Currency})
		return err
	}
	commerceService.ReviewShop = func(ctx context.Context, receipt commerce.ShopSettlement) error {
		return shopService.ReviewPayment(ctx, shop.PaymentReceipt{OrderID: receipt.OrderID, UserID: receipt.UserID, Index: receipt.Installment - 1, Reference: receipt.PaymentID, Amount: receipt.Amount, Currency: receipt.Currency})
	}
	server.SetShopService(shopService)
	collector := analytics.NewCollector(statistics)
	// The server binds both collector and store to the site's live calendar;
	// each flush/report captures that calendar once for the entire operation.
	server.SetAnalytics(collector)
	notificationService := &notification.Service{Store: notices, Users: adminAuth, Content: repository, Comments: comments, Engagement: engagements, Sources: activitySources, Settings: func() notification.Settings {
		settings := siteSettings.Snapshot().Settings
		return notification.Settings{Title: settings.Identity.Title, PublicURL: cfg.PublicURL, Timezone: settings.Identity.Timezone}
	}}
	if cfg.SMTP.Enabled() {
		notificationService.Sender, _ = maildelivery.NewSMTP(cfg.SMTP)
	}
	server.SetNotificationService(notificationService)
	server.SetPluginServices(market, origin)
	server.SetObjectStorage(storage)
	server.SetCDN(cdnService)
	wechatService := &wechat.Service{ModuleEnabled: func() bool { return siteSettings.ModuleEnabled("wechat") }, Store: wechatStore, Client: wechat.NewClient(), Content: repository, Media: mediaLibrary, PublicURL: cfg.PublicURL}
	server.SetWeChatService(wechatService)
	currencyRewards := &virtualcurrency.Service{Catalog: currencyCatalog, Users: adminAuth, Content: repository}
	notificationService.BeforeAck = func(ctx context.Context, event activity.Event) error {
		// Attempt every observer; any failure keeps the durable event for retry.
		return errors.Join(currencyRewards.Observe(ctx, event), progressionService.Observe(ctx, event), wechatService.Observe(ctx, event), cdnService.Observe(ctx, repository, event), server.ObservePluginActivity(ctx, event))
	}
	updates, err := update.New(update.Directory(cfg.DataDir), version.Version, cfg.ThemesDir, os.Getenv("NEWPRESS_UPDATE_LAUNCHER") == update.LauncherGeneration, update.DatabaseBackup(cfg.DatabaseURL != "", cfg.PublicURL))
	if err != nil {
		return nil, fmt.Errorf("initialize updates: %w", err)
	}
	server.SetUpdates(updates)
	var extensionOptions []extensionupdate.Option
	if source, configured := plugin.EnvironmentThemeSource(); configured {
		themeMarket, err := plugin.NewThemeMarket(cfg.ThemesDir, source)
		if err != nil {
			updates.Close()
			return nil, fmt.Errorf("initialize official theme market: %w", err)
		}
		extensionOptions = append(extensionOptions, extensionupdate.WithThemeMarket(themeMarket))
	}
	extensions, err := extensionupdate.New(filepath.Join(cfg.DataDir, "extension-updates.json"), market, plugins, themes, extensionOptions...)
	if err != nil {
		updates.Close()
		return nil, fmt.Errorf("initialize extension updates: %w", err)
	}
	server.SetExtensionUpdates(extensions)
	initialized = true
	return &App{
		config:        cfg,
		pool:          pool,
		server:        server,
		sessions:      sessions,
		themes:        themes,
		plugins:       plugins,
		pluginWorker:  pluginWorker,
		webhookWorker: webhookWorker,
		commentOrigin: origin,
		objectStorage: storage,
		cdn:           cdnService,
		media:         mediaLibrary,
		comments:      comments,
		navigation:    navigationStore,
		settings:      siteSettings,
		locks:         locks,
		updates:       updates,
		extensions:    extensions,
		notifications: notificationService,
		analytics:     collector,
		aiTelemetry:   aiTelemetry,
		wechat:        wechatService,
		commerce:      commerceService,
		shop:          shopService,
		circles:       circles,
		circleFiles:   circleFiles,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	if a.circles != nil && a.circleFiles != nil {
		if err := a.circleFiles.Reconcile(ctx, a.circles); err != nil {
			return fmt.Errorf("recover circle files: %w", err)
		}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	webhooksDone := make(chan struct{})
	go func() {
		defer close(webhooksDone)
		if a.webhookWorker != nil {
			_ = a.webhookWorker.Run(workerCtx)
		}
	}()
	pluginTasksDone := make(chan struct{})
	go func() {
		defer close(pluginTasksDone)
		if a.pluginWorker != nil {
			_ = a.pluginWorker.Run(workerCtx)
		}
	}()
	updateChecksDone := make(chan struct{})
	go func() { defer close(updateChecksDone); a.updates.RunChecks(workerCtx) }()
	done := make(chan struct{})
	go func() { defer close(done); a.notifications.Run(workerCtx) }()
	aiDone := make(chan struct{})
	go func() { defer close(aiDone); a.aiTelemetry.Run(workerCtx) }()
	writingDone := make(chan struct{})
	go func() { defer close(writingDone); a.server.RunAIWritingQueue(workerCtx) }()
	analyticsDone := make(chan struct{})
	go func() { defer close(analyticsDone); a.analytics.Run(workerCtx) }()
	wechatDone := make(chan struct{})
	go func() { defer close(wechatDone); a.wechat.Run(workerCtx) }()
	originDone := make(chan struct{})
	go func() { defer close(originDone); a.commentOrigin.Run(workerCtx) }()
	storageDone := make(chan struct{})
	go func() { defer close(storageDone); a.objectStorage.Run(workerCtx) }()
	cdnDone := make(chan struct{})
	go func() { defer close(cdnDone); a.cdn.Run(workerCtx) }()
	commerceDone := make(chan struct{})
	go func() { defer close(commerceDone); a.commerce.Run(workerCtx) }()
	shopDone := make(chan struct{})
	go func() { defer close(shopDone); a.shop.Run(workerCtx) }()
	err := a.server.Run(ctx)
	cancel()
	<-pluginTasksDone
	<-webhooksDone
	<-updateChecksDone
	<-done
	<-analyticsDone
	<-aiDone
	<-writingDone
	<-wechatDone
	<-originDone
	<-storageDone
	<-cdnDone
	<-commerceDone
	<-shopDone
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer flushCancel()
	if flushErr := a.analytics.Flush(flushCtx, time.Now()); flushErr != nil {
		slog.Warn("analytics shutdown flush failed", "error", flushErr)
	}
	if flushErr := a.aiTelemetry.Flush(time.Now()); flushErr != nil {
		slog.Warn("AI usage shutdown flush failed")
	}
	return err
}

func (a *App) Close() {
	if sessions, ok := a.sessions.(interface{ Flush(context.Context) error }); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := sessions.Flush(ctx); err != nil {
			slog.Warn("session shutdown flush failed")
		}
		cancel()
	}
	if a.cdn != nil {
		a.cdn.Close()
	}
	if a.objectStorage != nil {
		a.objectStorage.Close()
	}
	if a.extensions != nil {
		a.extensions.Close()
	}
	if a.updates != nil {
		a.updates.Close()
	}
	if a.pool != nil {
		a.pool.Close()
	}
	_ = a.locks.Close()
}
