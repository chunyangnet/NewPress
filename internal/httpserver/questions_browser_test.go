package httpserver

import (
	"net"
	"net/http"
	"newpress.local/newpress/internal/questions"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in, loopback-only fixture: all users and persisted data are temporary.
func TestQuestionsBrowserFixture(t *testing.T) {
	addr := os.Getenv("NEWPRESS_QUESTIONS_BROWSER_ADDR")
	if addr == "" {
		t.Skip("isolated browser fixture")
	}
	s, handler, users, _, ownerCookie, owner := newPersistentAdminTestServer(t)
	_, readerCookie, _ := createAndLoginTestUser(t, users, owner, "qa-reader", "qa-reader@example.test", "问答读者", "subscriber")
	store, err := questions.NewFileStore(filepath.Join(t.TempDir(), "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetQuestionsStore(store)
	s.config.PublicURL = "http://localhost:19364"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fixture/login/{role}", func(w http.ResponseWriter, r *http.Request) {
		cookie := *ownerCookie
		switch r.PathValue("role") {
		case "reader":
			cookie = *readerCookie
		case "guest":
			cookie.Value = ""
			cookie.MaxAge = -1
		}
		http.SetCookie(w, &cookie)
		http.Redirect(w, r, "/questions/", 303)
	})
	mux.Handle("/", handler)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Log("questions fixture ready", addr)
	if err = http.Serve(listener, mux); err != nil {
		t.Fatal(err)
	}
}
