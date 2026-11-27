package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"newpress.local/newpress/internal/aieditor"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/recognition"
	"newpress.local/newpress/internal/virtualcurrency"
)

func featureStatusCheckByID(t *testing.T, item featureStatusItem, id string) featureStatusCheck {
	t.Helper()
	for _, check := range item.Checks {
		if check.ID == id {
			return check
		}
	}
	t.Fatalf("missing check %s", id)
	return featureStatusCheck{}
}

func TestFeatureStatusCommentDefaultsDistinguishPages(t *testing.T) {
	s, h, _, repo, cookie, owner := newPersistentAdminTestServer(t)
	post := createOwnedTestPost(t, repo, owner.UserID, owner.DisplayName, "status-defaults", "已保存内容")
	for _, postDefault := range []bool{true, false} {
		snapshot := s.siteSettings.Snapshot()
		snapshot.Settings.Writing.PostCommentsEnabled = postDefault
		snapshot.Settings.Writing.PageCommentsEnabled = !postDefault
		if _, fields, err := s.siteSettings.Save(snapshot.Revision, snapshot.Settings); err != nil || len(fields) != 0 {
			t.Fatal(err, fields)
		}
		for _, feature := range []string{"comments", "article-comments", fmt.Sprintf("article-comments&object=%d", post.ID)} {
			v := featureStatusRead(t, h, cookie, feature)
			reason := featureStatusCheckByID(t, v.Items[0], "defaults").Reason
			labels := map[bool]string{true: "开放", false: "关闭"}
			if !strings.Contains(reason, "新文章默认"+labels[postDefault]) || !strings.Contains(reason, "新页面默认"+labels[!postDefault]) {
				t.Errorf("default comment explanation conflates content types: %s", reason)
			}
			if strings.Contains(feature, "object=") && !v.Items[0].Effective {
				t.Fatal("changing defaults closed an already saved article")
			}
		}
	}
}

type featureStatusFaultCatalog struct {
	virtualcurrency.Catalog
	stores map[string]virtualcurrency.Store
	faults map[string]error
}

func (c featureStatusFaultCatalog) Store(ctx context.Context, key string) (virtualcurrency.Store, error) {
	if err := c.faults[key]; err != nil {
		return nil, err
	}
	if store, ok := c.stores[key]; ok {
		return store, nil
	}
	return c.Catalog.Store(ctx, key)
}

type featureStatusFaultWallet struct {
	virtualcurrency.Store
	rules virtualcurrency.Settings
	err   error
}

func (s featureStatusFaultWallet) Settings(context.Context) (virtualcurrency.Settings, error) {
	return s.rules, s.err
}

func TestFeatureStatusRecognitionWalletFailuresAndPartialActions(t *testing.T) {
	s, h, _, _, cookie, _ := newPersistentAdminTestServer(t)
	service, coin, rules := recognitionHTTPStore(t, s)
	paused := rules
	paused.AllowTransfers = false
	broken := errors.New("private storage failure")
	base := service.Currencies
	for _, tc := range []struct {
		name, state, detail string
		other               bool
		catalog             virtualcurrency.Catalog
	}{
		{"ready", "ready", "", false, base},
		{"some paused", "partial", "转账", true, featureStatusFaultCatalog{Catalog: base, stores: map[string]virtualcurrency.Store{"bonus": featureStatusFaultWallet{rules: paused}}}},
		{"all paused", "needs_config", "转账", false, featureStatusFaultCatalog{Catalog: base, stores: map[string]virtualcurrency.Store{"main": featureStatusFaultWallet{rules: paused}}}},
		{"missing wallet", "needs_config", "不存在", false, featureStatusFaultCatalog{Catalog: base, faults: map[string]error{"main": fmt.Errorf("removed: %w", virtualcurrency.ErrCurrencyNotFound)}}},
		{"catalog read failure", "error", "读取", false, featureStatusFaultCatalog{Catalog: base, faults: map[string]error{"main": broken}}},
		{"wallet read failure", "error", "读取", false, featureStatusFaultCatalog{Catalog: base, stores: map[string]virtualcurrency.Store{"main": featureStatusFaultWallet{err: broken}}}},
		{"service unavailable", "error", "未装配", false, nil},
		{"some unreadable", "partial", "读取", true, featureStatusFaultCatalog{Catalog: base, faults: map[string]error{"bonus": broken}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Save valid settings first, then simulate a changed wallet or an I/O failure.
			service.Currencies = featureStatusFaultCatalog{Catalog: base, stores: map[string]virtualcurrency.Store{"bonus": coin}}
			st, err := service.Backend.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			st.Settings.Currencies = []recognition.TipCurrency{{Key: "main", Presets: []string{"5"}}}
			if tc.other {
				st.Settings.Currencies = append(st.Settings.Currencies, recognition.TipCurrency{Key: "bonus", Presets: []string{"5"}})
			}
			if err := service.SaveSettings(t.Context(), st.Settings); err != nil {
				t.Fatal(err)
			}
			service.Currencies = tc.catalog
			v := featureStatusRead(t, h, cookie, "recognition")
			item := v.Items[0]
			if item.State != tc.state || item.Effective != (tc.state == "ready" || tc.state == "partial") {
				t.Errorf("wrong availability: got %s, want %s", item.State, tc.state)
			}
			key := "main"
			if tc.other {
				key = "bonus"
			}
			check := featureStatusCheckByID(t, item, "wallet-"+key)
			if !strings.Contains(check.Reason, tc.detail) || strings.Contains(v.JSON(), broken.Error()) {
				t.Errorf("incorrect or unsafe wallet explanation: %s", check.Reason)
			}
			if tc.state == "partial" && (v.Overview.Action == nil || !v.Overview.Action.Allowed || v.Overview.Action.Target != "[data-recognition-currencies]") {
				t.Errorf("partially usable tips need an in-context repair action: %+v", v.Overview)
			}
		})
	}
}

func TestFeatureStatusRecognitionPresetLimitChanges(t *testing.T) {
	s, h, _, _, cookie, _ := newPersistentAdminTestServer(t)
	service, coin, rules := recognitionHTTPStore(t, s)
	rules.MinTransfer = 500 // The saved 1.25 preset is now below the 5.00 minimum.
	if _, err := coin.UpdateSettings(t.Context(), rules.Version, rules); err != nil {
		t.Fatal(err)
	}
	st, err := service.Backend.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.SaveSettings(t.Context(), st.Settings); !errors.Is(err, recognition.ErrInvalid) {
		t.Fatalf("expected a real configuration conflict, got %v", err)
	}
	v := featureStatusRead(t, h, cookie, "recognition")
	if v.Items[0].State != "partial" || !v.Items[0].Effective || v.Overview.Action == nil || !strings.Contains(v.Items[0].Summary, "快捷数值") {
		t.Fatalf("invalid presets must be explained without disabling valid custom tips: %+v", v.Items[0])
	}
	check := featureStatusCheckByID(t, v.Items[0], "presets-main")
	if check.Action == nil || !strings.Contains(check.Action.Target, "data-currency-presets") {
		t.Fatal("preset conflict has no repair target")
	}
}

func TestFeatureStatusRelatedTopicsMatchEditorPermissionsAndPrerequisites(t *testing.T) {
	s, h, users, repo, _, owner := newPersistentAdminTestServer(t)
	enableWritingFixture(t, s)
	_, cookie, author := createAndLoginTestUser(t, users, owner, "topic-status-author", "topic-status@example.test", "作者", "author")
	post := createOwnedTestPost(t, repo, author.UserID, author.DisplayName, "topic-status", "相关话题")
	other := createOwnedTestPost(t, repo, owner.UserID, owner.DisplayName, "topic-status-other", "其他作者内容")
	// Editing saved topics has no model/queue prerequisite.
	if _, err := s.aiSettings.Save(aieditor.Settings{Revision: s.aiSettings.Snapshot().Revision}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("https://example.test/admin/plugins/ai-pro-max/articles/%d/topics/", post.ID)
	page := serveContentAPI(h, cookie, "GET", path, "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "ai-related") {
		t.Errorf("topic editor has no matching status endpoint: %d", page.Code)
	}
	query := fmt.Sprintf("ai-related&object=%d", post.ID)
	v := featureStatusRead(t, h, cookie, query)
	if !v.Items[0].Effective || v.Items[0].Scope != "当前内容" || v.Items[0].Title != "相关话题管理" {
		t.Fatalf("manual topic editing incorrectly needs a model: %+v", v.Items[0])
	}
	for _, object := range []string{"", "0", "01", fmt.Sprint(other.ID)} {
		r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/feature-status?feature=ai-related&object="+object, "", nil)
		if r.Code != http.StatusNotFound {
			t.Errorf("invalid or unowned topic object %q: %d", object, r.Code)
		}
	}
	_, readerCookie, _ := createAndLoginTestUser(t, users, owner, "topic-status-reader", "topic-reader@example.test", "读者", "subscriber")
	if r := serveContentAPI(h, readerCookie, "GET", "https://example.test/api/v1/admin/feature-status?feature="+query, "", nil); r.Code != 403 {
		t.Fatal("topic status widened read access", r.Code)
	}
	if r := serveContentAPI(h, cookie, "GET", "https://example.test/api/v1/admin/feature-status?feature=ai", "", nil); r.Code != 403 {
		t.Fatal("topic permissions widened workbench access", r.Code)
	}
	if err := s.plugins.SetEnabled(plugin.AIProMax, false); err != nil {
		t.Fatal(err)
	}
	v = featureStatusRead(t, h, cookie, query)
	if v.Items[0].Effective || v.Overview.Action == nil || v.Overview.Action.Allowed {
		t.Fatal("inactive plugin or repair permission ignored")
	}
}
