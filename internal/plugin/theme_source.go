package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"newpress.local/newpress/internal/theme"
)

// Unset variables select the independent official theme repository. A partial
// override fails validation; never mix a custom catalog and another key.
func EnvironmentThemeSource() (MarketSource, bool) {
	urls, urlsSet := os.LookupEnv("NEWPRESS_OFFICIAL_THEME_CATALOGS")
	key, keySet := os.LookupEnv("NEWPRESS_OFFICIAL_THEME_PUBLIC_KEY")
	if !urlsSet && !keySet {
		return officialThemeSource(), true
	}
	source := MarketSource{PublicKey: strings.TrimSpace(key)}
	for _, value := range strings.Split(urls, ",") {
		if value = strings.TrimSpace(value); value != "" {
			source.URLs = append(source.URLs, value)
		}
	}
	return source, true
}

func (m *Market) ThemeCover(ctx context.Context, slug, digest string) ([]byte, string, error) {
	view := m.View()
	if !view.Expires.After(time.Now()) {
		return nil, "", errors.New("请先刷新主题市场")
	}
	for _, release := range view.Themes {
		if release.Manifest.Slug != slug || release.Cover == nil || release.Cover.SHA256 != digest {
			continue
		}
		if err := theme.ValidateCoverFile(release.Cover); err != nil {
			return nil, "", err
		}
		base, err := repositoryURL(view.Source)
		if err != nil {
			return nil, "", err
		}
		base = &url.URL{Scheme: base.Scheme, Host: base.Host, Path: path.Join(path.Dir(base.Path), release.Cover.Path)}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		data, err := m.fetchProgress(ctx, base.String(), release.Cover.Size, nil)
		if err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != release.Cover.Size || hex.EncodeToString(sum[:]) != digest {
			return nil, "", errors.New("主题封面完整性校验失败")
		}
		mime, err := theme.ValidateCover(data)
		return data, mime, err
	}
	return nil, "", os.ErrNotExist
}
