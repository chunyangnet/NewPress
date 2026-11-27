package plugin

const AIProMax = "ai-pro-max"

// AIProMaxManifest installs the reviewed host-side AI Pro Max console. Model
// access and each linked action continue to use NewPress's existing capability
// checks; the package itself does not receive provider credentials.
func AIProMaxManifest() Manifest {
	return Manifest{
		Schema: 1, Slug: AIProMax, Name: "AI Pro Max", Version: "1.1.0",
		Description: "文章补全、周期写作、相关话题、配图与评论自动化。",
		Category:    "content", Tags: []string{"AI", "写作", "SEO", "内容创作"},
		Author: "NewPress 官方", License: "Apache-2.0",
		NewPress: "builtin/ai-pro-max-v1", Runtime: "builtin/ai-pro-max-v1",
		Entry: "builtin.json", Capabilities: []string{},
	}
}
