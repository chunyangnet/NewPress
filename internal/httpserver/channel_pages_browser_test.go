package httpserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/announcement"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/questions"
)

// Opt-in browser fixture. Stores and settings are temporary; the live site is untouched.
func TestChannelPagesBrowserFixture(t *testing.T) {
	addr := os.Getenv("NEWPRESS_CHANNEL_BROWSER_ADDR")
	if addr == "" {
		t.Skip("isolated browser fixture")
	}
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatal("browser fixture must bind loopback")
	}
	s, h, _, _, cookie, owner := newPersistentAdminTestServer(t)
	s.config.PublicURL = "http://" + addr
	s.config.CommunityEnabled = true
	questionStore, err := questions.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(questionStore)
	now := time.Now().Add(-24 * time.Hour)
	titles := []string{"如何让首页的内容更有层次感？", "分享文章时，封面图片应该选择多大尺寸？", "新建圈子后，怎样设置成员加入规则？", "如何给长篇文章添加清晰的章节导航？", "问答中的采纳功能有什么用？", "可以根据自己的品牌修改主题配色吗？", "手机上阅读文章时，怎样获得更舒适的体验？", "有没有适合新手的内容创作方法？"}
	tags := []string{"主题设计", "内容发布", "圈子交流", "阅读体验", "站点使用", "灵感分享"}
	answers := []string{
		"建议保留文章标题、摘要和封面，将分类导航放在列表上方。相邻模块使用一致的间距，手机端改为单列。",
		"横向封面可以使用 1200 × 750 像素。上传前压缩图片，将主体放在画面中间，并检查卡片裁切后的显示效果。",
		"在圈子管理中选择加入方式：公开加入或申请审核。成员可见的圈子仅向成员展示话题和人数。",
	}
	for i, title := range titles {
		q, err := questionStore.Apply(t.Context(), 0, questions.Actor{ID: owner.UserID}, questions.Command{Action: "create", Key: fmt.Sprintf("channel-fixture-create-%d", i), Title: title, Body: "想听听大家的实际经验，欢迎分享你使用过的方法。", Tags: []string{tags[i%len(tags)]}}, now.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			a, err := questionStore.Apply(t.Context(), q.ID, questions.Actor{ID: owner.UserID}, questions.Command{Action: "answer", Key: fmt.Sprintf("channel-fixture-answer-%d", i), Version: q.Version, Body: answers[i]}, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := questionStore.Apply(t.Context(), q.ID, questions.Actor{ID: owner.UserID}, questions.Command{Action: "accept", Key: fmt.Sprintf("channel-fixture-accept-%d", i), Version: a.Version, AnswerID: a.AnswerID}, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	circleStore, err := community.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunityStore(circleStore)
	for i, entry := range []struct{ name, description string }{
		{"光影与日常", "摄影作品、器材选用和拍摄技巧。"},
		{"阅读与写作", "书评、阅读笔记、文章修改与写作讨论。"},
		{"设计灵感室", "界面设计、字体排版、配色方案和作品反馈。"},
		{"城市漫游", "城市步行路线、旅行记录与地点推荐。"},
		{"数字生活", "软件使用、设备选购和常见问题。"},
		{"一杯咖啡时间", "咖啡豆、冲煮方法与器具讨论。"},
	} {
		visibility, policy := "public", "open"
		if i == 2 {
			visibility, policy = "members", "approval"
		}
		_, err := circleStore.Apply(t.Context(), fmt.Sprintf("circle-%d", i), community.Actor{ID: owner.UserID, Admin: true}, community.Command{Action: "create", Key: fmt.Sprintf("channel-fixture-circle-%d", i), Name: entry.name, Description: entry.description, Rules: "友善交流，尊重彼此。", OwnerID: owner.UserID, Visibility: visibility, JoinPolicy: policy}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := s.announcements.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	notices := []string{"关于问答与圈子使用规则更新的公告", "关于站点系统升级维护的通知", "十一月文章征集说明", "关于圈子功能开放与使用说明", "关于移动端阅读设置调整的说明", "社区内容推荐规则说明", "关于账号安全与资料维护的提醒", "关于问答社区上线的通知", "本月创作者交流活动安排", "站点隐私与数据保护说明更新", "关于文章发布与审核流程的说明", "关于站点开放注册的通知"}
	for i, title := range notices {
		item := announcement.Item{Title: title, Published: true, Pinned: i < 2, Date: now.Add(-time.Duration(i) * 24 * time.Hour), Body: "问答与圈子的发布和互动规则调整如下。\n一、问题发布与采纳\n提问时请注明使用环境、具体步骤和错误提示。问题解决后，提问者可采纳一条有效回答；采纳状态将在问题列表中显示。\n二、圈子加入方式\n圈主可将加入方式设为公开加入或申请审核。需要审核的申请由圈主或管理员处理，申请人可在圈子页面查看当前状态。\n三、成员可见范围\n设为成员可见的圈子仅向成员开放话题。未加入的用户可查看圈子名称、简介和加入规则，不能浏览内部讨论。\n本次调整自公告发布之日起生效。已有问题和圈子继续保留原有内容。"}
		if i == 0 {
			item.URL = "/questions/"
		}
		state, err = s.announcements.Save(t.Context(), state.Revision, "save", item)
		if err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/style/{style}", func(w http.ResponseWriter, r *http.Request) {
		style := r.PathValue("style")
		if style != "classic" && style != "liquid-glass" {
			http.Error(w, "invalid style", 400)
			return
		}
		values := s.themes.SettingsSnapshot().Values
		values["visual_style"] = style
		values["cookie_consent_enabled"] = false
		if _, fields, err := s.themes.SaveSettings(values); err != nil || len(fields) > 0 {
			http.Error(w, fmt.Sprint(fields, err), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /fixture/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, cookie)
		http.Redirect(w, r, "/community/", 303)
	})
	mux.Handle("/", h)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer listener.Close()
	go func() { <-time.After(30 * time.Minute); _ = server.Shutdown(context.Background()) }()
	t.Log("channel pages fixture ready", addr)
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
