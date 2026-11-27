package wechat

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

func TestLocalPreparationRejectsBeforeAnyRemoteUpload(t *testing.T) {
	for _, problem := range []string{"long-body", "later-missing-image", "too-many-images"} {
		t.Run(problem, func(t *testing.T) {
			s, post := fixture(t)
			library, err := media.NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			s.Media = library
			var data bytes.Buffer
			if err = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
				t.Fatal(err)
			}
			item, err := library.Upload(t.Context(), bytes.NewReader(data.Bytes()), media.UploadInput{Filename: "cover.png"}, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			body := "<p>正文</p>"
			switch problem {
			case "long-body":
				body = "<p>" + strings.Repeat("文", 20000) + "</p>"
			case "later-missing-image":
				body += `<img src="` + item.PublicURL + `"><img src="/media/999999/missing.png">`
			case "too-many-images":
				for i := 0; i < 40; i++ {
					body += `<img src="` + item.PublicURL + `?variant=missing` + strings.Repeat("x", i) + `">`
				}
			}
			post, err = s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: post.Title, Slug: post.Slug, ContentSource: body, CoverURL: item.PublicURL})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				switch r.URL.Path {
				case "/cgi-bin/stable_token":
					return reply(r, `{"access_token":"token","expires_in":7200}`), nil
				case "/cgi-bin/material/add_material":
					return reply(r, `{"media_id":"cover-id"}`), nil
				case "/cgi-bin/media/uploadimg":
					return reply(r, `{"url":"https://mmbiz.qpic.cn/image"}`), nil
				default:
					return nil, errors.New("unexpected write")
				}
			})}
			if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
				t.Fatal(err)
			}
			if err = s.Tick(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			job := stateJob(t, s)
			if job.Status != "failed" || job.Error == "" || calls != 0 || len(job.Images) != 0 {
				t.Fatalf("local failure uploaded before validation: job=%+v remote_calls=%d", job, calls)
			}
		})
	}
}

func TestArticleChangesStopSubsequentRemoteWrites(t *testing.T) {
	for _, phase := range []string{"cover-upload", "body-upload", "draft"} {
		t.Run(phase, func(t *testing.T) {
			s, post, source := checkFixture(t)
			state, err := s.Store.View(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			cfg := state.Config
			cfg.AutoPublish = true
			if _, err = s.SaveConfig(t.Context(), cfg, false); err != nil {
				t.Fatal(err)
			}
			calls := map[string]int{}
			s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				calls[r.URL.Path]++
				changedAt := map[string]string{"cover-upload": "/cgi-bin/material/add_material", "body-upload": "/cgi-bin/media/uploadimg", "draft": "/cgi-bin/draft/add"}[phase]
				if r.URL.Path == changedAt {
					_, err := s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: "上传时修改的标题", Slug: post.Slug, CoverURL: source, ContentSource: "新正文"})
					if err != nil {
						t.Fatal(err)
					}
				}
				switch r.URL.Path {
				case "/cgi-bin/stable_token":
					return reply(r, `{"access_token":"token","expires_in":7200}`), nil
				case "/cgi-bin/material/add_material":
					return reply(r, `{"media_id":"cover-id"}`), nil
				case "/cgi-bin/media/uploadimg":
					return reply(r, `{"url":"https://mmbiz.qpic.cn/image"}`), nil
				case "/cgi-bin/draft/add":
					return reply(r, `{"media_id":"draft-id"}`), nil
				default:
					return nil, errors.New("unexpected remote write")
				}
			})}
			if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
				t.Fatal(err)
			}
			if err = s.Tick(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			job := stateJob(t, s)
			if job.Status != "failed" || !strings.Contains(job.Error, "已更新") || job.PublishID != "" || calls["/cgi-bin/freepublish/submit"] != 0 {
				t.Fatalf("changed content continued: job=%+v calls=%v", job, calls)
			}
			if phase == "draft" {
				if job.MediaID != "draft-id" || calls["/cgi-bin/draft/add"] != 1 {
					t.Fatalf("existing draft receipt lost: job=%+v calls=%v", job, calls)
				}
			} else if job.MediaID != "" || calls["/cgi-bin/draft/add"] != 0 {
				t.Fatalf("created a stale draft: job=%+v calls=%v", job, calls)
			}
			if phase == "cover-upload" && calls["/cgi-bin/media/uploadimg"] != 0 {
				t.Fatal("uploaded the next image after the article changed", calls)
			}
		})
	}
}

func TestUploadedImageURLsAreIncludedInFinalBodyLimit(t *testing.T) {
	s, post, source := checkFixture(t)
	post, err := s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: post.Title, Slug: post.Slug, CoverURL: source, ContentSource: `<p>` + strings.Repeat("文", 19950) + `</p><img src="` + source + `">`})
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Store.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	check, err := s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version)
	if err != nil || !check.Ready || check.ContentChars >= 20000 || len(check.Warnings) == 0 {
		t.Fatal("local minimum body check", check, err)
	}
	drafts := 0
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			return reply(r, `{"access_token":"token","expires_in":7200}`), nil
		case "/cgi-bin/material/add_material":
			return reply(r, `{"media_id":"cover-id"}`), nil
		case "/cgi-bin/media/uploadimg":
			return reply(r, `{"url":"https://mmbiz.qpic.cn/`+strings.Repeat("x", 80)+`"}`), nil
		case "/cgi-bin/draft/add":
			drafts++
		}
		return nil, errors.New("unexpected draft creation")
	})}
	if _, err = s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	job := stateJob(t, s)
	if job.Status != "failed" || !strings.Contains(job.Error, "正文") || len(job.Images) != 2 || job.MediaID != "" || drafts != 0 {
		t.Fatalf("remote image URLs were excluded from final body limit: job=%+v drafts=%d", job, drafts)
	}
}

func TestBusyLocalPreparationDefersQueuedSync(t *testing.T) {
	s, post, _ := checkFixture(t)
	calls := 0
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Path {
		case "/cgi-bin/stable_token":
			return reply(r, `{"access_token":"token","expires_in":7200}`), nil
		case "/cgi-bin/material/add_material":
			return reply(r, `{"media_id":"cover-id"}`), nil
		case "/cgi-bin/media/uploadimg":
			return reply(r, `{"url":"https://mmbiz.qpic.cn/image"}`), nil
		case "/cgi-bin/draft/add":
			return reply(r, `{"media_id":"draft-id"}`), nil
		}
		return nil, errors.New("unexpected publication")
	})}
	if _, err := s.Queue(t.Context(), post.ID, post.Version, "owner"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.localPreparation.Lock()
	err := s.Tick(t.Context(), now)
	s.localPreparation.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	job := stateJob(t, s)
	if job.Status != "queued" || job.Stage != "prepare" || !job.NextAt.After(now) || job.MediaID != "" || calls != 0 {
		t.Fatalf("local contention failed or started sending: job=%+v calls=%d", job, calls)
	}
	if err = s.Tick(t.Context(), job.NextAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	job = stateJob(t, s)
	if job.Status != "draft" || job.MediaID != "draft-id" || job.Attempts != 2 || calls != 4 || job.Error != "" {
		t.Fatalf("deferred synchronization did not resume: job=%+v calls=%d", job, calls)
	}
}
