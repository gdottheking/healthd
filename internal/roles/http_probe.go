package roles

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"time"
)

// NewHTTPClient builds an *http.Client with the given timeout. When
// insecureSkipVerify is true, TLS certificate verification is disabled so
// self-signed HTTPS endpoints are accepted — this trusts ANY certificate and
// must only be used on a trusted network. Otherwise a plain client with normal
// verification is returned.
func NewHTTPClient(timeout time.Duration, insecureSkipVerify bool) *http.Client {
	if !insecureSkipVerify {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
}

// httpProbe issues a GET to url using client and returns nil only on an HTTP
// 2xx response. The body is drained and closed so the connection can be reused.
// The caller controls the deadline via ctx and/or the client's Timeout. It is
// shared by internet_check and url_monitor, which apply the same 2xx-is-healthy
// rule.
func httpProbe(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("non-2xx status: %d", resp.StatusCode)
	}
	return nil
}
