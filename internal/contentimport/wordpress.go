package contentimport

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/content"
)

const wxrNamespace = "http://wordpress.org/export/1.2/"

type wxrItem struct {
	Title         string `xml:"title"`
	Link          string `xml:"link"`
	Creator       string `xml:"http://purl.org/dc/elements/1.1/ creator"`
	Body          string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
	Excerpt       string `xml:"http://wordpress.org/export/1.2/excerpt/ encoded"`
	ID            string `xml:"http://wordpress.org/export/1.2/ post_id"`
	Type          string `xml:"http://wordpress.org/export/1.2/ post_type"`
	Status        string `xml:"http://wordpress.org/export/1.2/ status"`
	Slug          string `xml:"http://wordpress.org/export/1.2/ post_name"`
	Date          string `xml:"http://wordpress.org/export/1.2/ post_date_gmt"`
	LocalDate     string `xml:"http://wordpress.org/export/1.2/ post_date"`
	Password      string `xml:"http://wordpress.org/export/1.2/ post_password"`
	CommentStatus string `xml:"http://wordpress.org/export/1.2/ comment_status"`
	Parent        string `xml:"http://wordpress.org/export/1.2/ post_parent"`
	Terms         []struct {
		Domain string `xml:"domain,attr"`
		Name   string `xml:",chardata"`
	} `xml:"category"`
	Comments []struct{} `xml:"http://wordpress.org/export/1.2/ comment"`
	Meta     []struct {
		Key   string `xml:"http://wordpress.org/export/1.2/ meta_key"`
		Value string `xml:"http://wordpress.org/export/1.2/ meta_value"`
	} `xml:"http://wordpress.org/export/1.2/ postmeta"`
	Attachment string `xml:"http://wordpress.org/export/1.2/ attachment_url"`
}

func checkXML(source string) error {
	d := xml.NewDecoder(strings.NewReader(strings.TrimPrefix(source, "\ufeff")))
	depth, tokens, itemDepth, items, channels, versions := 0, 0, 0, 0, 0, 0
	rootSeen := false
	parents := make([]xml.Name, 0, 64)
	fields := map[xml.Name]bool{}
	channelName := xml.Name{Local: "channel"}
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid("WordPress XML 格式无效或包含未定义实体")
		}
		tokens++
		if tokens > 500000 {
			return invalid("XML 节点过多")
		}
		switch v := token.(type) {
		case xml.Directive:
			return invalid("XML 不允许 DTD 或实体声明")
		case xml.ProcInst:
			if v.Target != "xml" || tokens != 1 {
				return invalid("XML 只允许文件开头的一次 XML 声明")
			}
		case xml.CharData:
			if depth == 0 && strings.Trim(string(v), " \t\r\n") != "" {
				return invalid("XML 根元素之外不能包含文本")
			}
		case xml.StartElement:
			depth++
			if depth > 64 {
				return invalid("XML 嵌套层数超过 64")
			}
			if depth == 1 {
				if rootSeen || v.Name != (xml.Name{Local: "rss"}) {
					return invalid("需要单个 WordPress WXR RSS 根元素")
				}
				rootSeen = true
			}
			parent := xml.Name{}
			if len(parents) > 0 {
				parent = parents[len(parents)-1]
			}
			attributes := make(map[xml.Name]bool, len(v.Attr))
			for _, attr := range v.Attr {
				if attributes[attr.Name] {
					return invalid("XML 元素不能含重复属性")
				}
				attributes[attr.Name] = true
			}
			// Unqualified struct tags in encoding/xml also match foreign
			// namespaces. Validate the RSS containers and fields explicitly so
			// another namespace cannot replace or hide imported content.
			if v.Name.Local == "channel" && (v.Name.Space == "" || depth == 2) {
				if depth != 2 || v.Name.Space != "" || channels != 0 {
					return invalid("RSS 必须包含单个直接子元素 channel")
				}
				channels++
			}
			if v.Name.Local == "item" && (v.Name.Space == "" || depth == 3 && parent == channelName) {
				if depth != 3 || parent != channelName || v.Name.Space != "" {
					return invalid("WordPress 内容项必须是 channel 的直接子元素")
				}
				items++
				itemDepth = depth
				fields = map[xml.Name]bool{}
				if items > 1000 {
					return invalid("单份导出最多 1000 项，请拆分文件")
				}
			}
			if v.Name == (xml.Name{Space: wxrNamespace, Local: "wxr_version"}) {
				if depth != 3 || parent != channelName || versions != 0 {
					return invalid("channel 必须包含唯一的 WordPress WXR 版本字段")
				}
				versions++
			}
			if itemDepth > 0 && depth == itemDepth+1 {
				if v.Name.Space != "" && (v.Name.Local == "title" || v.Name.Local == "link" || v.Name.Local == "category") {
					return invalid("WordPress RSS 内容字段的命名空间无效")
				}
				repeated := v.Name.Local == "category" || v.Name.Local == "postmeta" || v.Name.Local == "comment"
				if !repeated {
					if fields[v.Name] {
						return invalid("WordPress 内容含重复字段")
					}
					fields[v.Name] = true
				}
			}
			parents = append(parents, v.Name)
		case xml.EndElement:
			if depth == itemDepth {
				itemDepth = 0
			}
			depth--
			parents = parents[:len(parents)-1]
		}
	}
	if !rootSeen || channels != 1 || versions != 1 {
		return invalid("需要包含单个 channel 和 WXR 1.2 版本字段的 WordPress 导出")
	}
	return nil
}
func parseWXR(file File) ([]Item, []Skipped, []string, error) {
	items, skipped, warnings := []Item{}, []Skipped{}, []string{}
	if err := checkXML(file.Text); err != nil {
		return items, skipped, warnings, err
	}
	var document struct {
		Channel struct {
			Version string    `xml:"http://wordpress.org/export/1.2/ wxr_version"`
			Items   []wxrItem `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(file.Text), &document); err != nil {
		return items, skipped, warnings, invalid("无法读取 WXR 导出")
	}
	if document.Channel.Version != "1.2" {
		return items, skipped, warnings, invalid("首期支持 WordPress WXR 1.2，请从源站重新导出")
	}
	attachments := map[string]string{}
	for _, source := range document.Channel.Items {
		if source.Type == "attachment" {
			attachments[source.ID] = safeSourceURL(source.Attachment)
		}
	}
	for _, source := range document.Channel.Items {
		if !content.ValidType(source.Type) {
			skipped = append(skipped, Skipped{Key: source.ID, Kind: source.Type, Reason: "此类型尚未导入"})
			continue
		}
		id, err := strconv.ParseInt(source.ID, 10, 64)
		if err != nil || id <= 0 {
			return items, skipped, warnings, invalid("WordPress 内容 ID 无效")
		}
		if len(source.Body) > content.MaxContentBytes {
			return items, skipped, warnings, invalid("WordPress 单篇正文不能超过 2 MiB")
		}
		item := Item{Key: strconv.FormatInt(id, 10), Author: source.Creator, SourceURL: safeSourceURL(source.Link), Warnings: []string{}, Images: []string{}}
		encoded, _ := xml.Marshal(source)
		item.Entry.Digest = digest(string(encoded))
		input := content.PostInput{Type: source.Type, Status: source.Status, Slug: source.Slug, Title: source.Title, Excerpt: source.Excerpt, ContentSource: source.Body, CommentStatus: source.CommentStatus}
		if input.CommentStatus == "" {
			input.CommentStatus = content.CommentClosed
		}
		if source.Date != "" && source.Date != "0000-00-00 00:00:00" {
			input.PublishedAt, err = sourceTime(source.Date)
			if err != nil {
				item.Warnings = append(item.Warnings, "UTC 发布时间无效")
			}
		}
		if input.PublishedAt.IsZero() && source.LocalDate != "" {
			item.Warnings = append(item.Warnings, "缺少 UTC 日期，未猜测源站时区")
		}
		if source.Password != "" {
			input.Status = content.StatusPrivate
			item.Warnings = append(item.Warnings, "带密码内容转为私密；不迁移原密码")
		}
		if source.Parent != "" && source.Parent != "0" {
			item.Warnings = append(item.Warnings, "父子页面关系需人工重建")
		}
		for _, term := range source.Terms {
			switch term.Domain {
			case "category":
				item.Entry.Categories = appendUnique(item.Entry.Categories, strings.TrimSpace(term.Name))
			case "post_tag":
				input.TagNames = appendUnique(input.TagNames, strings.TrimSpace(term.Name))
			default:
				item.Warnings = append(item.Warnings, "自定义分类法未导入")
			}
		}
		if len(source.Comments) > 0 {
			item.Warnings = append(item.Warnings, fmt.Sprintf("%d 条评论未导入", len(source.Comments)))
		}
		if len(source.Meta) > 0 {
			item.Warnings = append(item.Warnings, "扩展元信息未执行或导入")
		}
		for _, meta := range source.Meta {
			if meta.Key == "_thumbnail_id" {
				input.CoverURL = attachments[meta.Value]
			}
		}
		if strings.Contains(source.Body, "[") {
			item.Warnings = append(item.Warnings, "正文可能包含短代码，保留文本供人工检查")
		}
		if strings.Contains(source.Body, "<!-- wp:") {
			item.Warnings = append(item.Warnings, "WordPress 块使用安全 HTML 投影；动态块需核对")
		}
		if p, ok := oldPath(item.SourceURL); ok {
			item.Entry.OldPath = p
		} else if source.Link != "" {
			item.Warnings = append(item.Warnings, "来源链接不能自动建立路径重定向")
		}
		item.Entry.Input = input
		items = append(items, item)
	}
	warnings = append(warnings, "作者映射到现有账号；用户密码、评论、附件文件与扩展数据不在本次导入范围")
	return items, skipped, warnings, nil
}
