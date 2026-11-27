package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

const maxDocumentBytes = 64 << 20

type document struct {
	NextID   int64     `json:"next_id"`
	Messages []Message `json:"messages"`
	Notices  []Notice  `json:"notices"`
}
type FileStore struct {
	mu     sync.RWMutex
	path   string
	data   document
	digest [sha256.Size]byte
	exists bool
}

func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, data: document{NextID: 1, Messages: []Message{}, Notices: []Notice{}}}
	if path == "" {
		return s, nil
	}
	var err error
	s.path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	body, exists, err := readMessageFile(s.path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return s, nil
	}
	// A persisted document must supply its own required fields.
	s.data = document{}
	if err := packagefs.DecodeJSONInto(bytes.NewReader(body), maxDocumentBytes, "messages", &s.data); err != nil {
		return nil, ErrInvalid
	}
	if err := validateDocument(s.data); err != nil {
		return nil, fmt.Errorf("validate messages: %w", err)
	}
	s.digest, s.exists = sha256.Sum256(body), true
	return s, nil
}

func readMessageFile(path string) ([]byte, bool, error) {
	file, before, err := packagefs.OpenRegularNoSymlink(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	if before.Size() > maxDocumentBytes {
		return nil, false, ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxDocumentBytes {
		return nil, false, ErrInvalid
	}
	after, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	current, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return nil, false, err
	}
	if !os.SameFile(before, after) || !os.SameFile(after, current) || before.Size() != int64(len(body)) || before.Size() != after.Size() || after.Size() != current.Size() || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(current.ModTime()) {
		return nil, false, ErrChanged
	}
	return body, true, nil
}

func (s *FileStore) unchanged() error {
	if s.path == "" {
		return nil
	}
	body, exists, err := readMessageFile(s.path)
	if err != nil {
		return err
	}
	if exists != s.exists || exists && sha256.Sum256(body) != s.digest {
		return ErrChanged
	}
	return nil
}

func validateDocument(d document) error {
	if d.NextID < 1 || d.Messages == nil || d.Notices == nil {
		return ErrInvalid
	}
	messages := make(map[int64]Message, len(d.Messages))
	nonces := make(map[struct {
		sender int64
		nonce  string
	}]bool, len(d.Messages))
	var last int64
	for _, m := range d.Messages {
		input, err := Normalize(Input{SenderID: m.SenderID, RecipientID: m.RecipientID, Content: m.Content, Nonce: m.Nonce})
		key := struct {
			sender int64
			nonce  string
		}{m.SenderID, m.Nonce}
		if err != nil || input.Content != m.Content || m.ID <= last || m.ID >= d.NextID || m.CreatedAt.IsZero() || nonces[key] || (m.ReadAt != nil && m.ReadAt.Before(m.CreatedAt)) {
			return ErrInvalid
		}
		last = m.ID
		nonces[key] = true
		messages[m.ID] = m
	}
	last = 0
	for _, n := range d.Notices {
		m, ok := messages[n.MessageID]
		if !ok || n.ID != m.ID || n.ID <= last || n.UserID != m.RecipientID || n.PeerID != m.SenderID || !n.CreatedAt.Equal(m.CreatedAt) || (n.ReadAt != nil && n.ReadAt.Before(n.CreatedAt)) {
			return ErrInvalid
		}
		last = n.ID
	}
	return nil
}

func (s *FileStore) change(ctx context.Context, update func(*document) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.unchanged(); err != nil {
		return err
	}
	d := document{NextID: s.data.NextID, Messages: append([]Message{}, s.data.Messages...), Notices: append([]Notice{}, s.data.Notices...)}
	if err := update(&d); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path != "" {
		body, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if len(body) > maxDocumentBytes || len(d.Messages) > len(s.data.Messages) && messageAdmissionBytes(d, len(body)) > maxDocumentBytes {
			return errors.New("message storage capacity exceeded")
		}
		if err := packagefs.MkdirAllNoSymlink(filepath.Dir(s.path), 0700); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(s.path), ".messages-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if err := file.Chmod(0600); err != nil {
			file.Close()
			return err
		}
		if _, err := file.Write(body); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := s.unchanged(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := packagefs.ReplaceRegularFileNoSymlink(file.Name(), s.path); err != nil {
			return err
		}
		s.digest, s.exists = sha256.Sum256(body), true
	}
	s.data = d
	return nil
}

func messageAdmissionBytes(d document, encodedBytes int) int64 {
	// Replacing JSON null with even a fractional, offset timestamp adds fewer
	// than 40 bytes. Reserve each unread field, including older messages, only
	// when admitting a new message. Reads consume that space and replays add none.
	required := int64(encodedBytes)
	for _, m := range d.Messages {
		if m.ReadAt == nil {
			required += 40
		}
	}
	for _, n := range d.Notices {
		if n.ReadAt == nil {
			required += 40
		}
	}
	return required
}

func (s *FileStore) Conversations(ctx context.Context, user, before int64) (Inbox, error) {
	result := Inbox{Items: []Conversation{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if user <= 0 || before < 0 {
		return result, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	byPeer := map[int64]Conversation{}
	for _, m := range s.data.Messages {
		if m.SenderID != user && m.RecipientID != user {
			continue
		}
		peer := m.SenderID
		if peer == user {
			peer = m.RecipientID
		}
		c := byPeer[peer]
		c.PeerID = peer
		c.Last = cloneMessage(m)
		if m.RecipientID == user && m.ReadAt == nil {
			c.Unread++
			result.Unread++
		}
		byPeer[peer] = c
	}
	for _, c := range byPeer {
		if before == 0 || c.Last.ID < before {
			result.Items = append(result.Items, c)
		}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Last.ID > result.Items[j].Last.ID })
	result.HasMore = len(result.Items) > PageSize
	if result.HasMore {
		result.Items = result.Items[:PageSize]
	}
	return result, nil
}

func (s *FileStore) Messages(ctx context.Context, user, peer int64, query MessageQuery) (MessagePage, error) {
	result := MessagePage{Items: []Message{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !ValidQuery(user, peer, query) {
		return result, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.data.Messages {
		if m.SenderID == user && m.RecipientID == peer && m.ReadAt != nil {
			result.ReadThrough = max(result.ReadThrough, m.ID)
		}
	}
	for step := 0; step < len(s.data.Messages); step++ {
		i := len(s.data.Messages) - 1 - step
		if query.After > 0 {
			i = step
		}
		m := s.data.Messages[i]
		if (query.Before > 0 && m.ID >= query.Before) || (query.After > 0 && m.ID <= query.After) {
			continue
		}
		if !(m.SenderID == user && m.RecipientID == peer || m.SenderID == peer && m.RecipientID == user) {
			continue
		}
		if len(result.Items) == PageSize {
			result.HasMore = true
			break
		}
		result.Items = append(result.Items, cloneMessage(m))
	}
	return result, nil
}

func (s *FileStore) Send(ctx context.Context, input Input) (Message, error) {
	input, err := Normalize(input)
	if err != nil {
		return Message{}, err
	}
	var result Message
	err = s.change(ctx, func(d *document) error {
		count := 0
		now := time.Now().UTC()
		for _, m := range d.Messages {
			if m.SenderID != input.SenderID {
				continue
			}
			if m.Nonce == input.Nonce {
				if m.RecipientID != input.RecipientID || m.Content != input.Content {
					return ErrConflict
				}
				result = cloneMessage(m)
				return nil
			}
			if m.CreatedAt.After(now.Add(-10 * time.Minute)) {
				count++
			}
		}
		if count >= SendLimit {
			return ErrRateLimited
		}
		result = Message{ID: d.NextID, SenderID: input.SenderID, RecipientID: input.RecipientID, Content: input.Content, Nonce: input.Nonce, CreatedAt: now}
		d.NextID++
		d.Messages = append(d.Messages, result)
		d.Notices = append(d.Notices, Notice{ID: result.ID, UserID: input.RecipientID, PeerID: input.SenderID, MessageID: result.ID, CreatedAt: now})
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	return result, nil
}

func (s *FileStore) Read(ctx context.Context, user, peer, through int64) error {
	if !ValidQuery(user, peer, MessageQuery{}) || through <= 0 {
		return ErrInvalid
	}
	return s.change(ctx, func(d *document) error {
		valid := false
		for _, m := range d.Messages {
			if m.ID == through && (m.RecipientID == user && m.SenderID == peer || m.SenderID == user && m.RecipientID == peer) {
				valid = true
				break
			}
		}
		if !valid {
			return ErrInvalid
		}
		now := time.Now().UTC()
		for i, m := range d.Messages {
			if m.RecipientID == user && m.SenderID == peer && m.ID <= through && m.ReadAt == nil {
				d.Messages[i].ReadAt = readReceiptAt(now, m.CreatedAt)
			}
		}
		for i, n := range d.Notices {
			if n.UserID == user && n.PeerID == peer && n.MessageID <= through && n.ReadAt == nil {
				d.Notices[i].ReadAt = readReceiptAt(now, n.CreatedAt)
			}
		}
		return nil
	})
}

func readReceiptAt(now, created time.Time) *time.Time {
	// A restored message can be ahead of the clock; its receipt must remain valid.
	if created.After(now) {
		now = created
	}
	return &now
}

func (s *FileStore) Notices(ctx context.Context, user int64, query NoticeQuery) (NoticePage, error) {
	result := NoticePage{Items: []Notice{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if user <= 0 || query.Before < 0 {
		return result, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.data.Notices) - 1; i >= 0; i-- {
		n := s.data.Notices[i]
		if n.UserID != user {
			continue
		}
		result.Through = max(result.Through, n.ID)
		if n.ReadAt == nil {
			result.Unread++
		}
		if (query.Before > 0 && n.ID >= query.Before) || (query.UnreadOnly && n.ReadAt != nil) {
			continue
		}
		if len(result.Items) >= PageSize {
			result.HasMore = true
			continue
		}
		result.Items = append(result.Items, cloneNotice(n))
	}
	return result, nil
}

func (s *FileStore) UpdateNotices(ctx context.Context, user, through int64, action string) error {
	if user <= 0 || through <= 0 || action != "read" && action != "read_all" && action != "clear" {
		return ErrInvalid
	}
	return s.change(ctx, func(d *document) error {
		now := time.Now().UTC()
		kept := []Notice{}
		for _, n := range d.Notices {
			if n.UserID == user && n.ID <= through && (action != "read" || n.ID == through) {
				if action == "clear" {
					continue
				}
				if n.ReadAt == nil {
					n.ReadAt = readReceiptAt(now, n.CreatedAt)
				}
			}
			kept = append(kept, n)
		}
		d.Notices = kept
		return nil
	})
}
