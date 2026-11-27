package aieditor

import (
	"strings"
	"testing"
)

func TestDocumentMetadataMarkdownAndSanitization(t *testing.T) {
	raw := "<newpress-meta>" + `{"title":"一个清楚的标题","slug":"clear-title","excerpt":"一段清楚的摘要","tags":["Go","go","写作"],"category_ids":[2,999,2],"cover_prompt":"纸张与自然光"}` + "</newpress-meta>\n\n## 开始写作\n\n有 **重点** 的完整正文。\n\n```html\n<script>alert(1)</script>\n```\n\n<script>bad()</script>\n\n![tracking](https://tracker.example/pixel)\n\n[bad](javascript:alert(1))\n\n| 内容 | 描述 |\n| --- | --- |\n| 正文 | 文章 |"
	doc, err := ParseDocument(raw, []Category{{2, "技术"}})
	if err != nil || doc.Title != "一个清楚的标题" || doc.Slug != "clear-title" || doc.Excerpt != "一段清楚的摘要" || len(doc.Tags) != 2 || len(doc.CategoryIDs) != 1 || doc.CategoryIDs[0] != 2 {
		t.Fatalf("document = %#v, %v", doc, err)
	}
	for _, bad := range []string{"<script", "<img", "tracker.example", "javascript:", "bad()", "<newpress-meta>"} {
		if strings.Contains(doc.ContentSource, bad) {
			t.Errorf("unsafe markup survived: %q in %s", bad, doc.ContentSource)
		}
	}
	for _, want := range []string{"<strong>重点</strong>", "&lt;script&gt;alert(1)&lt;/script&gt;", "<table>", "<h2>开始写作</h2>"} {
		if !strings.Contains(doc.ContentSource, want) {
			t.Errorf("lost safe markup %q: %s", want, doc.ContentSource)
		}
	}
	doc.Markdown = "## 手工修改\n\n修改后仍然是完整正文。"
	updated, err := PrepareDocument(doc, nil)
	if err != nil || strings.Contains(updated.ContentSource, "开始写作") || !strings.Contains(updated.ContentSource, "手工修改") {
		t.Fatal("manual Markdown edits not reflected", err)
	}
}

func TestDocumentFallbackAndMalformedMetadata(t *testing.T) {
	for _, raw := range []string{"# 从标题开始\n\n完整的正文。", "```markdown\n# 从标题开始\n\n完整的正文。\n```", "<newpress-meta>{oops}</newpress-meta>\n# 从标题开始\n\n完整的正文。"} {
		doc, err := ParseDocument(raw, nil)
		if err != nil || doc.Title != "从标题开始" || doc.Excerpt == "" || !slugPattern.MatchString(doc.Slug) || strings.Contains(doc.ContentSource, "<h1>") {
			t.Fatal("fallback did not produce a usable document", doc, err)
		}
	}
	for _, raw := range []string{"", "<newpress-meta>{\"title\":\"unfinished\"}", "<script>alert(1)</script>", strings.Repeat("x", MaxOutputBytes+1)} {
		if _, err := ParseDocument(raw, nil); err == nil {
			t.Fatal("invalid document accepted")
		}
	}
}

func TestPromptContractAndDraftLimits(t *testing.T) {
	generation := Generation{PromptID: "tutorial", Brief: "写一个 Go 教程", Tone: "natural", Length: "medium", Language: "zh-CN"}
	messages, err := generation.Messages([]Category{{1, "技术"}})
	if err != nil || len(messages) != 2 || !strings.Contains(messages[0].Content, "<newpress-meta>") || !strings.Contains(messages[1].Content, "前置条件") || !strings.Contains(messages[1].Content, "技术") {
		t.Fatal("incomplete generation contract", messages, err)
	}
	generation.PromptID = "rewrite"
	if _, err := generation.Messages(nil); err == nil {
		t.Fatal("rewrite accepted without draft")
	}
	generation.Draft = Draft{Title: "初稿", Content: "<p>原文</p>"}
	generation.Instruction = "保留所有例子"
	messages, err = generation.Messages(nil)
	if err != nil || !strings.Contains(messages[1].Content, "保留所有例子") || !strings.Contains(messages[1].Content, "原文") {
		t.Fatal("custom prompt or reference draft lost")
	}
	generation.Draft.Content = strings.Repeat("x", (128<<10)+1)
	if _, err := generation.Messages(nil); err == nil {
		t.Fatal("oversized draft accepted")
	}

	humanize := Generation{PromptID: "humanize", Brief: "去掉这篇文章的 AI 感", Tone: "natural", Length: "medium", Language: "zh-CN", Draft: Draft{Content: "原文"}}
	messages, err = humanize.Messages(nil)
	if err != nil || !strings.Contains(messages[0].Content, "虚假‘不是……而是……’对比") || !strings.Contains(messages[1].Content, "去掉这篇文章的 AI 感") {
		t.Fatal("humanizer prompt contract missing", messages, err)
	}
}
