package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/community"
	"newpress.local/newpress/internal/objectstorage"
	"newpress.local/newpress/internal/plugin"
)

const attachmentAuthorityBase = "https://example.test/api/v1/community/circles/authority"

func attachmentAuthorityCircle(t *testing.T, s *Server, owner auth.Principal) *community.FileStore {
	t.Helper()
	store, err := community.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	s.config.CommunityEnabled = true
	s.SetCommunityStore(store)
	attachmentAuthorityApply(t, store, community.Actor{ID: owner.UserID, Admin: true}, community.Command{
		Action: "create", Name: "Attachment authority", OwnerID: owner.UserID, Visibility: "members", JoinPolicy: "open",
	})
	return store
}

func attachmentAuthorityApply(t *testing.T, store community.Store, actor community.Actor, cmd community.Command) community.Result {
	t.Helper()
	if c, err := store.Get(t.Context(), "authority"); err == nil {
		cmd.Version = c.Version
	} else if !errors.Is(err, community.ErrNotFound) {
		t.Error(err)
		return community.Result{}
	}
	cmd.Key = fmt.Sprintf("attachment-authority-%s-%d", cmd.Action, cmd.Version)
	result, err := store.Apply(t.Context(), "authority", actor, cmd, time.Now().UTC())
	if err != nil {
		t.Errorf("%s: %v", cmd.Action, err)
	}
	return result
}

func attachmentAuthorityUpload(t *testing.T, cookie *http.Cookie, actor auth.Principal, version int64, key string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("version", fmt.Sprint(version)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("key", key); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("file", "private.txt")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise ParseMultipartForm's disk-spooling path too.
	if _, err = part.Write(bytes.Repeat([]byte("private attachment\n"), 40000)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, attachmentAuthorityBase+"/attachments", &body)
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", w.FormDataContentType())
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("X-CSRF-Token", actor.CSRFToken)
	return r
}

func TestCircleAttachmentUploadRechecksOriginalAuthority(t *testing.T) {
	for _, change := range []string{"logout", "logout-with-fallback", "suspension", "permission", "member-removed", "archive", "allowed", "downgraded-member"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
			role := "administrator"
			if change == "member-removed" {
				role = "subscriber"
			}
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "attachment-actor", "attachment@example.test", "Attachment actor", role)
			store := attachmentAuthorityCircle(t, s, owner)
			admin := community.Actor{ID: owner.UserID, Admin: true}
			if change == "member-removed" || change == "downgraded-member" {
				attachmentAuthorityApply(t, store, community.Actor{ID: actor.UserID}, community.Command{Action: "join"})
			}
			root := filepath.Join(t.TempDir(), "attachments")
			files, err := community.NewFiles(root)
			if err != nil {
				t.Fatal(err)
			}
			s.SetCommunityFiles(files)
			before, err := store.Get(t.Context(), "authority")
			if err != nil {
				t.Fatal(err)
			}
			request := attachmentAuthorityUpload(t, cookie, actor, before.Version, "authority-upload-body-key")
			if change == "logout-with-fallback" {
				request.AddCookie(ownerCookie)
			}
			changed := false
			request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: func() {
				changed = true
				switch change {
				case "logout", "logout-with-fallback":
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Fatal(err)
					}
				case "suspension", "permission", "downgraded-member":
					status, role := user.Status, "subscriber"
					if change == "suspension" {
						status, role = auth.UserStatusSuspended, "administrator"
					}
					if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{role}}); err != nil {
						t.Fatal(err)
					}
				case "member-removed":
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "remove_member", UserID: actor.UserID})
				case "archive":
					yes := true
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "archive_circle", Enabled: &yes})
				}
			}}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			after, err := store.Get(t.Context(), "authority")
			if err != nil || !changed {
				t.Fatalf("boundary not reached: changed=%v error=%v", changed, err)
			}
			if change == "allowed" || change == "downgraded-member" {
				if response.Code != http.StatusOK || len(after.Attachments) != 1 {
					t.Fatalf("authorized upload failed: %d %s", response.Code, response.Body.String())
				}
				return
			}
			want := http.StatusForbidden
			if change == "logout" || change == "logout-with-fallback" || change == "suspension" {
				want = http.StatusUnauthorized
			}
			// A concurrent circle mutation may instead reject the old version.
			validStatus := response.Code == want || (change == "member-removed" || change == "archive") && response.Code == http.StatusConflict
			if !validStatus || len(after.Attachments) != 0 {
				t.Fatalf("revoked upload committed: status=%d attachments=%d body=%s", response.Code, len(after.Attachments), response.Body.String())
			}
			entries, err := os.ReadDir(root)
			if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
				t.Fatalf("rejected upload left objects: entries=%d error=%v", len(entries), err)
			}
		})
	}
}

// Arm the callback only after synchronization and verification have finished.
// The real HTTP GET then changes authority while ReadCircle is in flight.
func attachmentAuthorityRemote(t *testing.T, s *Server, change func(), corrupt bool) *atomic.Bool {
	t.Helper()
	var armed atomic.Bool
	changed := &atomic.Bool{}
	dav := &storageAuthorityDAV{objects: map[string]string{}}
	dav.onRead = func(method, key, depth string) {
		if method == http.MethodGet && armed.Load() && changed.CompareAndSwap(false, true) {
			change()
			if corrupt {
				dav.mu.Lock()
				dav.objects[key] = "corrupt remote object"
				dav.mu.Unlock()
			}
		}
	}
	configureStorageAuthorityDAV(t, s, dav)
	s.objectStorage.SetCommunity(s.circles, s.circleFiles)
	status, err := s.objectStorage.Status()
	if err != nil {
		t.Fatal(err)
	}
	c := status.Settings.Settings
	c.UploadRoutes = map[string]objectstorage.UploadRoute{"circle-attachment": {Target: "test", Mode: "redirect"}}
	if _, err = s.objectStorage.Save(c, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.plugins.SetEnabled(plugin.ObjectStorage, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.objectStorage.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err = s.objectStorage.Status()
		if err != nil {
			t.Fatal(err)
		}
		if status.Counts["synced"] == 1 && !status.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attachment synchronization timed out: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	armed.Store(true)
	return changed
}

func TestCircleAttachmentDownloadRechecksAuthorityAfterRemoteRead(t *testing.T) {
	for _, change := range []string{"logout", "logout-with-fallback", "suspension", "permission", "member-removed", "hidden-topic", "hidden-reply", "deleted-topic", "public-to-private", "fallback-logout", "allowed", "public-anonymous", "fallback-allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
			role := "subscriber"
			if change == "permission" {
				role = "administrator"
			}
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "attachment-reader", "reader@example.test", "Attachment reader", role)
			store := attachmentAuthorityCircle(t, s, owner)
			admin := community.Actor{ID: owner.UserID, Admin: true}
			public := change == "public-anonymous" || change == "public-to-private"
			if public {
				attachmentAuthorityApply(t, store, admin, community.Command{Action: "settings", Name: "Public attachments", JoinPolicy: "open", Visibility: "public"})
			} else if change != "permission" {
				attachmentAuthorityApply(t, store, community.Actor{ID: actor.UserID}, community.Command{Action: "join"})
			}
			c, _ := store.Get(t.Context(), "authority")
			const secret = "PRIVATE ATTACHMENT MUST NOT LEAK"
			file, _, err := s.circleFiles.Upload(t.Context(), store, "authority", admin, "remote-attachment-key", c.Version, "private.txt", strings.NewReader(secret), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			topicCmd := community.Command{Action: "topic", Kind: "discussion", Title: "Attachment topic", Body: "Private body", Attachments: []string{file.ID}}
			if change == "hidden-reply" {
				topicCmd.Attachments = nil
			}
			topic := attachmentAuthorityApply(t, store, admin, topicCmd)
			if topic.TopicID == 0 {
				t.Fatal("attachment topic fixture was not created")
			}
			var reply community.Result
			if change == "hidden-reply" {
				reply = attachmentAuthorityApply(t, store, admin, community.Command{Action: "reply", TopicID: topic.TopicID, Body: "Private reply", Attachments: []string{file.ID}})
			}
			changed := attachmentAuthorityRemote(t, s, func() {
				yes := true
				switch change {
				case "logout", "logout-with-fallback", "fallback-logout":
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error(err)
					}
				case "suspension", "permission":
					status := user.Status
					if change == "suspension" {
						status = auth.UserStatusSuspended
					}
					if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{"subscriber"}}); err != nil {
						t.Error(err)
					}
				case "member-removed":
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "remove_member", UserID: actor.UserID})
				case "hidden-topic":
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "hide_topic", TopicID: topic.TopicID, Enabled: &yes})
				case "hidden-reply":
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "hide_reply", TopicID: topic.TopicID, ReplyID: reply.ReplyID, Enabled: &yes})
				case "deleted-topic":
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "archive_topic", TopicID: topic.TopicID, Enabled: &yes})
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "delete_topic", TopicID: topic.TopicID})
				case "public-to-private":
					attachmentAuthorityApply(t, store, admin, community.Command{Action: "settings", Name: "Private attachments", JoinPolicy: "open", Visibility: "members"})
				}
			}, strings.HasPrefix(change, "fallback-"))
			request := httptest.NewRequest(http.MethodGet, attachmentAuthorityBase+"/attachments/"+file.ID, nil)
			if !public {
				request.AddCookie(cookie)
			}
			if change == "logout-with-fallback" {
				request.AddCookie(ownerCookie)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if !changed.Load() {
				t.Fatal("test did not reach the remote read boundary")
			}
			if change == "allowed" || change == "public-anonymous" || change == "fallback-allowed" {
				if response.Code != http.StatusOK || response.Body.String() != secret || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("authorized download failed: %d %s", response.Code, response.Body.String())
				}
				return
			}
			if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), secret) || response.Header().Get("Content-Disposition") != "" {
				t.Fatalf("revoked attachment was disclosed: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

type circleAttachmentSnapshotStore struct {
	community.Store
	gets     int
	afterGet func(int)
}

func (s *circleAttachmentSnapshotStore) Get(ctx context.Context, slug string) (community.Circle, error) {
	c, err := s.Store.Get(ctx, slug)
	s.gets++
	if s.afterGet != nil {
		s.afterGet(s.gets)
	}
	return c, err
}

func TestCircleAttachmentUploadRechecksBeforeCommit(t *testing.T) {
	for _, change := range []string{"logout", "permission", "allowed"} {
		t.Run(change, func(t *testing.T) {
			s, handler, manager, _, _, owner := newPersistentAdminTestServer(t)
			user, cookie, actor := createAndLoginTestUser(t, manager, owner, "attachment-commit", "commit@example.test", "Attachment commit", "administrator")
			store := attachmentAuthorityCircle(t, s, owner)
			root := filepath.Join(t.TempDir(), "attachments")
			files, err := community.NewFiles(root)
			if err != nil {
				t.Fatal(err)
			}
			s.SetCommunityFiles(files)
			before, _ := store.Get(t.Context(), "authority")
			changed := false
			s.SetCommunityStore(&circleAttachmentSnapshotStore{Store: store, afterGet: func(gets int) {
				// The first read authorizes entry. The second follows the body
				// recheck; the mutation must revalidate again after staging.
				if gets != 2 {
					return
				}
				changed = true
				switch change {
				case "logout":
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Fatal(err)
					}
				case "permission":
					if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: user.Status, RoleSlugs: []string{"subscriber"}}); err != nil {
						t.Fatal(err)
					}
				}
			}})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, attachmentAuthorityUpload(t, cookie, actor, before.Version, "attachment-commit-key"))
			after, _ := store.Get(t.Context(), "authority")
			if !changed {
				t.Fatal("commit boundary not reached")
			}
			if change == "allowed" {
				if response.Code != http.StatusOK || len(after.Attachments) != 1 {
					t.Fatalf("authorized commit failed: %d %s", response.Code, response.Body.String())
				}
				return
			}
			want := http.StatusForbidden
			if change == "logout" {
				want = http.StatusUnauthorized
			}
			entries, readErr := os.ReadDir(root)
			if response.Code != want || len(after.Attachments) != 0 || len(entries) != 0 || readErr != nil {
				t.Fatalf("commit authorization failed: status=%d attachments=%d objects=%d error=%v", response.Code, len(after.Attachments), len(entries), readErr)
			}
		})
	}
}

type circleAttachmentFailingSessions struct {
	auth.SessionStore
	unavailable atomic.Bool
}

func (s *circleAttachmentFailingSessions) Get(ctx context.Context, id string) (auth.SessionRecord, error) {
	if s.unavailable.Load() {
		return auth.SessionRecord{}, errors.New("private attachment session storage details")
	}
	return s.SessionStore.Get(ctx, id)
}

func TestCircleAttachmentSessionOutageFailsClosed(t *testing.T) {
	for _, boundary := range []string{"upload-body", "upload-commit", "download"} {
		t.Run(boundary, func(t *testing.T) {
			s, _, _, _, _, _ := newPersistentAdminTestServer(t)
			users, err := auth.NewFileUserStore("")
			if err != nil {
				t.Fatal(err)
			}
			sessions := &circleAttachmentFailingSessions{SessionStore: auth.NewMemorySessionStore()}
			const password = "Attachment outage owner password 2026"
			hash, err := auth.HashPassword(password)
			if err != nil {
				t.Fatal(err)
			}
			manager, err := auth.NewWithContext(t.Context(), auth.Config{Username: "owner", PasswordHash: hash, Users: users, Sessions: sessions, BootstrapEmail: "owner@example.test"})
			if err != nil {
				t.Fatal(err)
			}
			token, owner, err := manager.AuthenticateContext(t.Context(), "attachment-outage", "owner", password)
			if err != nil {
				t.Fatal(err)
			}
			s.auth = manager
			cookie := &http.Cookie{Name: auth.CookieName, Value: token}
			store := attachmentAuthorityCircle(t, s, owner)
			before, _ := store.Get(t.Context(), "authority")
			var request *http.Request
			var remoteChanged *atomic.Bool
			if boundary == "download" {
				file, _, err := s.circleFiles.Upload(t.Context(), store, "authority", community.Actor{ID: owner.UserID, Admin: true}, "attachment-outage-key", before.Version, "private.txt", strings.NewReader("private attachment bytes"), time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				remoteChanged = attachmentAuthorityRemote(t, s, func() { sessions.unavailable.Store(true) }, false)
				request = httptest.NewRequest(http.MethodGet, attachmentAuthorityBase+"/attachments/"+file.ID, nil)
				request.AddCookie(cookie)
			} else {
				request = attachmentAuthorityUpload(t, cookie, owner, before.Version, "attachment-outage-key")
				if boundary == "upload-body" {
					request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: func() { sessions.unavailable.Store(true) }}
				} else {
					s.SetCommunityStore(&circleAttachmentSnapshotStore{Store: store, afterGet: func(gets int) {
						if gets == 2 {
							sessions.unavailable.Store(true)
						}
					}})
				}
			}
			response := httptest.NewRecorder()
			s.routes().ServeHTTP(response, request)
			if !sessions.unavailable.Load() || remoteChanged != nil && !remoteChanged.Load() {
				t.Fatal("outage boundary not reached")
			}
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "auth.session_unavailable") || response.Header().Get("Retry-After") != "5" || response.Header().Get("Set-Cookie") != "" {
				t.Fatalf("session outage did not preserve session and fail closed: %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private attachment") || response.Header().Get("Content-Disposition") != "" {
				t.Fatal("outage exposed private attachment or session store details")
			}
			if boundary != "download" {
				after, _ := store.Get(t.Context(), "authority")
				if after.Version != before.Version || len(after.Attachments) != 0 {
					t.Fatal("outage committed an upload")
				}
			}
		})
	}
}

func TestCircleAttachmentDownloadRevocationAbortsStream(t *testing.T) {
	for _, rangeHeader := range []string{"", "bytes=0-", "bytes=0-1572863,2097152-"} {
		t.Run(rangeHeader, func(t *testing.T) {
			s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
			store := attachmentAuthorityCircle(t, s, owner)
			before, _ := store.Get(t.Context(), "authority")
			const tail = "private-circle-tail-must-not-arrive"
			data := strings.Repeat("x", 2<<20) + tail
			file, _, err := s.circleFiles.Upload(t.Context(), store, "authority", community.Actor{ID: owner.UserID, Admin: true}, "attachment-stream-key", before.Version, "private.txt", strings.NewReader(data), time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			var revoked atomic.Bool
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(&storageAfterWrite{ResponseWriter: w, after: func() {
					if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
						t.Error(err)
					}
					revoked.Store(true)
				}}, r)
			}))
			defer local.Close()
			request, err := http.NewRequest(http.MethodGet, local.URL+"/api/v1/community/circles/authority/attachments/"+file.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.AddCookie(cookie)
			request.Header.Set("Range", rangeHeader)
			response, err := local.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			got, readErr := io.ReadAll(response.Body)
			want := http.StatusOK
			if rangeHeader != "" {
				want = http.StatusPartialContent
			}
			if !revoked.Load() || response.StatusCode != want || !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) > 1<<20 {
				t.Fatalf("revoked stream was not interrupted: status=%d bytes=%d error=%v", response.StatusCode, len(got), readErr)
			}
			if strings.Contains(string(got), tail) || strings.Contains(string(got), "internal_server_error") {
				t.Fatal("interrupted attachment disclosed its tail or appended a JSON error")
			}
		})
	}
}

func TestCircleAttachmentDownloadHTTPCompatibility(t *testing.T) {
	s, handler, _, _, cookie, owner := newPersistentAdminTestServer(t)
	store := attachmentAuthorityCircle(t, s, owner)
	before, _ := store.Get(t.Context(), "authority")
	const data = "complete attachment bytes"
	file, _, err := s.circleFiles.Upload(t.Context(), store, "authority", community.Actor{ID: owner.UserID, Admin: true}, "attachment-http-key", before.Version, "private.txt", strings.NewReader(data), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, rangeHeader, modified string
		status                              int
		body                                string
	}{
		{name: "full", method: "GET", status: 200, body: data},
		{name: "head", method: "HEAD", status: 200},
		{name: "range", method: "GET", rangeHeader: "bytes=0-7", status: 206, body: "complete"},
		{name: "multipart", method: "GET", rangeHeader: "bytes=0-7,20-24", status: 206},
		{name: "not-modified", method: "GET", modified: file.CreatedAt.Format(http.TimeFormat), status: 304},
		{name: "invalid-range", method: "GET", rangeHeader: "bytes=1000-2000", status: 416},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, attachmentAuthorityBase+"/attachments/"+file.ID, nil)
			r.AddCookie(cookie)
			r.Header.Set("Range", tc.rangeHeader)
			r.Header.Set("If-Modified-Since", tc.modified)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, r)
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("HTTP compatibility: %d %s", response.Code, response.Body.String())
			}
			if tc.name == "multipart" {
				if !strings.Contains(response.Body.String(), "complete") || !strings.Contains(response.Body.String(), "bytes") || !strings.HasPrefix(response.Header().Get("Content-Type"), "multipart/byteranges") {
					t.Fatal("multipart range content missing")
				}
			} else if tc.status != 416 && response.Body.String() != tc.body {
				t.Fatalf("unexpected body: %q", response.Body.String())
			}
		})
	}
}
