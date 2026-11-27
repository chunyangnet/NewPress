// Package objectstorage implements the reviewed, optional object-storage plugin.
// Original media remains the recovery copy; remote objects have an isolated namespace.
package objectstorage

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound    = errors.New("远端对象不存在")
	ErrUnsupported = errors.New("此存储不支持该操作")
	ErrConflict    = errors.New("配置已被其他页面修改，请刷新后重试")
)

// Profile keeps endpoint credentials separate from the optional public CDN URL.
// Secrets are persisted privately and are always removed from browser responses.
type Profile struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Endpoint       string `json:"endpoint"`
	Region         string `json:"region"`
	Bucket         string `json:"bucket"`
	Prefix         string `json:"prefix"`
	PublicURL      string `json:"public_url"`
	AccessKey      string `json:"access_key,omitempty"`
	SecretKey      string `json:"secret_key,omitempty"`
	SessionToken   string `json:"session_token,omitempty"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	PathStyle      bool   `json:"path_style"`
	AllowPrivate   bool   `json:"allow_private"`
	AllowHTTP      bool   `json:"allow_http"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// ObjectInfo describes immutable bytes. ETag is deliberately not a checksum:
// providers differ, especially for encryption and multipart uploads.
type ObjectInfo struct {
	Size        int64
	ContentType string
	SHA256      string
	ETag        string
}

type Backend interface {
	Put(context.Context, string, io.ReadSeeker, ObjectInfo) error
	Head(context.Context, string) (ObjectInfo, error)
	Open(context.Context, string) (io.ReadCloser, ObjectInfo, error)
	Delete(context.Context, string) error
	SignedURL(context.Context, string, time.Duration) (string, error)
}

type BackendFactory func(Profile) (Backend, error)

func NewBackend(p Profile) (Backend, error) {
	switch p.Provider {
	case "s3", "r2", "cos", "qiniu", "s3-compatible":
		return newS3Backend(p)
	case "oss":
		return newOSSBackend(p)
	case "cloudreve", "webdav":
		return newWebDAVBackend(p)
	default:
		return nil, errors.New("不支持的存储服务")
	}
}
