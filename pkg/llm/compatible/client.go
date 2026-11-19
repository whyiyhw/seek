// Package compatible provides a pkg/llm.Provider wrapper for
// OpenAI-compatible endpoints (vLLM, Ollama, SiliconFlow, etc.).
//
// Under the hood it delegates to pkg/llm/provider/openai with a custom
// base URL; only the Name() label is overridden.
package compatible

import (
	"context"

	"github.com/whyiyhw/seek/pkg/llm"
	"github.com/whyiyhw/seek/pkg/llm/provider/openai"
)

// Client wraps an OpenAI-compatible endpoint.
type Client struct {
	inner *openai.Client
	name  string
}

// New returns a Client that POSTs to baseURL with the given apiKey.
// name is used for TUI banners (e.g. "Ollama", "SiliconFlow").
func New(apiKey, baseURL, name string) *Client {
	return &Client{
		inner: openai.NewCompatible(apiKey, baseURL),
		name:  name,
	}
}

// Name satisfies llm.Provider.
func (c *Client) Name() string { return c.name }

// SetTokenSource forwards to the underlying OpenAI client: a per-request
// bearer instead of a static key (OpenCode OAuth rotates its token).
func (c *Client) SetTokenSource(fn func(ctx context.Context) (string, error)) {
	c.inner.SetTokenSource(fn)
}

// SetHeader forwards a static header to the underlying client (the
// OpenCode gateway needs x-org-id next to an OAuth bearer).
func (c *Client) SetHeader(key, value string) { c.inner.SetHeader(key, value) }

// ChatStream satisfies llm.Provider — delegates directly.
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	return c.inner.ChatStream(ctx, req)
}
