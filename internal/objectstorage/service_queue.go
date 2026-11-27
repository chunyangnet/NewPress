package objectstorage

import (
	"errors"
	"path"
	"strconv"
	"time"

	"newpress.local/newpress/internal/media"
)

func (s *Service) Synchronize(profileID string, version ...int64) (int, error) {
	return s.schedule(profileID, "upload", version...)
}

func (s *Service) Retry(profileID string, version ...int64) (int, error) {
	return s.schedule(profileID, "retry", version...)
}

func (s *Service) Verify(profileID string, version ...int64) (int, error) {
	return s.schedule(profileID, "verify", version...)
}

func (s *Service) Cleanup(profileID string, version ...int64) (int, error) {
	return s.schedule(profileID, "delete", version...)
}

func (s *Service) schedule(profileID, operation string, version ...int64) (int, error) {
	if err := s.load(false); err != nil {
		return 0, err
	}
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	if operation == "upload" {
		n, err := s.reconcile(profileID, true, false, version...)
		if err == nil || n > 0 {
			s.Wake()
		}
		return n, err
	}
	// Refresh existing receipts even when automatic uploads are disabled or the
	// selected profile is inactive. Cleanup must use the current media index.
	if _, err := s.reconcile("", false, false, version...); err != nil {
		return 0, err
	}
	s.mu.Lock()
	g, v := s.generation, s.settings.Version
	records := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		records = append(records, r)
	}
	_, ok := profileByID(s.settings, profileID)
	s.mu.Unlock()
	if !ok {
		return 0, errors.New("存储配置不存在")
	}
	if len(version) > 0 && v != version[0] {
		return 0, ErrConflict
	}
	n := 0
	for _, r := range records {
		if r.ProfileID != profileID {
			continue
		}
		eligible := operation == "retry" && r.Status == "failed" || operation == "verify" && r.Status == "synced" || operation == "delete" && r.Status == "orphaned"
		if !eligible {
			continue
		}
		if operation != "retry" {
			r.Operation = operation
		}
		r.Status = "pending"
		r.Attempts = 0
		r.Next = time.Time{}
		r.Error = ""
		r.Updated = time.Now().UTC()
		if err := s.commit(g, v, r, false); err != nil {
			return n, err
		}
		n++
	}
	s.Wake()
	return n, nil
}

func (s *Service) reconcile(profileID string, resetFailed, enabled bool, version ...int64) (int, error) {
	s.mu.Lock()
	c, g, ns := s.settings, s.generation, s.namespace
	records := make(map[string]Record, len(s.records))
	for k, r := range s.records {
		records[k] = r
	}
	s.mu.Unlock()
	p, ok := profileByID(c, profileID)
	if profileID != "" && !ok {
		return 0, errors.New("存储配置不存在")
	}
	if len(version) > 0 && c.Version != version[0] {
		return 0, ErrConflict
	}
	inventory, err := s.storageInventory()
	if err != nil {
		return 0, err
	}
	type objectRef struct {
		itemID int64
		key    string
	}
	live := make(map[objectRef]media.StorageObject, len(inventory))
	count := 0
	capacityReached := false
	now := time.Now().UTC()
	for _, o := range inventory {
		if o.Trashed {
			continue
		}
		live[objectRef{o.ItemID, o.ObjectKey}] = o
		if profileID == "" {
			continue
		}
		target, _ := routeFor(c, o.UploadSource)
		if o.UploadSource == "circle-attachment" && target == "" || o.UploadSource != "circle-attachment" && !media.ValidUploadSource(o.UploadSource) {
			continue
		}
		// Explicit routes are authoritative even for a manual full-library sync.
		source := o.UploadSource
		if route := c.UploadRoutes[source]; route.Target != "" && target != profileID {
			continue
		}
		if enabled && target != profileID {
			continue
		}
		id := recordID(profileID, o.ItemID, o.ObjectKey)
		r, exists := records[id]
		if !exists && len(records) >= maxRecords {
			capacityReached = true
			continue
		}
		if exists && r.Operation != "delete" && r.Status != "orphaned" && r.Status != "removed" && !(resetFailed && r.Status == "failed") {
			continue
		}
		r = Record{ID: id, ProfileID: profileID, Identity: profileIdentity(p), ItemID: o.ItemID, Filename: o.Filename, ObjectKey: o.ObjectKey, RemoteKey: path.Join(p.Prefix, ns, strconv.FormatInt(o.ItemID, 10), o.ObjectKey), SHA256: o.ChecksumSHA256, Size: o.SizeBytes, ContentType: o.MIMEType, Status: "pending", Operation: "upload", Updated: now}
		if err := s.commit(g, c.Version, r, enabled); err != nil {
			return count, err
		}
		records[id] = r
		count++
	}
	for _, r := range records {
		if o, present := live[objectRef{r.ItemID, r.ObjectKey}]; present {
			if !routeAllows(c, o.UploadSource, r.ProfileID) {
				continue
			}
			if r.Operation != "delete" && r.Status != "orphaned" && r.Status != "removed" {
				continue
			}
			// Restore repairs a previously mirrored object on every destination,
			// without discovering/uploading new media to inactive profiles.
			if previous, exists := profileByID(c, r.ProfileID); !exists || profileIdentity(previous) != r.Identity {
				continue
			}
			r.Status, r.Operation = "pending", "upload"
			r.SHA256, r.Size, r.ContentType, r.Filename = o.ChecksumSHA256, o.SizeBytes, o.MIMEType, o.Filename
			r.Attempts = 0
			r.Verified = time.Time{}
			count++
		} else if r.Status == "orphaned" || r.Status == "removed" || r.Operation == "delete" {
			continue
		} else {
			r.Status = "orphaned"
		}
		r.Updated = now
		r.Error = ""
		r.Next = time.Time{}
		if err := s.commit(g, c.Version, r, enabled); err != nil {
			return count, err
		}
	}
	if capacityReached {
		return count, errRecordLimit
	}
	return count, nil
}
