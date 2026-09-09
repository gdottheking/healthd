package notify

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

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
func NewEmail(host string, port int, from string, to []string, username, password string) *Email {
	return &Email{
		host:     host,
		port:     port,
		from:     from,
		to:       to,
		username: username,
		password: password,
		send:     smtp.SendMail,
	}
}

// Notify builds and sends the RFC822 message. ctx is honored best-effort: the
// send is run in a goroutine and abandoned if ctx is cancelled first.
func (e *Email) Notify(ctx context.Context, alert Alert) error {
	addr := fmt.Sprintf("%s:%d", e.host, e.port)
	subject := fmt.Sprintf("[roled] %s %s", alert.Role, alert.State)
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
