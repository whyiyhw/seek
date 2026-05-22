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

// ChatStream satisfies llm.Provider — delegates directly.
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	return c.inner.ChatStream(ctx, req)
}
