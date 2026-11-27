package media

// UploadSource is persisted with the object so background jobs and derivatives
// use the same routing decision, independently of the current HTTP request.
type UploadSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func UploadSources() []UploadSource {
	return []UploadSource{
		{"media-library", "媒体库上传"}, {"media-api", "媒体 API / 外部客户端"},
		{"editor-body", "编辑器正文（含粘贴、拖拽）"}, {"editor-cover", "文章 / 页面封面"},
		{"editor-component", "文章组件图片"}, {"series-cover", "选集封面"},
		{"settings-image", "后台设置图片（站点 / 主题 / 商品 / 勋章）"},
		{"avatar", "账号头像"}, {"ai-image", "AI 图片生成"},
		{"ai-pro-image", "AI 自动配图"}, {"wechat-import", "微信文章图片导入"},
		{"link-logo", "友链图标（上传 / 自动提取）"},
	}
}

func ValidUploadSource(source string) bool {
	for _, entry := range UploadSources() {
		if entry.ID == source {
			return true
		}
	}
	return false
}

func checksumKey(checksum, source string) string {
	return checksum + ":" + source
}
