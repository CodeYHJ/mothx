package skillhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultRequestTimeout = 20 * time.Second

// errBodyBytes bounds error response bodies kept for inspection. Sized to fit
// ClawHub AMBIGUOUS_SKILL_SLUG payloads with many candidate matches.
const errBodyBytes = 64 << 10

// defaultMaxJSONBytes caps successful JSON response bodies for getJSON callers.
const defaultMaxJSONBytes = 16 << 20

func newHTTPClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: defaultRequestTimeout}
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, out any) error {
	_, body, err := getWithStatus(ctx, client, endpoint, defaultMaxJSONBytes)
	if err != nil {
		return err
	}
	return decodeJSON(endpoint, body, out)
}

// getWithStatus performs a GET and returns the status code and response body.
// Error responses keep a bounded body so callers can inspect structured API
// errors (e.g. ClawHub AMBIGUOUS_SKILL_SLUG payloads).
func getWithStatus(ctx context.Context, client *http.Client, endpoint string, maxBytes int64) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyBytes))
		return resp.StatusCode, body, fmt.Errorf("GET %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	return resp.StatusCode, body, err
}

func decodeJSON(endpoint string, body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return nil
}

func endpoint(base, path string, query url.Values) string {
	u, _ := url.Parse(strings.TrimRight(base, "/") + path)
	u.RawQuery = query.Encode()
	return u.String()
}
