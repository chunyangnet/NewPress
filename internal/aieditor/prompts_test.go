package aieditor

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWritingPreferencesAndIntegrityAcrossTemplates(t *testing.T) {
	for _, prompt := range Prompts() {
		g := Generation{PromptID: prompt.ID, Brief: "整理这些材料", Tone: "professional", Length: "medium", Language: "zh-CN", Draft: Draft{Content: "原稿包含示例代码和事实"}, WritingStyle: "restrained", StyleReference: "短句开头。接下来说明完整原因。", AvoidPhrases: "赋能、总而言之", Instruction: "自定义语气"}
		messages, err := g.Messages(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{"不靠同义词轮换", "不要故意打乱顺序", "不将示例事实", "直接引用、代码、URL", "篇幅是参考", "只输出最终成稿", "<newpress-meta>"} {
			if !strings.Contains(messages[0].Content, expected) {
				t.Errorf("%s lost instruction %s", prompt.ID, expected)
			}
		}
		for _, expected := range []string{g.StyleReference, g.AvoidPhrases, "更克制，少套话", "自定义语气"} {
			if !strings.Contains(messages[1].Content, expected) {
				t.Errorf("%s lost preference %s", prompt.ID, expected)
			}
		}
	}
	g := Generation{PromptID: "article", Brief: "内容", Tone: "natural", Length: "short", Language: "zh-CN"}
	for _, change := range []func(*Generation){func(g *Generation) { g.WritingStyle = "invalid" }, func(g *Generation) { g.StyleReference = strings.Repeat("字", 2001) }, func(g *Generation) { g.AvoidPhrases = strings.Repeat("字", 1001) }} {
		invalid := g
		change(&invalid)
		if _, err := invalid.Messages(nil); err == nil {
			t.Fatal("invalid writing preferences accepted")
		}
	}
}

func TestGenericNewsBriefRequiresMaterial(t *testing.T) {
	base := Generation{PromptID: "article", Tone: "natural", Length: "medium", Language: "zh-CN"}
	for _, brief := range []string{"今日新闻", "今日新闻。", " 今天的新闻 ", "最新消息"} {
		generation := base
		generation.Brief = brief
		if _, err := generation.Messages(nil); err == nil || err.Error() != newsBriefHelp {
			t.Fatalf("brief %q was accepted without material: %v", brief, err)
		}
	}

	news := base
	news.PromptID = "news"
	news.Brief = "今日新闻"
	if _, err := news.Messages(nil); err == nil {
		t.Fatal("news template accepted a generic brief")
	}

	news.Brief = "根据市交通局 6 月 1 日通报，3 路公交改道，写清受影响站点和替代线路。"
	messages, err := news.Messages(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messages[1].Content, "3 路公交") || !strings.Contains(messages[1].Content, "具体事件和来源") {
		t.Fatal("news material or fact boundary missing")
	}

	// A generic label is valid for a rewrite when an actual draft is present;
	// the editor must never discard a user's source material just because its
	// brief is short.
	humanize := Generation{PromptID: "humanize", Brief: "今日新闻", Tone: "natural", Length: "short", Language: "zh-CN", Draft: Draft{Content: "已有事实稿"}}
	if _, err := humanize.Messages(nil); err != nil {
		t.Fatalf("rewrite with a draft was rejected: %v", err)
	}
}

func TestBriefGuidanceSharedCases(t *testing.T) {
	data, err := os.ReadFile("testdata/briefs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name              string `json:"name"`
		PromptID          string `json:"prompt_id"`
		Brief             string `json:"brief"`
		Draft             string `json:"draft"`
		DraftTitle        string `json:"draft_title"`
		StyleReference    string `json:"style_reference"`
		CustomInstruction string `json:"custom_instruction"`
		Blocked           bool   `json:"blocked"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, item := range cases {
		t.Run(item.Name, func(t *testing.T) {
			g := Generation{PromptID: item.PromptID, Brief: item.Brief, Tone: "natural", Length: "medium", Language: "zh-CN", Draft: Draft{Title: item.DraftTitle, Content: item.Draft}, StyleReference: item.StyleReference}
			for _, prompt := range Prompts() {
				if prompt.ID == item.PromptID {
					g.Instruction = prompt.Instruction
				}
			}
			if item.CustomInstruction != "" {
				g.Instruction = item.CustomInstruction
			}
			messages, err := g.Messages(nil)
			if item.Blocked {
				if err == nil || err.Error() != newsBriefHelp || len(messages) != 0 {
					t.Fatalf("missing material should stop before a provider request: %v", err)
				}
			} else if err != nil {
				t.Fatalf("valid brief rejected: %v", err)
			}
		})
	}
}

func TestNewsStarterRequiresActualSource(t *testing.T) {
	for _, prompt := range Prompts() {
		if prompt.ID != "news" {
			continue
		}
		g := Generation{PromptID: prompt.ID, Brief: prompt.Example, Tone: "natural", Length: "short", Language: "zh-CN"}
		if _, err := g.Messages(nil); err == nil || err.Error() != newsBriefHelp {
			t.Fatalf("unfilled news starter accepted: %v", err)
		}
		g.Brief += "\n图书馆公告：11 月 15 日起恢复周日开放。"
		messages, err := g.Messages(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range []string{"判断—例子—解释", "没有联网检索能力", "不代表你已打开", "不得把‘今日新闻’改成", "引用、代码、URL"} {
			if !strings.Contains(messages[0].Content, rule) {
				t.Errorf("missing writing boundary %q", rule)
			}
		}
		return
	}
	t.Fatal("news prompt missing")
}
