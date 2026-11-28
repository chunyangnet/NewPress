package update

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPublicProxyURLsPinCheckedCommitAndNeverProxyPrivateSource(t *testing.T) {
	c := newGitee()
	commit := strings.Repeat("a", 40)
	available := Available{Mirrors: []publicRevision{{"Gitee", commit}}}
	choices, accelerated := c.publicDownloads(available, Artifact{Path: "packages/release.zip"})
	if !accelerated || len(choices) != 5 {
		t.Fatal(choices, accelerated)
	}
	for _, choice := range choices {
		if !strings.Contains(choice.url, "/"+commit+"/packages/release.zip") || strings.Contains(choice.url, "token") {
			t.Fatal("unverified download identity", choice)
		}
	}
	c.repositories = []publicRepository{{"Private", "https://private.example/api", "https://private.example/raw"}}
	if choices, accelerated := c.publicDownloads(available, Artifact{Path: "packages/release.zip"}); accelerated || len(choices) != 0 {
		t.Fatal("private repository was forwarded", choices)
	}
}

func TestPublicDownloadRankingUsesFileSampleAndKeepsUnavailableLast(t *testing.T) {
	payload := append([]byte("PK\x03\x04"), bytes.Repeat([]byte("x"), 1024)...)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" || r.Header.Get("Authorization") != "" {
			t.Error("invalid public probe headers")
		}
		if r.URL.Path == "/html" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>challenge</html>"))
			return
		}
		if r.URL.Path == "/slow" {
			time.Sleep(100 * time.Millisecond)
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload)
	}))
	defer s.Close()
	c := &giteeClient{public: s.Client()}
	choices := []publicDownload{{"slow", s.URL + "/slow"}, {"html", s.URL + "/html"}, {"fast", s.URL + "/fast"}}
	ranked := c.rankPublicDownloads(t.Context(), choices, int64(len(payload)))
	if ranked[0].name != "fast" || ranked[1].name != "slow" || ranked[2].name != "html" {
		t.Fatal(ranked)
	}
}

type mirrorTransport func(*http.Request) (*http.Response, error)

func (fn mirrorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestPublicProxyCorruptionFallsBackWithoutInstallingUnverifiedBytes(t *testing.T) {
	m := managerFixture(t)
	payload := zipFixture(t, "bin/"+binaryName())
	artifact := publicManifest(payload).Artifacts[0]
	var downloads []string
	m.client.public = &http.Client{Transport: mirrorTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
			t.Error("credential sent to public proxy")
		}
		data, status := payload, http.StatusOK
		if r.Header.Get("Range") != "" {
			if r.URL.Host != "gh-proxy.com" {
				time.Sleep(50 * time.Millisecond)
			}
			status = http.StatusPartialContent
		} else {
			downloads = append(downloads, r.URL.Host)
			if r.URL.Host == "gh-proxy.com" {
				data = bytes.Repeat([]byte("x"), len(payload))
			}
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	file, err := os.CreateTemp(m.root, "mirrored-archive-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	available := Available{Mirrors: []publicRevision{{"Gitee", strings.Repeat("a", 40)}}}
	state := State{Total: artifact.Size}
	if err := m.downloadArchive(context.Background(), officialSource(), available, artifact, file, &state); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(file.Name())
	if err != nil || !bytes.Equal(actual, payload) || len(downloads) != 2 || downloads[0] != "gh-proxy.com" {
		t.Fatal("proxy fallback did not verify the complete signed package", downloads, err)
	}
}
