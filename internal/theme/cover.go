package theme

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"newpress.local/newpress/internal/packagefs"
)

const MaxCoverBytes = 2 << 20

// CoverFile is signed with the release catalog. It enables previews without
// downloading or executing the theme package.
type CoverFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func validCoverPath(name string) bool {
	return strings.HasPrefix(name, "assets/") && packagefs.ValidateRelativePath(name) == nil && coverExtension(name)
}
func coverExtension(name string) bool {
	switch path.Ext(name) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}
func ValidateCoverFile(c *CoverFile) error {
	if c == nil {
		return nil
	}
	if packagefs.ValidateRelativePath(c.Path) != nil || !strings.HasPrefix(c.Path, "previews/themes/") || !coverExtension(c.Path) || len(c.SHA256) != 64 || c.Size < 1 || c.Size > MaxCoverBytes {
		return errors.New("invalid theme preview metadata")
	}
	for _, char := range c.SHA256 {
		if !(char >= 'a' && char <= 'f' || char >= '0' && char <= '9') {
			return errors.New("invalid theme preview digest")
		}
	}
	return nil
}
func ValidateCover(data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxCoverBytes {
		return "", errors.New("theme preview exceeds 2 MiB")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 || config.Width*config.Height > 12_000_000 {
		return "", errors.New("theme preview must be a raster image up to 4096 px and 12 megapixels")
	}
	switch format {
	case "png", "jpeg", "webp":
		return "image/" + format, nil
	}
	return "", errors.New("unsupported theme preview format")
}
func readCover(directory string, manifest Manifest) ([]byte, string, error) {
	if !validCoverPath(manifest.Cover) {
		return nil, "", os.ErrNotExist
	}
	file, info, err := packagefs.OpenRegularNoSymlink(filepath.Join(directory, filepath.FromSlash(manifest.Cover)))
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	if info.Size() > MaxCoverBytes {
		return nil, "", errors.New("theme preview exceeds 2 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxCoverBytes+1))
	if err != nil {
		return nil, "", err
	}
	mime, err := ValidateCover(data)
	return data, mime, err
}
func (m *Manager) ReadCover(slug string) ([]byte, string, error) {
	if packagefs.ValidateSlug(slug) != nil {
		return nil, "", os.ErrNotExist
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	dir := filepath.Join(m.root, slug)
	manifest, err := readManifest(filepath.Join(dir, "theme.json"))
	if err != nil || manifest.Slug != slug {
		return nil, "", os.ErrNotExist
	}
	return readCover(dir, manifest)
}
