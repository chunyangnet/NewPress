package aieditor

import (
	"errors"
	"regexp"
	"strings"
)

const newsBriefHelp = "请补充具体事件和来源原文；只写“今日新闻”等主题还不能成稿。当前写作不自动检索新闻，有原稿可勾选“参考当前草稿”。"

// Deliberately conservative: this catches bare news requests, not arbitrary
// short topics. It is guidance, not a claim to verify the supplied facts.
// Keep the client equivalent covered by the shared testdata/briefs.json cases.
var briefFormatting = regexp.MustCompile(`[\s\x{00a0}\x{3000}，,。.!！?？:：;；、/／_—“”"'‘’（）()\-]+`)
var genericNewsBrief = regexp.MustCompile(`^(请|帮我|给我|写|生成|整理|一篇|一则|一份|篇|则|份|关于)*(今日|今天|最新|最近|近期|本周|当日|每日|热点|时事)?的?(科技|财经|国际|国内|社会)?(新闻|消息)(汇总|摘要|简报|报道|文章|吧)*$`)
var emptyDraftMarkup = regexp.MustCompile(`<[^<>]*>|&nbsp;|&#160;|&#x[aA]0;`)

func (g Generation) validateBrief(selected Prompt) error {
	if selected.ID != "article" && selected.ID != "news" && selected.ID != "essay" {
		return nil
	}
	if strings.TrimSpace(emptyDraftMarkup.ReplaceAllString(g.Draft.Content, "")) != "" {
		return nil
	}
	// An edited instruction may contain the actual source material. Style
	// references are intentionally not considered evidence for the article.
	if instruction := strings.TrimSpace(g.Instruction); instruction != "" && instruction != selected.Instruction {
		return nil
	}
	brief := strings.TrimSpace(g.Brief)
	if genericNewsBrief.MatchString(briefFormatting.ReplaceAllString(brief, "")) || selected.ID == "news" && brief == selected.Example {
		return errors.New(newsBriefHelp)
	}
	return nil
}
