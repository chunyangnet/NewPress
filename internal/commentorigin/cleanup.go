package commentorigin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/plugin"
)

// CommentExists returns false only for an authoritative deletion. Storage
// failures must return an error so a transient outage cannot erase records.
type CommentExists func(context.Context, int64) (bool, error)

func (s *Service) Forget(id int64) error {
	if id < 1 {
		return errors.New("invalid comment id")
	}
	err := s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, _ uint64) error {
		return os.Remove(recordPath(dir, id))
	})
	if os.IsNotExist(err) || errors.Is(err, plugin.ErrInactive) {
		err = nil
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return err
}

// pruneDeleted also handles cascaded replies and interrupted cleanup after a
// restart. Bound each pass, and never hold the plugin lock while reading the
// comment store (which may be PostgreSQL).
func (s *Service) pruneDeleted(ctx context.Context) {
	if s.commentExists == nil {
		return
	}
	var names []string
	var generation uint64
	err := s.manager.WithData(plugin.CommentOrigin, false, 0, func(dir string, gen uint64) error {
		entries, err := os.ReadDir(filepath.Join(dir, "records"))
		if err != nil {
			return err
		}
		generation = gen
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && entry.Name() > s.pruneAfter {
				names = append(names, entry.Name())
				if len(names) == 128 {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return
	}
	if len(names) == 0 {
		s.pruneAfter = ""
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, name := range names {
		id, err := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 64)
		if err != nil || id < 1 {
			s.pruneAfter = name
			continue
		}
		exists, err := s.commentExists(ctx, id)
		if err != nil || ctx.Err() != nil {
			return
		}
		if !exists {
			err = s.manager.WithData(plugin.CommentOrigin, false, generation, func(dir string, _ uint64) error {
				return os.Remove(recordPath(dir, id))
			})
			if err != nil && !os.IsNotExist(err) {
				return
			}
		}
		s.pruneAfter = name
	}
	if len(names) < 128 {
		s.pruneAfter = ""
	}
}
