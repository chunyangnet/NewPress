package wechat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"newpress.local/newpress/internal/safehttp"
)

// No ambient proxy, arbitrary origin, private address or unchecked redirect is
// used by the collection and official API clients. DNS is checked at dial time.
func publicIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	return ok && safehttp.PublicIP(address)
}
func allowedURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	switch u.Hostname() {
	case "api.weixin.qq.com", "mp.weixin.qq.com", "mmbiz.qpic.cn", "mmbiz.qlogo.cn":
		return true
	}
	return false
}
func safeHTTP() *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxIdleConns: 8, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("远程地址无效")
		}
		if !allowedURL(&url.URL{Scheme: "https", Host: address}) {
			return nil, errors.New("不支持该远程主机")
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("无法解析微信服务器")
		}
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, errors.New("远程地址不能指向本地或内网")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !allowedURL(req.URL) || req.URL.Hostname() != via[0].URL.Hostname() {
			return errors.New("不支持的微信跳转地址")
		}
		return nil
	}}
}

type APIError struct {
	Code      int
	Uncertain bool
	Message   string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("微信接口返回错误（%d）", e.Code)
}
func apiMessage(code int) string {
	switch code {
	case 40013:
		return "AppID 不正确"
	case 40125:
		return "AppSecret 不正确"
	case 40164:
		return "请将本站服务器出口 IP 加入公众号接口 IP 白名单"
	case 48001:
		return "公众号未获得此接口权限，请在微信公众平台检查账号的接口权限"
	case 45009, 45011:
		return "微信接口调用额度已达上限，请稍后重试"
	case 40007:
		return "公众号素材已失效，请核对封面或草稿素材 ID"
	case 53503:
		return "草稿未通过微信发布检查，请在公众平台核对内容"
	case 53504:
		return "微信要求在公众平台内操作此草稿，请前往公众号后台"
	case 89503:
		return "微信要求公众号管理员确认此次接口调用"
	}
	return fmt.Sprintf("微信接口拒绝请求（错误码 %d），请核对账号权限和内容", code)
}

type Client struct {
	HTTP              *http.Client
	mu                sync.Mutex
	token, credential string
	expires           time.Time
	refreshing        chan struct{}
}

func NewClient() *Client { return &Client{HTTP: safeHTTP()} }
func (c *Client) do(ctx context.Context, endpoint string, body []byte, contentType string, mutation bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.weixin.qq.com"+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("微信请求无效")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &APIError{Uncertain: mutation, Message: "连接微信失败或请求超时"}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || response.StatusCode != 200 {
		return nil, &APIError{Uncertain: mutation, Message: "微信返回结果不完整，需核对操作结果"}
	}
	code, err := responseErrorCode(data)
	if err != nil {
		return nil, &APIError{Uncertain: mutation, Message: "微信返回结果无效"}
	}
	if code != 0 {
		return nil, &APIError{Code: code, Message: apiMessage(code)}
	}
	return data, nil
}
func (c *Client) Token(ctx context.Context, cfg Config) (string, error) {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(cfg.AppID+"\x00"+cfg.Secret)))
	for {
		c.mu.Lock()
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return "", err
		}
		if c.credential == key && time.Now().Before(c.expires) && c.token != "" {
			token := c.token
			c.mu.Unlock()
			return token, nil
		}
		if pending := c.refreshing; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-pending:
				continue // Recheck both credentials and cache after the refresh.
			}
		}
		pending := make(chan struct{})
		c.refreshing = pending
		c.mu.Unlock()

		// Only the refreshing caller owns the network request. Waiters may
		// leave independently without canceling it or holding the cache lock.
		token, expires, err := c.fetchToken(ctx, cfg)
		c.mu.Lock()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			c.token, c.credential, c.expires = token, key, expires
		}
		c.refreshing = nil
		close(pending)
		c.mu.Unlock()
		if err != nil {
			return "", err
		}
		return token, nil
	}
}

func (c *Client) fetchToken(ctx context.Context, cfg Config) (string, time.Time, error) {
	body, _ := json.Marshal(map[string]any{"grant_type": "client_credential", "appid": cfg.AppID, "secret": cfg.Secret, "force_refresh": false})
	data, err := c.do(ctx, "/cgi-bin/stable_token", body, "application/json", false)
	if err != nil {
		return "", time.Time{}, err
	}
	var result struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if json.Unmarshal(data, &result) != nil || result.Token == "" || len(result.Token) > 2048 || result.Expires <= 0 {
		return "", time.Time{}, errors.New("微信未返回有效接口凭证")
	}
	return result.Token, time.Now().Add(time.Duration(max(1, result.Expires-60)) * time.Second), nil
}
func (c *Client) request(ctx context.Context, cfg Config, path string, body []byte, contentType string, mutation bool, guard func(context.Context) error) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.Token(ctx, cfg)
		if err != nil {
			return nil, err
		}
		// Token acquisition may wait on the network, including an expiry retry.
		// Revalidate the operation after that wait, before any write is sent.
		if guard != nil {
			if err = guard(ctx); err != nil {
				return nil, err
			}
		}
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		data, err := c.do(ctx, path+sep+"access_token="+url.QueryEscape(token), body, contentType, mutation)
		var api *APIError
		if attempt == 0 && errors.As(err, &api) && (api.Code == 40014 || api.Code == 42001 || api.Code == 40001) {
			c.mu.Lock()
			if c.token == token {
				c.expires = time.Time{}
			}
			c.mu.Unlock()
			continue
		}
		return data, err
	}
	return nil, errors.New("微信接口凭证失效，请稍后重试")
}
func (c *Client) json(ctx context.Context, cfg Config, path string, input, output any, mutation bool, guard func(context.Context) error) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	data, err := c.request(ctx, cfg, path, body, "application/json", mutation, guard)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, output); err != nil {
		return &APIError{Uncertain: mutation, Message: "微信返回的数据格式无效"}
	}
	return nil
}

type Article struct {
	Type        string `json:"article_type"`
	Title       string `json:"title"`
	Author      string `json:"author,omitempty"`
	Digest      string `json:"digest,omitempty"`
	Content     string `json:"content"`
	SourceURL   string `json:"content_source_url,omitempty"`
	Thumb       string `json:"thumb_media_id"`
	OpenComment int    `json:"need_open_comment"`
	FansComment int    `json:"only_fans_can_comment"`
}

func (c *Client) Upload(ctx context.Context, cfg Config, data []byte, cover bool) (string, error) {
	return c.upload(ctx, cfg, data, cover, nil)
}

func (c *Client) upload(ctx context.Context, cfg Config, data []byte, cover bool, guard func(context.Context) error) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("media", "image.jpg")
	_, _ = part.Write(data)
	_ = writer.Close()
	path := "/cgi-bin/media/uploadimg"
	if cover {
		path = "/cgi-bin/material/add_material?type=image"
	}
	raw, err := c.request(ctx, cfg, path, body.Bytes(), writer.FormDataContentType(), false, guard)
	if err != nil {
		return "", err
	}
	var result struct {
		ID  string `json:"media_id"`
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return "", errors.New("微信图片响应无效")
	}
	if cover && validRemoteID(result.ID) {
		return result.ID, nil
	}
	if !cover {
		if u, e := ImageURL(result.URL); e == nil {
			return u, nil
		}
	}
	return "", errors.New("微信未返回有效图片地址或素材 ID")
}
func (c *Client) Draft(ctx context.Context, cfg Config, a Article) (string, error) {
	return c.draft(ctx, cfg, a, nil)
}

func (c *Client) draft(ctx context.Context, cfg Config, a Article, guard func(context.Context) error) (string, error) {
	var result struct {
		ID string `json:"media_id"`
	}
	err := c.json(ctx, cfg, "/cgi-bin/draft/add", map[string]any{"articles": []Article{a}}, &result, true, guard)
	if err != nil {
		return "", err
	}
	if !validRemoteID(result.ID) {
		return "", &APIError{Uncertain: true, Message: "微信草稿响应未提供有效素材 ID，请在公众平台核对"}
	}
	return result.ID, nil
}
func (c *Client) Publish(ctx context.Context, cfg Config, id string) (string, error) {
	return c.publish(ctx, cfg, id, nil)
}

func (c *Client) publish(ctx context.Context, cfg Config, id string, guard func(context.Context) error) (string, error) {
	var result struct {
		ID json.RawMessage `json:"publish_id"`
	}
	err := c.json(ctx, cfg, "/cgi-bin/freepublish/submit", map[string]string{"media_id": id}, &result, true, guard)
	if err != nil {
		return "", err
	}
	value, valid := publicationID(result.ID)
	if !valid {
		return "", &APIError{Uncertain: true, Message: "微信发表响应未提供有效任务 ID，请在公众平台核对"}
	}
	return value, nil
}

type PublishResult struct {
	PublishID json.RawMessage `json:"publish_id"`
	Status    *int            `json:"publish_status"`
	ArticleID string          `json:"article_id"`
	Detail    struct {
		Items []struct {
			URL string `json:"article_url"`
		} `json:"item"`
	} `json:"article_detail"`
}

func (c *Client) Poll(ctx context.Context, cfg Config, id string) (PublishResult, error) {
	var r PublishResult
	err := c.json(ctx, cfg, "/cgi-bin/freepublish/get", map[string]string{"publish_id": id}, &r, false, nil)
	if err != nil {
		return r, err
	}
	returned, valid := publicationID(r.PublishID)
	if !valid || returned != id {
		return r, errors.New("微信返回的发表任务标识缺失或不匹配，稍后继续核对原任务")
	}
	if r.Status == nil || *r.Status < 0 || *r.Status > 6 {
		return r, errors.New("微信未返回有效发表状态，稍后继续核对原任务")
	}
	if *r.Status == 0 && !validOpaqueID(r.ArticleID) {
		return r, errors.New("微信已返回成功，但文章标识无效，稍后继续核对原任务")
	}
	return r, err
}
func ImageURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("图片地址无效")
	}
	if u.Scheme == "http" || u.Scheme == "" {
		u.Scheme = "https"
	}
	if !allowedURL(u) || (u.Hostname() != "mmbiz.qpic.cn" && u.Hostname() != "mmbiz.qlogo.cn") || len(raw) > 2048 {
		return "", errors.New("图片必须来自微信公众号图片服务")
	}
	u.Fragment = ""
	return u.String(), nil
}
func publishFailure(status int) string {
	switch status {
	case 2:
		return "微信原创审核未通过"
	case 3:
		return "微信常规发布检查未通过"
	case 4:
		return "微信平台审核未通过"
	case 5:
		return "公众号文章已被删除"
	case 6:
		return "公众号文章已被平台屏蔽"
	}
	return "微信发表失败（状态 " + strconv.Itoa(status) + "）"
}
