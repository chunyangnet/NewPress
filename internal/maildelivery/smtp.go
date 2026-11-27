// Package maildelivery sends bounded transactional mail through an explicitly
// configured SMTP relay. It never discovers destinations from recipient MX.
package maildelivery

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type Config struct{ Address, Username, Password, From, Mode string }

type Sender interface {
	Send(context.Context, string, string, string) error
}

// ErrUncertain means the relay may already have accepted DATA. Automatic
// retries would risk duplicates; the queue must preserve this distinct state.
var ErrUncertain = errors.New("mail acceptance is uncertain")

type Message struct{ To, Subject, Body, ID string }
type MessageSender interface {
	SendMessage(context.Context, Message) error
}

type SMTP struct{ config Config }

func (c Config) Enabled() bool { return c.Address != "" }

func (c Config) Validate() error {
	if !c.Enabled() {
		if c.Username != "" || c.Password != "" || c.From != "" {
			return errors.New("SMTP address is required when mail settings are supplied")
		}
		return nil
	}
	host, port, err := net.SplitHostPort(c.Address)
	n, parseErr := strconv.Atoi(port)
	if err != nil || host == "" || parseErr != nil || n < 1 || n > 65535 || strings.ContainsAny(host, "\r\n\x00") {
		return errors.New("SMTP address must be a host:port")
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil || from.Address != c.From || strings.ContainsAny(c.From, "\r\n") || len(c.From) > 320 {
		return errors.New("SMTP from must be a bare email address")
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("SMTP username and password must be configured together")
	}
	switch c.Mode {
	case "", "starttls", "tls":
	case "loopback":
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() || c.Password != "" {
			return errors.New("plain SMTP is only allowed on a literal loopback address without credentials")
		}
	default:
		return errors.New("SMTP mode must be starttls, tls, or loopback")
	}
	return nil
}

func NewSMTP(c Config) (*SMTP, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &SMTP{config: c}, nil
}

func (s *SMTP) Send(ctx context.Context, to, subject, body string) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	return s.SendMessage(ctx, Message{To: to, Subject: subject, Body: body, ID: hex.EncodeToString(id[:])})
}

func (s *SMTP) SendMessage(ctx context.Context, input Message) error {
	to, subject, body := input.To, input.Subject, input.Body
	if len(input.ID) < 16 || len(input.ID) > 64 {
		return errors.New("invalid message identity")
	}
	if _, err := hex.DecodeString(input.ID); err != nil {
		return errors.New("invalid message identity")
	}
	if s == nil || !s.config.Enabled() {
		return errors.New("mail is not configured")
	}
	parsed, err := mail.ParseAddress(to)
	if err != nil || parsed.Address != to || strings.ContainsAny(to+subject, "\r\n") || len(to) > 320 || len(subject) > 500 || len(body) > 16<<10 {
		return errors.New("invalid mail message")
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	host, _, _ := net.SplitHostPort(s.config.Address)
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", s.config.Address)
	if err != nil {
		return errors.New("mail relay connection failed")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if s.config.Mode == "tls" {
		secured := tls.Client(conn, tlsConfig)
		if err = secured.HandshakeContext(ctx); err != nil {
			return errors.New("mail TLS failed")
		}
		conn = secured
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return errors.New("mail relay greeting failed")
	}
	defer client.Close()
	if s.config.Mode != "tls" && s.config.Mode != "loopback" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("mail relay requires STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return errors.New("mail TLS failed")
		}
	}
	if s.config.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, host)); err != nil {
			return errors.New("mail relay authentication failed")
		}
	}
	if err = client.Mail(s.config.From); err != nil {
		return errors.New("mail sender rejected")
	}
	if err = client.Rcpt(to); err != nil {
		return errors.New("mail recipient rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("mail relay data failed")
	}
	message := "From: " + s.config.From + "\r\nTo: " + to + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMessage-ID: <" + input.ID + "@" + s.config.From[strings.LastIndex(s.config.From, "@")+1:] + ">\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	for len(encoded) > 0 {
		n := 76
		if len(encoded) < n {
			n = len(encoded)
		}
		message += encoded[:n] + "\r\n"
		encoded = encoded[n:]
	}
	if _, err = fmt.Fprint(writer, message); err != nil {
		return errors.New("mail relay write failed")
	}
	if err = writer.Close(); err != nil {
		var rejection *textproto.Error
		if errors.As(err, &rejection) {
			return errors.New("mail relay delivery rejected")
		}
		return ErrUncertain
	}
	// DATA acceptance is the delivery boundary. A failed QUIT cannot make an
	// accepted message unsent; do not invite an immediate duplicate retry.
	_ = client.Quit()
	return nil
}
