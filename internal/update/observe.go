package update

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"
)

func observationPeriod() (time.Duration, error) {
	raw := os.Getenv("NEWPRESS_UPDATE_OBSERVE_FOR")
	if raw == "" {
		return 2 * time.Minute, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 2*time.Second || d > 10*time.Minute {
		return 0, errors.New("NEWPRESS_UPDATE_OBSERVE_FOR 必须在 2s 和 10m 之间")
	}
	return d, nil
}

// The candidate serves real traffic during probation. Keep the journal and the old
// active pointer until the last sample succeeds. A restart during this window rolls back.
func observe(ctx context.Context, child *childProcess, address string, window time.Duration) error {
	ready, err := readyURL(address)
	if err != nil {
		return err
	}
	base, err := url.Parse(ready)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	probe := func(path string) bool {
		endpoint := *base
		endpoint.Path = path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return false
		}
		res, err := client.Do(req)
		if err != nil {
			return false
		}
		res.Body.Close()
		if path == base.Path {
			return res.StatusCode == 200 && res.Header.Get("X-NewPress-Instance") == child.instance
		}
		return res.StatusCode >= 200 && res.StatusCode < 400
	}
	end := time.Now().Add(window)
	interval := min(window/4, 5*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.done:
			return errors.New("新进程在观察期内退出")
		case <-ticker.C:
			if !probe(base.Path) || !probe("/") || !probe("/admin/login/") {
				failures++
				if failures >= 3 {
					return errors.New("就绪、首页或登录页连续三次检查失败")
				}
				continue
			}
			failures = 0
			if !time.Now().Before(end) {
				select {
				case <-child.done:
					return errors.New("新进程在观察期内退出")
				default:
					return nil
				}
			}
		}
	}
}
