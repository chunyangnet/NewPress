package presentation

import "html/template"

type Circle struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Rules           string `json:"rules"`
	URL             string `json:"url"`
	ManageURL       string `json:"manage_url,omitempty"`
	JoinPolicy      string `json:"join_policy"`
	JoinLabel       string `json:"join_label"`
	Visibility      string `json:"visibility"`
	VisibilityLabel string `json:"visibility_label"`
	PostPolicy      string `json:"post_policy"`
	PostLabel       string `json:"post_label"`
	JoinReason      bool   `json:"join_reason"`
	Role            string `json:"role"`
	RoleLabel       string `json:"role_label"`
	Owner           string `json:"owner"`
	Version         int64  `json:"version"`
	CanRead         bool   `json:"can_read"`
	CanPost         bool   `json:"can_post"`
	CanJoin         bool   `json:"can_join"`
	CanLeave        bool   `json:"can_leave"`
	Manage          bool   `json:"manage"`
	Own             bool   `json:"own"`
	MemberCount     int    `json:"member_count"`
	TopicCount      int    `json:"topic_count"`
	ReplyCount      int    `json:"reply_count"`
	PendingCount    int    `json:"pending_count"`
	Archived        bool   `json:"archived"`
	CanReport       bool   `json:"can_report"`
	OpenReports     int    `json:"open_reports"`
	TopicsUsed      int    `json:"topics_used"`
	TopicLimit      int    `json:"topic_limit"`
	FilesUsed       int    `json:"files_used"`
	FilesBytes      int64  `json:"files_bytes"`
	FilesLimit      int64  `json:"files_limit"`
}
type CircleMember struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Label    string `json:"label"`
	Change   bool   `json:"change"`
	Transfer bool   `json:"transfer"`
	Reason   string `json:"reason,omitempty"`
	Joined   string `json:"joined"`
}
type CircleReply struct {
	ID          int64              `json:"id"`
	Author      string             `json:"author"`
	Body        string             `json:"body"`
	Date        string             `json:"date"`
	Hidden      bool               `json:"hidden"`
	Accepted    bool               `json:"accepted"`
	CanEdit     bool               `json:"can_edit"`
	Edited      string             `json:"edited,omitempty"`
	Format      string             `json:"format"`
	BodyHTML    template.HTML      `json:"body_html"`
	Attachments []CircleAttachment `json:"attachments,omitempty"`
	CanDelete   bool               `json:"can_delete"`
}
type CircleTopic struct {
	ID          int64              `json:"id"`
	Title       string             `json:"title"`
	Body        string             `json:"body,omitempty"`
	Kind        string             `json:"kind"`
	Author      string             `json:"author"`
	URL         string             `json:"url"`
	Date        string             `json:"date"`
	Pinned      bool               `json:"pinned"`
	Featured    bool               `json:"featured"`
	Locked      bool               `json:"locked"`
	Hidden      bool               `json:"hidden"`
	AcceptedID  int64              `json:"accepted_id"`
	CanAccept   bool               `json:"can_accept"`
	CanEdit     bool               `json:"can_edit"`
	Edited      string             `json:"edited,omitempty"`
	ReplyCount  int                `json:"reply_count"`
	Replies     []CircleReply      `json:"replies,omitempty"`
	Format      string             `json:"format"`
	BodyHTML    template.HTML      `json:"body_html,omitempty"`
	Attachments []CircleAttachment `json:"attachments,omitempty"`
	Archived    bool               `json:"archived"`
	CanDelete   bool               `json:"can_delete"`
}
type CircleAttachment struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	MIME    string `json:"mime"`
	Size    int64  `json:"size"`
	Image   bool   `json:"image"`
	Unbound bool   `json:"unbound"`
}
type CircleReport struct {
	ID       int64  `json:"id"`
	TopicID  int64  `json:"topic_id"`
	ReplyID  int64  `json:"reply_id"`
	Reporter string `json:"reporter"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
	Status   string `json:"status"`
	Note     string `json:"note"`
	Date     string `json:"date"`
	URL      string `json:"url"`
}
type CircleChange struct {
	Version int64  `json:"version"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Date    string `json:"date"`
}
type Circles struct {
	Directory       bool               `json:"directory"`
	SignedIn        bool               `json:"signed_in"`
	CanCreate       bool               `json:"can_create"`
	CanManage       bool               `json:"can_manage"`
	UserID          int64              `json:"user_id"`
	CSRF            string             `json:"csrf"`
	Items           []Circle           `json:"items"`
	Circle          Circle             `json:"circle"`
	Topics          []CircleTopic      `json:"topics"`
	Topic           CircleTopic        `json:"topic"`
	Members         []CircleMember     `json:"members"`
	Page            int                `json:"page"`
	Pages           int                `json:"pages"`
	Total           int                `json:"total"`
	Previous        string             `json:"previous"`
	Next            string             `json:"next"`
	MemberPage      int                `json:"member_page"`
	MemberPages     int                `json:"member_pages"`
	MemberPrevious  string             `json:"member_previous"`
	MemberNext      string             `json:"member_next"`
	Query           string             `json:"query"`
	Scope           string             `json:"scope"`
	Kind            string             `json:"kind"`
	Filter          string             `json:"filter"`
	Sort            string             `json:"sort"`
	Reports         []CircleReport     `json:"reports,omitempty"`
	Changes         []CircleChange     `json:"changes,omitempty"`
	Files           []CircleAttachment `json:"files,omitempty"`
	ReportPrevious  string             `json:"report_previous"`
	ReportNext      string             `json:"report_next"`
	HistoryPrevious string             `json:"history_previous"`
	HistoryNext     string             `json:"history_next"`
}
