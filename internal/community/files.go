package community

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
	"newpress.local/newpress/internal/packagefs"
)

var fileIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validAttachment(f Attachment) bool {
	return fileIDPattern.MatchString(f.ID) && fileIDPattern.MatchString(f.SHA256) && textValid(f.Name, 1, 140, false) && !strings.ContainsAny(f.Name, `/\`+"\x00") && f.Size > 0 && f.Size <= MaxAttachmentBytes && f.AuthorID > 0 && f.TopicID >= 0 && f.ReplyID >= 0 && !f.CreatedAt.IsZero() && allowedFileMIME(f.MIME)
}
func allowedFileMIME(m string) bool {
	switch m {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "application/pdf", "text/plain", "application/zip":
		return true
	}
	return false
}

// Files stores private objects outside the public media namespace. Only the
// circle HTTP gateway can serve them after checking the current membership.
type Files struct {
	mu        sync.Mutex
	uploads   sync.Mutex
	uncertain string
	root      string
	memory    map[string][]byte
}

func attachmentID(slug, key string, user int64) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", slug, user, key)))
	return hex.EncodeToString(hash[:])
}

// Upload serializes staging and metadata commits. A definite business rejection
// removes its unreferenced object; an ambiguous storage failure retains it and
// admits only the same retry until recovery, bounding otherwise invisible files.
func (f *Files) Upload(ctx context.Context, store Store, slug string, actor Actor, key string, version int64, name string, reader io.Reader, now time.Time) (Attachment, Result, error) {
	f.uploads.Lock()
	defer f.uploads.Unlock()
	id := attachmentID(slug, key, actor.ID)
	if f.uncertain != "" && f.uncertain != id {
		return Attachment{}, Result{}, errors.New("community: an attachment commit needs retry or startup recovery")
	}
	file, err := f.Stage(ctx, slug, key, actor.ID, name, reader)
	if err != nil {
		return Attachment{}, Result{}, err
	}
	result, err := store.Apply(ctx, slug, actor, Command{Action: "add_attachment", Key: key, Version: version, Attachment: &file}, now)
	definite := err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrCapacity)
	if !definite {
		f.uncertain = id
		return file, result, err
	}
	current, readErr := store.Get(ctx, slug)
	if readErr != nil {
		f.uncertain = id
		return file, result, errors.Join(err, readErr)
	}
	if persisted, exists := AttachmentByID(current, file.ID); exists {
		file = persisted
	} else {
		if removeErr := f.Remove(file.ID); removeErr != nil {
			f.uncertain = id
			return file, result, errors.Join(err, removeErr)
		}
		if err == nil {
			err = ErrConflict // The old upload receipt outlived the removed file.
		}
	}
	f.uncertain = ""
	return file, result, err
}

func NewFiles(root string) (*Files, error) {
	f := &Files{root: root, memory: map[string][]byte{}}
	if root != "" {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		if info, statErr := packagefs.LstatNoSymlink(absolute); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		} else if statErr == nil && !info.IsDir() {
			return nil, fmt.Errorf("community file root is not a directory: %s", absolute)
		}
		f.root = absolute
	}
	return f, nil
}
func (f *Files) Stage(ctx context.Context, slug, key string, user int64, name string, reader io.Reader) (Attachment, error) {
	if !ValidSlug(slug) || !keyPattern.MatchString(key) || user <= 0 {
		return Attachment{}, ErrInvalid
	}
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if !textValid(name, 1, 140, false) || strings.ContainsAny(name, `/\`) {
		return Attachment{}, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxAttachmentBytes+1))
	if err != nil {
		return Attachment{}, err
	}
	if len(data) == 0 {
		return Attachment{}, ErrInvalid
	}
	if len(data) > MaxAttachmentBytes {
		return Attachment{}, ErrCapacity
	}
	mime := strings.Split(http.DetectContentType(data), ";")[0]
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		mime = "image/webp"
	}
	if !allowedFileMIME(mime) {
		return Attachment{}, ErrInvalid
	}
	extensions := map[string]string{".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf", ".txt": "text/plain", ".zip": "application/zip"}
	if extensions[strings.ToLower(filepath.Ext(name))] != mime {
		return Attachment{}, ErrInvalid
	}
	if mime == "text/plain" && !utf8.Valid(data) {
		return Attachment{}, ErrInvalid
	}
	if strings.HasPrefix(mime, "image/") {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 16384 || cfg.Height > 16384 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
			return Attachment{}, ErrInvalid
		}
	}
	digest := sha256.Sum256(data)
	file := Attachment{ID: attachmentID(slug, key, user), Name: name, MIME: mime, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), AuthorID: user}
	if err = ctx.Err(); err != nil {
		return Attachment{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.root == "" {
		if old, ok := f.memory[file.ID]; ok && !bytes.Equal(old, data) {
			return Attachment{}, ErrConflict
		}
		f.memory[file.ID] = bytes.Clone(data)
		return file, nil
	}
	target := filepath.Join(f.root, file.ID)
	if err = packagefs.MkdirAllNoSymlink(f.root, 0700); err != nil {
		return Attachment{}, err
	}
	if old, err := f.readLocked(file.ID); err == nil {
		if !bytes.Equal(old, data) {
			return Attachment{}, ErrConflict
		}
		return file, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Attachment{}, err
	}
	tmp, err := os.CreateTemp(f.root, ".upload-*")
	if err != nil {
		return Attachment{}, err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return Attachment{}, err
	}
	if closeErr != nil {
		return Attachment{}, closeErr
	}
	if err = ctx.Err(); err != nil {
		return Attachment{}, err
	}
	if err = packagefs.ReplaceRegularFileNoSymlink(tmp.Name(), target); err != nil {
		return Attachment{}, err
	}
	return file, nil
}
func (f *Files) readLocked(id string) ([]byte, error) {
	if !fileIDPattern.MatchString(id) {
		return nil, ErrInvalid
	}
	if f.root == "" {
		data, ok := f.memory[id]
		if !ok {
			return nil, os.ErrNotExist
		}
		return bytes.Clone(data), nil
	}
	file, info, err := packagefs.OpenRegularNoSymlink(filepath.Join(f.root, id))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > MaxAttachmentBytes {
		return nil, ErrCapacity
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxAttachmentBytes+1))
	if len(data) > MaxAttachmentBytes {
		return nil, ErrCapacity
	}
	return data, err
}
func (f *Files) Read(ctx context.Context, file Attachment) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := f.readLocked(file.ID)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != file.Size || hex.EncodeToString(digest[:]) != file.SHA256 {
		return nil, ErrInvalid
	}
	return data, nil
}
func (f *Files) Remove(id string) error {
	if !fileIDPattern.MatchString(id) {
		return ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.root == "" {
		delete(f.memory, id)
		return nil
	}
	target := filepath.Join(f.root, id)
	if _, err := packagefs.LstatNoSymlink(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Remove(target)
}

// RemoveUnreferenced coordinates with uploads before checking canonical
// references, including reuse of a key after its receipt has expired.
func (f *Files) RemoveUnreferenced(ctx context.Context, store Store, slug string, candidates []Attachment) error {
	f.uploads.Lock()
	defer f.uploads.Unlock()
	current, err := store.Get(ctx, slug)
	if err != nil {
		return err
	}
	for _, file := range candidates {
		if _, exists := AttachmentByID(current, file.ID); !exists {
			if err = f.Remove(file.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (f *Files) ValidateStore(ctx context.Context, store Store) error {
	items, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, h := range items {
		c, err := store.Get(ctx, h.Slug)
		if err != nil {
			return err
		}
		for _, file := range c.Attachments {
			if _, err = f.Read(ctx, file); err != nil {
				return fmt.Errorf("circle attachment %s: %w", file.ID, err)
			}
		}
	}
	return nil
}

// Reconcile runs only at service startup, after fact recovery and while the data
// directory is exclusively locked. Offline backup validation never calls it.
func (f *Files) Reconcile(ctx context.Context, store Store) error {
	f.uploads.Lock()
	defer f.uploads.Unlock()
	if err := f.ValidateStore(ctx, store); err != nil {
		return err
	}
	circles, err := store.List(ctx)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, header := range circles {
		c, err := store.Get(ctx, header.Slug)
		if err != nil {
			return err
		}
		for _, file := range c.Attachments {
			used[file.ID] = true
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.root == "" {
		for id := range f.memory {
			if !used[id] {
				delete(f.memory, id)
			}
		}
	} else {
		if _, err = packagefs.LstatNoSymlink(f.root); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		entries, err := os.ReadDir(f.root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if used[name] || !fileIDPattern.MatchString(name) && !strings.HasPrefix(name, ".upload-") {
				continue
			}
			target := filepath.Join(f.root, name)
			info, err := packagefs.LstatNoSymlink(target)
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("invalid circle object %s: %w", name, errors.Join(ErrInvalid, err))
			}
			if err = os.Remove(target); err != nil {
				return err
			}
		}
	}
	f.uncertain = ""
	return nil
}
