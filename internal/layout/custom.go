package layout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxCustomBlocks = 12
const MaxCustomBlocksBytes = 64 << 10

type CustomBlock struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	HTML       string `json:"html"`
	Surface    bool   `json:"surface"`
	Visibility string `json:"visibility"`
}

var customKeyPattern = regexp.MustCompile(`^custom(?:_[a-z0-9]{1,32})?$`)

// Empty text and [] both represent an empty collection.
func ParseCustomBlocks(raw string) ([]CustomBlock, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > MaxCustomBlocksBytes {
		return nil, fmt.Errorf("HTML 组件总内容不能超过 64 KiB")
	}
	var blocks []CustomBlock
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&blocks); err != nil || blocks == nil {
		return nil, fmt.Errorf("HTML 组件格式不正确")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("HTML 组件格式不正确")
	}
	if len(blocks) > MaxCustomBlocks {
		return nil, fmt.Errorf("最多添加 %d 个 HTML 组件", MaxCustomBlocks)
	}
	seen := map[string]bool{}
	for i, block := range blocks {
		if !customKeyPattern.MatchString(block.Key) || seen[block.Key] {
			return nil, fmt.Errorf("第 %d 个 HTML 组件的标识无效或重复", i+1)
		}
		seen[block.Key] = true
		if utf8.RuneCountInString(block.Title) > 80 || utf8.RuneCountInString(block.HTML) > 4096 {
			return nil, fmt.Errorf("第 %d 个 HTML 组件的标题最多 80 字，内容最多 4096 字", i+1)
		}
		if !slices.Contains([]string{"both", "desktop", "mobile", "hidden"}, block.Visibility) {
			return nil, fmt.Errorf("第 %d 个 HTML 组件的显示设备无效", i+1)
		}
	}
	return blocks, nil
}

func customBlocks(values map[string]any) []CustomBlock {
	raw, _ := values["home_custom_blocks"].(string)
	blocks, _ := ParseCustomBlocks(raw)
	return blocks
}

// EditorCustomJSON returns a deterministic representation for the form.
func EditorCustomJSON(values map[string]any) string {
	blocks := customBlocks(values)
	if blocks == nil {
		blocks = []CustomBlock{}
	}
	slices.SortFunc(blocks, func(a, b CustomBlock) int { return strings.Compare(a.Key, b.Key) })
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(blocks)
	return strings.TrimSuffix(encoded.String(), "\n")
}

func catalogFor(values map[string]any) []Region {
	result := Catalog()
	for i := range result {
		modules := []Block{}
		for _, block := range result[i].Modules {
			// Themes opt into modules through their schema defaults.
			if block.Setting != "" {
				if _, supported := values[block.Setting]; !supported {
					continue
				}
			}
			if block.Key != "custom" {
				modules = append(modules, block)
				continue
			}
			for _, custom := range customBlocks(values) {
				item := block
				item.Key, item.Visibility, item.Custom = custom.Key, custom.Visibility, &custom
				if strings.TrimSpace(custom.Title) != "" {
					item.Title = custom.Title
				}
				modules = append(modules, item)
			}
		}
		result[i].Modules = modules
	}
	return result
}
