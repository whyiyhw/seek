// Package deepseek is the first-class DeepSeek API client for seek.
//
// It intentionally does NOT implement the generic pkg/llm.Provider interface:
// DeepSeek-specific features (prefix-cache metadata, FIM endpoint, the
// reasoner's separate code path) lose information when squeezed through a
// lowest-common-denominator abstraction. Callers that want optimisation type-
// assert *deepseek.Client; callers that just want chat use the unified
// pkg/llm.Provider for Anthropic/OpenAI/Gemini.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	DefaultBaseURL = "https://api.deepseek.com"

	endpointChat = "/chat/completions"
	// endpointFIM is the fill-in-the-middle endpoint — used by the `edit`
	// tool's fast path. Wired up in a later milestone.
	endpointFIM = "/beta/completions"
)

// Client is a DeepSeek API client. Construct via New, then call Chat, ChatStream, or FIM.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// Option configures a Client. See WithAPIKey, WithBaseURL, WithHTTPClient.
type Option func(*Client)

// WithAPIKey returns an Option that sets the API key used for authentication.
// If DEEPSEEK_API_KEY is set in the environment, that value is used as default.
func WithAPIKey(k string) Option { return func(c *Client) { c.apiKey = k } }

// WithBaseURL returns an Option that overrides the default API base URL.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient returns an Option that replaces the default HTTP client.
// Useful for custom timeouts, testing with httptest, or injecting transport layers.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a Client with the given functional options.
// Defaults: baseURL = DefaultBaseURL (DeepSeek production), HTTP timeout = 5 minutes.
// At minimum, WithAPIKey or DEEPSEEK_API_KEY must be set before making requests.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Chat performs a non-streaming chat completion. Transient failures
// (transport errors, 5xx, 429) are retried once via retryCall — same
// policy as ChatStream — so a connection blip doesn't surface as a hard
// error on the first attempt.
func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil request")
	}
	r := *req
	r.Stream = false

	return retryCall(ctx, func() (*ChatResponse, error) {
		resp, err := c.do(ctx, endpointChat, &r)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("deepseek: read response: %w", err)
		}
		if resp.StatusCode/100 != 2 {
			return nil, parseAPIError(resp.StatusCode, body)
		}

		out := &ChatResponse{}
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("deepseek: decode response: %w", err)
		}
		return out, nil
	})
}

func (c *Client) do(ctx context.Context, path string, payload any) (*http.Response, error) {
	if c.apiKey == "" {
		return nil, errors.New("deepseek: missing api key (set DEEPSEEK_API_KEY)")
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("deepseek: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek: http: %w", err)
	}
	return resp, nil
}

func parseAPIError(status int, body []byte) error {
	env := errorEnvelope{}
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Message != "" {
		env.Error.StatusCode = status
		return &env.Error
	}
	return &APIError{
		StatusCode: status,
		Message:    fmt.Sprintf("status %d: %s", status, string(body)),
	}
}
