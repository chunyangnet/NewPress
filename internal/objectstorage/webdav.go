package objectstorage

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const webDAVMetadataLimit = 1 << 20

// webDAVBackend uses the protocol shared by Cloudreve 3 and 4. Metadata is
// obtained with PROPFIND: Cloudreve HEAD can redirect to a GET-signed URL.
type webDAVBackend struct {
	profile         Profile
	base            *url.URL
	client          *http.Client
	newClient       func(Profile) (*http.Client, error)
	mu              sync.Mutex
	directories     map[string]struct{}
	redirectClients map[string]*http.Client
}

func newWebDAVBackend(p Profile) (Backend, error) {
	u, err := url.Parse(p.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("WebDAV 地址必须是完整的 HTTP(S) 地址，且不包含凭据、查询参数或片段")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\\x00\r\n") {
			return nil, errors.New("WebDAV 地址路径无效")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = escapedDAVPath(u.Path)
	if p.Username == "" || p.Password == "" || strings.ContainsAny(p.Username, ":\r\n") || strings.ContainsAny(p.Password, "\r\n") {
		return nil, errors.New("请填写 WebDAV 用户名和专用密码")
	}
	client, err := newStorageHTTPClient(p)
	if err != nil {
		return nil, err
	}
	return &webDAVBackend{profile: p, base: u, client: client, newClient: newStorageHTTPClient, directories: make(map[string]struct{}), redirectClients: make(map[string]*http.Client)}, nil
}

func escapedDAVPath(p string) string {
	segments := strings.Split(p, "/")
	for i := range segments {
		segments[i] = strings.ReplaceAll(url.PathEscape(segments[i]), "+", "%2B")
	}
	return strings.Join(segments, "/")
}

func (b *webDAVBackend) objectURL(key string) (*url.URL, error) {
	if key == "" || len(key) > 4096 || !utf8.ValidString(key) || strings.ContainsAny(key, "\\\x00\r\n") {
		return nil, errors.New("WebDAV 对象路径无效")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, errors.New("WebDAV 对象路径无效")
		}
	}
	u := *b.base
	u.Path += "/" + key
	u.RawPath = escapedDAVPath(u.Path)
	return &u, nil
}

func (b *webDAVBackend) request(ctx context.Context, method string, u *url.URL, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, errors.New("无法构造 WebDAV 请求")
	}
	req.SetBasicAuth(b.profile.Username, b.profile.Password)
	return req, nil
}

func davRequestError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return &OperationError{"WebDAV 连接失败，请检查地址、网络和证书"}
}

func davStatusError(code int) error {
	if code == http.StatusNotFound {
		return ErrNotFound
	}
	return &OperationError{fmt.Sprintf("WebDAV 请求失败（HTTP %d）；请核对独立账号权限、目录、配额和代理限制", code)}
}

func closeDAVResponse(resp *http.Response) {
	// Bounded draining permits connection reuse without reading arbitrarily large
	// provider error pages. Neither error bodies nor signed URLs reach diagnostics.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

func (b *webDAVBackend) ensureDirectories(ctx context.Context, key string) error {
	segments := strings.Split(key, "/")
	for i := 1; i < len(segments); i++ {
		directory := strings.Join(segments[:i], "/")
		b.mu.Lock()
		_, cached := b.directories[directory]
		b.mu.Unlock()
		if cached {
			continue
		}
		u, err := b.objectURL(directory)
		if err != nil {
			return err
		}
		req, err := b.request(ctx, "MKCOL", u, nil)
		if err != nil {
			return err
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return davRequestError(ctx, err)
		}
		status := resp.StatusCode
		closeDAVResponse(resp)
		switch status {
		case http.StatusCreated:
		case http.StatusMethodNotAllowed, http.StatusConflict:
			_, isDirectory, err := b.stat(ctx, directory)
			if err != nil {
				return err
			}
			if !isDirectory {
				return errors.New("WebDAV 路径被同名文件占用")
			}
		default:
			return davStatusError(status)
		}
		b.mu.Lock()
		if len(b.directories) >= 512 {
			clear(b.directories)
		}
		b.directories[directory] = struct{}{}
		b.mu.Unlock()
	}
	return nil
}

func (b *webDAVBackend) Put(ctx context.Context, key string, body io.ReadSeeker, info ObjectInfo) error {
	u, err := b.objectURL(key)
	if err != nil {
		return err
	}
	if body == nil || info.Size < 0 {
		return errors.New("WebDAV 上传数据或长度无效")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := b.ensureDirectories(ctx, key); err != nil {
			return err
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return errors.New("无法读取 WebDAV 上传数据")
		}
		req, err := b.request(ctx, http.MethodPut, u, io.NopCloser(body))
		if err != nil {
			return err
		}
		// Do not use chunked transfer: Cloudreve determines the upload size from
		// Content-Length and treats an absent size as an empty file.
		req.ContentLength = info.Size
		if info.Size == 0 {
			req.Body = http.NoBody
		}
		req.Header.Set("Content-Type", info.ContentType)
		resp, err := b.client.Do(req)
		if err != nil {
			return davRequestError(ctx, err)
		}
		status := resp.StatusCode
		closeDAVResponse(resp)
		if status == http.StatusOK || status == http.StatusCreated || status == http.StatusNoContent {
			return nil
		}
		if status == http.StatusConflict && attempt == 0 {
			// An administrator may remove a previously cached parent directory.
			b.mu.Lock()
			clear(b.directories)
			b.mu.Unlock()
			continue
		}
		return davStatusError(status)
	}
	return errors.New("WebDAV 上传失败")
}

type davMultistatus struct {
	XMLName   xml.Name `xml:"DAV: multistatus"`
	Responses []struct {
		Href      string `xml:"href"`
		Status    string `xml:"status"`
		Propstats []struct {
			Status string `xml:"status"`
			Prop   struct {
				Length       string `xml:"getcontentlength"`
				Type         string `xml:"getcontenttype"`
				ETag         string `xml:"getetag"`
				ResourceType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func davXMLStatus(value string) int {
	fields := strings.Fields(value)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0
	}
	code, _ := strconv.Atoi(fields[1])
	return code
}

func (b *webDAVBackend) stat(ctx context.Context, key string) (ObjectInfo, bool, error) {
	u, err := b.objectURL(key)
	if err != nil {
		return ObjectInfo{}, false, err
	}
	body := `<d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:getcontenttype/><d:getetag/><d:resourcetype/></d:prop></d:propfind>`
	req, err := b.request(ctx, "PROPFIND", u, strings.NewReader(body))
	if err != nil {
		return ObjectInfo{}, false, err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	resp, err := b.client.Do(req)
	if err != nil {
		return ObjectInfo{}, false, davRequestError(ctx, err)
	}
	defer closeDAVResponse(resp)
	if resp.StatusCode != http.StatusMultiStatus {
		return ObjectInfo{}, false, davStatusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webDAVMetadataLimit+1))
	if err != nil {
		return ObjectInfo{}, false, davRequestError(ctx, err)
	}
	if len(data) > webDAVMetadataLimit {
		return ObjectInfo{}, false, errors.New("WebDAV 元数据响应过大")
	}
	var result davMultistatus
	if err := xml.Unmarshal(data, &result); err != nil || len(result.Responses) != 1 {
		return ObjectInfo{}, false, errors.New("WebDAV 元数据响应无效")
	}
	item := result.Responses[0]
	if item.Status != "" && davXMLStatus(item.Status) != http.StatusOK {
		return ObjectInfo{}, false, davStatusError(davXMLStatus(item.Status))
	}
	info := ObjectInfo{Size: -1}
	isDirectory, found := false, false
	for _, props := range item.Propstats {
		if davXMLStatus(props.Status) != http.StatusOK {
			continue
		}
		found = true
		p := props.Prop
		isDirectory = isDirectory || p.ResourceType.Collection != nil
		if p.Length != "" {
			size, err := strconv.ParseInt(strings.TrimSpace(p.Length), 10, 64)
			if err != nil || size < 0 {
				return ObjectInfo{}, false, errors.New("WebDAV 对象长度无效")
			}
			info.Size = size
		}
		if p.Type != "" {
			info.ContentType = strings.TrimSpace(p.Type)
		}
		if p.ETag != "" {
			info.ETag = strings.TrimSpace(p.ETag)
		}
	}
	if !found {
		return ObjectInfo{}, false, errors.New("WebDAV 元数据不可读取")
	}
	if isDirectory {
		return info, true, nil
	}
	if info.Size < 0 {
		return ObjectInfo{}, false, errors.New("WebDAV 未返回对象长度")
	}
	return info, false, nil
}

func (b *webDAVBackend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	info, directory, err := b.stat(ctx, key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if directory {
		return ObjectInfo{}, errors.New("WebDAV 对象路径指向目录")
	}
	return info, nil
}

func (b *webDAVBackend) redirectClient(target *url.URL) (*http.Client, error) {
	if target.Scheme == b.base.Scheme && strings.EqualFold(target.Host, b.base.Host) {
		return b.client, nil
	}
	if target.Scheme != "https" {
		return nil, errors.New("WebDAV 下载跳转必须使用 HTTPS")
	}
	origin := "https://" + target.Host
	b.mu.Lock()
	defer b.mu.Unlock()
	if client, ok := b.redirectClients[origin]; ok {
		return client, nil
	}
	p := b.profile
	p.Endpoint, p.AllowHTTP, p.AllowPrivate = origin, false, false
	p.Username, p.Password, p.AccessKey, p.SecretKey, p.SessionToken = "", "", "", "", ""
	client, err := b.newClient(p)
	if err != nil {
		return nil, errors.New("WebDAV 下载跳转地址不安全")
	}
	if len(b.redirectClients) >= 8 {
		for oldOrigin, oldClient := range b.redirectClients {
			oldClient.CloseIdleConnections()
			delete(b.redirectClients, oldOrigin)
			break
		}
	}
	b.redirectClients[origin] = client
	return client, nil
}

func (b *webDAVBackend) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	u, err := b.objectURL(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	req, err := b.request(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	client := b.client
	for redirects := 0; ; redirects++ {
		resp, err := client.Do(req)
		if err != nil {
			return nil, ObjectInfo{}, davRequestError(ctx, err)
		}
		switch resp.StatusCode {
		case http.StatusOK:
			return resp.Body, ObjectInfo{Size: resp.ContentLength, ContentType: resp.Header.Get("Content-Type"), ETag: resp.Header.Get("ETag")}, nil
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			target, err := resp.Location()
			closeDAVResponse(resp)
			if redirects >= 5 {
				return nil, ObjectInfo{}, errors.New("WebDAV 下载跳转次数过多")
			}
			if err != nil || target == nil || target.Host == "" || target.User != nil || target.Fragment != "" || (target.Scheme != "https" && target.Scheme != "http") {
				return nil, ObjectInfo{}, errors.New("WebDAV 下载跳转地址无效")
			}
			if req.URL.Scheme == "https" && target.Scheme != "https" {
				return nil, ObjectInfo{}, errors.New("WebDAV 下载不能降级到 HTTP")
			}
			client, err = b.redirectClient(target)
			if err != nil {
				return nil, ObjectInfo{}, err
			}
			// Construct a fresh request even for same-origin redirects. Never copy
			// Basic credentials, cookies, or provider headers to a signed URL.
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
			if err != nil {
				return nil, ObjectInfo{}, errors.New("WebDAV 下载跳转地址无效")
			}
		default:
			status := resp.StatusCode
			closeDAVResponse(resp)
			return nil, ObjectInfo{}, davStatusError(status)
		}
	}
}

func (b *webDAVBackend) Delete(ctx context.Context, key string) error {
	u, err := b.objectURL(key)
	if err != nil {
		return err
	}
	req, err := b.request(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return davRequestError(ctx, err)
	}
	defer closeDAVResponse(resp)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return davStatusError(resp.StatusCode)
}

func (b *webDAVBackend) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", ErrUnsupported
}

// Close releases pooled idle connections when the owning profile generation is
// replaced. In-flight requests remain controlled by their own contexts.
func (b *webDAVBackend) Close() error {
	b.client.CloseIdleConnections()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, client := range b.redirectClients {
		client.CloseIdleConnections()
	}
	clear(b.redirectClients)
	return nil
}
