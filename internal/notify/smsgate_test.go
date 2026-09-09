package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSMSGatePostsWithBasicAuth(t *testing.T) {
	var gotPath string
	var gotUser, gotPass string
	var gotOK bool
	var gotBody smsMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPass, gotOK = r.BasicAuth()
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sg := NewSMSGate(srv.URL+"/3rdparty/v1", "user", "secret", []string{"+15551234567"}, time.Second)
	alert := Alert{Role: "internet_check", State: "UNHEALTHY", Detail: "no route"}
	if err := sg.Notify(context.Background(), alert); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if !gotOK || gotUser != "user" || gotPass != "secret" {
		t.Fatalf("basic auth: ok=%v user=%q pass=%q", gotOK, gotUser, gotPass)
	}
	if !strings.HasSuffix(gotPath, "/message") {
		t.Fatalf("path should end in /message, got %q", gotPath)
	}
	if len(gotBody.PhoneNumbers) != 1 || gotBody.PhoneNumbers[0] != "+15551234567" {
		t.Fatalf("phoneNumbers: %+v", gotBody.PhoneNumbers)
	}
	if !strings.Contains(gotBody.Message, "internet_check") || !strings.Contains(gotBody.Message, "UNHEALTHY") {
		t.Fatalf("message should include role and state: %q", gotBody.Message)
	}
}

func TestSMSGateNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	sg := NewSMSGate(srv.URL, "u", "p", []string{"+1"}, time.Second)
	if err := sg.Notify(context.Background(), Alert{}); err == nil {
		t.Fatal("expected error on non-2xx response")
	}
}
