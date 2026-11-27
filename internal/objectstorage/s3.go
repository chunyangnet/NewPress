package objectstorage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/logging"
)

const storageChecksumMetadata = "newpress-sha256"

type s3Backend struct {
	client     *s3.Client
	httpClient *http.Client
	bucket     string
	timeout    time.Duration
}

func newS3Backend(p Profile) (Backend, error) {
	httpClient, err := newStorageHTTPClient(p)
	if err != nil {
		return nil, err
	}
	options := s3.Options{
		Region: p.Region,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: p.AccessKey, SecretAccessKey: p.SecretKey, SessionToken: p.SessionToken, Source: "NewPressObjectStorage"}, nil
		}),
		HTTPClient:                                httpClient,
		UsePathStyle:                              p.PathStyle,
		RetryMaxAttempts:                          3,
		RequestChecksumCalculation:                aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation:                aws.ResponseChecksumValidationWhenRequired,
		DisableMultiRegionAccessPoints:            true,
		DisableLogOutputChecksumValidationSkipped: true,
		Logger: logging.Nop{},
	}
	if p.Endpoint != "" {
		options.BaseEndpoint = aws.String(p.Endpoint)
	}
	return &s3Backend{client: s3.New(options), httpClient: httpClient, bucket: p.Bucket, timeout: storageOperationTimeout(p)}, nil
}

func (b *s3Backend) Close() error {
	b.httpClient.CloseIdleConnections()
	return nil
}

func (b *s3Backend) Put(ctx context.Context, key string, body io.ReadSeeker, info ObjectInfo) error {
	if err := validateStorageUpload(key, body, info); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket), Key: aws.String(key), Body: body,
		ContentLength: aws.Int64(info.Size), ContentType: aws.String(info.ContentType),
		Metadata: storageMetadata(info),
	})
	return safeStorageError("上传", err)
}

func (b *s3Backend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if err := validateStorageKey(key); err != nil {
		return ObjectInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	result, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)})
	if err != nil {
		return ObjectInfo{}, safeStorageError("查询", err)
	}
	size := int64(-1)
	if result.ContentLength != nil {
		size = *result.ContentLength
	}
	return ObjectInfo{Size: size, ContentType: aws.ToString(result.ContentType), SHA256: storageMetadataChecksum(result.Metadata), ETag: aws.ToString(result.ETag)}, nil
}

func (b *s3Backend) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateStorageKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	result, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)})
	if err != nil {
		cancel()
		return nil, ObjectInfo{}, safeStorageError("读取", err)
	}
	size := int64(-1)
	if result.ContentLength != nil {
		size = *result.ContentLength
	}
	info := ObjectInfo{Size: size, ContentType: aws.ToString(result.ContentType), SHA256: storageMetadataChecksum(result.Metadata), ETag: aws.ToString(result.ETag)}
	return &storageReadCloser{ReadCloser: result.Body, cancel: cancel}, info, nil
}

func (b *s3Backend) Delete(ctx context.Context, key string) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)})
	err = safeStorageError("删除", err)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (b *s3Backend) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := validateStorageSignedURL(key, ttl); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	result, err := s3.NewPresignClient(b.client).PresignGetObject(ctx,
		&s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)},
		func(options *s3.PresignOptions) { options.Expires = ttl },
	)
	if err != nil {
		return "", safeStorageError("生成下载链接", err)
	}
	return result.URL, nil
}

func storageOperationTimeout(p Profile) time.Duration {
	seconds := p.TimeoutSeconds
	if seconds <= 0 {
		seconds = 60
	}
	return time.Duration(min(seconds, 600)) * time.Second
}

func validateStorageKey(key string) error {
	if key == "" || len(key) > 1024 || !utf8.ValidString(key) || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return errors.New("对象路径无效")
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("对象路径无效")
		}
	}
	for _, char := range key {
		if unicode.IsControl(char) {
			return errors.New("对象路径无效")
		}
	}
	return nil
}

func validateStorageUpload(key string, body io.ReadSeeker, info ObjectInfo) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	if body == nil || info.Size < 0 || strings.ContainsAny(info.ContentType, "\r\n") {
		return errors.New("上传内容无效")
	}
	if info.SHA256 != "" {
		decoded, err := hex.DecodeString(info.SHA256)
		if err != nil || len(decoded) != 32 {
			return errors.New("上传校验值无效")
		}
	}
	size, err := body.Seek(0, io.SeekEnd)
	if err != nil || size != info.Size {
		return errors.New("上传内容长度不匹配")
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return errors.New("无法读取上传内容")
	}
	return nil
}

func validateStorageSignedURL(key string, ttl time.Duration) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	if ttl < time.Second || ttl > 7*24*time.Hour {
		return errors.New("下载链接有效期须为 1 秒至 7 天")
	}
	return nil
}

func storageMetadata(info ObjectInfo) map[string]string {
	if info.SHA256 == "" {
		return nil
	}
	return map[string]string{storageChecksumMetadata: strings.ToLower(info.SHA256)}
}

func storageMetadataChecksum(metadata map[string]string) string {
	for key, value := range metadata {
		if strings.EqualFold(key, storageChecksumMetadata) {
			decoded, err := hex.DecodeString(value)
			if err == nil && len(decoded) == 32 {
				return strings.ToLower(value)
			}
		}
	}
	return ""
}

// SDK errors may include credentials, provider response bodies and complete URLs.
// Preserve only context sentinels and HTTP status; never wrap the original error.
func safeStorageError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	status := 0
	var awsStatus interface{ HTTPStatusCode() int }
	var ossStatus interface{ HttpStatusCode() int }
	if errors.As(err, &awsStatus) {
		status = awsStatus.HTTPStatusCode()
	} else if errors.As(err, &ossStatus) {
		status = ossStatus.HttpStatusCode()
	}
	if status == http.StatusNotFound {
		return ErrNotFound
	}
	if status >= 300 && status <= 599 {
		return &OperationError{Message: fmt.Sprintf("对象存储%s失败（HTTP %d）", operation, status)}
	}
	return &OperationError{Message: fmt.Sprintf("对象存储%s失败，请检查连接、配置与权限", operation)}
}

type storageReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *storageReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	return n, safeStorageError("读取", err)
}

func (r *storageReadCloser) Close() error {
	defer r.cancel()
	return safeStorageError("关闭连接", r.ReadCloser.Close())
}
