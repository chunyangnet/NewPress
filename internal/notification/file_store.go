package notification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"newpress.local/newpress/internal/activity"
	"newpress.local/newpress/internal/packagefs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"
)

const maxFileBytes = 64 << 20

type document struct {
	Schema      int                   `json:"schema"`
	Kind        string                `json:"kind"`
	NextID      int64                 `json:"next_id"`
	Preferences map[int64]Preferences `json:"preferences"`
	Receipts    map[string]time.Time  `json:"receipts"`
	Notices     []Notice              `json:"notices"`
	Jobs        map[string]Job        `json:"jobs"`
}

func emptyDocument() document {
	return document{Schema: 1, Kind: "newpress.notifications", NextID: 1, Preferences: map[int64]Preferences{}, Receipts: map[string]time.Time{}, Notices: []Notice{}, Jobs: map[string]Job{}}
}
func (d document) preferences(user int64) Preferences {
	if p, ok := d.Preferences[user]; ok {
		return p
	}
	return Defaults(user)
}

type FileStore struct {
	mu     sync.RWMutex
	path   string
	digest [32]byte
	state  document
}

var _ Store = (*FileStore)(nil)

func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{state: emptyDocument()}
	if path == "" {
		return s, nil
	}
	var err error
	s.path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := readFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	s.state, err = packagefs.DecodeJSON[document](bytes.NewReader(data), maxFileBytes, "notification store")
	if err != nil {
		return nil, err
	}
	var shape struct {
		Preferences map[string]json.RawMessage `json:"preferences"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, err
	}
	for key := range shape.Preferences {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != key {
			return nil, ErrInvalid
		}
	}
	if err = validateDocument(s.state); err != nil {
		return nil, err
	}
	s.digest = sha256.Sum256(data)
	return s, nil
}
func readFile(path string) ([]byte, error) {
	f, before, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("notification store exceeds size limit")
	}
	after, err := packagefs.LstatNoSymlink(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != int64(len(data)) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, errors.New("notification file changed during read")
	}
	return data, nil
}

func validateDocument(d document) error {
	if d.Schema != 1 || d.Kind != "newpress.notifications" || d.NextID <= 0 || d.Preferences == nil || d.Receipts == nil || d.Jobs == nil {
		return ErrInvalid
	}
	for id, p := range d.Preferences {
		key, err := hex.DecodeString(p.UnsubscribeKey)
		if id <= 0 || p.UserID != id || p.Version <= 0 || p.UpdatedAt.IsZero() || err != nil || len(key) != 32 || p.Email && p.EmailSince.IsZero() || p.Weekly && p.WeeklySince.IsZero() {
			return ErrInvalid
		}
	}
	seen := map[int64]bool{}
	for _, n := range d.Notices {
		if n.ID <= 0 || n.ID >= d.NextID || seen[n.ID] || n.CreatedAt.IsZero() {
			return ErrInvalid
		}
		if err := ValidateDelivery(n.Delivery); err != nil {
			return err
		}
		seen[n.ID] = true
	}
	for key, at := range d.Receipts {
		if !validReceipt(key) || at.IsZero() {
			return ErrInvalid
		}
	}
	for key, j := range d.Jobs {
		if key != j.Key || !validStoredJobKey(key, j, d.Receipts) || j.UserID <= 0 || j.Attempts < 0 || j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() || j.AvailableAt.IsZero() {
			return ErrInvalid
		}
		if j.Kind == Weekly {
			if j.Event.Due.IsZero() || j.Event.At.IsZero() {
				return ErrInvalid
			}
		} else if err := ValidateDelivery(j.Delivery); err != nil {
			return err
		}
		switch j.State {
		case "pending", "sent", "failed", "uncertain", "cancelled":
		case "sending":
			if j.LeaseUntil == nil {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

func (s *FileStore) readyLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := readFile(s.path)
	if errors.Is(err, os.ErrNotExist) && s.digest == ([32]byte{}) {
		return nil
	}
	if err != nil {
		return err
	}
	if sha256.Sum256(data) != s.digest {
		return errors.New("notification file changed outside this process")
	}
	return nil
}
func (s *FileStore) Ready(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.readyLocked()
}
func (s *FileStore) change(ctx context.Context, fn func(*document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.readyLocked(); err != nil {
		return err
	}
	raw, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	var candidate document
	original := raw
	if err = json.Unmarshal(raw, &candidate); err != nil {
		return err
	}
	if err = fn(&candidate); err != nil {
		return err
	}
	raw, err = json.Marshal(candidate)
	if err != nil {
		return err
	}
	if len(raw) > maxFileBytes {
		return errors.New("notification store exceeds 64 MiB")
	}
	// New records must not use the space needed to claim/finish queued mail,
	// mark notices read/cleared, or save existing notification preferences.
	// Existing transitions consume that reserve without reserving it again.
	if admitsNotificationEntries(s.state, candidate) && notificationAdmissionBytes(candidate, len(raw)) > maxFileBytes {
		return errors.New("notification store has insufficient space for record updates")
	}
	if bytes.Equal(raw, original) {
		return ctx.Err()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if s.path != "" {
		if err = writeFile(s.path, raw); err != nil {
			return err
		}
		s.digest = sha256.Sum256(raw)
	}
	s.state = candidate
	return nil
}
func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := packagefs.MkdirAllNoSymlink(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".notifications-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = packagefs.ReplaceRegularFileNoSymlink(f.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if dirFile, err := packagefs.OpenDirectoryNoSymlink(dir); err == nil {
			_ = dirFile.Sync()
			_ = dirFile.Close()
		}
	}
	return nil
}
func (s *FileStore) Preferences(ctx context.Context, user int64) (Preferences, error) {
	if user <= 0 {
		return Preferences{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.preferences(user), ctx.Err()
}
func (s *FileStore) SavePreferences(ctx context.Context, input Preferences, now time.Time) (Preferences, error) {
	var p Preferences
	err := s.change(ctx, func(d *document) error {
		var err error
		p, err = UpdatedPreferences(d.preferences(input.UserID), input, now)
		if err == nil {
			d.Preferences[p.UserID] = p
			cancelDisabledMail(d, p, now)
		}
		return err
	})
	return p, err
}
func (s *FileStore) Unsubscribe(ctx context.Context, user int64, key string, now time.Time) error {
	return s.change(ctx, func(d *document) error {
		p := d.preferences(user)
		if p.UnsubscribeKey == "" || subtle.ConstantTimeCompare([]byte(key), []byte(p.UnsubscribeKey)) != 1 {
			return ErrInvalid
		}
		if !p.Email && !p.Weekly {
			return nil
		}
		p.Email = false
		p.Weekly = false
		p.Version++
		p.UpdatedAt = now
		d.Preferences[user] = p
		cancelDisabledMail(d, p, now)
		return nil
	})
}

func cancelDisabledMail(d *document, p Preferences, now time.Time) {
	for key, j := range d.Jobs {
		if j.UserID != p.UserID || j.State != "pending" && j.State != "failed" {
			continue
		}
		if j.Kind == Weekly && p.Weekly || j.Kind != Weekly && WantsEmail(p, j.Delivery) {
			continue
		}
		j.State = "cancelled"
		j.UpdatedAt = now
		d.Jobs[key] = j
	}
}
func (s *FileStore) Project(ctx context.Context, receipt string, deliveries []Delivery, now time.Time) error {
	if err := ValidateProjection(receipt, deliveries, now); err != nil {
		return err
	}
	return s.change(ctx, func(d *document) error {
		if _, ok := d.Receipts[receipt]; ok {
			return nil
		}
		d.Receipts[receipt] = now
		seen := map[string]bool{}
		for _, delivery := range deliveries {
			key := JobKey(receipt, delivery)
			if seen[key] {
				continue
			}
			seen[key] = true
			p := d.preferences(delivery.UserID)
			if !p.Allows(delivery.Kind) {
				continue
			}
			delivery = cloneDelivery(delivery)
			d.Notices = append(d.Notices, Notice{ID: d.NextID, Delivery: delivery, CreatedAt: now})
			d.NextID++
			if WantsEmail(p, delivery) {
				d.Jobs[key] = NewJob(key, delivery, now)
			}
		}
		return nil
	})
}

func cloneDelivery(d Delivery) Delivery {
	d.Event.Mentions = append([]string(nil), d.Event.Mentions...)
	d.Event.Recipients = append([]int64(nil), d.Event.Recipients...)
	return d
}

func (s *FileStore) Notices(ctx context.Context, user int64, query Query) (Page, error) {
	if user <= 0 || query.Before < 0 {
		return Page{}, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := Page{Items: []Notice{}}
	for i := len(s.state.Notices) - 1; i >= 0; i-- {
		n := s.state.Notices[i]
		if n.UserID != user || n.ClearedAt != nil {
			continue
		}
		p.Through = max(p.Through, n.ID)
		if n.ReadAt == nil {
			p.Unread++
		}
		if query.Before > 0 && n.ID >= query.Before || query.UnreadOnly && n.ReadAt != nil || query.Kind != "" && query.Kind != n.Kind {
			continue
		}
		if len(p.Items) == PageSize {
			p.HasMore = true
			continue
		}
		if n.ReadAt != nil {
			at := *n.ReadAt
			n.ReadAt = &at
		}
		n.Event.Mentions = append([]string(nil), n.Event.Mentions...)
		n.Event.Recipients = append([]int64(nil), n.Event.Recipients...)
		p.Items = append(p.Items, n)
	}
	return p, ctx.Err()
}
func (s *FileStore) UpdateNotices(ctx context.Context, user, through int64, action string, now time.Time) error {
	if user <= 0 || through <= 0 || action != "read" && action != "read_all" && action != "clear" {
		return ErrInvalid
	}
	return s.change(ctx, func(d *document) error {
		for i := range d.Notices {
			n := &d.Notices[i]
			if n.UserID != user || n.ID > through || action == "read" && n.ID != through {
				continue
			}
			if action == "clear" {
				n.ClearedAt = &now
			} else if n.ReadAt == nil {
				n.ReadAt = &now
			}
		}
		return nil
	})
}
func (s *FileStore) Subscribers(ctx context.Context, after int64, limit int) ([]Preferences, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	items := []Preferences{}
	for id, p := range s.state.Preferences {
		if id > after && p.Weekly {
			items = append(items, p)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UserID < items[j].UserID })
	return items[:min(len(items), limit)], ctx.Err()
}
func (s *FileStore) QueueWeekly(ctx context.Context, user int64, end, now time.Time) error {
	return s.change(ctx, func(d *document) error {
		p := d.preferences(user)
		if !p.Weekly || !p.WeeklySince.Before(end) {
			return nil
		}
		key := WeeklyKey(user, end)
		if _, ok := d.Jobs[key]; ok {
			return nil
		}
		d.Jobs[key] = NewJob(key, Delivery{UserID: user, Kind: Weekly, Event: activity.Event{At: end.AddDate(0, 0, -7), Due: end}}, now)
		return nil
	})
}
func (s *FileStore) ClaimMail(ctx context.Context, now time.Time) (Job, bool, error) {
	var selected Job
	found := false
	err := s.change(ctx, func(d *document) error {
		for key, j := range d.Jobs {
			if j.State == "sending" && j.LeaseUntil != nil && !j.LeaseUntil.After(now) {
				j.State = "uncertain"
				j.LeaseUntil = nil
				j.UpdatedAt = now
				d.Jobs[key] = j
			}
			if j.State != "pending" || j.AvailableAt.After(now) {
				continue
			}
			if !found || j.AvailableAt.Before(selected.AvailableAt) || j.AvailableAt.Equal(selected.AvailableAt) && j.Key < selected.Key {
				selected = j
				found = true
			}
		}
		if found {
			lease := now.Add(time.Minute)
			selected.State = "sending"
			selected.Attempts++
			selected.LeaseUntil = &lease
			selected.UpdatedAt = now
			d.Jobs[selected.Key] = selected
			// The worker owns its snapshot, including the lease pointer.
			selected.Delivery = cloneDelivery(selected.Delivery)
			leaseSnapshot := lease
			selected.LeaseUntil = &leaseSnapshot
		}
		return nil
	})
	if err != nil {
		return Job{}, false, err
	}
	return selected, found, nil
}
func (s *FileStore) FinishMail(ctx context.Context, job Job, state string, now time.Time) error {
	return s.change(ctx, func(d *document) error {
		current, ok := d.Jobs[job.Key]
		// Attempts can repeat after a manual retry. Match the persisted claim's
		// timestamps as well so a delayed worker cannot finish a newer claim.
		if !ok || job.State != "sending" || current.State != "sending" || current.Attempts != job.Attempts || !current.UpdatedAt.Equal(job.UpdatedAt) || current.LeaseUntil == nil || job.LeaseUntil == nil || !current.LeaseUntil.Equal(*job.LeaseUntil) {
			return ErrConflict
		}
		finished, err := Finished(current, state, now)
		if err == nil {
			d.Jobs[job.Key] = finished
		}
		return err
	})
}
func (s *FileStore) MailStatus(ctx context.Context, user int64) (MailStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result MailStatus
	for _, j := range s.state.Jobs {
		if j.UserID != user {
			continue
		}
		switch j.State {
		case "pending", "sending":
			result.Pending++
		case "failed":
			result.Failed++
		case "uncertain":
			result.Uncertain++
		}
	}
	return result, ctx.Err()
}
func (s *FileStore) RetryFailed(ctx context.Context, user int64, now time.Time) error {
	return s.change(ctx, func(d *document) error {
		for key, j := range d.Jobs {
			if j.UserID == user && j.State == "failed" {
				j.State = "pending"
				j.Attempts = 0
				j.AvailableAt = now
				j.UpdatedAt = now
				d.Jobs[key] = j
			}
		}
		return nil
	})
}
