package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SMSGate posts short messages to a self-hosted SMS Gateway for Android
// (sms-gate.app) REST API using HTTP Basic auth.
type SMSGate struct {
	baseURL    string
	username   string
	password   string
	recipients []string
	client     *http.Client
}

// NewSMSGate returns an SMSGate channel. password is the resolved secret read
// from the environment by the caller.
func NewSMSGate(baseURL, username, password string, recipients []string, timeout time.Duration) *SMSGate {
	return &SMSGate{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		recipients: recipients,
		client:     &http.Client{Timeout: timeout},
	}
}

type smsMessage struct {
	Message      string   `json:"message"`
	PhoneNumbers []string `json:"phoneNumbers"`
}

// Notify posts a short SMS-friendly message to all recipients. A non-2xx
// response is treated as an error.
func (s *SMSGate) Notify(ctx context.Context, alert Alert) error {
	text := fmt.Sprintf("[roled] %s %s: %s", alert.Role, alert.State, alert.Detail)
	body, err := json.Marshal(smsMessage{Message: text, PhoneNumbers: s.recipients})
	if err != nil {
		return fmt.Errorf("smsgate marshal: %w", err)
	}

	url := s.baseURL + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("smsgate request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.username, s.password)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("smsgate post: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("smsgate non-2xx status: %d", resp.StatusCode)
	}
	return nil
}
