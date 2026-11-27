package objectstorage

import (
	"context"
	"encoding/xml"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type RemoteFile struct {
	Key       string `json:"key"`
	Size      int64  `json:"size"`
	Directory bool   `json:"directory"`
	Managed   bool   `json:"managed"`
}
type RemotePage struct {
	Files []RemoteFile `json:"files"`
	Next  string       `json:"next"`
}
type remoteLister interface {
	List(context.Context, string, string) (RemotePage, error)
}

func (b *s3Backend) List(ctx context.Context, prefix, token string) (RemotePage, error) {
	result := RemotePage{Files: []RemoteFile{}}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	input := &s3.ListObjectsV2Input{Bucket: aws.String(b.bucket), Prefix: aws.String(prefix), Delimiter: aws.String("/"), MaxKeys: aws.Int32(100)}
	if token != "" {
		input.ContinuationToken = aws.String(token)
	}
	page, err := b.client.ListObjectsV2(ctx, input)
	if err != nil {
		return result, safeStorageError("浏览目录", err)
	}
	for _, d := range page.CommonPrefixes {
		result.Files = append(result.Files, RemoteFile{Key: aws.ToString(d.Prefix), Directory: true})
	}
	for _, f := range page.Contents {
		if aws.ToString(f.Key) != prefix {
			result.Files = append(result.Files, RemoteFile{Key: aws.ToString(f.Key), Size: aws.ToInt64(f.Size), Directory: strings.HasSuffix(aws.ToString(f.Key), "/")})
		}
	}
	result.Next = aws.ToString(page.NextContinuationToken)
	return result, nil
}

func (b *ossBackend) List(ctx context.Context, prefix, token string) (RemotePage, error) {
	result := RemotePage{Files: []RemoteFile{}}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	input := &oss.ListObjectsV2Request{Bucket: oss.Ptr(b.bucket), Prefix: oss.Ptr(prefix), Delimiter: oss.Ptr("/"), MaxKeys: 100}
	if token != "" {
		input.ContinuationToken = oss.Ptr(token)
	}
	page, err := b.client.ListObjectsV2(ctx, input)
	if err != nil {
		return result, safeStorageError("浏览目录", err)
	}
	for _, d := range page.CommonPrefixes {
		result.Files = append(result.Files, RemoteFile{Key: oss.ToString(d.Prefix), Directory: true})
	}
	for _, f := range page.Contents {
		if oss.ToString(f.Key) != prefix {
			result.Files = append(result.Files, RemoteFile{Key: oss.ToString(f.Key), Size: f.Size, Directory: strings.HasSuffix(oss.ToString(f.Key), "/")})
		}
	}
	result.Next = oss.ToString(page.NextContinuationToken)
	return result, nil
}

func (b *webDAVBackend) List(ctx context.Context, prefix, token string) (RemotePage, error) {
	result := RemotePage{Files: []RemoteFile{}}
	u := b.base
	var err error
	if prefix != "" {
		u, err = b.objectURL(strings.TrimSuffix(prefix, "/"))
		if err != nil {
			return result, err
		}
	}
	req, err := b.request(ctx, "PROPFIND", u, strings.NewReader(`<d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:resourcetype/></d:prop></d:propfind>`))
	if err != nil {
		return result, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := b.client.Do(req)
	if err != nil {
		return result, davRequestError(ctx, err)
	}
	defer closeDAVResponse(resp)
	if resp.StatusCode != http.StatusMultiStatus {
		return result, davStatusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webDAVMetadataLimit+1))
	if err != nil || len(data) > webDAVMetadataLimit {
		return result, &OperationError{"目录响应过大或读取失败，请缩小目录范围"}
	}
	var page davMultistatus
	if xml.Unmarshal(data, &page) != nil {
		return result, &OperationError{"目录响应无效"}
	}
	for _, entry := range page.Responses {
		href, e := url.Parse(entry.Href)
		if e != nil || (href.Host != "" && href.Host != b.base.Host) {
			continue
		}
		root := strings.TrimRight(b.base.Path, "/") + "/"
		if !strings.HasPrefix(href.Path, root) {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(href.Path, root), "/")
		if key == strings.TrimSuffix(prefix, "/") || !strings.HasPrefix(key, prefix) || strings.Contains(strings.TrimPrefix(key, prefix), "/") {
			continue
		}
		for _, prop := range entry.Propstats {
			if davXMLStatus(prop.Status) != http.StatusOK {
				continue
			}
			directory := prop.Prop.ResourceType.Collection != nil
			size, _ := strconv.ParseInt(prop.Prop.Length, 10, 64)
			if directory {
				key += "/"
			}
			result.Files = append(result.Files, RemoteFile{Key: key, Size: size, Directory: directory})
			break
		}
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Key < result.Files[j].Key })
	start := 0
	if token != "" {
		start, err = strconv.Atoi(token)
		if err != nil || start < 0 {
			return RemotePage{}, &OperationError{"分页参数无效"}
		}
	}
	start = min(start, len(result.Files))
	end := min(start+100, len(result.Files))
	if end < len(result.Files) {
		result.Next = strconv.Itoa(end)
	}
	result.Files = result.Files[start:end]
	return result, nil
}
