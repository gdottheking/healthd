package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// implicitTLSPort is the SMTPS submission port, which expects a TLS handshake
// immediately on connect (no STARTTLS). Providers like Gmail serve it here.
const implicitTLSPort = 465

// smtpSendFunc matches net/smtp.SendMail so tests can inject a fake sender.
type smtpSendFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// Email sends alerts as plain RFC822 messages over SMTP.
type Email struct {
	host     string
	port     int
	from     string
	to       []string
	username string
	password string
	send     smtpSendFunc
}

// NewEmail returns an Email channel. password is the resolved secret (read
// from the environment by the caller); it is never stored in config.
//
// The send strategy is chosen by port: port 465 uses an implicit-TLS
// connection (tls.Dial from the start), while every other port uses
// smtp.SendMail, which connects in cleartext and upgrades via STARTTLS (e.g.
// port 587). Pointing STARTTLS at an implicit-TLS port yields an EOF as the
// server drops the plaintext SMTP greeting.
func NewEmail(host string, port int, from string, to []string, username, password string) *Email {
	send := smtp.SendMail
	if useImplicitTLS(port) {
		send = sendImplicitTLS
	}
	return &Email{
		host:     host,
		port:     port,
		from:     from,
		to:       to,
		username: username,
		password: password,
		send:     send,
	}
}

// useImplicitTLS reports whether the port speaks implicit TLS (SMTPS).
func useImplicitTLS(port int) bool { return port == implicitTLSPort }

// sendImplicitTLS delivers a message over a connection that is TLS from the
// start. It matches smtpSendFunc so it is interchangeable with smtp.SendMail.
// The server certificate is verified against the host (derived from addr); it
// does not skip verification.
func sendImplicitTLS(addr string, auth smtp.Auth, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("split host: %w", err)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt %s: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return client.Quit()
}

// Notify builds and sends the RFC822 message. ctx is honored best-effort: the
// send is run in a goroutine and abandoned if ctx is cancelled first.
func (e *Email) Notify(ctx context.Context, alert Alert) error {
	addr := fmt.Sprintf("%s:%d", e.host, e.port)
	subject := fmt.Sprintf("[healthd] %s %s", alert.Role, alert.State)
	msg := buildMessage(e.from, e.to, subject, alert)

	var auth smtp.Auth
	if e.username != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}

	done := make(chan error, 1)
	go func() {
		// Recover here so a panic in the sender does not escape to the
		// runtime on this goroutine, where the dispatcher's recover cannot
		// reach it. Forward it as an error to preserve panic isolation.
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("email send panic: %v", r)
			}
		}()
		done <- e.send(addr, auth, e.from, e.to, msg)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("email send: %w", err)
		}
		return nil
	}
}

func buildMessage(from string, to []string, subject string, alert Alert) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + alert.Timestamp.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(fmt.Sprintf("Role: %s\r\n", alert.Role))
	b.WriteString(fmt.Sprintf("State: %s\r\n", alert.State))
	b.WriteString(fmt.Sprintf("Time: %s\r\n", alert.Timestamp.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("Consecutive failures: %d\r\n", alert.ConsecutiveFailures))
	b.WriteString(fmt.Sprintf("Detail: %s\r\n", alert.Detail))
	return []byte(b.String())
}
