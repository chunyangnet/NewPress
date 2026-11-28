package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"time"
)

func (m *Manager) downloadArchive(ctx context.Context, source Source, available Available, artifact Artifact, file *os.File, state *State) error {
	var attempts []func() (*http.Response, error)
	if source.Official {
		candidates, accelerated := m.client.publicDownloads(available, artifact)
		if accelerated {
			candidates = m.client.rankPublicDownloads(ctx, candidates, artifact.Size)
		}
		for _, candidate := range candidates {
			attempts = append(attempts, func() (*http.Response, error) {
				return m.client.publicGet(ctx, candidate.url)
			})
		}
	} else {
		attempts = append(attempts, func() (*http.Response, error) {
			return m.client.get(ctx, source, "/raw/"+escapedPath(artifact.Path), available.Commit)
		})
	}
	last := errors.New("没有已核验为同一发布的可用下载源")
	for _, attempt := range attempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := file.Truncate(0); err != nil {
			return errors.New("无法重置更新缓存")
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return errors.New("无法重置更新缓存")
		}
		state.Downloaded = 0
		r, err := attempt()
		if err != nil {
			last = err
			continue
		}
		h := sha256.New()
		progress := &downloadProgress{m: m, s: state, last: time.Now()}
		n, copyErr := io.Copy(io.MultiWriter(file, h, progress), io.LimitReader(r.Body, artifact.Size+1))
		r.Body.Close()
		switch {
		case copyErr != nil:
			last = errors.New("下载中断或写入失败，请重试")
		case n != artifact.Size:
			last = errors.New("更新包长度与清单不一致")
		case hex.EncodeToString(h.Sum(nil)) != artifact.SHA256:
			last = errors.New("SHA-256 校验失败，已阻止安装")
		default:
			return nil
		}
	}
	return last
}
