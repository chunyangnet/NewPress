package objectstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/media"
	"newpress.local/newpress/internal/plugin"
)

func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	defer s.Close()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-timer.C:
		}
		err := s.cycle(ctx)
		if err != nil && !errors.Is(err, plugin.ErrInactive) && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrConflict) {
			s.mu.Lock()
			s.lastError = safeError(err)
			s.mu.Unlock()
		}
		s.mu.Lock()
		delay := time.Duration(s.settings.ScanSeconds) * time.Second
		s.mu.Unlock()
		if delay < 30*time.Second {
			delay = 30 * time.Second
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
}

func (s *Service) cycle(ctx context.Context) error {
	if err := s.load(true); err != nil {
		return err
	}
	s.mu.Lock()
	c, g := s.settings, s.generation
	configCtx := s.configContext
	s.mu.Unlock()
	lease, err := s.manager.ActiveContext(plugin.ObjectStorage, g)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.active = lease
	s.mu.Unlock()
	if c.Paused {
		return nil
	}
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(lease, cancel)
	defer stop()
	stopConfig := context.AfterFunc(configCtx, cancel)
	defer stopConfig()
	s.jobMu.Lock()
	if c.AutoSync {
		for _, profile := range c.Profiles {
			_, err = s.reconcile(profile.ID, false, true, c.Version)
			if err != nil {
				break
			}
		}
	}
	if errors.Is(err, errRecordLimit) {
		s.mu.Lock()
		s.lastError = safeError(err)
		s.mu.Unlock()
		err = nil
	}
	// The active profile may discover new media, while every existing profile
	// still needs orphan/restore reconciliation. The empty profile ID is a
	// bounded index pass: it never creates new receipts for inactive profiles.
	if err == nil {
		_, err = s.reconcile("", false, true, c.Version)
		if errors.Is(err, errRecordLimit) {
			s.mu.Lock()
			s.lastError = safeError(err)
			s.mu.Unlock()
			err = nil
		}
	}
	if err != nil {
		s.jobMu.Unlock()
		return err
	}
	s.jobMu.Unlock()
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
	// At most 128 objects per cycle keeps bulk catch-up bounded and responsive.
	for batch := 0; batch < 128; {
		if jobCtx.Err() != nil {
			return jobCtx.Err()
		}
		jobs := s.readyJobs(time.Now(), min(c.Workers, 128-batch))
		if len(jobs) == 0 {
			return nil
		}
		n := len(jobs)
		var wg sync.WaitGroup
		failures := make([]error, n)
		for i, r := range jobs[:n] {
			wg.Add(1)
			go func(i int, r Record) {
				defer wg.Done()
				failures[i] = s.process(jobCtx, g, c, r)
			}(i, r)
		}
		wg.Wait()
		for _, failure := range failures {
			if failure != nil {
				// An uncommitted receipt remains eligible. Let Run back off
				// instead of immediately claiming it again in this cycle.
				return failure
			}
		}
		batch += n
	}
	s.Wake()
	return nil
}

func (s *Service) readyJobs(now time.Time, limit int) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	ready := newRecordSelection(limit, false)
	for _, r := range s.records {
		if (r.Status == "pending" || r.Status == "syncing") && !r.Next.After(now) {
			ready.add(r)
		}
	}
	jobs := make([]Record, len(ready.values))
	for i, position := range ready.sorted() {
		jobs[i] = s.records[position.id]
	}
	return jobs
}

func safeError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "操作已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "存储请求超时"
	}
	if errors.Is(err, ErrNotFound) {
		return "远端对象不存在"
	}
	// Adapters return redacted errors. Do not propagate unknown third-party or
	// filesystem error strings which may contain presigned URLs or credentials.
	var safe *OperationError
	if errors.As(err, &safe) {
		return safe.Error()
	}
	return "操作失败，请检查连接配置、存储权限和服务状态"
}

type OperationError struct{ Message string }

func (e *OperationError) Error() string { return e.Message }

func (s *Service) process(ctx context.Context, g uint64, c Settings, r Record) error {
	if r.Operation != "delete" {
		source := ""
		if strings.HasPrefix(r.ObjectKey, "links/") {
			source = "link-logo"
		} else if strings.HasPrefix(r.ObjectKey, "private/") {
			source = "circle-attachment"
		} else {
			if item, err := s.media.Get(r.ItemID); err == nil {
				source = item.UploadSource
			}
		}
		if !routeAllows(c, source, r.ProfileID) {
			s.jobMu.Lock()
			defer s.jobMu.Unlock()
			s.mu.Lock()
			current, exists := s.records[r.ID]
			s.mu.Unlock()
			if !exists || !current.Updated.Equal(r.Updated) {
				return nil
			}
			r.Status, r.Error, r.Updated = "failed", "入口路由已变更，任务已停止；恢复路由后可重试", time.Now().UTC()
			return s.commit(g, c.Version, r, true)
		}
	}
	p, ok := profileByID(c, r.ProfileID)
	if !ok || profileIdentity(p) != r.Identity {
		return ErrConflict
	}
	// A job selected before a manual action/reconcile must not overwrite that
	// newer receipt. Serialize only the short claim, never network requests.
	s.jobMu.Lock()
	s.mu.Lock()
	current, exists := s.records[r.ID]
	s.mu.Unlock()
	if !exists || !current.Updated.Equal(r.Updated) || (current.Status != "pending" && current.Status != "syncing") {
		s.jobMu.Unlock()
		return nil
	}
	r.Status = "syncing"
	r.Attempts++
	r.Updated = time.Now().UTC()
	claimErr := s.commit(g, c.Version, r, true)
	s.jobMu.Unlock()
	if claimErr != nil {
		return claimErr
	}
	startedAt := r.Updated
	b, err := s.backend(p)
	if err == nil {
		switch r.Operation {
		case "delete":
			// Deletion is confined to recorded objects which are still absent.
			var present bool
			present, err = s.hasStorageObject(ctx, r.ItemID, r.ObjectKey)
			if err != nil {
				break
			}
			if present {
				r.Status, r.Operation = "pending", "upload"
				r.Attempts, r.Error, r.Next = 0, "", time.Time{}
				r.Updated = time.Now().UTC()
				if err := s.commit(g, c.Version, r, true, startedAt); err != nil {
					return err
				}
				s.Wake()
				return nil
			}
			err = b.Delete(ctx, r.RemoteKey)
		case "verify":
			err = verifyRemote(ctx, b, r, true)
		default:
			var item media.Item
			var reader media.ReadSeekCloser
			item, reader, err = s.openStorageObject(ctx, r.ItemID, r.ObjectKey)
			if err == nil {
				func() {
					defer reader.Close()
					if item.SizeBytes != r.Size || item.ChecksumSHA256 != r.SHA256 {
						err = &OperationError{"本地媒体信息已变更"}
						return
					}
					if err = verifyReader(ctx, reader, r); err != nil {
						return
					}
					if _, err = reader.Seek(0, io.SeekStart); err != nil {
						return
					}
					err = b.Put(ctx, r.RemoteKey, reader, ObjectInfo{Size: r.Size, ContentType: r.ContentType, SHA256: r.SHA256})
				}()
				if err == nil {
					err = verifyRemote(ctx, b, r, c.VerifyDownload)
				}
			}
		}
	}
	// Pause/disable/save leaves a retryable receipt, never publishes old config.
	if ctx.Err() != nil {
		r.Status = "pending"
		r.Next = time.Now().UTC().Add(30 * time.Second)
		r.Error = ""
		if err := s.commit(g, c.Version, r, false, startedAt); err != nil {
			return err
		}
		return ctx.Err()
	}
	r.Updated = time.Now().UTC()
	if err == nil {
		r.Status = "synced"
		if r.Operation == "delete" {
			r.Status = "removed"
		}
		r.Error = ""
		r.Verified = r.Updated
		r.Next = time.Time{}
	}
	if err != nil {
		r.Error = safeError(err)
		if errors.Is(err, media.ErrNotFound) {
			r.Status = "orphaned"
		} else if r.Attempts >= c.MaxAttempts {
			r.Status = "failed"
		} else {
			r.Status = "pending"
			delay := min(time.Duration(1<<min(r.Attempts, 6))*15*time.Second, 30*time.Minute)
			r.Next = r.Updated.Add(delay)
		}
	}
	if commitErr := s.commit(g, c.Version, r, true, startedAt); commitErr != nil {
		return commitErr
	}
	if err == nil && p.PublicURL != "" && r.Operation != "verify" && s.OnDeliveryChange != nil {
		s.OnDeliveryChange(publicObjectURL(p.PublicURL, r.RemoteKey))
	}
	return nil
}

func verifyReader(ctx context.Context, reader io.Reader, r Record) error {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(&contextReader{ctx: ctx, r: reader}, r.Size+1))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return &OperationError{"文件大小或 SHA-256 校验不一致"}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func verifyRemote(ctx context.Context, b Backend, r Record, download bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i, err := b.Head(ctx, r.RemoteKey)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.Size != r.Size || i.SHA256 != "" && !strings.EqualFold(i.SHA256, r.SHA256) {
		return &OperationError{"云端对象大小或校验值不一致"}
	}
	if !download && i.SHA256 != "" {
		return nil
	}
	body, _, err := b.Open(ctx, r.RemoteKey)
	if err != nil {
		return err
	}
	defer body.Close()
	return verifyReader(ctx, body, r)
}
