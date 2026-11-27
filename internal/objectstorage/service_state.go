package objectstorage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
)

const maxSettingsBytes = 128 << 10

func settingsAt(dir string) (Settings, error) {
	c, err := plugin.ReadPrivate[Settings](filepath.Join(dir, "settings.json"), maxSettingsBytes)
	if os.IsNotExist(err) {
		return Defaults(), nil
	}
	if err != nil {
		return c, errors.New("对象存储配置无法读取")
	}
	return normalizeSettings(c)
}

func (s *Service) load(enabled bool) error {
	if s == nil || s.manager == nil || s.media == nil {
		return plugin.ErrInactive
	}
	return s.manager.WithData(plugin.ObjectStorage, enabled, 0, func(dir string, g uint64) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.loaded && s.generation == g {
			return nil
		}
		c, err := settingsAt(dir)
		if err != nil {
			return err
		}
		var identity struct {
			Namespace string `json:"namespace"`
		}
		identity, err = plugin.ReadPrivate[struct {
			Namespace string `json:"namespace"`
		}](filepath.Join(dir, "identity.json"), 1024)
		if os.IsNotExist(err) {
			var b [16]byte
			if _, err = rand.Read(b[:]); err != nil {
				return err
			}
			identity.Namespace = "newpress-" + hex.EncodeToString(b[:])
			err = plugin.WritePrivate(filepath.Join(dir, "identity.json"), identity)
		}
		if err != nil || !regexpNamespace(identity.Namespace) {
			return errors.New("对象存储目录标识无法读取")
		}
		recordDir := filepath.Join(dir, "records")
		if err = packagefs.MkdirAllNoSymlink(recordDir, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(recordDir)
		if err != nil {
			return errors.New("对象存储记录无法读取")
		}
		if len(entries) > maxRecords {
			return errors.New("对象存储记录数量超过限制")
		}
		records := make(map[string]Record, len(entries))
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			r, e := plugin.ReadPrivate[Record](filepath.Join(recordDir, entry.Name()), 8192)
			if e != nil || !validRecord(r, identity.Namespace) || entry.Name() != r.ID+".json" {
				return errors.New("对象存储记录校验失败")
			}
			if r.Status == "syncing" {
				r.Status = "pending"
			}
			records[r.ID] = r
		}
		if s.configCancel != nil {
			s.configCancel()
		}
		s.configContext, s.configCancel = context.WithCancel(context.Background())
		s.loaded = true
		s.generation = g
		s.settings = c
		s.namespace = identity.Namespace
		s.records = records
		s.closeBackendsLocked()
		s.backends = make(map[string]Backend)
		s.lastError = ""
		s.active = nil
		return nil
	})
}

func regexpNamespace(v string) bool {
	if !strings.HasPrefix(v, "newpress-") || len(v) != 41 {
		return false
	}
	_, err := hex.DecodeString(v[9:])
	return err == nil
}

func recordID(profile string, id int64, key string) string {
	h := sha256.Sum256([]byte(profile + "\x00" + strconv.FormatInt(id, 10) + "\x00" + key))
	return hex.EncodeToString(h[:])
}

func validRecord(r Record, namespace string) bool {
	if !identifierPattern.MatchString(r.ProfileID) || r.ItemID < 1 || r.Size < 1 || r.Attempts < 0 || r.Attempts > 100 || !cleanKey(r.ObjectKey) || !cleanKey(r.RemoteKey) || r.ID != recordID(r.ProfileID, r.ItemID, r.ObjectKey) || len(r.SHA256) != 64 || len(r.Identity) != 64 || len(r.Error) > 512 {
		return false
	}
	if _, e := hex.DecodeString(r.SHA256); e != nil {
		return false
	}
	if _, e := hex.DecodeString(r.Identity); e != nil {
		return false
	}
	// The final path is always scoped to this installation and indexed media ID.
	suffix := namespace + "/" + strconv.FormatInt(r.ItemID, 10) + "/" + r.ObjectKey
	if r.RemoteKey != suffix && !strings.HasSuffix(r.RemoteKey, "/"+suffix) {
		return false
	}
	switch r.Status {
	case "pending", "syncing", "synced", "failed", "orphaned", "removed":
	default:
		return false
	}
	switch r.Operation {
	case "upload", "verify", "delete":
	default:
		return false
	}
	return true
}

func (s *Service) Save(c Settings, clearTokens []string) (SettingsView, error) {
	if err := s.load(false); err != nil {
		return SettingsView{}, err
	}
	var result SettingsView
	err := s.manager.WithData(plugin.ObjectStorage, false, 0, func(dir string, g uint64) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.loaded || g != s.generation || c.Version != s.settings.Version {
			return ErrConflict
		}
		old := map[string]Profile{}
		for _, p := range s.settings.Profiles {
			old[p.ID] = p
		}
		clear := map[string]bool{}
		for _, id := range clearTokens {
			clear[id] = true
		}
		c.Profiles = append([]Profile(nil), c.Profiles...)
		c = cloneRoutes(c)
		for i, p := range c.Profiles {
			previous, ok := old[p.ID]
			// Normalize endpoints before comparing identities without ever carrying
			// a previous secret to a changed destination.
			probe := p
			if probe.SecretKey == "" {
				probe.SecretKey = "validation"
			}
			if probe.Password == "" {
				probe.Password = "validation"
			}
			if p.Provider == "cloudreve" || p.Provider == "webdav" {
				probe.SecretKey = ""
			} else {
				probe.Password = ""
			}
			normalized, e := normalizeProfile(probe)
			if e != nil {
				return e
			}
			same := ok && profileIdentity(previous) == profileIdentity(normalized)
			if same && p.AccessKey == previous.AccessKey && p.SecretKey == "" {
				p.SecretKey = previous.SecretKey
			}
			if same && p.AccessKey == previous.AccessKey && p.SessionToken == "" && !clear[p.ID] {
				p.SessionToken = previous.SessionToken
			}
			if clear[p.ID] {
				p.SessionToken = ""
			}
			if same && p.Password == "" {
				p.Password = previous.Password
			}
			p, e = normalizeProfile(p)
			if e != nil {
				return e
			}
			if ok && !same {
				for _, r := range s.records {
					if r.ProfileID == p.ID {
						return errors.New("此存储已有同步记录；更换桶、目录、服务或账号时请新增存储配置")
					}
				}
			}
			c.Profiles[i] = p
		}
		var err error
		c, err = normalizeSettings(c)
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, p := range c.Profiles {
			present[p.ID] = true
		}
		for _, r := range s.records {
			if !present[r.ProfileID] && r.Status != "removed" {
				return errors.New("存储仍有同步记录，请先保留该配置并切换当前存储")
			}
		}
		c.Version++
		// Check the final JSON, including retained credentials and the new
		// version, against the same limit used when restarting the service.
		encoded, err := json.Marshal(c)
		if err != nil {
			return errors.New("存储配置保存失败")
		}
		if len(encoded) > maxSettingsBytes {
			return errors.New("存储配置总大小不能超过 128 KiB，请减少存储配置或凭据长度")
		}
		if err = plugin.WritePrivate(filepath.Join(dir, "settings.json"), c); err != nil {
			return errors.New("存储配置保存失败")
		}
		if s.configCancel != nil {
			s.configCancel()
		}
		s.configContext, s.configCancel = context.WithCancel(context.Background())
		s.settings = c
		s.closeBackendsLocked()
		s.backends = make(map[string]Backend)
		s.lastError = ""
		result = sanitized(c)
		return nil
	})
	if err == nil {
		s.Wake()
	}
	return result, err
}

// commit serializes each small receipt against disable/uninstall and config
// changes. The remote side is idempotent; no network call holds these locks.
func (s *Service) commit(g uint64, v int64, r Record, enabled bool, expected ...time.Time) error {
	return s.manager.WithData(plugin.ObjectStorage, enabled, g, func(dir string, _ uint64) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.generation != g || s.settings.Version != v {
			return ErrConflict
		}
		if len(expected) > 0 {
			current, ok := s.records[r.ID]
			if !ok || !current.Updated.Equal(expected[0]) || current.Status != "syncing" {
				return ErrConflict
			}
		}
		if !validRecord(r, s.namespace) {
			return errors.New("对象同步记录无效")
		}
		if _, exists := s.records[r.ID]; !exists && len(s.records) >= maxRecords {
			return errRecordLimit
		}
		if err := plugin.WritePrivate(filepath.Join(dir, "records", r.ID+".json"), r); err != nil {
			return &OperationError{"同步记录保存失败"}
		}
		s.records[r.ID] = r
		return nil
	})
}
