package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// FIMRequest is the body of a DeepSeek fill-in-the-middle completion. It
// hits /beta/completions, an OpenAI legacy-completions-shaped endpoint —
// not the chat endpoint.
//
// Compared to chat, FIM is meaningfully cheaper for in-place edits with a
// known surrounding context (PRD §4.8.3). The model fills in the gap
// between Prompt (prefix) and Suffix.
type FIMRequest struct {
	Model       string   `json:"model"`
	Prompt      string   `json:"prompt"`
	Suffix      string   `json:"suffix,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type FIMResponse struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Choices []FIMChoice `json:"choices"`
	Usage   Usage       `json:"usage"`
}

type FIMChoice struct {
	Index        int    `json:"index"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
}

// FIM issues a non-streaming fill-in-the-middle completion. The streaming
// variant is intentionally deferred — FIM's natural use is small, fast
// completions where bulk-buffering is fine and the saved round-trips
// don't matter.
func (c *Client) FIM(ctx context.Context, req *FIMRequest) (*FIMResponse, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil FIM request")
	}
	if req.Prompt == "" {
		return nil, errors.New("deepseek: FIM requires a non-empty Prompt")
	}
	if req.Model == "" {
		req.Model = ModelChat
	}

	resp, err := c.do(ctx, endpointFIM, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("deepseek FIM: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, parseAPIError(resp.StatusCode, body)
	}

	out := &FIMResponse{}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("deepseek FIM: decode response: %w", err)
	}
	return out, nil
}
