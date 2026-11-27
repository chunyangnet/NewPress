package theme

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"newpress.local/newpress/internal/packagefs"
)

// Hash the asset tree once per load/update. Relative CSS URLs stay inside this
// namespace, even when two themes publish the same version and filenames.
type assetIndex struct {
	revision string
	files    map[string][sha256.Size]byte
}

func hashAssets(directory string) (assetIndex, error) {
	root := filepath.Join(directory, "assets")
	hash := sha256.New()
	index := assetIndex{files: map[string][sha256.Size]byte{}}
	limits := packagefs.DefaultLimits()
	var count int
	var total int64
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && name == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		file, info, err := packagefs.OpenRegularNoSymlink(name)
		if err != nil {
			return err
		}
		defer file.Close()
		count++
		total += info.Size()
		if count > limits.MaxFiles || info.Size() > limits.MaxFileBytes || total > limits.MaxTotal {
			return fmt.Errorf("theme assets exceed package limits")
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		fmt.Fprintf(hash, "%s\x00%d\x00", relative, info.Size())
		fileHash := sha256.New()
		read, err := io.Copy(io.MultiWriter(hash, fileHash), io.LimitReader(file, limits.MaxFileBytes+1))
		if err != nil {
			return err
		}
		if read != info.Size() {
			return fmt.Errorf("asset changed while loading: %s", relative)
		}
		index.files[relative] = [sha256.Size]byte(fileHash.Sum(nil))
		return nil
	})
	if err != nil {
		return assetIndex{}, fmt.Errorf("hash theme assets: %w", err)
	}
	index.revision = hex.EncodeToString(hash.Sum(nil))[:24]
	return index, nil
}

func (m *Manager) AssetURL(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return "/theme-assets/" + m.info.Slug + "/" + m.assetRevision + "/" + strings.TrimLeft(name, "/")
}

// ReadVersionedAsset captures verified bytes under the directory-swap lock.
// A manual edit without reload also fails closed, instead of serving changed
// content under an immutable URL. The package file limit bounds the snapshot.
func (m *Manager) ReadVersionedAsset(slug, revision, relative string) ([]byte, os.FileInfo, error) {
	data, info, _, err := m.ReadVersionedAssetWithETag(slug, revision, relative)
	return data, info, err
}

// ReadVersionedAssetWithETag returns the validator already checked against the
// captured bytes, avoiding a second full hash in the HTTP delivery layer.
func (m *Manager) ReadVersionedAssetWithETag(slug, revision, relative string) ([]byte, os.FileInfo, string, error) {
	if err := packagefs.ValidateRelativePath(relative); err != nil {
		return nil, nil, "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if slug != m.info.Slug || revision != m.assetRevision {
		return nil, nil, "", fs.ErrNotExist
	}
	expected, ok := m.assetHashes[relative]
	if !ok {
		return nil, nil, "", fs.ErrNotExist
	}
	file, info, err := packagefs.OpenRegularNoSymlink(m.assetPath(relative))
	if err != nil {
		return nil, nil, "", err
	}
	defer file.Close()
	limit := packagefs.DefaultLimits().MaxFileBytes
	if info.Size() > limit {
		return nil, nil, "", fs.ErrNotExist
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, nil, "", err
	}
	if int64(len(data)) > limit || sha256.Sum256(data) != expected {
		return nil, nil, "", fs.ErrNotExist
	}
	return data, info, `"` + hex.EncodeToString(expected[:]) + `"`, nil
}

type CSSSnapshot struct{ CSS, Revision string }

func (m *Manager) SettingsCSSSnapshot() CSSSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	css := settingsCSS(m.settingsSchema, m.settings)
	digest := sha256.Sum256([]byte(css))
	return CSSSnapshot{CSS: css, Revision: hex.EncodeToString(digest[:16])}
}

// Caller holds the manager read lock. Owners are slugs, never staging paths.
func (m *Manager) assetPath(relative string) string {
	owner := m.active
	if value := m.assetOwners[relative]; value != "" {
		owner = value
	}
	return filepath.Join(m.root, owner, "assets", filepath.FromSlash(relative))
}
