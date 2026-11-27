// Package messaging owns account-scoped private conversations and inbox notices.
package messaging

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid     = errors.New("invalid message")
	ErrRateLimited = errors.New("message rate limit exceeded")
	ErrConflict    = errors.New("message nonce already used")
	ErrChanged     = errors.New("message file changed outside this store")
)

const (
	PageSize        = 50
	MaxContentRunes = 2000
	MaxContentBytes = 8000
	SendLimit       = 60
)

type Message struct {
	ID          int64      `json:"id"`
	SenderID    int64      `json:"sender_id"`
	RecipientID int64      `json:"recipient_id"`
	Content     string     `json:"content"`
	Nonce       string     `json:"nonce"`
	CreatedAt   time.Time  `json:"created_at"`
	ReadAt      *time.Time `json:"read_at"`
}

type Conversation struct {
	PeerID int64   `json:"peer_id"`
	Last   Message `json:"last"`
	Unread int     `json:"unread"`
}

type Notice struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"user_id"`
	PeerID    int64      `json:"peer_id"`
	MessageID int64      `json:"message_id"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

type Input struct {
	SenderID, RecipientID int64
	Content, Nonce        string
}

type MessageQuery struct{ Before, After int64 }
type NoticeQuery struct {
	Before     int64
	UnreadOnly bool
}
type Inbox struct {
	Items   []Conversation
	Unread  int
	HasMore bool
}
type MessagePage struct {
	Items   []Message
	HasMore bool
	// The last outgoing message read by the peer, including older pages.
	ReadThrough int64
}
type NoticePage struct {
	Items   []Notice
	Unread  int
	HasMore bool
	// A snapshot boundary independent of the active filter and page.
	Through int64
}

type Store interface {
	Conversations(context.Context, int64, int64) (Inbox, error)
	Messages(context.Context, int64, int64, MessageQuery) (MessagePage, error)
	Send(context.Context, Input) (Message, error)
	Read(context.Context, int64, int64, int64) error
	Notices(context.Context, int64, NoticeQuery) (NoticePage, error)
	UpdateNotices(context.Context, int64, int64, string) error
}

func Normalize(input Input) (Input, error) {
	input.Content = strings.TrimSpace(strings.ReplaceAll(input.Content, "\r\n", "\n"))
	if input.SenderID <= 0 || input.RecipientID <= 0 || input.SenderID == input.RecipientID ||
		!utf8.ValidString(input.Content) || utf8.RuneCountInString(input.Content) < 1 || utf8.RuneCountInString(input.Content) > MaxContentRunes || len(input.Content) > MaxContentBytes ||
		len(input.Nonce) < 16 || len(input.Nonce) > 80 {
		return input, ErrInvalid
	}
	for _, char := range input.Content {
		if unicode.IsControl(char) && char != '\n' && char != '\t' {
			return input, ErrInvalid
		}
	}
	for _, char := range input.Nonce {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
			return input, ErrInvalid
		}
	}
	return input, nil
}

func ValidQuery(user, peer int64, query MessageQuery) bool {
	return user > 0 && peer > 0 && user != peer && query.Before >= 0 && query.After >= 0 && (query.Before == 0 || query.After == 0)
}

func cloneMessage(m Message) Message {
	if m.ReadAt != nil {
		at := *m.ReadAt
		m.ReadAt = &at
	}
	return m
}
func cloneNotice(n Notice) Notice {
	if n.ReadAt != nil {
		at := *n.ReadAt
		n.ReadAt = &at
	}
	return n
}
