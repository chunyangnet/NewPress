package presentation

import "time"

type AnnouncementItem struct {
	ID         int64
	Title, URL string
	Date       time.Time
	Pinned     bool
}
type AnnouncementParagraph struct {
	Text    string
	Heading bool
}
type AnnouncementPageLink struct {
	Number  int
	URL     string
	Current bool
}
type AnnouncementPage struct {
	Items                                    []AnnouncementItem
	Current                                  AnnouncementItem
	PreviousItem, NextItem                   *AnnouncementItem
	Paragraphs                               []AnnouncementParagraph
	RelatedURL, Query, Scope                 string
	Total, PinnedTotal, Results, Page, Pages int
	Previous, Next                           string
	PageLinks                                []AnnouncementPageLink
}

// Questions is a public page projection. Reports, receipts, hidden answers and
// repository objects are deliberately absent; permissions come from the core.
type Questions struct {
	Items, Featured                                         []Question
	Question                                                Question
	SignedIn, Manage, New                                   bool
	UserID                                                  int64
	CSRF, Query, Status, Tag, Scope, Sort                   string
	Tags                                                    []string
	Total, Page, Pages, PublicTotal, OpenTotal, SolvedTotal int
	Previous, Next, AllURL, OpenURL, SolvedURL              string
	Topics                                                  []QuestionTopic
}

type Question struct {
	ID, Version, AuthorID, AcceptedID                          int64
	Title, Body, HTML, URL, Author, AcceptedURL, AnswerPreview string
	Tags                                                       []string
	CreatedAt, UpdatedAt                                       time.Time
	Hidden, Locked, CanEdit, CanAccept                         bool
	Count                                                      int
	AnswersView                                                []QuestionAnswer
	NotesView                                                  []QuestionNote
}

type QuestionAnswer struct {
	ID, AuthorID        int64
	Body, HTML, Author  string
	CreatedAt, EditedAt time.Time
	CanEdit, Accepted   bool
	NotesView           []QuestionNote
}

type QuestionNote struct {
	ID, AuthorID int64
	Author, Body string
	At           time.Time
}

type QuestionTopic struct {
	Name  string
	Count int
}

// Flash carries only fields already authorized by the public/editor route.
type Flash struct {
	UserID                                              int64
	Items                                               []FlashItem
	Current                                             FlashItem
	Total                                               int
	Query, Filter, PreviousURL, NextURL, CSRF, Endpoint string
	Authenticated, Admin, Editing                       bool
}

type FlashItem struct {
	ID, Version                                                                       int64
	Title, Body, HTML, URL, Slug, Status, StatusLabel, Day, Time, PublishedAt, Author string
	SourceName, SourceURL, ReviewNote                                                 string
	Important, Comments                                                               bool
}
