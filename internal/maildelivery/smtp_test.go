package maildelivery

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestSMTPConfigurationBoundary(t *testing.T) {
	for _, c := range []Config{{Address: "mail.test:25", From: "sender@example.test", Mode: "loopback"}, {Address: "127.0.0.1:25", From: "sender@example.test", Mode: "loopback", Username: "user", Password: "secret"}, {Address: "mail.test:587", From: "sender@example.test\r\nBcc: attacker@example.test"}, {Address: "mail.test:0", From: "sender@example.test"}, {Address: "mail.test:587", From: "sender@example.test", Username: "user"}} {
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted bad config %#v", c)
		}
	}
}

func TestSMTPDeliveryAndTLSRequirement(t *testing.T) {
	for _, mode := range []string{"loopback", "starttls"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			messages := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 local test\r\n")
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250-local\r\n250 SIZE 20000\r\n")
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						fmt.Fprint(conn, "250 OK\r\n")
					case line == "DATA\r\n":
						fmt.Fprint(conn, "354 send\r\n")
						var body strings.Builder
						for {
							part, err := reader.ReadString('\n')
							if err != nil {
								return
							}
							if part == ".\r\n" {
								break
							}
							body.WriteString(part)
						}
						messages <- body.String()
						fmt.Fprint(conn, "250 accepted\r\n")
					case line == "QUIT\r\n":
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "500 unknown\r\n")
					}
				}
			}()
			s, err := NewSMTP(Config{Address: listener.Addr().String(), From: "sender@example.test", Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			err = s.Send(context.Background(), "reader@example.test", "确认你的邮箱", "这是确认链接 https://example.test/account/access/#token=fixture")
			if mode == "starttls" {
				if err == nil {
					t.Fatal("downgraded to plaintext")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case payload := <-messages:
				message, err := mail.ReadMessage(strings.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
				if err != nil || !strings.Contains(string(decoded), "确认链接") {
					t.Fatal("unicode body corrupted", err)
				}
			case <-time.After(time.Second):
				t.Fatal("no delivered mail")
			}
		})
	}
}
