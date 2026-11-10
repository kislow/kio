package standup

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

// DefaultEndpoint is GitHub's GraphQL API.
const DefaultEndpoint = "https://api.github.com/graphql"

// requestTimeout bounds one GraphQL call.
const requestTimeout = 30 * time.Second

// Client queries GitHub's GraphQL API as one account. It only reads.
type Client struct {
	Endpoint string
	// Token belongs to the identity the standup is for; it is sent only as a
	// header and never appears in errors.
	Token string
	HTTP  *http.Client
}

// NewClient returns a client for endpoint (DefaultEndpoint when empty).
func NewClient(endpoint, token string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Client{Endpoint: endpoint, Token: token, HTTP: &http.Client{Timeout: requestTimeout}}
}

type gqlError struct {
	Message string `json:"message"`
}

// query runs one GraphQL query and decodes its data into out.
func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return fmt.Errorf("encoding query: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ki-recap")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("calling GitHub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only response; nothing to recover on close
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("reading GitHub response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned %s: %s", resp.Status, snippet(raw))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decoding GitHub response: %w", err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("GitHub: %s", strings.Join(msgs, "; "))
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decoding GitHub data: %w", err)
	}
	return nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
