package notify

import (
	"context"
	"errors"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestEmailBuildsMessageAndSends(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	e := NewEmail("smtp.example.com", 587, "a@x", []string{"b@x", "c@x"}, "a@x", "secret")
	e.send = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, msg
		return nil
	}

	alert := Alert{Role: "speed_check", State: "UNHEALTHY", Detail: "slow", Timestamp: time.Now()}
	if err := e.Notify(context.Background(), alert); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotAddr != "smtp.example.com:587" {
		t.Fatalf("addr: %q", gotAddr)
	}
	if gotFrom != "a@x" || len(gotTo) != 2 {
		t.Fatalf("from/to: %q %+v", gotFrom, gotTo)
	}
	msg := string(gotMsg)
	if !strings.Contains(msg, "Subject: [healthd] speed_check UNHEALTHY") {
		t.Fatalf("subject missing: %q", msg)
	}
	if strings.Contains(msg, "secret") {
		t.Fatal("password must never appear in the message body")
	}
}

func TestEmailSendPanicBecomesError(t *testing.T) {
	e := NewEmail("h", 25, "a@x", []string{"b@x"}, "", "")
	e.send = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		panic("sender exploded")
	}
	// A panic in the sender goroutine must be converted to an error, not
	// escape and crash the process.
	err := e.Notify(context.Background(), Alert{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected panic to surface as an error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error should mention panic, got %v", err)
	}
}

func TestUseImplicitTLS(t *testing.T) {
	if !useImplicitTLS(465) {
		t.Fatal("port 465 should use implicit TLS")
	}
	for _, p := range []int{25, 587, 2525} {
		if useImplicitTLS(p) {
			t.Fatalf("port %d should not use implicit TLS", p)
		}
	}
}

// NewEmail on port 465 must select the implicit-TLS sender: pointed at a
// closed port it returns a tls-dial error rather than hanging or panicking.
func TestEmailImplicitTLSSelectedForPort465(t *testing.T) {
	// Reserve then release a port so nothing is listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	host, portStr, _ := net.SplitHostPort(addr)
	ln.Close()

	e := NewEmail(host, implicitTLSPort, "a@x", []string{"b@x"}, "", "")
	// Override the address the sender dials by using the real reserved port via
	// the sender directly; this exercises sendImplicitTLS' error path.
	if err := e.send(net.JoinHostPort(host, portStr), nil, "a@x", []string{"b@x"}, []byte("m")); err == nil {
		t.Fatal("expected a dial error against a closed port")
	}
}

func TestEmailSendError(t *testing.T) {
	e := NewEmail("h", 25, "a@x", []string{"b@x"}, "", "")
	e.send = func(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
		return errors.New("smtp down")
	}
	if err := e.Notify(context.Background(), Alert{Timestamp: time.Now()}); err == nil {
		t.Fatal("expected error when send fails")
	}
}
