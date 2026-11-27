package maildelivery

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPAcceptanceBoundaryAndStableIdentity(t *testing.T) {
	for _, mode := range []string{"lost-ack", "rejected", "accepted-quit-lost"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			received := make(chan string, 1)
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
						fmt.Fprint(conn, "250 local\r\n")
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
						received <- body.String()
						if mode == "rejected" {
							fmt.Fprint(conn, "451 temporarily unavailable\r\n")
						} else if mode == "accepted-quit-lost" {
							fmt.Fprint(conn, "250 accepted\r\n")
						}
						return
					default:
						return
					}
				}
			}()
			sender, err := NewSMTP(Config{Address: listener.Addr().String(), From: "sender@example.test", Mode: "loopback"})
			if err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 64)
			err = sender.SendMessage(t.Context(), Message{To: "reader@example.test", Subject: "投递状态测试", Body: "只在本机接收", ID: id})
			if mode == "lost-ack" && !errors.Is(err, ErrUncertain) {
				t.Fatal("lost acknowledgment was safe to retry", err)
			}
			if mode == "rejected" && (err == nil || errors.Is(err, ErrUncertain)) {
				t.Fatal("explicit rejection classification", err)
			}
			if mode == "accepted-quit-lost" && err != nil {
				t.Fatal("QUIT caused duplicate retry", err)
			}
			select {
			case payload := <-received:
				if !strings.Contains(payload, "Message-ID: <"+id+"@example.test>") {
					t.Fatal("message identity changed")
				}
			case <-time.After(time.Second):
				t.Fatal("no data")
			}
		})
	}
}
