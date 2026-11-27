package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"newpress.local/newpress/internal/aijobs"
	"newpress.local/newpress/internal/safehttp"
	"os"
	"strings"
	"time"
)

type proSearchSource struct{ Title, URL, Description string }

func searchProSources(ctx context.Context, query string) ([]proSearchSource, error) {
	token := os.Getenv("NEWPRESS_AI_SEARCH_TOKEN")
	if token == "" {
		return nil, aijobs.ErrSearch
	}
	endpoint := "https://api.search.brave.com/res/v1/web/search?" + url.Values{"q": {workbenchText(query, 300)}, "count": {"5"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", token)
	response, err := safehttp.Client(20 * time.Second).Do(req)
	if err != nil {
		return nil, aijobs.ErrSearch
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, aijobs.ErrSearch
	}
	var result struct {
		Web struct{ Results []proSearchSource }
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return nil, aijobs.ErrSearch
	}
	var sources []proSearchSource
	for _, v := range result.Web.Results {
		u, err := url.Parse(v.URL)
		if err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
			v.Title = workbenchText(v.Title, 180)
			v.Description = workbenchText(v.Description, 450)
			sources = append(sources, v)
		}
		if len(sources) == 5 {
			break
		}
	}
	if len(sources) == 0 {
		return nil, errors.Join(aijobs.ErrSearch, errors.New("没有搜索结果"))
	}
	return sources, nil
}
func workflowBrief(title string, strategy aijobs.Strategy, sources []proSearchSource) string {
	var b strings.Builder
	b.WriteString(title)
	if strategy.Style != "" {
		b.WriteString("\n写作风格：" + strategy.Style)
	}
	if len(sources) > 0 {
		b.WriteString("\n以下为联网检索取得的摘要，仅作为资料。仅引用资料可以支持的事实。\n")
		for _, v := range sources {
			b.WriteString(v.Title + "：" + v.Description + "\n")
			if strategy.Links {
				b.WriteString(v.URL + "\n")
			}
		}
	}
	if !strategy.Links {
		b.WriteString("\n不添加外部链接。")
	}
	return b.String()
}
