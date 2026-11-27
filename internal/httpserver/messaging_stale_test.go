package httpserver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"newpress.local/newpress/internal/messaging"
)

func TestSocialMessagingStaleFileReturnsUnavailableWithoutLosingMessages(t *testing.T) {
	s, handler, manager, _, cookie, owner := newPersistentAdminTestServer(t)
	peer, peerCookie, reader := createAndLoginTestUser(t, manager, owner, "stale-reader", "stale-reader@example.test", "读者", "subscriber")
	path := filepath.Join(t.TempDir(), "messages.json")
	store, err := messaging.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.messages = store
	base := "https://example.test/api/v1/social"
	messagePath := fmt.Sprintf("%s/messages/%d", base, peer.ID)
	input := `{"content":"original message","nonce":"http-stale-nonce-000001"}`
	headers := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": owner.CSRFToken}
	readerHeaders := map[string]string{"Content-Type": "application/json", "Origin": "https://example.test", "X-CSRF-Token": reader.CSRFToken}
	sent := serveContentAPI(handler, cookie, "POST", messagePath, input, headers)
	if sent.Code != 201 {
		t.Fatal(sent.Code, sent.Body.String())
	}
	var response struct {
		Message socialMessage `json:"message"`
	}
	decodeAPIResponse(t, sent, &response)
	writer, err := messaging.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	const newerBody = "newer-private-body"
	newer, err := writer.Send(t.Context(), messaging.Input{SenderID: peer.ID, RecipientID: owner.UserID, Content: newerBody, Nonce: "external-private-nonce-002"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"send", "replay", "read", "clear"} {
		t.Run(operation, func(t *testing.T) {
			requestPath, body, requestCookie, requestHeaders := messagePath, input, cookie, headers
			switch operation {
			case "send":
				body = `{"content":"third message","nonce":"http-stale-nonce-000003"}`
			case "read":
				requestPath = fmt.Sprintf("%s/messages/%d/read", base, owner.UserID)
				body = fmt.Sprintf(`{"through":%d}`, response.Message.ID)
				requestCookie, requestHeaders = peerCookie, readerHeaders
			case "clear":
				requestPath = base + "/notifications"
				body = fmt.Sprintf(`{"action":"clear","through":%d}`, response.Message.ID)
				requestCookie, requestHeaders = peerCookie, readerHeaders
			}
			result := serveContentAPI(handler, requestCookie, "POST", requestPath, body, requestHeaders)
			assertAPIError(t, result, 503, "messages.unavailable")
			for _, private := range []string{newerBody, newer.Nonce, path} {
				if strings.Contains(result.Body.String(), private) {
					t.Fatal("storage failure exposed private details", result.Body.String())
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("HTTP operation overwrote a newer conversation", err)
			}
		})
	}
	s.messages, err = messaging.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	result := serveContentAPI(handler, cookie, "POST", messagePath, `{"content":"third message","nonce":"http-stale-nonce-000003"}`, headers)
	if result.Code != 201 {
		t.Fatal("reloaded store did not recover", result.Code, result.Body.String())
	}
	page, err := s.messages.Messages(t.Context(), owner.UserID, peer.ID, messaging.MessageQuery{})
	if err != nil || len(page.Items) != 3 || page.Items[1].ID != newer.ID || page.Items[1].Content != newerBody {
		t.Fatal("recovery lost the newer message", page, err)
	}
}
