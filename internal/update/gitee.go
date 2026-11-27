package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"newpress.local/newpress/internal/safehttp"
)

type giteeClient struct {
	client       *http.Client
	base         string
	public       *http.Client
	repositories []publicRepository
}

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func newGitee() *giteeClient {
	return &giteeClient{base: "https://gitee.com/api/v5", client: &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, public: safehttp.ClientWithHeaderTimeout(15*time.Minute, 45*time.Second), repositories: officialRepositories()}
}

func (c *giteeClient) get(ctx context.Context, s Source, route, ref string) (*http.Response, error) {
	endpoint := c.base + "/repos/" + url.PathEscape(s.Owner) + "/" + url.PathEscape(s.Repo) + route
	q := url.Values{"access_token": {s.Token}}
	if ref != "" {
		q.Set("ref", ref)
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("无法创建 Gitee 请求")
	}
	r.Header.Set("User-Agent", "NewPress-Updater/1")
	response, err := c.client.Do(r)
	// Transport errors may contain the URL and its access_token. Never expose them.
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("更新请求已取消或超时")
		}
		return nil, errors.New("无法连接 Gitee，请检查网络后重试")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		switch response.StatusCode {
		case 401, 403:
			return nil, errors.New("Gitee 令牌无效或没有私有仓库读取权限")
		case 404:
			return nil, errors.New("Gitee 仓库、分支或发布文件不存在")
		case 429:
			return nil, errors.New("Gitee 请求频率受限，请稍后重试")
		}
		return nil, fmt.Errorf("Gitee 返回 HTTP %d，未下载更新文件", response.StatusCode)
	}
	return response, nil
}

func escapedPath(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (c *giteeClient) check(ctx context.Context, s Source, key ed25519.PublicKey) (Available, error) {
	if err := s.validate(); err != nil {
		return Available{}, err
	}
	if s.Official {
		return c.checkPublic(ctx, s, key)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r, err := c.get(ctx, s, "/commits/"+url.PathEscape(s.Ref), "")
	if err != nil {
		return Available{}, err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&commit)
	r.Body.Close()
	if err != nil || !commitPattern.MatchString(commit.SHA) {
		return Available{}, errors.New("Gitee 没有返回有效的提交标识")
	}
	r, err = c.get(ctx, s, "/raw/"+escapedPath(s.Manifest), commit.SHA)
	if err != nil {
		return Available{}, err
	}
	defer r.Body.Close()
	a, err := decodeSigned(r.Body, key)
	if err != nil {
		return Available{}, err
	}
	a.Commit = commit.SHA
	a.Fingerprint = sourceFingerprint(s)
	return a, nil
}
