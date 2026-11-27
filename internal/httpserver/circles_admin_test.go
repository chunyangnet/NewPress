package httpserver

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/community"
)

func TestCircleWorkspaceUsesCircleRolesAndKeepsPublicRecordsPersonal(t *testing.T) {
	s, handler, users, _, adminCookie, admin := newPersistentAdminTestServer(t)
	_, ownerCookie, owner := createAndLoginTestUser(t, users, admin, "circle-owner", "circle-owner@example.test", "圈主", "subscriber")
	_, moderatorCookie, moderator := createAndLoginTestUser(t, users, admin, "circle-moderator", "circle-moderator@example.test", "版主", "subscriber")
	_, memberCookie, member := createAndLoginTestUser(t, users, admin, "circle-member", "circle-member@example.test", "成员", "subscriber")
	store, err := community.NewFileStore(filepath.Join(t.TempDir(), "circles.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetCommunityStore(store)
	s.config.CommunityEnabled = true
	sequence := 0
	apply := func(slug string, actor community.Actor, command community.Command) community.Result {
		t.Helper()
		sequence++
		command.Key = fmt.Sprintf("workspace-%016d", sequence)
		if current, err := store.Get(t.Context(), slug); err == nil {
			command.Version = current.Version
		}
		result, err := store.Apply(t.Context(), slug, actor, command, time.Now())
		if err != nil {
			t.Fatalf("%s: %v", command.Action, err)
		}
		return result
	}
	adminActor := community.Actor{ID: admin.UserID, Admin: true}
	ownerActor := community.Actor{ID: owner.UserID}
	moderatorActor := community.Actor{ID: moderator.UserID}
	memberActor := community.Actor{ID: member.UserID}
	apply("managed", adminActor, community.Command{Action: "create", Name: "我管理的圈子", OwnerID: owner.UserID, JoinPolicy: "open", Visibility: "public"})
	apply("other", adminActor, community.Command{Action: "create", Name: "不属于版主的圈子", OwnerID: admin.UserID, JoinPolicy: "open", Visibility: "public"})
	apply("managed", moderatorActor, community.Command{Action: "join"})
	apply("managed", memberActor, community.Command{Action: "join"})
	apply("managed", ownerActor, community.Command{Action: "member", UserID: moderator.UserID, Role: "moderator"})
	topic := apply("managed", ownerActor, community.Command{Action: "topic", Title: "讨论主题", Body: "正文", Kind: "discussion"})
	for i := 0; i < 20; i++ {
		apply("managed", ownerActor, community.Command{Action: "topic", Title: fmt.Sprintf("分页讨论 %d", i), Body: "正文", Kind: "discussion"})
	}
	for i := 0; i < 26; i++ {
		apply("managed", memberActor, community.Command{Action: "reply", TopicID: topic.TopicID, Body: fmt.Sprintf("分页回应 %d", i)})
	}
	apply("managed", memberActor, community.Command{Action: "report", TopicID: topic.TopicID, Category: "other", Reason: "MEMBER_PRIVATE_REPORT"})

	get := func(cookie *http.Cookie, path string, want int) string {
		t.Helper()
		response := serveContentAPI(handler, cookie, http.MethodGet, "https://example.test"+path, "", nil)
		if response.Code != want {
			t.Fatalf("%s: got %d, want %d: %s", path, response.Code, want, response.Body.String())
		}
		if want == http.StatusOK && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("private circle page cached: %s", response.Header().Get("Cache-Control"))
		}
		return response.Body.String()
	}
	get(nil, "/admin/circles/", http.StatusSeeOther)
	get(memberCookie, "/admin/circles/", http.StatusForbidden)
	get(memberCookie, "/admin/circles/managed/", http.StatusForbidden)
	get(moderatorCookie, "/admin/circles/other/", http.StatusForbidden)
	get(moderatorCookie, "/admin/settings/", http.StatusForbidden)
	directory := get(moderatorCookie, "/admin/circles/", http.StatusOK)
	if !strings.Contains(directory, "admin-workbench") || !strings.Contains(directory, `href="/admin/circles/managed/"`) || strings.Contains(directory, "不属于版主的圈子") || strings.Contains(directory, `data-circle-command="create"`) {
		t.Fatal("moderator directory must use the admin shell and only manageable circles")
	}
	ownerPage := get(ownerCookie, "/admin/circles/managed/", http.StatusOK)
	if !strings.Contains(ownerPage, `href="/admin/circles/managed/?page=2#circle-topics"`) {
		t.Fatal("topic pagination left the workspace")
	}
	for _, expected := range []string{`class="admin-settings-layout circle-settings-layout"`, `data-settings-target="circle-members"`, `data-settings-target="circle-topics"`, `data-circle-members-dialog`, `data-member-search`, `data-circle-command="settings"`, `data-circle-command="member"`, "MEMBER_PRIVATE_REPORT", `data-circle-command="review_report"`, `data-circle-workspace`, `js/chunnel-circles.js`} {
		if !strings.Contains(ownerPage, expected) {
			t.Fatalf("workspace missing %q", expected)
		}
	}
	moderatorPage := get(moderatorCookie, "/admin/circles/managed/", http.StatusOK)
	if strings.Contains(moderatorPage, `data-circle-command="settings"`) || strings.Contains(moderatorPage, `data-circle-action="archive_circle"`) {
		t.Fatal("moderator received owner-only controls")
	}
	publicOwner := get(ownerCookie, "/community/managed/", http.StatusOK)
	for _, forbidden := range []string{`data-circle-command="settings"`, `data-circle-command="member"`, `data-circle-command="review_report"`, `id="circle-workspace"`, "MEMBER_PRIVATE_REPORT"} {
		if strings.Contains(publicOwner, forbidden) {
			t.Fatalf("public page still exposes workspace data: %q", forbidden)
		}
	}
	if !strings.Contains(publicOwner, `href="/admin/circles/managed/"`) {
		t.Fatal("public owner page lost its workspace entry")
	}
	memberPage := get(memberCookie, "/community/managed/", http.StatusOK)
	if !strings.Contains(memberPage, "我的举报") || !strings.Contains(memberPage, "MEMBER_PRIVATE_REPORT") {
		t.Fatal("reporter lost personal report history")
	}
	publicDirectory := get(adminCookie, "/community/", http.StatusOK)
	if strings.Contains(publicDirectory, `data-circle-command="create"`) || !strings.Contains(publicDirectory, `href="/admin/circles/"`) {
		t.Fatal("creation must live in the admin workspace")
	}
	publicTopic := get(moderatorCookie, fmt.Sprintf("/community/managed/topics/%d", topic.TopicID), http.StatusOK)
	if strings.Contains(publicTopic, `data-circle-action="hide_topic"`) || strings.Contains(publicTopic, `data-circle-command="edit_topic"`) {
		t.Fatal("moderator controls remained on the public topic")
	}
	adminTopic := get(moderatorCookie, fmt.Sprintf("/admin/circles/managed/topics/%d", topic.TopicID), http.StatusOK)
	if !strings.Contains(adminTopic, `data-circle-action="hide_topic"`) || !strings.Contains(adminTopic, `data-circle-command="edit_topic"`) {
		t.Fatal("topic moderation missing from workspace")
	}
	if !strings.Contains(adminTopic, fmt.Sprintf(`href="/admin/circles/managed/topics/%d?page=2"`, topic.TopicID)) {
		t.Fatal("reply pagination left the workspace")
	}
}
