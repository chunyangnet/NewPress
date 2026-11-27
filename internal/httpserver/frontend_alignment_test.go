package httpserver

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/feedback"
)

func TestFrontendPopulatedComponentLayoutContract(t *testing.T) {
	server, handler, _, repository, cookie, principal := newPersistentAdminTestServer(t)
	post, err := repository.Create(t.Context(), content.PostInput{
		Type: content.TypePost, Status: content.StatusPublish, Slug: "alignment-article",
		Title: "组件对齐检查：正文、目录与评论", Excerpt: "这是一篇用于布局回归的短文章。",
		ContentSource: "<h2>正文内容</h2><p>正文结束后，操作栏、作者和评论应自然衔接。</p>",
		AuthorID:      principal.UserID, AuthorName: principal.Label(), CommentStatus: content.CommentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.Create(t.Context(), content.PostInput{
		Type: content.TypePage, Status: content.StatusPublish, Slug: "alignment-page",
		Title: "独立页面布局", ContentSource: "<p>独立页面的底部组件不应被侧栏撑开。</p>",
		AuthorID: principal.UserID, AuthorName: principal.Label(),
	}); err != nil {
		t.Fatal(err)
	}
	seriesBody, err := json.Marshal(content.SeriesInput{
		Kind: content.SeriesKindSequence, Status: content.StatusPublish, Slug: "alignment-series",
		Title: "组件布局阅读系列", Description: "从内容列到卡片边界，检查实际有内容的系列。", PostIDs: []int64{post.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/admin/series", string(seriesBody), authenticatedAPIHeaders(principal))
	if response.Code != http.StatusCreated {
		t.Fatalf("series fixture: %d %s", response.Code, response.Body.String())
	}
	store, err := community.NewFileStore(filepath.Join(t.TempDir(), "community.json"))
	if err != nil {
		t.Fatal(err)
	}
	server.SetCommunityStore(store)
	server.config.CommunityEnabled = true
	sequence := 0
	circleCommand := func(command community.Command) community.Result {
		t.Helper()
		sequence++
		command.Key = fmt.Sprintf("alignment-circle-%016d", sequence)
		if circle, loadErr := store.Get(t.Context(), "alignment-circle"); loadErr == nil {
			command.Version = circle.Version
		}
		body, marshalErr := json.Marshal(command)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		result := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/community/circles/alignment-circle", string(body), authenticatedAPIHeaders(principal))
		if result.Code != http.StatusOK {
			t.Fatalf("circle fixture: %d %s", result.Code, result.Body.String())
		}
		var parsed community.Result
		if decodeErr := json.Unmarshal(result.Body.Bytes(), &parsed); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return parsed
	}
	circleCommand(community.Command{Action: "create", Name: "组件布局交流圈", Description: "检查圈子介绍、话题和侧栏的排列。", Rules: "聚焦内容，友善讨论。", JoinPolicy: "open", Visibility: "public", OwnerID: principal.UserID})
	topic := circleCommand(community.Command{Action: "topic", Title: "不同屏幕下的组件边界如何保持一致？", Body: "检查标题、作者信息与回复区域，避免内容互相挤压。", Kind: "question"})
	circleCommand(community.Command{Action: "reply", TopicID: topic.TopicID, Body: "这是用于检查回复排版的测试内容。"})
	feedbackCommand := func(command feedback.Command) feedback.Result {
		t.Helper()
		sequence++
		command.Key = fmt.Sprintf("alignment-feedback-%016d", sequence)
		if board, loadErr := server.feedback.Get(t.Context(), "alignment-feedback"); loadErr == nil {
			command.Version = board.Version
		}
		body, marshalErr := json.Marshal(command)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		result := serveContentAPI(handler, cookie, http.MethodPost, "https://example.test/api/v1/feedback/alignment-feedback", string(body), authenticatedAPIHeaders(principal))
		if result.Code != http.StatusOK {
			t.Fatalf("feedback fixture: %d %s", result.Code, result.Body.String())
		}
		var parsed feedback.Result
		if decodeErr := json.Unmarshal(result.Body.Bytes(), &parsed); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return parsed
	}
	settings := feedback.Defaults()
	feedbackCommand(feedback.Command{Action: "create", Settings: &settings})
	item := feedbackCommand(feedback.Command{Action: "submit", Title: "让组件在双端保持清晰对齐", Body: "检查投票、标题、状态、回复与表单的边界。", Kind: "suggestion"})
	feedbackCommand(feedback.Command{Action: "reply", ItemID: item.ItemID, Body: "已加入布局检查。", Official: true})
	exportDirectory := os.Getenv("NEWPRESS_LAYOUT_FIXTURES")
	var index strings.Builder
	index.WriteString("<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><title>组件布局测试样例</title><h1>组件布局测试样例（静态模板，无提交功能）</h1><ul>")
	for _, page := range []struct{ name, path, component string }{
		{"article", "/posts/alignment-article", `class="article-content"`},
		{"page", "/pages/alignment-page", `class="article-content"`},
		{"series", "/series/", "portal-series-card"},
		{"series-detail", "/series/alignment-series/", "series-directory"},
		{"account", "/account/", "account-room-layout"},
		{"circles", "/community/", "circle-index-card"},
		{"circle-detail", "/community/alignment-circle/", "circle-topic-row"},
		{"circle-topic", fmt.Sprintf("/community/alignment-circle/topics/%d", topic.TopicID), "circle-reply"},
		{"feedback", "/feedback/", "feedback-board"},
		{"feedback-board", "/feedback/alignment-feedback/", "feedback-card"},
		{"feedback-roadmap", "/feedback/alignment-feedback/?view=roadmap", "feedback-roadmap"},
		{"feedback-detail", fmt.Sprintf("/feedback/alignment-feedback/items/%d", item.ItemID), "feedback-reply"},
	} {
		t.Run(page.name, func(t *testing.T) {
			rendered := serveCookieRequest(handler, http.MethodGet, "https://example.test"+page.path, cookie, nil)
			if rendered.Code != http.StatusOK {
				t.Fatalf("render: HTTP %d", rendered.Code)
			}
			body := rendered.Body.String()
			for _, expected := range []string{page.component, "chunnel-portal.css", "chunnel-community.css", "</html>"} {
				if !strings.Contains(body, expected) {
					t.Errorf("missing populated component or stylesheet: %s", expected)
				}
			}
			if strings.Count(body, "<main ") != 1 || strings.Count(body, "</main>") != 1 {
				t.Error("page must retain one main landmark")
			}
			if exportDirectory != "" {
				if mkdirErr := os.MkdirAll(exportDirectory, 0755); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
				body = regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>|<noscript>.*?</noscript>`).ReplaceAllString(body, "")
				body = strings.ReplaceAll(body, principal.CSRFToken, "layout-fixture")
				if writeErr := os.WriteFile(filepath.Join(exportDirectory, page.name+".html"), []byte(body), 0644); writeErr != nil {
					t.Fatal(writeErr)
				}
				fmt.Fprintf(&index, `<li><a href="/%s.html">%s</a></li>`, page.name, html.EscapeString(page.path))
			}
		})
	}
	if exportDirectory != "" {
		index.WriteString("</ul></html>")
		if err = os.WriteFile(filepath.Join(exportDirectory, "index.html"), []byte(index.String()), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
