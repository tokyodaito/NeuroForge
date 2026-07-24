package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client talks to the daemon loopback API. Both the CLI and the TUI use it so
// the command surface stays unified (ADR-0004).
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient returns a client for baseURL (e.g. "http://127.0.0.1:54321") with
// the given bearer token.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// ErrNotRunning is returned when the daemon cannot be reached.
var ErrNotRunning = errors.New("transport: daemon not reachable")

// Health calls GET /healthz. A nil error means the daemon responded healthy.
func (c *Client) Health(ctx context.Context) (HealthResponse, error) {
	var hr HealthResponse
	if err := c.getJSON(ctx, "/healthz", &hr); err != nil {
		return HealthResponse{}, err
	}
	return hr, nil
}

// Status calls GET /status.
func (c *Client) Status(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.getJSON(ctx, "/status", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Audit calls GET /audit?limit=limit (newest-first). A limit<=0 uses the
// server default.
func (c *Client) Audit(ctx context.Context, limit int) ([]AuditEntry, error) {
	path := "/audit"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out []AuditEntry
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Shutdown calls POST /shutdown (graceful daemon shutdown). The daemon exits on
// its own; callers should then wait for the process to terminate.
func (c *Client) Shutdown(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/shutdown", nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("shutdown: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Stream opens the /events SSE stream and returns a channel of events. The
// stream runs until ctx is cancelled or the connection breaks; cancel ctx to
// stop.
func (c *Client) Stream(ctx context.Context) (<-chan Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/events", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	// Disable the per-request timeout for a long-lived stream.
	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("events: unexpected status %d", resp.StatusCode)
	}

	out := make(chan Event, defaultSubscribeBuf)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			payload := bytes.TrimSpace(line[6:])
			if len(payload) == 0 {
				continue
			}
			var evt Event
			if err := json.Unmarshal(payload, &evt); err != nil {
				continue
			}
			select {
			case out <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("%s: decode: %w", path, err)
	}
	return nil
}

func (c *Client) setAuth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// readBody reads and returns the response body (capped at 1 MiB).
func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// parseErrorMessage extracts the "error" field from a JSON error body, or falls
// back to the raw body text.
func parseErrorMessage(body []byte, status int) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err == nil {
		if msg, ok := m["error"].(string); ok && msg != "" {
			return msg
		}
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("status %d", status)
	}
	return text
}
