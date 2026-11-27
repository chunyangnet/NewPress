package wechat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// CollectArticles keeps input order, limits simultaneous reads, and leaves
// time for signing partial previews before the HTTP handler's own deadline.
func (c *Client) CollectArticles(ctx context.Context, urls []string) ([]Collected, []CollectFailure, error) {
	return c.collectArticles(ctx, urls, 12*time.Second, 40*time.Second)
}

func (c *Client) collectArticles(ctx context.Context, urls []string, requestTimeout, batchTimeout time.Duration) ([]Collected, []CollectFailure, error) {
	if len(urls) < 1 || len(urls) > MaxCollect {
		return nil, nil, errors.New("每批请输入 1–10 个文章链接")
	}
	type result struct {
		url     string
		article Collected
		err     error
	}
	results := []result{}
	seen := map[string]bool{}
	for _, raw := range urls {
		address, err := ArticleURL(raw)
		key := address
		if err != nil {
			key = strings.TrimSpace(raw)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, result{url: key, err: err})
	}
	readCtx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()
	queue := make(chan int, len(results))
	for i := range results {
		if results[i].err == nil {
			queue <- i
		}
	}
	close(queue)
	var workers sync.WaitGroup
	for range min(3, len(results)) {
		workers.Go(func() {
			for index := range queue {
				item := &results[index]
				if readCtx.Err() != nil {
					item.err = errors.New("本批采集时间已到，可单独重试此链接")
					continue
				}
				requestCtx, stop := context.WithTimeout(readCtx, requestTimeout)
				item.article, item.err = c.FetchArticle(requestCtx, item.url)
				if requestCtx.Err() != nil {
					item.err = errors.New("文章读取超时或中断，可单独重试此链接")
				}
				stop()
			}
		})
	}
	workers.Wait()
	// A disconnected caller cannot use a preview; a batch-only timeout can.
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	articles, failures := []Collected{}, []CollectFailure{}
	identities, total := map[string]bool{}, 0
	for _, item := range results {
		if item.err != nil {
			failures = append(failures, CollectFailure{URL: item.url, Message: item.err.Error()})
			continue
		}
		if identities[item.article.Key] {
			continue
		}
		total += len(item.article.Body)
		if total > 8<<20 {
			return nil, nil, errors.New("本批正文总计超过 8 MiB，请拆分链接")
		}
		identities[item.article.Key] = true
		articles = append(articles, item.article)
	}
	return articles, failures, nil
}
