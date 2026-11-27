package objectstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/media"
)

// SetCommunity is called before starting workers. Private attachments never
// enter the public media library or receive publicly accessible download URLs.
func (s *Service) SetCommunity(store community.Store, files *community.Files) {
	s.circles, s.circleFiles = store, files
}

func circleObjectKey(slug, id string) string { return "private/circles/" + slug + "/" + id }

func (s *Service) storageInventory() ([]media.StorageObject, error) {
	objects := s.media.StorageInventory()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	links, err := s.linkInventory(ctx)
	if err != nil {
		return nil, err
	}
	objects = append(objects, links...)
	if s.circles == nil || s.circleFiles == nil {
		return objects, nil
	}
	headers, err := s.circles.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, header := range headers {
		circle, err := s.circles.Get(ctx, header.Slug)
		if err != nil {
			return nil, err
		}
		for _, file := range circle.Attachments {
			objects = append(objects, media.StorageObject{ItemID: 1, Filename: file.Name, UploadSource: "circle-attachment", ImageObject: media.ImageObject{ObjectKey: circleObjectKey(circle.Slug, file.ID), MIMEType: file.MIME, SizeBytes: file.Size, ChecksumSHA256: file.SHA256}})
		}
	}
	return objects, nil
}

func (s *Service) circleFile(ctx context.Context, key string) (community.Attachment, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "private" || parts[1] != "circles" || s.circles == nil || s.circleFiles == nil {
		return community.Attachment{}, media.ErrNotFound
	}
	circle, err := s.circles.Get(ctx, parts[2])
	if err != nil {
		return community.Attachment{}, err
	}
	file, ok := community.AttachmentByID(circle, parts[3])
	if !ok {
		return file, media.ErrNotFound
	}
	return file, nil
}

type memoryReader struct{ *bytes.Reader }

func (memoryReader) Close() error { return nil }

func (s *Service) openStorageObject(ctx context.Context, id int64, key string) (media.Item, media.ReadSeekCloser, error) {
	if strings.HasPrefix(key, "links/") {
		return s.openLink(ctx, id, key)
	}
	if !strings.HasPrefix(key, "private/") {
		return s.media.OpenStorageObject(id, key)
	}
	f, err := s.circleFile(ctx, key)
	if err != nil {
		return media.Item{}, nil, err
	}
	data, err := s.circleFiles.Read(ctx, f)
	if err != nil {
		return media.Item{}, nil, err
	}
	return media.Item{ID: 1, UploadSource: "circle-attachment", ObjectKey: key, Filename: f.Name, SizeBytes: f.Size, MIMEType: f.MIME, ChecksumSHA256: f.SHA256}, memoryReader{bytes.NewReader(data)}, nil
}

func (s *Service) hasStorageObject(ctx context.Context, id int64, key string) (bool, error) {
	if strings.HasPrefix(key, "links/") {
		_, reader, err := s.openLink(ctx, id, key)
		if err == nil {
			reader.Close()
			return true, nil
		}
		if errors.Is(err, media.ErrNotFound) || errors.Is(err, linkpage.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if !strings.HasPrefix(key, "private/") {
		return s.media.HasStorageObject(id, key), nil
	}
	_, err := s.circleFile(ctx, key)
	if errors.Is(err, media.ErrNotFound) || errors.Is(err, community.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ReadCircle must only be called after the HTTP gateway's membership check.
// Proxying (rather than redirecting) also works with WebDAV and public buckets.
func (s *Service) ReadCircle(ctx context.Context, slug string, file community.Attachment) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	if !s.loaded || s.active == nil || s.active.Err() != nil || s.configContext.Err() != nil {
		s.mu.Unlock()
		return nil, false
	}
	c, g := s.settings, s.generation
	target, mode := routeFor(c, "circle-attachment")
	p, ok := profileByID(c, target)
	r, found := s.records[recordID(target, 1, circleObjectKey(slug, file.ID))]
	s.mu.Unlock()
	if mode != "redirect" || !ok || !found || r.Status != "synced" || r.Operation == "delete" || r.SHA256 != file.SHA256 || r.Identity != profileIdentity(p) {
		return nil, false
	}
	b, err := s.backend(p)
	if err != nil {
		return nil, false
	}
	reader, _, err := b.Open(ctx, r.RemoteKey)
	if err != nil {
		return nil, false
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, file.Size+1))
	if err != nil || verifyReader(ctx, bytes.NewReader(data), r) != nil || !s.deliveryCurrent(g, c.Version) {
		return nil, false
	}
	return data, true
}
