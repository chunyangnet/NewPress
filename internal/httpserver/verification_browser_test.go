package httpserver

import (
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"newpress.local/newpress/internal/content"
)

// Opt-in, isolated browser fixture: no live accounts, settings or credentials.
func TestVerificationBrowserFixture(t *testing.T) {
	if os.Getenv("NEWPRESS_VERIFICATION_BROWSER") != "1" {
		t.Skip("opt-in browser fixture")
	}
	s, _, _, _, _, _ := newPersistentAdminTestServer(t)
	s.config.PublicURL = "http://127.0.0.1:19439"
	s.repository = content.NewMemoryRepository(true)
	s.mailSender = &accessMailbox{}
	setRegistration(t, s, "open")
	listener, err := net.Listen("tcp", "127.0.0.1:19439")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	t.Log("Verification browser fixture: http://127.0.0.1:19439")
	select {
	case <-t.Context().Done():
	case <-time.After(25 * time.Minute):
	}
}
