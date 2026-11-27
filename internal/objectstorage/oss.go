package objectstorage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
)

type ossBackend struct {
	client     *oss.Client
	httpClient *http.Client
	bucket     string
	timeout    time.Duration
}

func newOSSBackend(p Profile) (Backend, error) {
	httpClient, err := newStorageHTTPClient(p)
	if err != nil {
		return nil, err
	}
	// NewConfig deliberately avoids the SDK's environment and logging defaults.
	config := oss.NewConfig().WithRegion(p.Region).WithEndpoint(p.Endpoint).
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(p.AccessKey, p.SecretKey, p.SessionToken)).
		WithHttpClient(httpClient).WithRetryMaxAttempts(3).WithUsePathStyle(p.PathStyle).
		WithSignatureVersion(oss.SignatureVersionV4).WithEnabledRedirect(false).
		WithLogLevel(oss.LogOff)
	config.HttpClient = ossHTTPClient{client: httpClient}
	return &ossBackend{client: oss.NewClient(config), httpClient: httpClient, bucket: p.Bucket, timeout: storageOperationTimeout(p)}, nil
}

func (b *ossBackend) Close() error {
	b.httpClient.CloseIdleConnections()
	return nil
}

// OSS v2 wraps even an explicitly empty body in its progress/checksum reader.
// net/http treats a non-nil body with ContentLength == 0 as unknown length and
// otherwise emits chunked PUTs. Keep the SDK's retry reader intact while giving
// the transport an unambiguously empty body for known zero-byte uploads.
type ossHTTPClient struct{ client *http.Client }

func (c ossHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if request.ContentLength == 0 && request.Header.Get("Content-Length") == "0" {
		request = request.Clone(request.Context())
		request.Body = http.NoBody
	}
	return c.client.Do(request)
}

func (b *ossBackend) Put(ctx context.Context, key string, body io.ReadSeeker, info ObjectInfo) error {
	if err := validateStorageUpload(key, body, info); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	_, err := b.client.PutObject(ctx, &oss.PutObjectRequest{
		Bucket: oss.Ptr(b.bucket), Key: oss.Ptr(key), Body: body,
		ContentLength: oss.Ptr(info.Size), ContentType: oss.Ptr(info.ContentType),
		Metadata: storageMetadata(info),
	})
	return safeStorageError("上传", err)
}

func (b *ossBackend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if err := validateStorageKey(key); err != nil {
		return ObjectInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	result, err := b.client.HeadObject(ctx, &oss.HeadObjectRequest{Bucket: oss.Ptr(b.bucket), Key: oss.Ptr(key)})
	if err != nil {
		return ObjectInfo{}, safeStorageError("查询", err)
	}
	return ObjectInfo{Size: result.ContentLength, ContentType: oss.ToString(result.ContentType), SHA256: storageMetadataChecksum(result.Metadata), ETag: oss.ToString(result.ETag)}, nil
}

func (b *ossBackend) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateStorageKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	result, err := b.client.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(b.bucket), Key: oss.Ptr(key)})
	if err != nil {
		cancel()
		return nil, ObjectInfo{}, safeStorageError("读取", err)
	}
	info := ObjectInfo{Size: result.ContentLength, ContentType: oss.ToString(result.ContentType), SHA256: storageMetadataChecksum(result.Metadata), ETag: oss.ToString(result.ETag)}
	return &storageReadCloser{ReadCloser: result.Body, cancel: cancel}, info, nil
}

func (b *ossBackend) Delete(ctx context.Context, key string) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	_, err := b.client.DeleteObject(ctx, &oss.DeleteObjectRequest{Bucket: oss.Ptr(b.bucket), Key: oss.Ptr(key)})
	err = safeStorageError("删除", err)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (b *ossBackend) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := validateStorageSignedURL(key, ttl); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	result, err := b.client.Presign(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(b.bucket), Key: oss.Ptr(key)}, oss.PresignExpires(ttl))
	if err != nil {
		return "", safeStorageError("生成下载链接", err)
	}
	return result.URL, nil
}
