package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Webhook POSTs the JSON-serialized alert to a configured URL.
type Webhook struct {
	url    string
	client *http.Client
}

// NewWebhook returns a Webhook posting to url with the given per-request
// timeout. A zero or negative timeout disables the client timeout.
func NewWebhook(url string, timeout time.Duration) *Webhook {
	return &Webhook{
		url:    url,
		client: &http.Client{Timeout: timeout},
	}
}

// Notify POSTs the alert as JSON. A non-2xx response is treated as an error.
func (w *Webhook) Notify(ctx context.Context, alert Alert) error {
	body, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("webhook marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook post: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook non-2xx status: %d", resp.StatusCode)
	}
	return nil
}
