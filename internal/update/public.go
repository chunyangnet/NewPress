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

// Public endpoints are pinned by the application, never supplied by a browser.
type publicRepository struct{ name, api, raw string }
type publicRevision struct {
	Source string `json:"source"`
	Commit string `json:"commit"`
}

func officialSource() Source {
	return Source{Official: true, Owner: "chunyangnet_0", Repo: "newpress", Ref: "releases", Manifest: "updates/beta.json"}
}

func officialRepositories() []publicRepository {
	return []publicRepository{
		{"GitHub", "https://api.github.com/repos/chunyangnet/NewPress", "https://raw.githubusercontent.com/chunyangnet/NewPress"},
		{"Gitee", "https://gitee.com/api/v5/repos/chunyangnet_0/newpress", "https://gitee.com/chunyangnet_0/newpress/raw"},
	}
}

func (c *giteeClient) publicGet(ctx context.Context, raw string) (*http.Response, error) {
	request := func(target string) (*http.Response, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, errors.New("无法创建公开更新请求")
		}
		r.Header.Set("User-Agent", "NewPress-Updater/1")
		r.Header.Set("Cache-Control", "no-cache")
		r.Header.Set("Accept", "application/octet-stream")
		if strings.HasPrefix(target, "https://api.github.com/") {
			r.Header.Set("Accept", "application/vnd.github+json")
		}
		response, err := c.public.Do(r)
		if err != nil {
			return nil, errors.New("无法连接公开更新源或请求超时")
		}
		return response, nil
	}
	r, err := request(raw)
	if err != nil {
		return nil, err
	}
	if r.StatusCode >= 300 && r.StatusCode < 400 {
		target, redirectErr := publicRedirect(raw, r.Header.Get("Location"))
		r.Body.Close()
		if redirectErr != nil {
			return nil, redirectErr
		}
		r, err = request(target)
		if err != nil {
			return nil, err
		}
	}
	if r.StatusCode != http.StatusOK {
		r.Body.Close()
		return nil, fmt.Errorf("公开更新源返回 HTTP %d；请确认发布分支和文件已同步", r.StatusCode)
	}
	return r, nil
}

func publicRedirect(origin, target string) (string, error) {
	fail := errors.New("公开更新源跳转未通过校验")
	o, err := url.Parse(origin)
	u, targetErr := safehttp.ValidateURL(target)
	if err != nil || targetErr != nil || o.Host != "gitee.com" || u.Host != "raw.giteeusercontent.com" || u.EscapedPath() != o.EscapedPath() || len(target) > 8192 {
		return "", fail
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["signature"]) != 1 || len(q.Get("signature")) > 4096 || !regexp.MustCompile(`^[A-Za-z0-9_+/=-]+$`).MatchString(q.Get("signature")) {
		return "", fail
	}
	return u.String(), nil
}

func (c *giteeClient) checkPublic(ctx context.Context, s Source, key ed25519.PublicKey) (Available, error) {
	type result struct {
		available Available
		revision  publicRevision
		err       error
	}
	results := make(chan result, len(c.repositories))
	for _, repository := range c.repositories {
		go func(repo publicRepository) {
			probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			var out result
			out.revision.Source = repo.name
			defer func() { results <- out }()
			r, err := c.publicGet(probeCtx, repo.api+"/commits/"+url.PathEscape(s.Ref))
			if err != nil {
				out.err = err
				return
			}
			var commit struct {
				SHA string `json:"sha"`
			}
			err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&commit)
			r.Body.Close()
			if err != nil || !commitPattern.MatchString(commit.SHA) {
				out.err = errors.New("公开仓库没有返回有效提交标识")
				return
			}
			out.revision.Commit = commit.SHA
			r, err = c.publicGet(probeCtx, repo.raw+"/"+commit.SHA+"/"+escapedPath(s.Manifest))
			if err != nil {
				out.err = err
				return
			}
			defer r.Body.Close()
			out.available, out.err = decodeSigned(r.Body, key)
		}(repository)
	}
	var selected Available
	var failures []string
	for range c.repositories {
		r := <-results
		if r.err != nil {
			failures = append(failures, r.revision.Source+"："+r.err.Error())
			continue
		}
		if selected.Manifest.Sequence == r.available.Manifest.Sequence && selected.PayloadHash != "" && selected.PayloadHash != r.available.PayloadHash {
			return Available{}, errors.New("官方双源同一发布序号的内容冲突，请等待发布者完成同步")
		}
		if r.available.Manifest.Sequence > selected.Manifest.Sequence {
			selected = r.available
			selected.Commit = r.revision.Commit
			selected.Mirrors = []publicRevision{r.revision}
		} else if r.available.Manifest.Sequence == selected.Manifest.Sequence {
			selected.Mirrors = append(selected.Mirrors, r.revision)
		}
	}
	if len(selected.Mirrors) == 0 {
		return Available{}, fmt.Errorf("官方公开更新源尚不可用；请确认已发布签名清单。%s", strings.Join(failures, "；"))
	}
	selected.Fingerprint = sourceFingerprint(s)
	return selected, nil
}
