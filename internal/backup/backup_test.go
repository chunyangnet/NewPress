package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/analytics"
	"newpress.local/newpress/internal/app"
	"newpress.local/newpress/internal/audit"
	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/config"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/dirlock"
	"newpress.local/newpress/internal/engagement"
	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/messaging"
	"newpress.local/newpress/internal/notification"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/site"
	"newpress.local/newpress/internal/theme"
	"newpress.local/newpress/internal/virtualcurrency"
	"newpress.local/newpress/internal/wechat"
)

type fixture struct {
	cfg     config.Config
	parent  string
	post    content.Post
	message messaging.Message
	media   media.Item
	trash   media.Item
}

func fixtureSite(t *testing.T, populated bool) fixture {
	t.Helper()
	parent := t.TempDir()
	cfg := config.Config{
		DataDir: filepath.Join(parent, "site", "data"), ThemesDir: filepath.Join(parent, "site", "themes"),
		PluginsDir: filepath.Join(parent, "site", "plugins"), ActiveTheme: "backup-test", SiteTitle: "备份验收",
		SiteTagline: "保持完整关系", PublicURL: "http://localhost:8080", SeedDemo: populated,
		AdminUsername: "backup-admin", AdminEmail: "backup@example.test", AdminDisplayName: "备份管理员", AdminLocale: "zh-CN",
	}
	files := map[string]string{
		"theme.json":            `{"schema":1,"slug":"backup-test","name":"Backup test","version":"1.0.0","engine":"go-template/v1","view":"public/v1","templates":{"home":"home.html","single":"single.html"}}`,
		"templates/base.html":   `{{define "base"}}{{template "content" .}}{{end}}`,
		"templates/home.html":   `{{define "content"}}home{{end}}`,
		"templates/single.html": `{{define "content"}}single{{end}}`,
		"assets/site.css":       "body { color: #13272f; }\n",
	}
	for name, body := range files {
		writeFixtureFile(t, filepath.Join(cfg.ThemesDir, cfg.ActiveTheme, filepath.FromSlash(name)), []byte(body))
	}
	if populated {
		var err error
		cfg.AdminPassHash, err = auth.HashPassword("Backup fixture password 2026")
		if err != nil {
			t.Fatal(err)
		}
	}
	application, err := app.New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	application.Close()
	result := fixture{cfg: cfg, parent: parent}
	if !populated {
		return result
	}
	ledger, err := audit.Open(filepath.Join(cfg.DataDir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	users, err := auth.NewFileUserStore(filepath.Join(cfg.DataDir, "users.json"), ledger)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := users.CreateUser(t.Context(), auth.UserMutation{Actor: "user:1"}, auth.UserInput{
		Username: "backup-reader", Email: "reader@example.test", DisplayName: "读者", Locale: "zh-CN",
		Status: auth.UserStatusActive, RoleSlugs: []string{"subscriber"},
	}, cfg.AdminPassHash)
	if err != nil {
		t.Fatal(err)
	}
	_, invitation, err := auth.NewAccessToken(auth.AccessInvite, "backup-invited@example.test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = users.IssueAccess(t.Context(), invitation, "backup-admin"); err != nil {
		t.Fatal(err)
	}
	posts, err := content.NewFileRepository(filepath.Join(cfg.DataDir, "content.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	category, err := posts.CreateTerm(t.Context(), content.TermInput{Taxonomy: content.TaxonomyCategory, Slug: "backup-category", Name: "备份分类"})
	if err != nil {
		t.Fatal(err)
	}
	input := content.PostInput{Title: "备份文章", Slug: "backup-post", AuthorID: 1, AuthorName: "备份管理员", ContentSource: `<p>原始正文</p><aside data-callout="note"><p>备份提示块</p></aside><pre><code class="language-go">a &lt; b</code></pre>`, CategoryIDs: []int64{category.ID}, TagNames: []string{"恢复"}}
	created, err := posts.Submit(t.Context(), content.Submission{Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Action: "create", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	post, err := posts.GetByID(t.Context(), created.Receipt.PostID)
	if err != nil {
		t.Fatal(err)
	}
	input.Title, input.Status = "备份文章第二版", content.StatusPublish
	post, err = posts.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := post
	snapshot.Title = "尚未保存的恢复点"
	if _, err := posts.SaveAutosave(t.Context(), content.Autosave{
		PostID: post.ID, OwnerKey: "user:1", SourceVersion: post.Version, Snapshot: snapshot, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	result.post = post
	batch := content.ImportBatch{Source: "backup-import", Format: "markdown", Items: []content.ImportEntry{{Key: "note.md", Digest: strings.Repeat("a", 64), Input: content.PostInput{Title: "Imported note", Slug: "backup-import-note"}}}}
	plan, err := posts.PreviewImport(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = posts.ApplyImport(t.Context(), batch, plan.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := posts.SaveRedirect(t.Context(), 0, content.Mutation{}, content.RedirectInput{Path: "/legacy/backup-post.html", PostID: post.ID}); err != nil {
		t.Fatal(err)
	}
	statistics, err := analytics.NewFileStore(filepath.Join(cfg.DataDir, "analytics.json"))
	if err != nil {
		t.Fatal(err)
	}
	collector := analytics.NewCollector(statistics)
	collector.Record(analytics.Event{At: time.Now(), Source: "direct", PostID: post.ID})
	if err = collector.Flush(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	alerts, err := notification.NewFileStore(filepath.Join(cfg.DataDir, "notifications.json"))
	if err != nil {
		t.Fatal(err)
	}
	wxStore, err := wechat.NewFileStore(filepath.Join(cfg.DataDir, "wechat.json"))
	if err != nil {
		t.Fatal(err)
	}
	wx := &wechat.Service{Store: wxStore, Content: posts}
	wxConfig := wechat.Defaults()
	wxConfig.Enabled = true
	wxConfig.AppID = "wx0123456789abcdef"
	wxConfig.Secret = strings.Repeat("w", 32)
	if _, err = wx.SaveConfig(t.Context(), wxConfig, false); err != nil {
		t.Fatal(err)
	}
	if _, err = wx.Queue(t.Context(), post.ID, post.Version, "backup"); err != nil {
		t.Fatal(err)
	}
	prefs := notification.Defaults(reader.ID)
	prefs.Email = true
	if _, err = alerts.SavePreferences(t.Context(), prefs, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	event := activity.New(activity.Publication, post.ID, post.ID, post.AuthorID, post.Version, post.Status, post.UpdatedAt)
	if err = alerts.Project(t.Context(), event.Key, []notification.Delivery{{UserID: reader.ID, Kind: notification.Publication, Event: event}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := alerts.ClaimMail(t.Context(), time.Now()); err != nil || !ok {
		t.Fatal("backup mail job missing", err)
	}
	engagements, err := engagement.NewFileStore(filepath.Join(cfg.DataDir, "engagement.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := engagements.Set(t.Context(), engagement.Input{UserID: reader.ID, TargetID: post.ID, Kind: engagement.Bookmark, Collection: "恢复验收", Active: true}); err != nil {
		t.Fatal(err)
	}
	currency, err := virtualcurrency.NewFileStore(filepath.Join(cfg.DataDir, "currency.json"))
	if err != nil {
		t.Fatal(err)
	}
	currencyConfig := virtualcurrency.DefaultSettings()
	currencyConfig.Enabled, currencyConfig.AllowTransfers, currencyConfig.CheckInEnabled = true, true, true
	currencyConfig.RequireVerifiedEmail, currencyConfig.Precision = false, 2
	// Keep the fixture's two-decimal reward independent of main-points defaults.
	currencyConfig.CheckInReward = 10
	currencyConfig, err = currency.UpdateSettings(t.Context(), 1, currencyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = currency.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: 1, Delta: 12345, ExpectedVersion: currencyConfig.Version, RequestID: "backup-currency-issue", Reason: "备份前发放"}); err != nil {
		t.Fatal(err)
	}
	if _, err = currency.Transfer(t.Context(), virtualcurrency.TransferInput{UserID: 1, RecipientID: reader.ID, Amount: 250, ExpectedVersion: currencyConfig.Version, RequestID: "backup-currency-transfer"}); err != nil {
		t.Fatal(err)
	}
	if _, err = currency.Reward(t.Context(), virtualcurrency.RewardInput{UserID: reader.ID, Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: currencyConfig.Version, RequestID: "backup-currency-checkin"}); err != nil {
		t.Fatal(err)
	}
	currencyCatalog, err := virtualcurrency.NewFileCatalog(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = currencyCatalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "gold", Name: "金币", Symbol: "枚", Precision: 2, ActorID: 1, RequestID: "backup-create-gold-currency"}); err != nil {
		t.Fatal(err)
	}
	gold, err := currencyCatalog.Store(t.Context(), "gold")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = gold.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: reader.ID, Delta: 99, ExpectedVersion: 1, RequestID: "backup-currency-issue", Reason: "金币备份"}); err != nil {
		t.Fatal(err)
	}
	contentPayload, err := os.ReadFile(filepath.Join(cfg.DataDir, "content.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(cfg.DataDir, "content.json.bak"), contentPayload)
	if err = ledger.EnsureBaselinePath(audit.FactCommunity, filepath.Join(cfg.DataDir, "community.json")); err != nil {
		t.Fatal(err)
	}
	circles, err := community.NewFileStore(filepath.Join(cfg.DataDir, "community.json"), ledger)
	if err != nil {
		t.Fatal(err)
	}
	circleCommands := []community.Command{
		{Action: "create", Key: "backup-circle-create", Name: "恢复讨论", JoinPolicy: "open", Visibility: "members", OwnerID: 1},
		{Action: "join", Key: "backup-circle-join", Version: 1},
		{Action: "topic", Key: "backup-circle-topic", Version: 2, Title: "保留问题", Body: "QUESTION", Kind: "question"},
		{Action: "reply", Key: "backup-circle-reply", Version: 3, TopicID: 1, Body: "ANSWER"},
		{Action: "accept", Key: "backup-circle-accept", Version: 4, TopicID: 1, ReplyID: 1},
	}
	for i, command := range circleCommands {
		actor := community.Actor{ID: 1, Admin: true}
		if i == 1 || i == 3 {
			actor = community.Actor{ID: reader.ID}
		}
		if _, err = circles.Apply(t.Context(), "backup-circle", actor, command, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	circleFiles, err := community.NewFiles(filepath.Join(cfg.DataDir, "community-files"))
	if err != nil {
		t.Fatal(err)
	}
	file, receipt, err := circleFiles.Upload(t.Context(), circles, "backup-circle", community.Actor{ID: 1, Admin: true}, "backup-circle-file-key", 5, "private.txt", strings.NewReader("PRIVATE CIRCLE FILE"), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = circles.Apply(t.Context(), "backup-circle", community.Actor{ID: 1, Admin: true}, community.Command{Action: "edit_topic", Key: "backup-circle-rich-edit", Version: receipt.Version, TopicID: 1, Title: "保留问题", Body: "**QUESTION**", Format: "markdown", Attachments: []string{file.ID}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	messages, err := messaging.NewFileStore(filepath.Join(cfg.DataDir, "messages.json"))
	if err != nil {
		t.Fatal(err)
	}
	result.message, err = messages.Send(t.Context(), messaging.Input{SenderID: 1, RecipientID: reader.ID, Content: "恢复后还在的私信", Nonce: "backup-message-nonce-0001"})
	if err != nil {
		t.Fatal(err)
	}
	if err := messages.Read(t.Context(), reader.ID, 1, result.message.ID); err != nil {
		t.Fatal(err)
	}
	if err := messages.UpdateNotices(t.Context(), reader.ID, result.message.ID, "clear"); err != nil {
		t.Fatal(err)
	}
	library, err := media.NewManager(filepath.Join(cfg.DataDir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 120, G: 80, B: 42, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	result.media, err = library.Upload(t.Context(), &encoded, media.UploadInput{Filename: "fixture.png", Title: "图片", AltText: "恢复图片", UploadedBy: "user:1"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result.media, err = library.ProcessImage(t.Context(), result.media.ID, result.media.Version, media.ImageInput{FocalX: 75, FocalY: 25})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = posts.SaveSeries(t.Context(), 0, content.Mutation{}, content.SeriesInput{Kind: content.SeriesKindSequence, Status: content.StatusPublish, Title: "恢复阅读目录", Slug: "backup-series", Description: "保留封面和章节顺序", CoverURL: result.media.PublicURL, PostIDs: []int64{post.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := users.UpdateProfile(t.Context(), reader.ID, auth.UserMutation{ExpectedVersion: reader.Version, Actor: reader.Username}, auth.ProfileInput{DisplayName: reader.DisplayName, Locale: reader.Locale, Bio: "恢复后还在的简介", AvatarMediaID: result.media.ID}); err != nil {
		t.Fatal(err)
	}
	result.trash, err = library.Upload(t.Context(), strings.NewReader("%PDF-1.7\nRetained backup attachment\n%%EOF\n"), media.UploadInput{Filename: "retained.pdf"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	result.trash, err = library.Trash(result.trash.ID, result.trash.Version)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := site.NewManager(filepath.Join(cfg.DataDir, "site-settings.json"), site.DefaultSettings(cfg.SiteTitle, cfg.SiteTagline))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := settings.Save(settings.Snapshot().Revision, settings.Snapshot().Settings); err != nil {
		t.Fatal(err)
	}
	themes := theme.NewManager(cfg.ThemesDir, cfg.ActiveTheme, theme.WithSettingsDir(filepath.Join(cfg.DataDir, "theme-settings")))
	if err := themes.Load(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := themes.SaveSettings(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(cfg.PluginsDir, "backup-plugin", "plugin.json"), []byte(`{"schema":1,"slug":"backup-plugin","name":"Backup plugin","version":"1.0.0","runtime":"wasm/core-v1","entry":"plugin.wasm","capabilities":[]}`))
	module, err := hex.DecodeString("0061736d010000000105016000017e030201000712010e6e705f6170695f76657273696f6e00000a0601040042010b")
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(cfg.PluginsDir, "backup-plugin", "plugin.wasm"), module)
	writeFixtureFile(t, filepath.Join(cfg.PluginsDir, "backup-plugin", ".disabled"), []byte("disabled by administrator\n"))
	writeFixtureFile(t, filepath.Join(cfg.DataDir, "media", "tmp", "interrupted-upload.tmp"), []byte("temporary upload"))
	writeFixtureFile(t, filepath.Join(parent, "deployment-secret.txt"), []byte("outside-secret"))
	return result
}

func writeFixtureFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFullLocalBackupRestorePreservesStateAndNextIdentity(t *testing.T) {
	fixture := fixtureSite(t, true)
	archivePath := filepath.Join(fixture.parent, "site.zip")
	created, err := Create(t.Context(), fixture.cfg, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Verified || !created.ApplicationValidated || created.Files < 10 || created.AuditSequence == 0 {
		t.Fatalf("incomplete backup report: %+v", created)
	}
	inspected, err := Inspect(t.Context(), archivePath)
	if err != nil || !inspected.Verified || inspected.ApplicationValidated || inspected.ArchiveSHA256 != created.ArchiveSHA256 {
		t.Fatalf("inspection = %+v, %v", inspected, err)
	}
	destination := filepath.Join(fixture.parent, "restored")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(t.Context(), archivePath, destination)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.ApplicationValidated || restored.ManifestSHA256 != created.ManifestSHA256 || restored.ArchiveSHA256 != created.ArchiveSHA256 || restored.Environment["NEWPRESS_DATABASE_URL"] != "" || restored.Environment["NEWPRESS_SEED_DEMO"] != "false" {
		t.Fatalf("restore report = %+v", restored)
	}
	dataDir := restored.Environment["NEWPRESS_DATA_DIR"]
	for _, relative := range []string{"theme-settings/backup-test.json", "content.json.bak"} {
		before, beforeErr := os.ReadFile(filepath.Join(fixture.cfg.DataDir, filepath.FromSlash(relative)))
		after, afterErr := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(relative)))
		if beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("restore did not preserve %s", relative)
		}
	}
	plugins, err := plugin.NewManager(restored.Environment["NEWPRESS_PLUGINS_DIR"]).List()
	if err != nil || len(plugins) != 1 || plugins[0].Enabled || plugins[0].Error != "" || plugins[0].Manifest.Slug != "backup-plugin" {
		t.Fatalf("restored plugin state = %+v, %v", plugins, err)
	}
	ledger, err := audit.Open(filepath.Join(dataDir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	a, err := openArchive(t.Context(), archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer a.file.Close()
	verified, err := ledger.VerifyCheckpoint(a.manifest.Audit, factPaths(dataDir, ledger))
	if err != nil || verified.Relation != audit.CheckpointRelationExact {
		t.Fatalf("restored audit = %+v, %v", verified, err)
	}
	posts, err := content.NewFileRepository(filepath.Join(dataDir, "content.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	post, err := posts.GetPublishedBySlug(t.Context(), fixture.post.Slug)
	receipt, receiptErr := posts.FindSubmission(t.Context(), strings.Repeat("a", 64), strings.Repeat("b", 64))
	if receiptErr != nil || !receipt.Replayed || receipt.Receipt.PostID != post.ID || receipt.Receipt.Version != 1 {
		t.Fatal("restored creation receipt lost", receipt, receiptErr)
	}
	// A response lost before backup must still replay after restore, even when the article has a newer revision.
	if replay, err := posts.Submit(t.Context(), content.Submission{Key: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Action: "create"}); err != nil || !replay.Replayed || replay.Receipt.PostID != post.ID {
		t.Fatal(replay, err)
	}
	importPlan, importErr := posts.PreviewImport(t.Context(), content.ImportBatch{Source: "backup-import", Format: "markdown", Items: []content.ImportEntry{{Key: "note.md", Digest: strings.Repeat("a", 64), Input: content.PostInput{Title: "Imported note", Slug: "backup-import-note"}}}})
	if importErr != nil || importPlan.Existing != 1 || importPlan.New != 0 {
		t.Fatal("restored import mapping missing", importPlan, importErr)
	}
	if target, redirectErr := posts.ResolveRedirect(t.Context(), "/legacy/backup-post.html"); redirectErr != nil || target != content.PublicPath(fixture.post) {
		t.Fatal("redirect restore changed", target, redirectErr)
	}
	if err == nil && (post.ContentSource != fixture.post.ContentSource || post.ContentHTML != fixture.post.ContentHTML) {
		t.Fatal("restored article lost structured content")
	}
	if err != nil || post.ID != fixture.post.ID || post.Title != fixture.post.Title || post.Version != 2 || post.AuthorID != 1 || len(post.Tags) != 1 || post.Categories[0].ID != fixture.post.Categories[0].ID {
		t.Fatalf("restored article = %+v, %v", post, err)
	}
	revisions, err := posts.ListRevisions(t.Context(), post.ID, 20)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("restored revisions = %+v, %v", revisions, err)
	}
	series, chapters, err := posts.PublishedSeries(t.Context(), "backup-series")
	if err != nil || series.Title != "恢复阅读目录" || series.CoverURL != fixture.media.PublicURL || len(chapters) != 1 || chapters[0].ID != fixture.post.ID {
		t.Fatal("series restore changed", series, err)
	}
	statistics, err := analytics.NewFileStore(filepath.Join(dataDir, "analytics.json"))
	if err != nil {
		t.Fatal(err)
	}
	statisticsState, err := statistics.Snapshot(t.Context(), time.Now())
	if err != nil || statisticsState.Days[analytics.Date(time.Now())].Posts[post.ID] != 1 {
		t.Fatal("analytics restore changed", statisticsState, err)
	}
	circles, err := community.NewFileStore(filepath.Join(dataDir, "community.json"))
	if err != nil {
		t.Fatal(err)
	}
	circle, err := circles.Get(t.Context(), "backup-circle")
	if err != nil || circle.Version != 7 || circle.Topics[0].AcceptedID != 1 || len(circle.Members) != 1 || circle.Topics[0].Replies[0].Body != "ANSWER" || circle.Topics[0].Format != "markdown" || len(circle.Attachments) != 1 || len(circle.Activities) == 0 {
		t.Fatal("community restore changed", circle, err)
	}
	circleFiles, err := community.NewFiles(filepath.Join(dataDir, "community-files"))
	if err != nil {
		t.Fatal(err)
	}
	fileData, err := circleFiles.Read(t.Context(), circle.Attachments[0])
	if err != nil || string(fileData) != "PRIVATE CIRCLE FILE" || !ledger.HasFact(audit.FactCommunity) {
		t.Fatal("private files or circle audit missing", err)
	}
	replay, err := circles.Apply(t.Context(), "backup-circle", community.Actor{ID: 1, Admin: true}, community.Command{Action: "accept", Key: "backup-circle-accept", Version: 4, TopicID: 1, ReplyID: 1}, time.Now().UTC())
	if err != nil || !replay.Replay || replay.Version != 5 {
		t.Fatal("community replay lost", replay, err)
	}
	autosave, err := posts.GetAutosave(t.Context(), post.ID, "user:1")
	if err != nil || autosave.Snapshot.Title != "尚未保存的恢复点" {
		t.Fatalf("restored autosave = %+v, %v", autosave, err)
	}
	next, err := posts.Create(t.Context(), content.PostInput{Title: "恢复后新增", Slug: "restored-next"})
	if err != nil || next.ID <= post.ID {
		t.Fatalf("restored next ID = %+v, %v", next, err)
	}
	users, err := auth.NewFileUserStore(filepath.Join(dataDir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := users.GetUser(t.Context(), 2)
	if err != nil || reader.Username != "backup-reader" || reader.PasswordHash != fixture.cfg.AdminPassHash {
		t.Fatal("restored reader identity or password hash changed", err)
	}
	if reader.Bio != "恢复后还在的简介" || reader.AvatarMediaID != fixture.media.ID {
		t.Fatal("restored profile or avatar reference changed")
	}
	invitations, err := users.ListInvitations(t.Context(), time.Now())
	if err != nil || len(invitations) != 1 || invitations[0].Email != "backup-invited@example.test" {
		t.Fatal("restored account invitations changed", err)
	}
	engagements, err := engagement.NewFileStore(filepath.Join(dataDir, "engagement.json"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := engagements.Entries(t.Context(), reader.ID, engagement.Bookmark)
	if err != nil || len(saved) != 1 || saved[0].TargetID != fixture.post.ID || saved[0].Collection != "恢复验收" {
		t.Fatalf("restored bookmarks = %#v, %v", saved, err)
	}
	currency, err := virtualcurrency.NewFileStore(filepath.Join(dataDir, "currency.json"))
	if err != nil {
		t.Fatal(err)
	}
	wallet, err := currency.Wallet(t.Context(), reader.ID, 0, 30)
	if err != nil || wallet.Balance != 260 || !wallet.Settings.PrecisionLocked || len(wallet.Entries) != 2 || wallet.NextCheckInAt.IsZero() {
		t.Fatal("restored currency wallet changed", wallet, err)
	}
	transfer, err := currency.Transfer(t.Context(), virtualcurrency.TransferInput{UserID: 1, RecipientID: reader.ID, Amount: 250, ExpectedVersion: wallet.Settings.Version, RequestID: "backup-currency-transfer"})
	if err != nil || !transfer.Replayed || transfer.Sent.Balance != 12095 {
		t.Fatal("restored transfer receipt lost", transfer, err)
	}
	reward, err := currency.Reward(t.Context(), virtualcurrency.RewardInput{UserID: reader.ID, Kind: virtualcurrency.RewardCheckIn, ExpectedVersion: wallet.Settings.Version, RequestID: "backup-currency-checkin"})
	if err != nil || !reward.Replayed || reward.Entry.ID != wallet.Entries[0].ID {
		t.Fatal("restored reward duplicated", reward, err)
	}
	currencyCatalog, err := virtualcurrency.NewFileCatalog(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	createdCurrency, replayed, err := currencyCatalog.Create(t.Context(), virtualcurrency.CreateCurrency{Key: "gold", Name: "金币", Symbol: "枚", Precision: 2, ActorID: 1, RequestID: "backup-create-gold-currency"})
	if err != nil || !replayed || createdCurrency.Key != "gold" {
		t.Fatal("restored creation receipt", createdCurrency, replayed, err)
	}
	gold, err := currencyCatalog.Store(t.Context(), "gold")
	if err != nil {
		t.Fatal(err)
	}
	if balance, err := gold.Balance(t.Context(), reader.ID); err != nil || balance != 99 {
		t.Fatal("restored independent currency", balance, err)
	}
	if entry, err := gold.Adjust(t.Context(), virtualcurrency.Adjustment{ActorID: 1, UserID: reader.ID, Delta: 99, ExpectedVersion: 1, RequestID: "backup-currency-issue", Reason: "金币备份"}); err != nil || entry.Balance != 99 {
		t.Fatal("restored currency receipt duplicated", entry, err)
	}
	wxStore, err := wechat.NewFileStore(filepath.Join(dataDir, "wechat.json"))
	if err != nil {
		t.Fatal(err)
	}
	wxState, err := wxStore.View(t.Context())
	if err != nil || wxState.Config.Secret != strings.Repeat("w", 32) || len(wxState.Jobs) != 1 || wxState.Jobs[0].PostID != fixture.post.ID {
		t.Fatal("restored WeChat state changed", err)
	}
	alerts, err := notification.NewFileStore(filepath.Join(dataDir, "notifications.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := alerts.Preferences(t.Context(), reader.ID)
	if err != nil || !prefs.Email || len(prefs.UnsubscribeKey) != 64 {
		t.Fatal("restored preferences changed", err)
	}
	alertPage, err := alerts.Notices(t.Context(), reader.ID, notification.Query{})
	if err != nil || len(alertPage.Items) != 1 || alertPage.Items[0].Event.PostID != fixture.post.ID {
		t.Fatal("restored notification missing", err)
	}
	if _, ok, err := alerts.ClaimMail(t.Context(), time.Now().Add(2*time.Minute)); err != nil || ok {
		t.Fatal("restored sending job was resent", err)
	}
	mailState, err := alerts.MailStatus(t.Context(), reader.ID)
	if err != nil || mailState.Uncertain != 1 {
		t.Fatal("ambiguous backup job was lost", err)
	}
	messages, err := messaging.NewFileStore(filepath.Join(dataDir, "messages.json"))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := messages.Messages(t.Context(), 2, 1, messaging.MessageQuery{})
	if err != nil || len(conversation.Items) != 1 || conversation.Items[0].Content != fixture.message.Content || conversation.Items[0].ReadAt == nil {
		t.Fatalf("restored private conversation = %+v, %v", conversation, err)
	}
	notices, err := messages.Notices(t.Context(), 2, messaging.NoticeQuery{})
	if err != nil || len(notices.Items) != 0 {
		t.Fatalf("cleared notifications reappeared: %+v, %v", notices, err)
	}
	library, err := media.NewManager(filepath.Join(dataDir, "media"))
	if err != nil {
		t.Fatal(err)
	}
	item, stream, err := library.Open(fixture.media.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, stream); err != nil || hex.EncodeToString(digest.Sum(nil)) != item.ChecksumSHA256 {
		t.Fatal("restored media checksum mismatch", err)
	}
	if item.Image != fixture.media.Image {
		t.Fatal("restored image recipe mismatch")
	}
	for _, variant := range item.Image.Variants {
		for _, format := range []string{"native", "webp"} {
			object, reader, err := library.OpenVariant(item.ID, variant.Name, format, false)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.New()
			_, copyErr := io.Copy(digest, reader)
			reader.Close()
			if copyErr != nil || hex.EncodeToString(digest.Sum(nil)) != object.ChecksumSHA256 {
				t.Fatal("restored image derivative mismatch", copyErr)
			}
		}
	}
	trash, err := library.Get(fixture.trash.ID)
	if err != nil || trash.Status() != media.StatusTrash || trash.Version != fixture.trash.Version || trash.PublicURL != fixture.trash.PublicURL || !trash.TrashedAt.Equal(fixture.trash.TrashedAt) {
		t.Fatalf("trash was not retained: %+v %v", trash, err)
	}
	if _, _, err := library.Open(trash.ID); !errors.Is(err, media.ErrNotFound) {
		t.Fatal("trash became public after backup restore", err)
	}
	recovered, err := library.Restore(t.Context(), trash.ID, trash.Version)
	if err != nil || recovered.PublicURL != fixture.trash.PublicURL || recovered.Status() != media.StatusActive {
		t.Fatalf("trash recovery after backup = %+v %v", recovered, err)
	}
	for _, entry := range a.manifest.Files {
		if strings.Contains(entry.Path, dirlock.FileName) || strings.HasPrefix(entry.Path, "data/media/tmp/") || strings.Contains(entry.Path, "deployment-secret") {
			t.Fatalf("excluded file entered archive: %s", entry.Path)
		}
	}
	reportJSON, _ := json.Marshal(restored)
	if bytes.Contains(reportJSON, []byte(fixture.cfg.AdminPassHash)) || bytes.Contains(reportJSON, []byte(fixture.message.Content)) {
		t.Fatal("report exposed private data")
	}
	assertNoStaging(t, fixture.parent)
}

func TestBackupOfInitializedEmptySiteDoesNotSeedMissingStores(t *testing.T) {
	fixture := fixtureSite(t, false)
	archivePath := filepath.Join(fixture.parent, "empty.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.parent, "restored")
	if _, err := Restore(t.Context(), archivePath, destination); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"messages.json", "comments.json", "navigation.json"} {
		if _, err := os.Stat(filepath.Join(destination, "data", file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("empty store %s was unexpectedly seeded: %v", file, err)
		}
	}
}

func TestCreateRefusesLiveProcessesOverwriteAndPartialSources(t *testing.T) {
	fixture := fixtureSite(t, true)
	archivePath := filepath.Join(fixture.parent, "site.zip")
	application, err := app.New(t.Context(), fixture.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(t.Context(), fixture.cfg, archivePath); !errors.Is(err, dirlock.ErrBusy) {
		application.Close()
		t.Fatalf("live backup not refused: %v", err)
	}
	application.Close()
	if _, err := os.Stat(archivePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("live backup left an output")
	}
	writeFixtureFile(t, archivePath, []byte("existing backup"))
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err == nil {
		t.Fatal("existing backup overwritten")
	}
	if payload, _ := os.ReadFile(archivePath); string(payload) != "existing backup" {
		t.Fatal("existing output changed")
	}
	if _, err := Create(t.Context(), fixture.cfg, filepath.Join(fixture.cfg.DataDir, "inside.zip")); err == nil {
		t.Fatal("backup inside source accepted")
	}
	invalid := fixture.cfg
	invalid.DatabaseURL = "postgres://example.invalid/site"
	if _, err := Create(t.Context(), invalid, filepath.Join(fixture.parent, "database.zip")); err == nil {
		t.Fatal("database mode created misleading local backup")
	}
	invalid = fixture.cfg
	invalid.PluginsDir = filepath.Join(invalid.DataDir, "nested-plugins")
	if err := os.Mkdir(invalid.PluginsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(t.Context(), invalid, filepath.Join(fixture.parent, "nested.zip")); err == nil {
		t.Fatal("overlapping source directories accepted")
	}
	object := filepath.Join(fixture.cfg.DataDir, "media", filepath.FromSlash(fixture.media.ObjectKey))
	payload, err := os.ReadFile(object)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 1
	writeFixtureFile(t, object, payload)
	if _, err := Create(t.Context(), fixture.cfg, filepath.Join(fixture.parent, "corrupt-media.zip")); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("corrupt media accepted by backup: %v", err)
	}
	assertNoStaging(t, fixture.parent)
}

type zipEntry struct {
	header zip.FileHeader
	body   []byte
}

func readZIP(t *testing.T, path string) []zipEntry {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := make([]zipEntry, 0, len(reader.File))
	for _, entry := range reader.File {
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, zipEntry{header: entry.FileHeader, body: body})
	}
	return entries
}

func writeZIP(t *testing.T, path string, entries []zipEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range entries {
		header := entry.header
		output, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := output.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveCorruptionIsRejectedBeforeDestinationPublication(t *testing.T) {
	fixture := fixtureSite(t, true)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func([]zipEntry) []zipEntry{
		"checksum": func(entries []zipEntry) []zipEntry { entries[len(entries)-1].body[0] ^= 1; return entries },
		"traversal": func(entries []zipEntry) []zipEntry {
			entries[len(entries)-1].header.Name = "data/../../outside"
			return entries
		},
		"absolute":  func(entries []zipEntry) []zipEntry { entries[len(entries)-1].header.Name = "/outside"; return entries },
		"duplicate": func(entries []zipEntry) []zipEntry { return append(entries, entries[len(entries)-1]) },
		"case collision": func(entries []zipEntry) []zipEntry {
			extra := entries[len(entries)-1]
			extra.header.Name = strings.ToUpper(extra.header.Name)
			return append(entries, extra)
		},
		"symlink": func(entries []zipEntry) []zipEntry {
			entries[len(entries)-1].header.SetMode(os.ModeSymlink | 0o777)
			return entries
		},
		"missing": func(entries []zipEntry) []zipEntry { return entries[:len(entries)-1] },
		"unknown manifest field": func(entries []zipEntry) []zipEntry {
			entries[0].body = bytes.Replace(entries[0].body, []byte("{"), []byte(`{"unexpected":true,`), 1)
			return entries
		},
		"duplicate manifest field": func(entries []zipEntry) []zipEntry {
			entries[0].body = bytes.Replace(entries[0].body, []byte("{"), []byte(`{"schema":1,`), 1)
			return entries
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "bad.zip")
			writeZIP(t, bad, mutate(readZIP(t, archivePath)))
			if _, err := Inspect(t.Context(), bad); err == nil {
				t.Fatal("invalid archive passed inspection")
			}
			destination := filepath.Join(t.TempDir(), "restore")
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := Restore(t.Context(), bad, destination); err == nil {
				t.Fatal("invalid archive restored")
			}
			children, _ := os.ReadDir(destination)
			if len(children) != 0 {
				t.Fatal("failed restore changed destination")
			}
			assertNoStaging(t, filepath.Dir(destination))
		})
	}
	for _, kind := range []string{"truncated", "directory limit", "count limit"} {
		t.Run(kind, func(t *testing.T) {
			payload, err := os.ReadFile(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "truncated":
				payload = payload[:len(payload)-12]
			case "directory limit":
				binary.LittleEndian.PutUint32(payload[len(payload)-22+12:], maxDirectoryBytes+1)
			case "count limit":
				binary.LittleEndian.PutUint16(payload[len(payload)-22+10:], MaxEntries+2)
			}
			bad := filepath.Join(t.TempDir(), "bad.zip")
			writeFixtureFile(t, bad, payload)
			if _, err := Inspect(t.Context(), bad); err == nil {
				t.Fatal("invalid ZIP metadata accepted")
			}
		})
	}
}

func TestRestoreLoadsDomainFactsBeyondArchiveDigests(t *testing.T) {
	fixture := fixtureSite(t, true)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	objects, err := os.ReadDir(filepath.Join(fixture.cfg.DataDir, "community-files"))
	if err != nil || len(objects) != 1 {
		t.Fatal("circle file fixture", err)
	}
	for _, name := range []string{"data/content.json", "data/community.json", "data/community-files/" + objects[0].Name(), "data/media/" + fixture.media.ObjectKey} {
		t.Run(name, func(t *testing.T) {
			entries := readZIP(t, archivePath)
			var manifest Manifest
			if err := json.Unmarshal(entries[0].body, &manifest); err != nil {
				t.Fatal(err)
			}
			for index := range entries {
				if entries[index].header.Name != name {
					continue
				}
				entries[index].body[0] ^= 1
				digest := sha256.Sum256(entries[index].body)
				for item := range manifest.Files {
					if manifest.Files[item].Path == name {
						manifest.Files[item].SHA256 = hex.EncodeToString(digest[:])
					}
				}
			}
			manifest.ManifestSHA256 = manifestDigest(manifest)
			entries[0].body, _ = json.Marshal(manifest)
			bad := filepath.Join(t.TempDir(), "invalid-domain.zip")
			writeZIP(t, bad, entries)
			if _, err := Inspect(t.Context(), bad); name == "data/community.json" {
				if err == nil {
					t.Fatal("audited circle tampering passed checkpoint inspection")
				}
			} else if err != nil {
				t.Fatalf("byte-level inspection should pass recomputed checksums: %v", err)
			}
			destination := filepath.Join(t.TempDir(), "restored")
			if _, err := Restore(t.Context(), bad, destination); err == nil {
				t.Fatal("invalid domain state published")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed semantic validation published a destination")
			}
			assertNoStaging(t, filepath.Dir(destination))
		})
	}
}

func TestRestorePreservesExistingSiteAndCancellationCleansUp(t *testing.T) {
	fixture := fixtureSite(t, false)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.parent, "existing")
	sentinel := filepath.Join(destination, "do-not-touch.txt")
	writeFixtureFile(t, sentinel, []byte("existing site"))
	if _, err := Restore(t.Context(), archivePath, destination); err == nil {
		t.Fatal("restored into nonempty directory")
	}
	if payload, _ := os.ReadFile(sentinel); string(payload) != "existing site" {
		t.Fatal("existing site changed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Create(ctx, fixture.cfg, filepath.Join(fixture.parent, "cancelled.zip")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled backup: %v", err)
	}
	if _, err := Restore(ctx, archivePath, filepath.Join(fixture.parent, "cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled restore: %v", err)
	}
	assertNoStaging(t, fixture.parent)
}

func TestBackupRefusesLinkedSourceAndRestoreDestination(t *testing.T) {
	fixture := fixtureSite(t, false)
	archivePath := filepath.Join(fixture.parent, "valid.zip")
	if _, err := Create(t.Context(), fixture.cfg, archivePath); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	linked := filepath.Join(fixture.cfg.DataDir, "linked-directory")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("OS does not permit symbolic links: %v", err)
	}
	if _, err := Create(t.Context(), fixture.cfg, filepath.Join(fixture.parent, "linked.zip")); err == nil {
		t.Fatal("linked backup source accepted")
	}
	if _, err := Restore(t.Context(), archivePath, linked); err == nil {
		t.Fatal("linked restore destination accepted")
	}
	children, _ := os.ReadDir(outside)
	if len(children) != 0 {
		t.Fatal("outside directory changed")
	}
}

func assertNoStaging(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".newpress-restore-") || strings.HasPrefix(entry.Name(), ".newpress-backup-") {
			t.Fatalf("temporary backup artifact leaked: %s", entry.Name())
		}
	}
}
