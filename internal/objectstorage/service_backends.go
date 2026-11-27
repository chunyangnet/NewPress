package objectstorage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"time"

	"newpress.local/newpress/internal/plugin"
)

func profileByID(c Settings, id string) (Profile, bool) {
	for _, p := range c.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

func (s *Service) backend(p Profile) (Backend, error) {
	encoded, _ := json.Marshal(p)
	digest := sha256.Sum256(encoded)
	key := hex.EncodeToString(digest[:])
	s.mu.Lock()
	current, present := profileByID(s.settings, p.ID)
	if !present || current != p || s.configContext == nil || s.configContext.Err() != nil {
		s.mu.Unlock()
		return nil, ErrConflict
	}
	g, v := s.generation, s.settings.Version
	b := s.backends[key]
	s.mu.Unlock()
	if b != nil {
		return b, nil
	}
	b, err := s.factory(p)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if g != s.generation || v != s.settings.Version || s.configContext.Err() != nil {
		if closer, ok := b.(io.Closer); ok {
			_ = closer.Close()
		}
		return nil, ErrConflict
	}
	if existing := s.backends[key]; existing != nil {
		if closer, ok := b.(io.Closer); ok {
			_ = closer.Close()
		}
		return existing, nil
	}
	if len(s.backends) < 16 {
		s.backends[key] = b
	}
	return b, nil
}

func (s *Service) closeBackendsLocked() {
	for _, b := range s.backends {
		if closer, ok := b.(io.Closer); ok {
			_ = closer.Close()
		}
	}
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configCancel != nil {
		s.configCancel()
	}
	s.closeBackendsLocked()
	s.backends = map[string]Backend{}
}

func (s *Service) Test(ctx context.Context, profileID string, version ...int64) (result ProbeResult, resultErr error) {
	result = ProbeResult{Stages: []string{}}
	start := time.Now()
	defer func() { result.DurationMS = time.Since(start).Milliseconds() }()
	select {
	case s.probeGate <- struct{}{}:
		defer func() { <-s.probeGate }()
	default:
		return result, &OperationError{"已有连接测试正在进行"}
	}
	if err := s.load(false); err != nil {
		return result, err
	}
	s.mu.Lock()
	c, g, ns, configCtx := s.settings, s.generation, s.namespace, s.configContext
	s.mu.Unlock()
	p, ok := profileByID(c, profileID)
	if !ok {
		return result, errors.New("存储配置不存在")
	}
	if len(version) > 0 && c.Version != version[0] {
		return result, ErrConflict
	}
	lease, err := s.manager.InstalledContext(plugin.ObjectStorage, g)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()
	stop := context.AfterFunc(lease, cancel)
	defer stop()
	stopCfg := context.AfterFunc(configCtx, cancel)
	defer stopCfg()
	b, err := s.backend(p)
	if err != nil {
		return result, err
	}
	var random [24]byte
	if _, err = rand.Read(random[:]); err != nil {
		return result, err
	}
	payload := append([]byte("NewPress storage connectivity probe\n"), random[:]...)
	sum := sha256.Sum256(payload)
	key := path.Join(p.Prefix, ns, ".probes", hex.EncodeToString(random[:])+".txt")
	info := ObjectInfo{Size: int64(len(payload)), ContentType: "text/plain", SHA256: hex.EncodeToString(sum[:])}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupStop := context.AfterFunc(lease, cleanupCancel)
		defer cleanupStop()
		deleteErr := b.Delete(cleanupCtx, key)
		result.CleanupOK = deleteErr == nil
		if deleteErr == nil {
			result.Stages = append(result.Stages, "测试文件清理成功")
		} else if resultErr == nil {
			resultErr = &OperationError{"读写通过，但测试文件清理失败；请检查删除权限"}
		}
		result.OK = resultErr == nil && result.CleanupOK
	}()
	if err = b.Put(ctx, key, bytes.NewReader(payload), info); err != nil {
		return result, err
	}
	result.Stages = append(result.Stages, "上传成功")
	verifyErr := verifyRemote(ctx, b, Record{RemoteKey: key, Size: info.Size, SHA256: info.SHA256}, true)
	if verifyErr == nil {
		result.Stages = append(result.Stages, "读取与 SHA-256 校验成功")
	}
	if verifyErr != nil {
		return result, verifyErr
	}
	return result, nil
}
