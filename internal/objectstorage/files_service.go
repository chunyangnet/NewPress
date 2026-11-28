package objectstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"newpress.local/newpress/internal/plugin"
)

const RemoteUploadLimit int64 = 64 << 20

// AfterFunc wakes blocked I/O on revocation. Err also observes the leases
// synchronously at operation boundaries, before those callbacks are scheduled.
type fileSessionContext struct {
	context.Context
	lease, config context.Context
	cancel        context.CancelFunc
}

func (c *fileSessionContext) Err() error {
	if c.lease.Err() != nil || c.config.Err() != nil {
		c.cancel()
	}
	return c.Context.Err()
}

func fileKey(p Profile, key string, directory bool) (string, error) {
	if directory {
		key = strings.TrimSuffix(key, "/")
	}
	if key != "" {
		if err := validateStorageKey(key); err != nil {
			return "", &OperationError{"文件路径无效"}
		}
	} else if !directory {
		return "", &OperationError{"请选择文件"}
	}
	if p.Prefix != "" && key != p.Prefix && !strings.HasPrefix(key, p.Prefix+"/") {
		return "", &OperationError{"文件路径必须位于配置的目录前缀内"}
	}
	if directory && key != "" {
		key += "/"
	}
	return key, nil
}

func managedKey(key string) bool {
	for _, segment := range strings.Split(key, "/") {
		if regexpNamespace(segment) {
			return true
		}
	}
	return false
}

func (s *Service) fileSession(ctx context.Context, id string, version int64) (context.Context, context.CancelFunc, Backend, Profile, error) {
	if err := checkFileAccess(ctx); err != nil {
		return ctx, func() {}, nil, Profile{}, err
	}
	if err := s.load(false); err != nil {
		return ctx, func() {}, nil, Profile{}, err
	}
	s.mu.Lock()
	p, ok := profileByID(s.settings, id)
	v, g, config := s.settings.Version, s.generation, s.configContext
	s.mu.Unlock()
	if !ok || v != version {
		return ctx, func() {}, nil, p, ErrConflict
	}
	lease, err := s.manager.InstalledContext(plugin.ObjectStorage, g)
	if err != nil {
		return ctx, func() {}, nil, p, err
	}
	ctx, cancel := context.WithTimeout(ctx, storageOperationTimeout(p))
	stop := context.AfterFunc(lease, cancel)
	stopConfig := context.AfterFunc(config, cancel)
	close := func() { stop(); stopConfig(); cancel() }
	ctx = &fileSessionContext{Context: ctx, lease: lease, config: config, cancel: cancel}
	if err := ctx.Err(); err != nil {
		close()
		return ctx, close, nil, p, err
	}
	b, err := s.backend(p)
	if err != nil {
		close()
	}
	return ctx, close, b, p, err
}

func (s *Service) RemoteList(ctx context.Context, id, prefix, token string, version int64) (RemotePage, error) {
	ctx, close, b, p, err := s.fileSession(ctx, id, version)
	defer close()
	if err != nil {
		return RemotePage{}, err
	}
	if prefix == "" {
		prefix = p.Prefix
	}
	prefix, err = fileKey(p, prefix, true)
	if err != nil {
		return RemotePage{}, err
	}
	lister, ok := b.(remoteLister)
	if !ok {
		return RemotePage{}, ErrUnsupported
	}
	page, err := lister.List(ctx, prefix, token)
	if err != nil {
		return page, err
	}
	if err := checkFileAccess(ctx); err != nil {
		return RemotePage{}, err
	}
	filtered := []RemoteFile{}
	for _, f := range page.Files {
		if _, e := fileKey(p, f.Key, f.Directory); e == nil && strings.HasPrefix(f.Key, prefix) {
			f.Managed = managedKey(f.Key)
			filtered = append(filtered, f)
		}
	}
	page.Files = filtered
	return page, nil
}

func (s *Service) RemoteDownload(ctx context.Context, id, key string, version int64) (io.ReadCloser, ObjectInfo, error) {
	ctx, close, b, p, err := s.fileSession(ctx, id, version)
	if err != nil {
		close()
		return nil, ObjectInfo{}, err
	}
	if _, err = fileKey(p, key, false); err != nil {
		close()
		return nil, ObjectInfo{}, err
	}
	reader, info, err := b.Open(ctx, key)
	if err != nil {
		close()
		return nil, info, err
	}
	if err := checkFileAccess(ctx); err != nil {
		reader.Close()
		close()
		return nil, ObjectInfo{}, err
	}
	return newFileDownloadReader(ctx, reader, close), info, nil
}

// Transfers are bounded, spooled to disk and verified before a move deletes its source.
func transferFile(ctx context.Context, b Backend, key string, reader io.Reader, source ObjectInfo) error {
	if err := checkFileAccess(ctx); err != nil {
		return err
	}
	if _, err := b.Head(ctx, key); err == nil {
		return &OperationError{"目标文件已存在，请使用其他名称"}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	temp, err := os.CreateTemp("", "newpress-cloud-*")
	if err != nil {
		return &OperationError{"无法创建文件传输缓存"}
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(&contextReader{ctx: ctx, r: reader}, RemoteUploadLimit+1))
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil {
		return &OperationError{"文件读取失败"}
	}
	if size > RemoteUploadLimit {
		return &OperationError{"单文件最多支持 64 MiB"}
	}
	info := ObjectInfo{Size: size, ContentType: source.ContentType, SHA256: hex.EncodeToString(hash.Sum(nil))}
	// A verified destination is only useful if the copy contains the complete
	// source. Negative size means an upstream stream without a known length.
	if source.Size >= 0 && size != source.Size || source.SHA256 != "" && !strings.EqualFold(source.SHA256, info.SHA256) {
		return &OperationError{"来源文件大小或 SHA-256 校验不一致，已停止传输"}
	}
	if _, err = temp.Seek(0, io.SeekStart); err != nil {
		return &OperationError{"无法读取文件传输缓存"}
	}
	if err := checkFileAccess(ctx); err != nil {
		return err
	}
	if err = b.Put(ctx, key, temp, info); err != nil {
		return err
	}
	if err := verifyRemote(ctx, b, Record{RemoteKey: key, Size: size, SHA256: info.SHA256}, true); err != nil {
		return err
	}
	return checkFileAccess(ctx)
}

func (s *Service) RemoteUpload(ctx context.Context, id, key string, version int64, reader io.Reader, contentType string) error {
	if !s.fileMu.TryLock() {
		return &OperationError{"已有文件操作正在执行，请完成后重试"}
	}
	defer s.fileMu.Unlock()
	ctx, close, b, p, err := s.fileSession(ctx, id, version)
	defer close()
	if err != nil {
		return err
	}
	if _, err = fileKey(p, key, false); err != nil {
		return err
	}
	if managedKey(key) {
		return &OperationError{"本站同步目录请通过媒体库管理，上传请选择独立目录"}
	}
	return transferFile(ctx, b, key, reader, ObjectInfo{Size: -1, ContentType: contentType})
}

func (s *Service) RemoteAction(ctx context.Context, id, key, targetID, target, action string, version int64) error {
	if !s.fileMu.TryLock() {
		return &OperationError{"已有文件操作正在执行，请完成后重试"}
	}
	defer s.fileMu.Unlock()
	ctx, close, b, p, err := s.fileSession(ctx, id, version)
	defer close()
	if err != nil {
		return err
	}
	if _, err = fileKey(p, key, false); err != nil {
		return err
	}
	if action != "copy" && managedKey(key) {
		return &OperationError{"本站同步文件请通过媒体库删除或存储迁移功能管理"}
	}
	if action == "delete" {
		if _, err = b.Head(ctx, key); err != nil {
			return err
		}
		if err := checkFileAccess(ctx); err != nil {
			return err
		}
		return b.Delete(ctx, key)
	}
	if action != "copy" && action != "move" {
		return &OperationError{"不支持的文件操作"}
	}
	if targetID == "" {
		targetID = id
	}
	targetCtx, targetClose, destination, targetProfile, err := s.fileSession(ctx, targetID, version)
	defer targetClose()
	if err != nil {
		return err
	}
	if _, err = fileKey(targetProfile, target, false); err != nil {
		return err
	}
	if managedKey(target) || (targetID == id && target == key) {
		return &OperationError{"请选择独立目录中的新目标路径"}
	}
	reader, info, err := b.Open(ctx, key)
	if err != nil {
		return err
	}
	defer reader.Close()
	if info.Size > RemoteUploadLimit {
		return &OperationError{"单文件最多支持 64 MiB"}
	}
	if err = transferFile(targetCtx, destination, target, reader, info); err != nil {
		return err
	}
	if err := checkFileAccess(targetCtx); err != nil {
		return err
	}
	if action == "move" {
		return b.Delete(ctx, key)
	}
	return nil
}
