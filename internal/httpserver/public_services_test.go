package httpserver

import (
	"encoding/json"
	"html/template"
	"strings"
	"testing"

	"newpress.local/newpress/internal/questions"
)

func TestPublicQuestionsPermissionAndIsolation(t *testing.T) {
	q := questionView{Question: questions.Question{ID: 7, Body: "PRIVATE EDIT SOURCE", Tags: []string{"配置"}, Reports: []questions.Report{{Reason: "PRIVATE REPORT"}}}, HTML: template.HTML("<p>公开问题</p>"), AnswersView: []questionAnswerView{
		{Answer: questions.Answer{ID: 1, Body: "HIDDEN ANSWER", Hidden: true}},
		{Answer: questions.Answer{ID: 2, Body: "PRIVATE ANSWER SOURCE"}, HTML: template.HTML("<p>公开回答</p>")},
	}, NotesView: []questionNoteView{{Note: questions.Note{Body: "公开补充"}, Author: "读者"}}}
	v := questionsView{Items: []questionView{q}, Question: q, Tags: []string{"配置"}}
	p := publicQuestions(v)
	raw, _ := json.Marshal(p)
	for _, secret := range []string{"PRIVATE EDIT SOURCE", "PRIVATE REPORT", "HIDDEN ANSWER", "PRIVATE ANSWER SOURCE"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("projection leaked %s", secret)
		}
	}
	if p.Question.HTML != "<p>公开问题</p>" || len(p.Question.AnswersView) != 1 || p.Question.AnswersView[0].ID != 2 {
		t.Fatal("public answer lost")
	}
	p.Items[0].Tags[0] = "changed"
	p.Question.NotesView[0].Body = "changed"
	p.Tags[0] = "changed"
	if q.Tags[0] != "配置" || q.NotesView[0].Body != "公开补充" || v.Tags[0] != "配置" {
		t.Fatal("projection aliases source data")
	}
	q.CanEdit, q.AnswersView[1].CanEdit = true, true
	editor := publicQuestion(q)
	if editor.Body != q.Body || editor.AnswersView[0].Body != q.AnswersView[1].Body {
		t.Fatal("authorized editor lost source")
	}
}

func TestPublicFlashCopiesAuthorizedView(t *testing.T) {
	v := flashPageView{Items: []flashItemView{{Title: "公开快讯", HTML: "<p>正文</p>"}}, Current: flashItemView{ID: 5, Body: "编辑草稿", ReviewNote: "编辑提示"}, Editing: true, Authenticated: true, Endpoint: "/api/v1/flash/5/"}
	p := publicFlash(v)
	if p.Current.Body != v.Current.Body || p.Endpoint != v.Endpoint || !p.Editing {
		t.Fatal("authorized edit projection incomplete")
	}
	p.Items[0].Title = "changed"
	if v.Items[0].Title != "公开快讯" {
		t.Fatal("flash aliases source items")
	}
}

func TestPublicAnnouncementsCopiesPagination(t *testing.T) {
	v := announcementPublicView{Query: "更新", Scope: "pinned", Total: 20, Page: 2, Pages: 3, Previous: "/announcements/?page=1", Next: "/announcements/?page=3"}
	p := publicAnnouncements(v)
	if p.Query != v.Query || p.Scope != v.Scope || p.Page != 2 || p.Next != v.Next || p.Previous != v.Previous {
		t.Fatal("announcement navigation lost")
	}
}
