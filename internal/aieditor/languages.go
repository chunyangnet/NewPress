package aieditor

import (
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
	"strings"
)

type OutputLanguage struct{ Code, Name string }

func LanguageName(code string) string {
	if code == "auto" {
		return "自动识别并沿用原文或选题的语言"
	}
	if len(code) < 2 || len(code) > 35 || strings.ContainsAny(code, " _") {
		return ""
	}
	tag, err := language.Parse(code)
	if err != nil || tag == language.Und {
		return ""
	}
	return display.Chinese.Languages().Name(tag)
}

func OutputLanguages() []OutputLanguage {
	codes := strings.Fields("auto zh-CN zh-TW en ja ko es fr de pt ar af am as ay az ba be bg bh bi bm bn bo br bs ca ce ch co cr cs cu cv cy da dv dz ee el eo et eu fa ff fi fj fo fy ga gd gl gn gu gv ha he hi ho hr ht hu hy hz ia id ie ig ii ik io is it iu jv ka kg ki kj kk kl km kn kr ks ku kv kw ky la lb lg li ln lo lt lu lv mg mh mi mk ml mn mr ms mt my na nb nd ne ng nl nn no nr nv ny oc oj om or os pa pi pl ps pt-BR qu rm rn ro ru rw sa sc sd se sg si sk sl sm sn so sq sr ss st su sv sw ta te tg th ti tk tl tn to tr ts tt tw ty ug uk ur uz ve vi vo wa wo xh yi yo za zu")
	out := make([]OutputLanguage, 0, len(codes))
	for _, code := range codes {
		name := LanguageName(code)
		if name != "" {
			out = append(out, OutputLanguage{code, name})
		}
	}
	return out
}
