package httpserver

import (
	"io"
	"strings"
	"unicode/utf8"

	htmlparser "golang.org/x/net/html"
)

const (
	revisionBlockMaxCount        = 200
	revisionBlockMaxDepth        = 64
	revisionBlockMaxPreviewRunes = 160
)

type adminRevisionBlockDiffView struct {
	Available     bool
	Unavailable   string
	Rows          []adminRevisionBlockRowView
	FromCount     int
	ToCount       int
	AddedCount    int
	RemovedCount  int
	ReplacedCount int
}

type adminRevisionBlockRowView struct {
	Kind      string
	Label     string
	From      adminRevisionBlockSideView
	To        adminRevisionBlockSideView
	Collapsed int
}

type adminRevisionBlockSideView struct {
	Number  int
	Label   string
	Preview string
	Source  string
}

type revisionSourceBlock struct {
	Source string
	Label  string
}

// Comparison uses complete, balanced top-level source spans, not a repaired
// DOM or persistent block identities. Exact bytes (including whitespace) are
// the equality key. Unsupported structure falls back to the existing source
// diff; none of these reads changes a revision or editable content_source.
func buildRevisionBlockDiff(from, to string) adminRevisionBlockDiffView {
	left, leftReason := revisionSourceBlocks(from)
	right, rightReason := revisionSourceBlocks(to)
	if leftReason != "" || rightReason != "" {
		reason := "正文包含不能可靠分块的 HTML，继续使用下方源码差异核对。"
		if leftReason == "capacity" || rightReason == "capacity" {
			reason = "正文超过每侧 64 KiB 或 200 块的比较范围，继续使用下方有界源码差异核对。"
		}
		return adminRevisionBlockDiffView{Unavailable: reason}
	}
	leftSource, rightSource := make([]string, len(left)), make([]string, len(right))
	for i, block := range left {
		leftSource[i] = block.Source
	}
	for i, block := range right {
		rightSource[i] = block.Source
	}
	rows, added, removed, replaced := pairRevisionDiffOperations(revisionDiffOperations(leftSource, rightSource))
	out := adminRevisionBlockDiffView{
		Available: true, FromCount: len(left), ToCount: len(right),
		AddedCount: added, RemovedCount: removed, ReplacedCount: replaced,
	}
	labels := map[string]string{"equal": "上下文", "added": "新增", "removed": "删除", "changed": "替换"}
	for _, row := range collapseRevisionEqualRows(rows) {
		item := adminRevisionBlockRowView{Kind: row.Kind, Collapsed: row.Collapsed, Label: labels[row.Kind]}
		if row.FromLine > 0 {
			item.From = revisionBlockSide(row.FromLine, left[row.FromLine-1])
		}
		if row.ToLine > 0 {
			item.To = revisionBlockSide(row.ToLine, right[row.ToLine-1])
		}
		out.Rows = append(out.Rows, item)
	}
	return out
}

func revisionBlockSide(number int, block revisionSourceBlock) adminRevisionBlockSideView {
	return adminRevisionBlockSideView{Number: number, Label: block.Label, Source: block.Source,
		Preview: revisionBlockPreview(block.Source)}
}

func revisionBlockPreview(source string) string {
	var parts []string
	tokens := htmlparser.NewTokenizer(strings.NewReader(source))
	for kind := tokens.Next(); kind != htmlparser.ErrorToken; kind = tokens.Next() {
		if kind == htmlparser.TextToken {
			if text := strings.TrimSpace(string(tokens.Text())); text != "" {
				parts = append(parts, text)
			}
		}
	}
	preview := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	if preview == "" {
		preview = source
	}
	runes := []rune(preview)
	if len(runes) > revisionBlockMaxPreviewRunes {
		return string(runes[:revisionBlockMaxPreviewRunes]) + "…"
	}
	return preview
}

func revisionSourceBlocks(source string) ([]revisionSourceBlock, string) {
	if len(source) > revisionDiffMaxSourceBytes {
		return nil, "capacity"
	}
	if !utf8.ValidString(source) {
		return nil, "structure"
	}
	tokens := htmlparser.NewTokenizer(strings.NewReader(source))
	blocks := make([]revisionSourceBlock, 0, 16)
	stack := make([]string, 0, 8)
	offset, start, label := 0, 0, ""
	appendBlock := func() bool {
		if len(blocks) == revisionBlockMaxCount {
			return false
		}
		blocks = append(blocks, revisionSourceBlock{Source: source[start:offset], Label: label})
		start = offset
		return true
	}
	for {
		kind := tokens.Next()
		if kind == htmlparser.ErrorToken {
			// EOF can follow incomplete markup: also check consumed byte coverage
			// and the explicit stack, rather than trusting the tokenizer alone.
			if tokens.Err() != io.EOF || len(stack) != 0 || offset != len(source) {
				return nil, "structure"
			}
			break
		}
		raw := tokens.Raw()
		offset += len(raw)
		switch kind {
		case htmlparser.TextToken:
			if len(stack) == 0 && strings.TrimSpace(string(raw)) != "" {
				return nil, "structure"
			}
		case htmlparser.CommentToken:
			if !strings.HasPrefix(string(raw), "<!--") || !strings.HasSuffix(string(raw), "-->") {
				return nil, "structure"
			}
			if len(stack) == 0 {
				label = "保留 HTML"
				if !appendBlock() {
					return nil, "capacity"
				}
			}
		case htmlparser.StartTagToken, htmlparser.SelfClosingTagToken:
			token := tokens.Token()
			if token.Data == "svg" || token.Data == "math" || revisionImplicitClose(stack, token.Data) {
				return nil, "structure"
			}
			if len(stack) == 0 {
				label = revisionBlockLabel(token)
			}
			if revisionVoidElement(token.Data) {
				if len(stack) == 0 && !appendBlock() {
					return nil, "capacity"
				}
				continue
			}
			if kind == htmlparser.SelfClosingTagToken || len(stack) == revisionBlockMaxDepth {
				return nil, "structure"
			}
			stack = append(stack, token.Data)
		case htmlparser.EndTagToken:
			token := tokens.Token()
			if len(stack) == 0 || stack[len(stack)-1] != token.Data {
				return nil, "structure"
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 && !appendBlock() {
				return nil, "capacity"
			}
		default:
			return nil, "structure"
		}
	}
	// Preserve trailing whitespace, including a whitespace-only document, so
	// formatting-only edits cannot be reported as byte-identical content.
	if start != len(source) {
		if len(blocks) == 0 {
			blocks = append(blocks, revisionSourceBlock{Source: source, Label: "空白"})
		} else {
			blocks[len(blocks)-1].Source += source[start:]
		}
	}
	return blocks, ""
}

func revisionImplicitClose(stack []string, tag string) bool {
	// Common HTML elements close an open paragraph implicitly. Reject that
	// shape instead of turning the browser's repaired tree into source spans.
	closesParagraph := false
	switch tag {
	case "address", "article", "aside", "blockquote", "details", "div", "dl", "fieldset", "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr", "main", "menu", "nav", "ol", "p", "pre", "section", "table", "ul":
		closesParagraph = true
	}
	if closesParagraph {
		for _, open := range stack {
			if open == "p" {
				return true
			}
		}
	}
	return false
}

func revisionVoidElement(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}

func revisionBlockLabel(token htmlparser.Token) string {
	switch token.Data {
	case "p":
		return "段落"
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return "标题"
	case "blockquote":
		return "引用"
	case "pre":
		return "代码"
	case "table":
		return "表格"
	case "ol":
		return "有序列表"
	case "ul":
		return "无序列表"
	case "figure", "img":
		return "图片 / 图文"
	case "hr":
		return "分隔线"
	case "aside":
		for _, attr := range token.Attr {
			if attr.Key == "data-callout" && (attr.Val == "note" || attr.Val == "tip" || attr.Val == "warning") {
				return "提示"
			}
		}
	}
	return "保留 HTML"
}
