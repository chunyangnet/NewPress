package update

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type publicDownload struct{ name, url string }

// Proxies only transport public, commit-pinned bytes. The signed manifest remains
// the authority for the complete package size and hash; no private URL is proxied.
func (c *giteeClient) publicDownloads(available Available, artifact Artifact) ([]publicDownload, bool) {
	var candidates []publicDownload
	seen := map[string]bool{}
	add := func(name, url string) {
		if !seen[url] {
			seen[url] = true
			candidates = append(candidates, publicDownload{name, url})
		}
	}
	accelerated := false
	for _, mirror := range available.Mirrors {
		if !commitPattern.MatchString(mirror.Commit) {
			continue
		}
		for _, repo := range c.repositories {
			if mirror.Source == repo.name {
				add(repo.name, repo.raw+"/"+mirror.Commit+"/"+escapedPath(artifact.Path))
			}
			// Official mirrors publish the same Git objects. A signed manifest
			// checked on Gitee can therefore identify the corresponding GitHub URL.
			if repo.name == "GitHub" && repo.raw == "https://raw.githubusercontent.com/chunyangnet/NewPress" {
				original := repo.raw + "/" + mirror.Commit + "/" + escapedPath(artifact.Path)
				add("GitHub", original)
				for _, proxy := range []publicDownload{{"gh-proxy", "https://gh-proxy.com/"}, {"ghfast", "https://ghfast.top/"}, {"ghproxy", "https://ghproxy.net/"}} {
					add(proxy.name, proxy.url+original)
				}
				accelerated = true
			}
		}
	}
	return candidates, accelerated
}

// Every candidate transfers the same bounded file sample. Compare elapsed time,
// rather than ping or homepage latency, and retain failed probes as last resorts.
func (c *giteeClient) rankPublicDownloads(ctx context.Context, candidates []publicDownload, size int64) []publicDownload {
	type measurement struct {
		candidate publicDownload
		elapsed   time.Duration
		index     int
		ok        bool
	}
	results := make(chan measurement, len(candidates))
	for index, candidate := range candidates {
		go func() {
			out := measurement{candidate: candidate, index: index}
			defer func() { results <- out }()
			probe, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			limit := min(size, int64(256*1024))
			if limit < 4 {
				return
			}
			r, err := http.NewRequestWithContext(probe, http.MethodGet, candidate.url, nil)
			if err != nil {
				return
			}
			r.Header.Set("User-Agent", "NewPress-Updater/1")
			r.Header.Set("Range", "bytes=0-262143")
			r.Header.Set("Accept-Encoding", "identity")
			started := time.Now()
			response, err := c.public.Do(r)
			if err != nil {
				return
			}
			defer response.Body.Close()
			if (response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent) || strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/html") {
				return
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, limit))
			if err != nil || int64(len(data)) != limit || !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
				return
			}
			out.ok, out.elapsed = true, time.Since(started)
		}()
	}
	measured := make([]measurement, 0, len(candidates))
	for range candidates {
		measured = append(measured, <-results)
	}
	sort.Slice(measured, func(i, j int) bool {
		a, b := measured[i], measured[j]
		if a.ok != b.ok {
			return a.ok
		}
		if a.ok && a.elapsed != b.elapsed {
			return a.elapsed < b.elapsed
		}
		return a.index < b.index
	})
	ordered := make([]publicDownload, len(measured))
	for i, item := range measured {
		ordered[i] = item.candidate
	}
	return ordered
}
