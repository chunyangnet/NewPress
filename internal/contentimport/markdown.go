package contentimport

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"go.yaml.in/yaml/v3"
	"newpress.local/newpress/internal/content"
)

func parseMarkdown(file File) (Item, error) {
	item := Item{Key: file.Name, Warnings: []string{}, Images: []string{}}
	item.Entry.Digest = digest(file.Text)
	if ext := strings.ToLower(path.Ext(file.Name)); ext != ".md" && ext != ".markdown" {
		return item, invalid("Markdown 文件须使用 .md 或 .markdown 扩展名")
	}
	body := strings.TrimPrefix(file.Text, "\ufeff")
	item.Entry.Input.Title = fileTitle(file.Name)
	item.Entry.Input.Type = content.TypePost
	item.Entry.Input.Status = content.StatusDraft
	item.Entry.Input.CommentStatus = content.CommentClosed
	if strings.HasPrefix(body, "---\n") || strings.HasPrefix(body, "---\r\n") {
		lines := strings.Split(body, "\n")
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimSuffix(lines[i], "\r") == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return item, invalid("Markdown 元信息缺少结束分隔符")
		}
		meta := strings.Join(lines[1:end], "\n")
		if len(meta) > 64<<10 {
			return item, invalid("Markdown 元信息不能超过 64 KiB")
		}
		var root yaml.Node
		decoder := yaml.NewDecoder(strings.NewReader(meta))
		if err := decoder.Decode(&root); err != nil {
			return item, invalid("Markdown 元信息不是有效 YAML")
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); err != io.EOF {
			return item, invalid("每个文件只能有一份 YAML 元信息")
		}
		if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
			return item, invalid("Markdown 元信息必须是字段映射")
		}
		seen := map[string]bool{}
		nodes := root.Content[0].Content
		for i := 0; i < len(nodes); i += 2 {
			k, v := nodes[i], nodes[i+1]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Anchor != "" || v.Anchor != "" || seen[k.Value] {
				return item, invalid("元信息不能含重复字段、锚点或复杂键")
			}
			seen[k.Value] = true
			if v.Kind != yaml.ScalarNode && v.Kind != yaml.SequenceNode {
				return item, invalid("元信息只接受单值和简单数组")
			}
			list := []string{}
			if v.Kind == yaml.SequenceNode {
				for _, n := range v.Content {
					if n.Kind != yaml.ScalarNode || n.Anchor != "" || n.Tag != "!!str" {
						return item, invalid("元信息数组只接受字符串")
					}
					list = append(list, n.Value)
				}
			}
			value := strings.TrimSpace(v.Value)
			input := &item.Entry.Input
			switch k.Value {
			case "categories", "tags":
				if v.Kind == yaml.ScalarNode {
					list = []string{value}
				}
				if k.Value == "categories" {
					item.Entry.Categories = list
				} else {
					input.TagNames = list
				}
			default:
				if v.Kind != yaml.ScalarNode {
					return item, invalid("此元信息字段不接受数组")
				}
				switch k.Value {
				case "title":
					input.Title = value
				case "slug":
					input.Slug = value
				case "type":
					input.Type = value
				case "status":
					input.Status = value
				case "draft":
					if value == "true" {
						input.Status = content.StatusDraft
					} else if value == "false" {
						input.Status = content.StatusPublish
					} else {
						return item, invalid("draft 只能为 true 或 false")
					}
				case "date", "published_at":
					var e error
					input.PublishedAt, e = sourceTime(value)
					if e != nil {
						return item, invalid("日期须为 ISO 8601、YYYY-MM-DD 或 UTC 日期时间")
					}
				case "excerpt", "description":
					input.Excerpt = value
				case "author":
					item.Author = value
				case "cover", "image":
					input.CoverURL = value
				case "permalink":
					if p, ok := oldPath(value); ok {
						item.Entry.OldPath = p
					} else {
						item.Warnings = append(item.Warnings, "permalink 不能转换为站内旧路径，需手动处理")
					}
				default:
					item.Warnings = append(item.Warnings, fmt.Sprintf("元信息字段 %s 未导入", k.Value))
				}
			}
		}
		if seen["date"] && seen["published_at"] || seen["excerpt"] && seen["description"] || seen["cover"] && seen["image"] || seen["draft"] && seen["status"] {
			return item, invalid("同一含义的元信息字段只能填写一次")
		}
		body = strings.Join(lines[end+1:], "\n")
	}
	if len(body) > content.MaxContentBytes {
		return item, invalid("单篇 Markdown 正文不能超过 2 MiB")
	}
	var rendered bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(body), &rendered); err != nil {
		return item, invalid("Markdown 转换失败")
	}
	if len(rendered.Bytes()) > content.MaxContentBytes {
		return item, invalid("Markdown 转换后的正文超过 2 MiB")
	}
	item.Entry.Input.ContentSource = rendered.String()
	if strings.Contains(body, "<") {
		item.Warnings = append(item.Warnings, "Markdown 内嵌 HTML 按解析器安全规则处理，请检查预览")
	}
	return item, nil
}
