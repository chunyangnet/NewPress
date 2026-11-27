package content

// AI metadata participates in the same optimistic write and revision as the post.
func ApplyAIMetadata(meta map[string]any, input PostInput) {
	ApplyResourceMetadata(meta, input)
	ApplyArticleBadgeMetadata(meta, input)
	ApplyFlashMetadata(meta, input)
	if input.AIPolicy != "" {
		meta["ai_pro_max"] = input.AIPolicy
	}
	if input.AITopics != "" {
		meta["ai_pro_max_topics"] = input.AITopics
	}
}
func DefaultExcerpt(post Post) bool {
	return post.Excerpt == "" || post.Excerpt == generatedExcerpt(post.ContentHTML)
}
