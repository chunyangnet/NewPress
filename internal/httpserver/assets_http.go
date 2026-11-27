package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"newpress.local/newpress/internal/coreui"
)

func (s *Server) favicon(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "public, max-age=300")
	http.Redirect(writer, request, s.siteIconURL(), http.StatusTemporaryRedirect)
}

func (s *Server) themeAssets() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		relative, err := safeAssetPath(request.URL.Path)
		if err != nil {
			http.NotFound(writer, request)
			return
		}
		var reader io.ReadSeeker
		var info os.FileInfo
		var etag string
		parts := strings.SplitN(relative, "/", 3)
		versioned := len(parts) == 3 && len(parts[1]) == 24
		if versioned {
			var data []byte
			data, info, etag, err = s.themes.ReadVersionedAssetWithETag(parts[0], parts[1], parts[2])
			reader = bytes.NewReader(data)
		} else {
			var file *os.File
			file, info, _, err = s.themes.OpenAsset(relative)
			if err == nil {
				defer file.Close()
				reader = file
			}
		}
		if err != nil {
			// Old demo posts used stable theme paths. Their assets are now core owned.
			if !versioned && (relative == "images/cover-core.svg" || relative == "images/cover-chunnel.svg" || relative == "images/cover-plugin.svg") {
				http.Redirect(writer, request, coreui.AssetURL(relative), http.StatusTemporaryRedirect)
				return
			}
			http.NotFound(writer, request)
			return
		}
		if etag == "" {
			digest := sha256.New()
			if _, err := io.Copy(digest, reader); err != nil {
				http.NotFound(writer, request)
				return
			}
			if _, err := reader.Seek(0, io.SeekStart); err != nil {
				http.NotFound(writer, request)
				return
			}
			etag = `"` + hex.EncodeToString(digest.Sum(nil)) + `"`
		}
		writer.Header().Set("ETag", etag)
		if versioned {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			writer.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		}
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		if base, ttl := s.cdn.Delivery(); base != "" {
			writer.Header().Set("Access-Control-Allow-Origin", "*")
			writer.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
			if versioned {
				writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", ttl))
			}
		}
		if etagMatches(request.Header.Get("If-None-Match"), etag) {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		http.ServeContent(writer, request, filepath.Base(relative), info.ModTime(), reader)
	})
}

func (s *Server) siteIconURL() string {
	iconURL := strings.TrimSpace(s.siteSettings.Snapshot().Settings.Identity.IconURL)
	if iconURL != "" {
		parsed, err := url.Parse(iconURL)
		if err == nil && !(parsed.Host == "" && parsed.Path == "/favicon.ico") {
			return iconURL
		}
	}
	return coreui.AssetURL("images/favicon.svg")
}

func safeAssetPath(requestPath string) (string, error) {
	if !strings.HasPrefix(requestPath, "/theme-assets/") {
		return "", errors.New("invalid asset prefix")
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(requestPath, "/theme-assets/"))
	if err != nil || decoded == "" || strings.ContainsRune(decoded, '\x00') || strings.Contains(decoded, "\\") || strings.Contains(decoded, ":") {
		return "", errors.New("invalid asset path")
	}
	clean := path.Clean(decoded)
	if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("asset path escapes theme")
	}
	return clean, nil
}
