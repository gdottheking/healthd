package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookPostsJSON(t *testing.T) {
	var gotBody Alert
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	wh := NewWebhook(srv.URL, time.Second)
	alert := Alert{Role: "ping_monitor", State: "UNHEALTHY", Detail: "down", ConsecutiveFailures: 3, Timestamp: time.Now()}
	if err := wh.Notify(context.Background(), alert); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content-type: got %q", gotContentType)
	}
	if gotBody.Role != "ping_monitor" || gotBody.State != "UNHEALTHY" || gotBody.ConsecutiveFailures != 3 {
		t.Fatalf("unexpected body: %+v", gotBody)
	}
}

func TestWebhookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	wh := NewWebhook(srv.URL, time.Second)
	if err := wh.Notify(context.Background(), Alert{}); err == nil {
		t.Fatal("expected error on non-2xx response")
	}
}
