package objectstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"newpress.local/newpress/internal/linkpage"
	"newpress.local/newpress/internal/media"
)

func (s *Service) SetLinkPages(store linkpage.Store) { s.linkPages = store }

func linkObject(slug string, item linkpage.Item) (media.Item, []byte, error) {
	data, err := linkpage.DecodeLogo(item.Logo)
	if err != nil {
		return media.Item{}, nil, err
	}
	h := sha256.Sum256(data)
	checksum := hex.EncodeToString(h[:])
	return media.Item{ID: item.ID, UploadSource: "link-logo", Filename: item.Name + ".png", MIMEType: "image/png", SizeBytes: int64(len(data)), ChecksumSHA256: checksum, ObjectKey: "links/" + slug + "/" + strconv.FormatInt(item.ID, 10) + "/" + checksum + ".png"}, data, nil
}

func (s *Service) linkInventory(ctx context.Context) ([]media.StorageObject, error) {
	objects := []media.StorageObject{}
	if s.linkPages == nil {
		return objects, nil
	}
	boards, err := s.linkPages.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, header := range boards {
		board, err := s.linkPages.Get(ctx, header.Slug)
		if err != nil {
			return nil, err
		}
		for _, entry := range board.Items {
			if entry.Logo == "" {
				continue
			}
			item, _, err := linkObject(board.Slug, entry)
			if err != nil {
				return nil, err
			}
			objects = append(objects, media.StorageObject{ItemID: item.ID, Filename: item.Filename, UploadSource: item.UploadSource, ImageObject: item.Objects()[0]})
		}
	}
	return objects, nil
}

func (s *Service) openLink(ctx context.Context, id int64, key string) (media.Item, media.ReadSeekCloser, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || s.linkPages == nil {
		return media.Item{}, nil, media.ErrNotFound
	}
	board, err := s.linkPages.Get(ctx, parts[1])
	if err != nil {
		return media.Item{}, nil, err
	}
	for _, entry := range board.Items {
		if entry.ID != id || entry.Logo == "" {
			continue
		}
		item, data, err := linkObject(board.Slug, entry)
		if err != nil {
			return item, nil, err
		}
		if item.ObjectKey == key {
			return item, memoryReader{bytes.NewReader(data)}, nil
		}
	}
	return media.Item{}, nil, media.ErrNotFound
}

func (s *Service) LinkRedirectURL(ctx context.Context, slug string, entry linkpage.Item) (string, bool) {
	item, _, err := linkObject(slug, entry)
	if err != nil {
		return "", false
	}
	return s.RedirectURL(ctx, item)
}
