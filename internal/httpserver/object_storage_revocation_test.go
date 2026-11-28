package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/objectstorage"
	"newpress.local/newpress/internal/plugin"
)

// Use a real local WebDAV server so the authorization checks must survive
// request decoding, backend requests and transfer verification.
type storageAuthorityDAV struct {
	mu      sync.Mutex
	objects map[string]string
	puts    int
	deletes int
	onRead  func(string, string, string)
}

func (d *storageAuthorityDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/dav/")
	if d.onRead != nil && (r.Method == "PROPFIND" || r.Method == http.MethodGet) {
		d.onRead(r.Method, key, r.Header.Get("Depth"))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch r.Method {
	case "MKCOL":
		w.WriteHeader(http.StatusCreated)
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body failed", http.StatusBadRequest)
			return
		}
		d.puts++
		d.objects[key] = string(data)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		d.deletes++
		delete(d.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		data, ok := d.objects[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = io.WriteString(w, data)
	case "PROPFIND":
		if r.Header.Get("Depth") == "1" {
			key = "assets/source.txt"
		}
		data, ok := d.objects[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, key, len(data))
	default:
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}
}

func configureStorageAuthorityDAV(t *testing.T, s *Server, dav *storageAuthorityDAV) int64 {
	t.Helper()
	remote := httptest.NewServer(dav)
	t.Cleanup(remote.Close)
	if err := s.plugins.InstallBundled(plugin.ObjectStorage); err != nil {
		t.Fatal(err)
	}
	s.SetObjectStorage(objectstorage.New(s.plugins, s.media))
	config := objectstorage.Defaults()
	config.ActiveProfile = "test"
	config.Profiles = []objectstorage.Profile{{ID: "test", Name: "Local fixture", Provider: "webdav", Endpoint: remote.URL + "/dav", Prefix: "assets", Username: "fixture", Password: "fixture-password", AllowHTTP: true, AllowPrivate: true, TimeoutSeconds: 10}}
	saved, err := s.objectStorage.Save(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	return saved.Version
}

func TestObjectStorageFilesRecheckAuthority(t *testing.T) {
	for _, operation := range []string{"upload", "action-body", "copy-source", "move-verification", "delete-head", "list", "download"} {
		for _, change := range []string{"logout", "logout-with-fallback", "permission", "suspension", "allowed"} {
			t.Run(operation+"/"+change, func(t *testing.T) {
				s, handler, manager, _, ownerCookie, owner := newPersistentAdminTestServer(t)
				user, cookie, actor := createAndLoginTestUser(t, manager, owner, "storage-actor", "storage-actor@example.test", "Storage actor", "administrator")
				var changed bool
				var once sync.Once
				changeAuthority := func() {
					once.Do(func() {
						changed = true
						switch change {
						case "logout", "logout-with-fallback":
							if err := manager.RevokeSessionContext(t.Context(), cookie.Value); err != nil {
								t.Error(err)
							}
						case "permission", "suspension":
							status, role := user.Status, "editor"
							if change == "suspension" {
								status, role = auth.UserStatusSuspended, "administrator"
							}
							if _, err := manager.UpdateUser(t.Context(), owner, user.ID, user.Version, auth.UserInput{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Locale: user.Locale, Status: status, RoleSlugs: []string{role}}); err != nil {
								t.Error(err)
							}
						}
					})
				}
				const secret = "private cloud bytes"
				dav := &storageAuthorityDAV{objects: map[string]string{"assets/source.txt": secret}}
				dav.onRead = func(method, key, depth string) {
					if operation == "copy-source" && method == http.MethodGet && key == "assets/source.txt" ||
						operation == "move-verification" && method == http.MethodGet && key == "assets/target.txt" ||
						operation == "delete-head" && method == "PROPFIND" && key == "assets/source.txt" ||
						operation == "list" && method == "PROPFIND" && depth == "1" ||
						operation == "download" && method == http.MethodGet {
						changeAuthority()
					}
				}
				version := configureStorageAuthorityDAV(t, s, dav)
				const base = "https://example.test/api/v1/admin/plugins/object-storage/files"
				query := "?profile=test&key=assets/source.txt&version=" + strconv.FormatInt(version, 10)
				var request *http.Request
				switch operation {
				case "upload":
					request = httptest.NewRequest(http.MethodPost, base+"/upload"+strings.Replace(query, "source.txt", "target.txt", 1)+"&expected_user_id="+strconv.FormatInt(actor.UserID, 10), strings.NewReader(secret))
					request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: changeAuthority}
				case "list":
					request = httptest.NewRequest(http.MethodGet, base+strings.Replace(query, "assets/source.txt", "assets", 1), nil)
				case "download":
					request = httptest.NewRequest(http.MethodGet, base+query+"&download=1", nil)
				default:
					action := "delete"
					if operation == "copy-source" {
						action = "copy"
					} else if operation == "move-verification" {
						action = "move"
					}
					body, _ := json.Marshal(map[string]any{"action": action, "profile": "test", "key": "assets/source.txt", "target_profile": "test", "target": "assets/target.txt", "expected_version": version, "expected_user_id": actor.UserID})
					request = httptest.NewRequest(http.MethodPost, base+"/action", strings.NewReader(string(body)))
					if operation == "action-body" {
						request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: changeAuthority}
					}
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Origin", "https://example.test")
				request.Header.Set("X-CSRF-Token", actor.CSRFToken)
				request.AddCookie(cookie)
				if change == "logout-with-fallback" {
					request.AddCookie(ownerCookie)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				dav.mu.Lock()
				defer dav.mu.Unlock()
				if !changed {
					t.Fatal("test did not reach revocation boundary")
				}
				if change == "allowed" {
					if response.Code != http.StatusOK {
						t.Fatalf("authorized request failed: %d %s", response.Code, response.Body.String())
					}
					if operation == "download" && response.Body.String() != secret || operation == "list" && !strings.Contains(response.Body.String(), "assets/source.txt") {
						t.Fatal("authorized read lost its result")
					}
					if operation == "upload" || operation == "copy-source" || operation == "move-verification" {
						if dav.puts != 1 || dav.objects["assets/target.txt"] != secret {
							t.Fatal("authorized transfer did not store complete bytes")
						}
					}
					if (operation == "action-body" || operation == "delete-head" || operation == "move-verification") && dav.deletes != 1 {
						t.Fatal("authorized deletion did not finish")
					}
					return
				}
				want := http.StatusForbidden
				if change == "logout" || change == "logout-with-fallback" || change == "suspension" {
					want = http.StatusUnauthorized
				}
				if response.Code != want {
					t.Errorf("revoked request status=%d, want=%d: %s", response.Code, want, response.Body.String())
				}
				if dav.deletes != 0 || dav.objects["assets/source.txt"] != secret {
					t.Error("revoked request deleted its source")
				}
				wantPuts := 0
				if operation == "move-verification" {
					wantPuts = 1 // The upload already committed; preserve it and the source.
				}
				if dav.puts != wantPuts {
					t.Errorf("revoked request wrote remote objects: puts=%d want=%d", dav.puts, wantPuts)
				}
				if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "assets/source.txt") {
					t.Error("revoked request disclosed private cloud data")
				}
			})
		}
	}
}

func TestObjectStorageUploadSessionOutageFailsClosed(t *testing.T) {
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	sessions := &observedSessionStore{SessionStore: auth.NewMemorySessionStore()}
	const password = "Storage outage password 2026"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.New(auth.Config{Username: "owner", PasswordHash: hash, Sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	token, actor, err := manager.Authenticate("storage-outage", "owner", password)
	if err != nil {
		t.Fatal(err)
	}
	s.auth = manager
	dav := &storageAuthorityDAV{objects: map[string]string{}}
	version := configureStorageAuthorityDAV(t, s, dav)
	request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v1/admin/plugins/object-storage/files/upload?profile=test&key=assets/new.txt&expected_user_id=0&version="+strconv.FormatInt(version, 10), strings.NewReader("private upload"))
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	request.Header.Set("Origin", "https://example.test")
	request.Header.Set("X-CSRF-Token", actor.CSRFToken)
	const privateError = "private session store details"
	request.Body = &mediaAuthorityBody{ReadCloser: request.Body, onRead: func() { sessions.getErr = errors.New(privateError) }}
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, request)
	dav.mu.Lock()
	defer dav.mu.Unlock()
	if response.Code != http.StatusServiceUnavailable || dav.puts != 0 || !strings.Contains(response.Body.String(), "auth.session_unavailable") {
		t.Fatalf("session outage allowed upload: status=%d puts=%d body=%s", response.Code, dav.puts, response.Body.String())
	}
	if strings.Contains(response.Body.String(), privateError) || response.Header().Get("Set-Cookie") != "" || response.Header().Get("WWW-Authenticate") != "" || response.Header().Get("Retry-After") != "5" {
		t.Fatal("outage must remain private and preserve the browser session")
	}
}

type storageAfterWrite struct {
	http.ResponseWriter
	once  sync.Once
	after func()
}

func (w *storageAfterWrite) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.once.Do(w.after)
	}
	return n, err
}

func TestObjectStorageDownloadRevocationAbortsHTTPStream(t *testing.T) {
	s, handler, manager, _, cookie, _ := newPersistentAdminTestServer(t)
	const tail = "private-tail-must-not-arrive"
	data := strings.Repeat("x", 2<<20) + tail
	dav := &storageAuthorityDAV{objects: map[string]string{"assets/source.txt": data}}
	version := configureStorageAuthorityDAV(t, s, dav)
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
	request, err := http.NewRequest(http.MethodGet, local.URL+"/api/v1/admin/plugins/object-storage/files?profile=test&key=assets/source.txt&download=1&version="+strconv.FormatInt(version, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	response, err := local.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got, readErr := io.ReadAll(response.Body)
	if !revoked.Load() || response.StatusCode != http.StatusOK || !errors.Is(readErr, io.ErrUnexpectedEOF) || len(got) == 0 || len(got) >= 1<<20 {
		t.Fatalf("revoked stream was not interrupted: status=%d bytes=%d error=%v", response.StatusCode, len(got), readErr)
	}
	if strings.Contains(string(got), tail) || strings.Contains(string(got), "internal_server_error") {
		t.Fatal("interrupted file disclosed its tail or appended a JSON error")
	}
}
