package presentation

// IdentityBadge is a public display projection. It never contains application
// evidence, administrative permissions or award audit records.
type IdentityBadge struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon"`
	Color       string `json:"color"`
	Background  string `json:"background,omitempty"`
	Style       string `json:"style"`
	Image       string `json:"image,omitempty"`
	GrantedAt   string `json:"granted_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Level       int    `json:"level,omitempty"`
}
type CommentSupport struct {
	Supported bool   `json:"supported"`
	Label     string `json:"label,omitempty"`
}
type Recognition struct {
	CopyrightTitle, CopyrightText string
	TipsEnabled                   bool
	TipAPI                        string
	AuthorBadges, AccountBadges   []IdentityBadge
}
