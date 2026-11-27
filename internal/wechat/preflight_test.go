package wechat

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/media"
)

func checkFixture(t *testing.T) (*Service, content.Post, string) {
	t.Helper()
	s, post := fixture(t)
	library, err := media.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.Media = library
	var picture bytes.Buffer
	if err = png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 40, 30))); err != nil {
		t.Fatal(err)
	}
	item, err := library.Upload(t.Context(), bytes.NewReader(picture.Bytes()), media.UploadInput{Filename: "check.png"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	post, err = s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: post.Title, Slug: post.Slug, CoverURL: item.PublicURL, ContentSource: `<p>检查正文</p><img src="` + item.PublicURL + `" alt="说明"><img src="` + item.PublicURL + `" alt="再次使用">`})
	if err != nil {
		t.Fatal(err)
	}
	s.Client.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		t.Error("local check made a remote request", r.URL.Path)
		return nil, errors.New("unexpected remote request")
	})}
	return s, post, item.PublicURL
}

func TestSyncCheckDoesNotWriteAndCleansTemporaryImages(t *testing.T) {
	for _, file := range []bool{false, true} {
		name := "memory"
		if file {
			name = "file"
		}
		t.Run(name, func(t *testing.T) {
			s, post, _ := checkFixture(t)
			before, err := s.Store.View(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			storePath := ""
			if file {
				storePath = filepath.Join(t.TempDir(), "wechat.json")
				store, err := NewFileStore(storePath)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.Update(t.Context(), func(v *State) error { *v = before; return nil }); err != nil {
					t.Fatal(err)
				}
				s.Store = store
			}
			spool := t.TempDir()
			t.Setenv("TMP", spool)
			t.Setenv("TEMP", spool)
			t.Setenv("TMPDIR", spool)
			check, err := s.CheckSync(t.Context(), post.ID, post.Version, before.Config.Version)
			if err != nil || !check.Ready || len(check.Issues) != 0 || check.Images != 2 || check.CheckedImages != 2 || check.UploadBytes <= 0 || check.UploadBytes >= 2<<20 || check.PostID != post.ID || check.Version != post.Version {
				t.Fatal(check, err)
			}
			after, err := s.Store.View(t.Context())
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("inspection changed stored state", err)
			}
			if file {
				reopened, err := NewFileStore(storePath)
				if err != nil {
					t.Fatal(err)
				}
				after, err = reopened.View(t.Context())
				if err != nil || !reflect.DeepEqual(after, before) {
					t.Fatal("inspection persisted state", err)
				}
			}
			entries, err := os.ReadDir(spool)
			if err != nil || len(entries) != 0 {
				t.Fatal("temporary JPEGs retained", entries, err)
			}
		})
	}
}

func TestSyncCheckCollectsLocalProblemsBeforeImageWork(t *testing.T) {
	s, post, source := checkFixture(t)
	state, _ := s.Store.View(t.Context())
	cfg := state.Config
	cfg.ThumbMediaID = ""
	if _, err := s.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	post, err := s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: strings.Repeat("题", 33), Slug: post.Slug, ContentSource: `<p>` + strings.Repeat("文", 20000) + `</p><img src="` + source + `">`})
	if err != nil {
		t.Fatal(err)
	}
	state, _ = s.Store.View(t.Context())
	check, err := s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version)
	if err != nil || check.Ready || check.TitleChars != 33 || check.CheckedImages != 0 || check.UploadBytes != 0 {
		t.Fatal(check, err)
	}
	fields := map[string]bool{}
	for _, issue := range check.Issues {
		fields[issue.Field] = true
	}
	if !fields["title"] || !fields["cover"] || !fields["content"] {
		t.Fatal("missing combined corrections", check.Issues)
	}
}

func TestSyncCheckChecksEveryImageAndCleansOnFailure(t *testing.T) {
	s, post, source := checkFixture(t)
	post, err := s.Content.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: post.Title, Slug: post.Slug, CoverURL: source, ContentSource: `<p>正文</p><img src="https://external.example/image.png"><img src="/media/999/missing.png">`})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := s.Store.View(t.Context())
	spool := t.TempDir()
	t.Setenv("TMP", spool)
	t.Setenv("TEMP", spool)
	t.Setenv("TMPDIR", spool)
	check, err := s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version)
	if err != nil || check.Ready || check.Images != 3 || check.CheckedImages != 3 || len(check.Issues) != 2 {
		t.Fatal(check, err)
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed check left images", entries, err)
	}
}

type changingCheckRepository struct {
	content.Repository
	beforeSecondRead func()
	reads            int
}

func (r *changingCheckRepository) GetByID(ctx context.Context, id int64) (content.Post, error) {
	r.reads++
	if r.reads == 2 {
		r.beforeSecondRead()
	}
	return r.Repository.GetByID(ctx, id)
}

func TestSyncCheckRevalidatesVersionsAndBoundsImagePreparation(t *testing.T) {
	s, post, _ := checkFixture(t)
	state, _ := s.Store.View(t.Context())
	for _, input := range [][2]int64{{post.Version + 1, state.Config.Version}, {post.Version, state.Config.Version - 1}} {
		if _, err := s.CheckSync(t.Context(), post.ID, input[0], input[1]); !errors.Is(err, ErrConflict) {
			t.Fatal("stale version accepted", err)
		}
	}
	s.localPreparation.Lock()
	_, err := s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version)
	s.localPreparation.Unlock()
	if !errors.Is(err, ErrCheckBusy) {
		t.Fatal("parallel image work accepted", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.CheckSync(ctx, post.ID, post.Version, state.Config.Version); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled work continued", err)
	}
	delegate := s.Content
	s.Content = &changingCheckRepository{Repository: delegate, beforeSecondRead: func() {
		_, err := delegate.Update(t.Context(), post.ID, content.Mutation{ExpectedVersion: post.Version}, content.PostInput{Type: post.Type, Status: post.Status, Title: "后续修改", Slug: post.Slug, ContentSource: "保留最新正文"})
		if err != nil {
			t.Fatal(err)
		}
	}}
	if _, err = s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("changed article approved", err)
	}
	s.Content = delegate
	state, _ = s.Store.View(t.Context())
	cfg := state.Config
	cfg.Enabled = false
	if _, err = s.SaveConfig(t.Context(), cfg, false); err != nil {
		t.Fatal(err)
	}
	state, _ = s.Store.View(t.Context())
	if _, err = s.CheckSync(t.Context(), post.ID, post.Version, state.Config.Version); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled module allowed check", err)
	}
}
