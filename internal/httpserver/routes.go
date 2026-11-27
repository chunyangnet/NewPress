package httpserver

import (
	"net/http"
)

func (s *Server) routes() http.Handler {
	s.moduleRoutes = nil
	mux := &moduleMux{ServeMux: http.NewServeMux(), server: s}
	s.registerModuleRoutes(mux)
	s.registerPermalinkRoutes(mux)
	s.registerLayoutRoutes(mux)
	s.registerAnnouncementRoutes(mux)
	s.registerSeriesRoutes(mux)
	s.registerSnippetRoutes(mux)
	s.registerAnalyticsRoutes(mux)
	s.registerAIUsageRoutes(mux)
	s.registerAIWorkbenchRoutes(mux)
	s.registerRegistrationCodeRoutes(mux)
	s.registerSystemStatusRoutes(mux)
	s.registerFeedbackRoutes(mux)
	s.registerLinkPageRoutes(mux)
	s.registerPluginRoutes(mux)
	s.registerObjectStorageRoutes(mux)
	s.registerCDNRoutes(mux)
	s.registerExtensionUpdateRoutes(mux)
	s.registerCircleRoutes(mux)
	s.registerQuestionRoutes(mux)
	s.registerSocialRoutes(mux)
	s.registerArticleComponentRoutes(mux)
	s.registerResourceRoutes(mux)
	s.registerCurrencyRoutes(mux)
	s.registerProgressionRoutes(mux)
	s.registerRecognitionRoutes(mux)
	s.registerCommerceRoutes(mux)
	s.registerShopRoutes(mux)
	s.registerSetupRoutes(mux)
	s.registerDeviceMockupRoutes(mux)
	mux.HandleFunc("GET /account/notifications/unsubscribe/", s.unsubscribeNotifications)
	mux.HandleFunc("POST /account/notifications/unsubscribe/", s.unsubscribeNotifications)
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /api/v1/system", s.systemAPI)
	mux.HandleFunc("GET /api/v1/posts", s.postsAPI)
	mux.HandleFunc("GET /api/v1/posts/{slug}/comments", s.publicCommentsAPI)
	mux.HandleFunc("POST /api/v1/posts/{slug}/comments", s.publicCommentCreateAPI)
	mux.HandleFunc("POST /api/v1/posts/{slug}/comments/receipt", s.publicCommentReceiptAPI)
	mux.HandleFunc("GET /api/v1/plugins", s.pluginsAPI)
	mux.HandleFunc("GET /api/v1/themes", s.themesAPI)
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"} {
		mux.HandleFunc(method+" /api/v1/translations", s.translationsAPI)
	}
	mux.HandleFunc("GET /api/v1/admin/session", s.adminSessionAPI)
	mux.HandleFunc("GET /api/v1/admin/sessions", s.adminSessionsAPI)
	mux.HandleFunc("DELETE /api/v1/admin/sessions/{session_id}", s.adminSessionDeleteAPI)
	mux.HandleFunc("POST /api/v1/admin/sessions/revoke-others", s.adminSessionsRevokeOthersAPI)
	mux.HandleFunc("GET /api/v1/admin/contents", s.adminContentsAPI)
	mux.HandleFunc("POST /api/v1/admin/contents/slug-suggestion", s.adminSlugSuggestion)
	mux.HandleFunc("GET /api/v1/admin/article-widgets", s.adminArticleWidgetsAPI)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/article-widgets", s.adminAPIMethodNotAllowed("GET"))
	}
	mux.HandleFunc("GET /api/v1/admin/media", s.adminMediaListAPI)
	mux.HandleFunc("POST /api/v1/admin/media", s.adminMediaUploadAPI)
	mux.HandleFunc("GET /api/v1/admin/media/{id}", s.adminMediaItemAPI)
	mux.HandleFunc("PATCH /api/v1/admin/media/{id}", s.adminMediaPatchAPI)
	mux.HandleFunc("POST /api/v1/admin/media/{id}/process", s.adminMediaProcessAPI)
	for _, method := range []string{"GET", "HEAD", "PUT", "PATCH", "DELETE"} {
		mux.HandleFunc(method+" /api/v1/admin/media/{id}/process", s.adminAPIMethodNotAllowed("POST"))
	}
	mux.HandleFunc("DELETE /api/v1/admin/media/{id}", s.adminMediaLifecycleAPI("delete"))
	for _, action := range []string{"trash", "restore"} {
		mux.HandleFunc("POST /api/v1/admin/media/{id}/"+action, s.adminMediaLifecycleAPI(action))
		for _, method := range []string{"GET", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			mux.HandleFunc(method+" /api/v1/admin/media/{id}/"+action, s.adminAPIMethodNotAllowed("POST"))
		}
	}
	mux.HandleFunc("GET /api/v1/admin/media/{id}/references", s.adminMediaReferencesAPI)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/media/{id}/references", s.adminAPIMethodNotAllowed("GET"))
	}
	for _, method := range []string{"PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/media", s.adminAPIMethodNotAllowed("GET, POST"))
	}
	for _, method := range []string{"POST", "PUT", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/media/{id}", s.adminAPIMethodNotAllowed("GET, PATCH, DELETE"))
	}
	mux.HandleFunc("POST /api/v1/admin/contents", s.adminContentCreateAPI)
	mux.HandleFunc("GET /api/v1/admin/contents/{id}", s.adminContentAPI)
	mux.HandleFunc("PATCH /api/v1/admin/contents/{id}", s.adminContentUpdateAPI)
	mux.HandleFunc("DELETE /api/v1/admin/contents/{id}", s.adminContentDeleteAPI)
	mux.HandleFunc("GET /api/v1/admin/contents/{id}/revisions", s.adminContentRevisionsAPI)
	mux.HandleFunc("POST /api/v1/admin/contents/{id}/revisions/{revision_id}/restore", s.adminContentRestoreRevisionAPI)
	mux.HandleFunc("GET /api/v1/admin/comments", s.adminCommentsAPI)
	mux.HandleFunc("GET /api/v1/admin/comments/{id}", s.adminCommentAPI)
	mux.HandleFunc("PATCH /api/v1/admin/comments/{id}", s.adminCommentUpdateAPI)
	mux.HandleFunc("PUT /api/v1/admin/comments/{id}", s.adminCommentEditAPI)
	mux.HandleFunc("DELETE /api/v1/admin/comments/{id}", s.adminCommentDeleteAPI)
	mux.HandleFunc("POST /api/v1/admin/comments/{id}/replies", s.adminCommentReplyAPI)
	mux.HandleFunc("POST /api/v1/admin/comments/{id}/reply-draft", s.adminCommentReplyDraftAPI)
	mux.HandleFunc("GET /api/v1/admin/comments/{id}/revisions", s.adminCommentRevisionsAPI)
	mux.HandleFunc("POST /api/v1/admin/comments/{id}/revisions/{revision_version}/restore", s.adminCommentRestoreRevisionAPI)
	mux.HandleFunc("GET /api/v1/admin/audit-events", s.adminAuditEventsAPI)
	mux.HandleFunc("GET /api/v1/navigation/{location}", s.publicNavigationLocationAPI)
	mux.HandleFunc("GET /api/v1/admin/navigation/menus", s.adminNavigationMenusAPI)
	mux.HandleFunc("POST /api/v1/admin/navigation/menus", s.adminNavigationMenuCreateAPI)
	mux.HandleFunc("GET /api/v1/admin/navigation/menus/{id}", s.adminNavigationMenuAPI)
	mux.HandleFunc("PATCH /api/v1/admin/navigation/menus/{id}", s.adminNavigationMenuUpdateAPI)
	mux.HandleFunc("DELETE /api/v1/admin/navigation/menus/{id}", s.adminNavigationMenuDeleteAPI)
	mux.HandleFunc("PUT /api/v1/admin/navigation/menus/{id}/items", s.adminNavigationMenuItemsAPI)
	mux.HandleFunc("GET /api/v1/admin/navigation/sources", s.adminNavigationSourcesAPI)
	for _, pattern := range []string{
		"POST /api/v1/navigation/{location}", "PUT /api/v1/navigation/{location}",
		"PATCH /api/v1/navigation/{location}", "DELETE /api/v1/navigation/{location}",
		"OPTIONS /api/v1/navigation/{location}",
	} {
		mux.HandleFunc(pattern, s.publicNavigationAPIMethodNotAllowed("GET"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/session", "PUT /api/v1/admin/session", "PATCH /api/v1/admin/session",
		"DELETE /api/v1/admin/session", "OPTIONS /api/v1/admin/session",
	} {
		mux.HandleFunc(pattern, s.adminAPIMethodNotAllowed("GET"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/sessions", "PUT /api/v1/admin/sessions", "PATCH /api/v1/admin/sessions",
		"DELETE /api/v1/admin/sessions", "OPTIONS /api/v1/admin/sessions",
	} {
		mux.HandleFunc(pattern, s.adminAPIMethodNotAllowed("GET"))
	}
	for _, pattern := range []string{
		"GET /api/v1/admin/sessions/revoke-others", "PUT /api/v1/admin/sessions/revoke-others",
		"PATCH /api/v1/admin/sessions/revoke-others", "DELETE /api/v1/admin/sessions/revoke-others",
		"OPTIONS /api/v1/admin/sessions/revoke-others",
	} {
		mux.HandleFunc(pattern, s.adminAPIMethodNotAllowed("POST"))
	}
	for _, pattern := range []string{
		"GET /api/v1/admin/sessions/{session_id}", "POST /api/v1/admin/sessions/{session_id}",
		"PUT /api/v1/admin/sessions/{session_id}", "PATCH /api/v1/admin/sessions/{session_id}",
		"OPTIONS /api/v1/admin/sessions/{session_id}",
	} {
		mux.HandleFunc(pattern, s.adminAPIMethodNotAllowed("DELETE"))
	}
	for _, pattern := range []string{
		"PUT /api/v1/admin/contents", "PATCH /api/v1/admin/contents", "DELETE /api/v1/admin/contents",
		"OPTIONS /api/v1/admin/contents",
	} {
		mux.HandleFunc(pattern, s.adminContentAPIMethodNotAllowed("GET, POST"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/contents/{id}", "PUT /api/v1/admin/contents/{id}",
		"OPTIONS /api/v1/admin/contents/{id}",
	} {
		mux.HandleFunc(pattern, s.adminContentAPIMethodNotAllowed("GET, PATCH, DELETE"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/contents/{id}/revisions", "PUT /api/v1/admin/contents/{id}/revisions",
		"PATCH /api/v1/admin/contents/{id}/revisions", "DELETE /api/v1/admin/contents/{id}/revisions",
		"OPTIONS /api/v1/admin/contents/{id}/revisions",
	} {
		mux.HandleFunc(pattern, s.adminContentAPIMethodNotAllowed("GET"))
	}
	for _, pattern := range []string{
		"GET /api/v1/admin/contents/{id}/revisions/{revision_id}/restore",
		"PUT /api/v1/admin/contents/{id}/revisions/{revision_id}/restore",
		"PATCH /api/v1/admin/contents/{id}/revisions/{revision_id}/restore",
		"DELETE /api/v1/admin/contents/{id}/revisions/{revision_id}/restore",
		"OPTIONS /api/v1/admin/contents/{id}/revisions/{revision_id}/restore",
	} {
		mux.HandleFunc(pattern, s.adminContentAPIMethodNotAllowed("POST"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/audit-events", "PUT /api/v1/admin/audit-events", "PATCH /api/v1/admin/audit-events",
		"DELETE /api/v1/admin/audit-events", "OPTIONS /api/v1/admin/audit-events",
	} {
		mux.HandleFunc(pattern, s.adminAPIMethodNotAllowed("GET"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/comments", "PUT /api/v1/admin/comments", "PATCH /api/v1/admin/comments",
		"DELETE /api/v1/admin/comments", "OPTIONS /api/v1/admin/comments",
	} {
		mux.HandleFunc(pattern, s.adminCommentsAPIMethodNotAllowed("GET"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/comments/{id}", "OPTIONS /api/v1/admin/comments/{id}",
	} {
		mux.HandleFunc(pattern, s.adminCommentsAPIMethodNotAllowed("GET, PATCH, PUT, DELETE"))
	}
	for _, pattern := range []string{
		"GET /api/v1/admin/comments/{id}/replies", "PUT /api/v1/admin/comments/{id}/replies",
		"PATCH /api/v1/admin/comments/{id}/replies", "DELETE /api/v1/admin/comments/{id}/replies",
		"OPTIONS /api/v1/admin/comments/{id}/replies",
	} {
		mux.HandleFunc(pattern, s.adminCommentsAPIMethodNotAllowed("POST"))
	}
	for _, method := range []string{"GET", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/comments/{id}/reply-draft", s.adminCommentsAPIMethodNotAllowed("POST"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/comments/{id}/revisions", "PUT /api/v1/admin/comments/{id}/revisions",
		"PATCH /api/v1/admin/comments/{id}/revisions", "DELETE /api/v1/admin/comments/{id}/revisions",
		"OPTIONS /api/v1/admin/comments/{id}/revisions",
	} {
		mux.HandleFunc(pattern, s.adminCommentsAPIMethodNotAllowed("GET"))
	}
	for _, pattern := range []string{
		"GET /api/v1/admin/comments/{id}/revisions/{revision_version}/restore",
		"PUT /api/v1/admin/comments/{id}/revisions/{revision_version}/restore",
		"PATCH /api/v1/admin/comments/{id}/revisions/{revision_version}/restore",
		"DELETE /api/v1/admin/comments/{id}/revisions/{revision_version}/restore",
		"OPTIONS /api/v1/admin/comments/{id}/revisions/{revision_version}/restore",
	} {
		mux.HandleFunc(pattern, s.adminCommentsAPIMethodNotAllowed("POST"))
	}
	for _, pattern := range []string{
		"PUT /api/v1/admin/navigation/menus", "PATCH /api/v1/admin/navigation/menus",
		"DELETE /api/v1/admin/navigation/menus", "OPTIONS /api/v1/admin/navigation/menus",
	} {
		mux.HandleFunc(pattern, s.adminNavigationAPIMethodNotAllowed("GET, POST"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/navigation/menus/{id}", "PUT /api/v1/admin/navigation/menus/{id}",
		"OPTIONS /api/v1/admin/navigation/menus/{id}",
	} {
		mux.HandleFunc(pattern, s.adminNavigationAPIMethodNotAllowed("GET, PATCH, DELETE"))
	}
	for _, pattern := range []string{
		"GET /api/v1/admin/navigation/menus/{id}/items", "POST /api/v1/admin/navigation/menus/{id}/items",
		"PATCH /api/v1/admin/navigation/menus/{id}/items", "DELETE /api/v1/admin/navigation/menus/{id}/items",
		"OPTIONS /api/v1/admin/navigation/menus/{id}/items",
	} {
		mux.HandleFunc(pattern, s.adminNavigationAPIMethodNotAllowed("PUT"))
	}
	for _, pattern := range []string{
		"POST /api/v1/admin/navigation/sources", "PUT /api/v1/admin/navigation/sources",
		"PATCH /api/v1/admin/navigation/sources", "DELETE /api/v1/admin/navigation/sources",
		"OPTIONS /api/v1/admin/navigation/sources",
	} {
		mux.HandleFunc(pattern, s.adminNavigationAPIMethodNotAllowed("GET"))
	}
	mux.HandleFunc("GET /theme-settings.css", s.themeSettingsCSS)
	mux.HandleFunc("GET /navigation.css", s.navigationCSS)
	mux.HandleFunc("GET /robots.txt", s.robotsTXT)
	mux.HandleFunc("GET /sitemap.xml", s.sitemapXML)
	mux.HandleFunc("GET /feed.xml", s.publicFeed)
	mux.HandleFunc("GET /.well-known/security.txt", s.securityTXT)
	mux.HandleFunc("GET /posts/{slug}", s.single)
	mux.HandleFunc("POST /posts/{slug}/comments", s.publicCommentCreate)
	mux.HandleFunc("GET /pages/{slug}", s.page)
	mux.HandleFunc("GET /categories/{slug}/{$}", s.categoryArchive)
	mux.HandleFunc("GET /tags/{slug}/{$}", s.tagArchive)
	s.registerFlashRoutes(mux)
	mux.HandleFunc("GET /explore/", s.explore)
	mux.HandleFunc("GET /community/", s.community)
	mux.HandleFunc("GET /account/", s.accountPage)
	mux.HandleFunc("GET /account/access/", s.accountAccessPage)
	mux.HandleFunc("GET /auth/{provider}/start", s.oauthStart)
	mux.HandleFunc("GET /auth/{provider}/callback", s.oauthCallback)
	mux.HandleFunc("GET /authors/{slug}/", s.author)
	mux.HandleFunc("GET /admin/", s.admin)
	mux.HandleFunc("GET /admin/login/", s.adminLogin)
	mux.HandleFunc("POST /admin/login/", s.adminLoginSubmit)
	mux.HandleFunc("POST /admin/logout/", s.adminLogout)
	mux.HandleFunc("GET /admin/appearance/", s.adminAppearance)
	mux.HandleFunc("GET /admin/appearance/preview/{$}", s.appearancePreview)
	mux.HandleFunc("POST /admin/appearance/preview/{$}", s.appearancePreview)
	mux.HandleFunc("POST /admin/appearance/", s.adminAppearanceSubmit)
	mux.HandleFunc("GET /admin/appearance/export/", s.adminAppearanceExport)
	mux.HandleFunc("GET /admin/settings/", s.adminSiteSettings)
	mux.HandleFunc("GET /site-integrations.js", s.integrationScript)
	mux.HandleFunc("POST /admin/settings/", s.adminSiteSettingsSubmit)
	mux.HandleFunc("GET /admin/settings/export/", s.adminSiteSettingsExport)
	mux.HandleFunc("GET /admin/tools/{$}", s.adminTools)
	mux.HandleFunc("GET /admin/seo/{$}", s.adminSEO)
	mux.HandleFunc("GET /admin/import/{$}", s.adminContentImport)
	mux.HandleFunc("GET /admin/wechat/{$}", s.adminWeChat)
	mux.HandleFunc("GET /api/v1/admin/wechat", s.adminWeChatRead)
	mux.HandleFunc("POST /api/v1/admin/wechat/config", s.adminWeChatConfig)
	mux.HandleFunc("POST /api/v1/admin/wechat/test", s.adminWeChatTest)
	mux.HandleFunc("POST /api/v1/admin/wechat/sync", s.adminWeChatSync)
	mux.HandleFunc("POST /api/v1/admin/wechat/sync/check", s.adminWeChatSyncCheck)
	mux.HandleFunc("POST /api/v1/admin/wechat/jobs/{id}", s.adminWeChatJob)
	mux.HandleFunc("POST /api/v1/admin/wechat/collect/preview", s.adminWeChatCollect)
	mux.HandleFunc("POST /api/v1/admin/wechat/collect/apply", s.adminWeChatCollect)
	mux.HandleFunc("POST /api/v1/admin/imports/preview", s.adminContentImportAPI)
	mux.HandleFunc("POST /api/v1/admin/imports/apply", s.adminContentImportAPI)
	mux.HandleFunc("POST /admin/seo/{$}", s.adminSEOSubmit)
	mux.HandleFunc("GET /api/v1/admin/redirects", s.adminRedirectListAPI)
	mux.HandleFunc("POST /api/v1/admin/redirects", s.adminRedirectSaveAPI)
	mux.HandleFunc("GET /api/v1/admin/redirects/{id}", s.adminRedirectItemAPI)
	mux.HandleFunc("PUT /api/v1/admin/redirects/{id}", s.adminRedirectSaveAPI)
	mux.HandleFunc("DELETE /api/v1/admin/redirects/{id}", s.adminRedirectDeleteAPI)
	mux.HandleFunc("GET /api/v1/admin/seo/sources", s.adminSEOSourcesAPI)
	mux.HandleFunc("GET /api/v1/admin/contents/{id}/links", s.adminContentLinksAPI)
	mux.HandleFunc("GET /admin/updates/{$}", s.adminUpdates)
	mux.HandleFunc("GET /api/v1/admin/updates", s.adminUpdateStatus)
	mux.HandleFunc("PUT /api/v1/admin/updates/source", s.adminUpdateConfigure)
	mux.HandleFunc("PUT /api/v1/admin/updates/policy", s.adminUpdatePolicy)
	mux.HandleFunc("POST /api/v1/admin/updates/check", s.adminUpdateCheck)
	mux.HandleFunc("POST /api/v1/admin/updates/start", s.adminUpdateStart)
	mux.HandleFunc("POST /api/v1/admin/updates/cancel", s.adminUpdateCancel)
	mux.HandleFunc("GET /api/v1/admin/updates/progress", s.adminUpdateProgress)
	mux.HandleFunc("GET /admin/tools/support-report.json", s.adminToolsSupportReport)
	mux.HandleFunc("GET /admin/tools/audit-events.json", s.adminToolsAuditExport)
	mux.HandleFunc("GET /admin/tools/navigation-export.json", s.adminToolsNavigationExport)
	mux.HandleFunc("POST /admin/tools/navigation-import/", s.adminToolsNavigationImport)
	mux.HandleFunc("GET /admin/media/{$}", s.adminMedia)
	mux.HandleFunc("POST /admin/media/upload/{$}", s.adminMediaUpload)
	mux.HandleFunc("GET /admin/media/{id}/edit/{$}", s.adminMediaEdit)
	mux.HandleFunc("GET /admin/media/{id}/preview/{$}", s.adminMediaPreview)
	mux.HandleFunc("POST /admin/media/{id}/edit/{$}", s.adminMediaUpdate)
	mux.HandleFunc("POST /admin/media/{id}/process/{$}", s.adminMediaProcess)
	mux.HandleFunc("GET /admin/content/{$}", s.adminContent)
	mux.HandleFunc("POST /admin/content/bulk/{$}", s.adminContentBulk)
	mux.HandleFunc("GET /admin/content/new/{$}", s.adminContentNew)
	mux.HandleFunc("POST /admin/content/new/{$}", s.adminContentCreate)
	mux.HandleFunc("GET /admin/content/{id}/edit/{$}", s.adminContentEdit)
	mux.HandleFunc("POST /admin/content/{id}/edit/{$}", s.adminContentUpdate)
	mux.HandleFunc("GET /admin/content/{id}/autosave/{$}", s.adminContentAutosaveStatus)
	mux.HandleFunc("POST /admin/content/{id}/autosave/{$}", s.adminContentAutosaveSubmit)
	mux.HandleFunc("GET /admin/content/{id}/preview/{$}", s.adminContentPreview)
	mux.HandleFunc("GET /admin/content/{id}/revisions/compare/{$}", s.adminContentRevisionCompare)
	mux.HandleFunc("POST /admin/quick-draft/{$}", s.adminQuickDraftCreate)
	mux.HandleFunc("GET /api/v1/admin/ai/prompts", s.adminAIPrompts)
	mux.HandleFunc("GET /api/v1/admin/ai/connections", s.adminAIConnections)
	mux.HandleFunc("GET /api/v1/admin/ai/settings", s.adminAISettings)
	mux.HandleFunc("GET /api/v1/admin/access/settings", s.adminAccessSettings)
	mux.HandleFunc("GET /api/v1/admin/access/check", s.adminRegistrationCheck)
	mux.HandleFunc("GET /api/v1/social/referrals", s.socialReferrals)
	mux.HandleFunc("POST /api/v1/social/referrals", s.socialReferrals)
	mux.HandleFunc("POST /api/v1/admin/access/settings", s.adminAccessSettingsSave)
	mux.HandleFunc("POST /api/v1/admin/access/sms-test", s.adminAccessSMSTest)
	mux.HandleFunc("GET /api/v1/admin/verification/settings", s.adminVerificationSettings)
	mux.HandleFunc("POST /api/v1/admin/verification/settings", s.adminVerificationSave)
	mux.HandleFunc("GET /api/v1/verification/config", s.publicVerificationConfig)
	mux.HandleFunc("POST /api/v1/verification/challenge", s.verificationChallenge)
	mux.HandleFunc("POST /api/v1/verification/verify", s.verificationVerify)
	mux.HandleFunc("POST /api/v1/admin/ai/settings", s.adminAISettingsSave)
	for route, handler := range map[string]http.HandlerFunc{
		"models": s.adminAIModels, "generate": s.adminAIGenerate, "prepare": s.adminAIPrepare,
		"images": s.adminAIImages, "balance": s.adminAIBalance,
	} {
		mux.HandleFunc("POST /api/v1/admin/ai/"+route, handler)
		for _, method := range []string{"GET", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			mux.HandleFunc(method+" /api/v1/admin/ai/"+route, s.adminAPIMethodNotAllowed("POST"))
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/ai/prompts", s.adminAPIMethodNotAllowed("GET"))
		mux.HandleFunc(method+" /api/v1/admin/ai/connections", s.adminAPIMethodNotAllowed("GET"))
	}
	for _, method := range []string{"PUT", "PATCH", "DELETE", "OPTIONS"} {
		mux.HandleFunc(method+" /api/v1/admin/ai/settings", s.adminAPIMethodNotAllowed("GET, POST"))
	}
	mux.HandleFunc("GET /admin/taxonomy/{$}", s.adminTaxonomy)
	mux.HandleFunc("POST /admin/taxonomy/{$}", s.adminTaxonomySubmit)
	mux.HandleFunc("GET /admin/users/{$}", s.adminUsers)
	mux.HandleFunc("POST /admin/users/{$}", s.adminUsersSubmit)
	mux.HandleFunc("GET /admin/sessions/{$}", s.adminSessions)
	mux.HandleFunc("POST /admin/sessions/{$}", s.adminSessionsSubmit)
	mux.HandleFunc("GET /admin/comments/{$}", s.adminComments)
	mux.HandleFunc("POST /admin/comments/{$}", s.adminCommentsSubmit)
	mux.HandleFunc("POST /admin/comments/bulk/{$}", s.adminCommentsBulkSubmit)
	mux.HandleFunc("GET /admin/comments/{id}/edit/{$}", s.adminCommentEditor)
	mux.HandleFunc("POST /admin/comments/{id}/edit/{$}", s.adminCommentEditSubmit)
	mux.HandleFunc("POST /admin/comments/{id}/reply/{$}", s.adminCommentReplySubmit)
	mux.HandleFunc("POST /admin/comments/{id}/revisions/{revision_version}/restore/{$}", s.adminCommentRevisionRestoreSubmit)
	mux.HandleFunc("GET /admin/navigation/{$}", s.adminNavigation)
	mux.HandleFunc("POST /admin/navigation/{$}", s.adminNavigationSubmit)
	mux.HandleFunc("GET /media/{id}/{filename}", s.mediaAsset)
	mux.HandleFunc("GET /favicon.ico", s.favicon)
	mux.Handle("GET /theme-assets/", s.themeAssets())
	mux.HandleFunc("GET /core-assets/", s.coreAssets)
	mux.HandleFunc("GET /", s.home)

	return recoverer(requestID(securityHeaders(s.verificationHeaders(accessLog(s.captureSiteTime(s.maintenanceMode(s.sessionState(s.observeAnalytics(mux)))))))))
}
