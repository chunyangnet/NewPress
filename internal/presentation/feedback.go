package presentation

type FeedbackChoice struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	Count    int    `json:"count"`
}
type FeedbackReply struct {
	ID       int64  `json:"id"`
	Author   string `json:"author"`
	Body     string `json:"body"`
	Date     string `json:"date"`
	Official bool   `json:"official"`
	Hidden   bool   `json:"hidden"`
}
type FeedbackEvent struct {
	Author string `json:"author"`
	Status string `json:"status"`
	Label  string `json:"label"`
	Note   string `json:"note"`
	Date   string `json:"date"`
}
type FeedbackItem struct {
	ID            int64           `json:"id"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Kind          string          `json:"kind"`
	KindLabel     string          `json:"kind_label"`
	Category      string          `json:"category"`
	Status        string          `json:"status"`
	StatusLabel   string          `json:"status_label"`
	Priority      string          `json:"priority"`
	PriorityLabel string          `json:"priority_label"`
	Author        string          `json:"author"`
	Date          string          `json:"date"`
	Updated       string          `json:"updated"`
	URL           string          `json:"url"`
	Visibility    string          `json:"visibility"`
	Voting        bool            `json:"voting"`
	Pinned        bool            `json:"pinned"`
	Locked        bool            `json:"locked"`
	Private       bool            `json:"private"`
	Pending       bool            `json:"pending"`
	Hidden        bool            `json:"hidden"`
	Own           bool            `json:"own"`
	Voted         bool            `json:"voted"`
	CanVote       bool            `json:"can_vote"`
	CanEdit       bool            `json:"can_edit"`
	CanComment    bool            `json:"can_comment"`
	Votes         int             `json:"votes"`
	ReplyCount    int             `json:"reply_count"`
	Replies       []FeedbackReply `json:"replies"`
	History       []FeedbackEvent `json:"history"`
}
type FeedbackColumn struct {
	Value string         `json:"value"`
	Label string         `json:"label"`
	URL   string         `json:"url"`
	Count int            `json:"count"`
	Items []FeedbackItem `json:"items"`
}
type FeedbackBoard struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Enabled     bool   `json:"enabled"`
}
type Feedback struct {
	Directory    bool             `json:"directory"`
	SignedIn     bool             `json:"signed_in"`
	CanRead      bool             `json:"can_read"`
	CanSubmit    bool             `json:"can_submit"`
	Manage       bool             `json:"manage"`
	Configure    bool             `json:"configure"`
	Roadmap      bool             `json:"roadmap"`
	Slug         string           `json:"slug"`
	URL          string           `json:"url"`
	API          string           `json:"api"`
	CSRF         string           `json:"csrf"`
	Search       string           `json:"search"`
	Status       string           `json:"status"`
	Kind         string           `json:"kind"`
	Category     string           `json:"category"`
	Sort         string           `json:"sort"`
	Scope        string           `json:"scope"`
	View         string           `json:"view"`
	Previous     string           `json:"previous"`
	Next         string           `json:"next"`
	ListURL      string           `json:"list_url"`
	RoadmapURL   string           `json:"roadmap_url"`
	UserID       int64            `json:"user_id"`
	Version      int64            `json:"version"`
	Settings     FeedbackSettings `json:"settings"`
	Boards       []FeedbackBoard  `json:"boards"`
	Items        []FeedbackItem   `json:"items"`
	Item         FeedbackItem     `json:"item"`
	Columns      []FeedbackColumn `json:"columns"`
	Kinds        []FeedbackChoice `json:"kinds"`
	Statuses     []FeedbackChoice `json:"statuses"`
	Categories   []FeedbackChoice `json:"categories"`
	Page         int              `json:"page"`
	Pages        int              `json:"pages"`
	Total        int              `json:"total"`
	AllCount     int              `json:"all_count"`
	DoneCount    int              `json:"done_count"`
	ActiveCount  int              `json:"active_count"`
	PendingCount int              `json:"pending_count"`
}
type FeedbackSettings struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Guidelines         string   `json:"guidelines"`
	SubmitLabel        string   `json:"submit_label"`
	Enabled            bool     `json:"enabled"`
	Submissions        bool     `json:"submissions"`
	Voting             bool     `json:"voting"`
	Comments           bool     `json:"comments"`
	OfficialReplies    bool     `json:"official_replies"`
	Roadmap            bool     `json:"roadmap"`
	History            bool     `json:"history"`
	Moderation         bool     `json:"moderation"`
	PrivateSubmissions bool     `json:"private_submissions"`
	DefaultView        string   `json:"default_view"`
	Accent             string   `json:"accent"`
	PageSize           int      `json:"page_size"`
	Kinds              []string `json:"kinds"`
	Categories         []string `json:"categories"`
}
