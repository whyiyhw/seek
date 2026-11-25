// Package openai implements pkg/llm.Provider for the OpenAI Chat Completions
// API and any compatible endpoint.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/whyiyhw/seek/pkg/llm"
)

const defaultBaseURL = "https://api.openai.com/v1/chat/completions"

// seekUserAgent identifies seek to every endpoint this client POSTs to.
// The OpenCode Go lane requires a self-identifying User-Agent — a
// generic client-library name (or none at all) routes Go-plan requests
// to per-token billing — and it is good etiquette elsewhere. Derived
// from build info so it needs no per-release bump: "seek/v0.11.0" for
// release builds, "seek/dev" for local ones. Deliberately not
// internal/tui's VersionString: pkg/ stays clean of internal deps, and
// the " · hash+" suffix there is human-banner formatting, not UA fuel.
var seekUserAgent = func() string {
	v := "dev"
	if info, ok := debug.ReadBuildInfo(); ok {
		if mv := info.Main.Version; mv != "" && mv != "(devel)" && !strings.HasPrefix(mv, "v0.0.0-") {
			v = mv
		}
	}
	return "seek/" + v
}()

// Client is a streaming OpenAI Chat Completions client satisfying llm.Provider.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	// tokenSource, when set, supplies the bearer per request instead of
	// apiKey (OpenCode OAuth: a 30-day token that refreshes itself).
	// Set before the first request; never mutated afterwards.
	tokenSource func(ctx context.Context) (string, error)
	// headers are static extra headers (e.g. OpenCode's x-org-id, which
	// the gateway requires alongside an OAuth bearer).
	headers map[string]string
}

// New returns a Client pointed at the official OpenAI endpoint.
func New(apiKey string) *Client {
	return &Client{apiKey: apiKey, baseURL: defaultBaseURL, http: &http.Client{Timeout: 5 * time.Minute}}
}

// NewCompatible returns a Client pointed at an OpenAI-compatible endpoint.
func NewCompatible(apiKey, baseURL string) *Client {
	return &Client{apiKey: apiKey, baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Minute}}
}

// SetTokenSource replaces the static apiKey with a per-request credential
// source. Used by the OpenCode OAuth lane, whose bearer rotates. An error
// from the source aborts the request rather than sending an empty bearer
// (the gateway would answer 401 and the user would see a misleading
// "invalid key").
func (c *Client) SetTokenSource(fn func(ctx context.Context) (string, error)) {
	c.tokenSource = fn
}

// SetHeader adds a header to every request from this client.
func (c *Client) SetHeader(key, value string) {
	if c.headers == nil {
		c.headers = map[string]string{}
	}
	c.headers[key] = value
}

// Name satisfies llm.Provider.
func (c *Client) Name() string { return "OpenAI" }

// --- wire types ---

type openAIRequest struct {
	Model         string          `json:"model"`
	Messages      []openAIMessage `json:"messages"`
	MaxTokens     int             `json:"max_tokens,omitempty"`
	Tools         []openAITool    `json:"tools,omitempty"`
	Stream        bool            `json:"stream"`
	StreamOptions *streamOptions  `json:"stream_options,omitempty"`
	// Temperature is a pointer so "absent" (nil) and explicit 0 are
	// different wires: nil omits the key, keeping text-only requests
	// byte-identical to their pre-knob form (prefix-cache contract).
	Temperature *float64 `json:"temperature,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// openAIContentPart is one block of the array-form `content` the
// multimodal Chat-Completions wire accepts (OpenAI, and every
// compatible endpoint that speaks it — the OpenCode Go gateway among
// them). Text-only messages keep the plain-string form, so their bytes
// are unchanged.
type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
	// File carries one document attachment (type:"file" parts). The
	// Chat-Completions file part is PDF-only per OpenAI's file-inputs
	// guide; file_data holds the data: URL verbatim, filename rides
	// alongside (both fields per the API reference's content-part
	// object).
	File *openAIFileRef `json:"file,omitempty"`
}

type openAIImageURL struct {
	URL string `json:"url"`
}

// openAIFileRef is the `file` object of a type:"file" content part:
// "filename": "x.pdf", "file_data": "data:application/pdf;base64,…".
type openAIFileRef struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data"`
}

type openAIToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function openAIToolCallFn `json:"function"`
}

type openAIToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string       `json:"type"`
	Function openAIToolFn `json:"function"`
}

type openAIToolFn struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
	Usage   *streamUsage   `json:"usage,omitempty"`
}

type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Content   *string        `json:"content"`
	ToolCalls []tcDeltaChunk `json:"tool_calls,omitempty"`
}

type tcDeltaChunk struct {
	Index    int        `json:"index"`
	ID       string     `json:"id,omitempty"`
	Function *tcFnChunk `json:"function,omitempty"`
}

type tcFnChunk struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// streamUsage mirrors the OpenAI usage block. prompt_tokens_details is
// how OpenAI-compatible providers (GLM's paas/v4 among them) report
// server-side prompt-cache hits; providers that don't cache simply omit
// it and CachedTokens stays 0.
type streamUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// cachedTokens returns the cache-hit portion of the prompt, 0 when the
// provider reports no cache detail.
func (u *streamUsage) cachedTokens() int {
	if u == nil || u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// toolAccum accumulates one streaming tool call by index.
type toolAccum struct {
	id   string
	name string
	args bytes.Buffer
}

// --- conversion helpers ---

func convertMessages(msgs []llm.Message) []openAIMessage {
	out := make([]openAIMessage, len(msgs))
	for i, m := range msgs {
		om := openAIMessage{Role: m.Role}
		switch m.Role {
		case "tool":
			om.Content, om.ToolCallID, om.Name = m.Content, m.ToolCallID, m.ToolName
		case "assistant":
			if len(m.ToolCalls) > 0 {
				tc := make([]openAIToolCall, len(m.ToolCalls))
				for j, t := range m.ToolCalls {
					tc[j] = openAIToolCall{ID: t.ID, Type: "function", Function: openAIToolCallFn{Name: t.Name, Arguments: t.Arguments}}
				}
				om.ToolCalls = tc
			} else {
				om.Content = m.Content
			}
		default:
			om.Content = contentFor(m)
		}
		out[i] = om
	}
	return out
}

// contentFor renders one message's content: the plain string when
// there are no images or files (byte-identical to the pre-vision wire
// shape, so text-only prompts keep their prefix-cache behaviour), else
// the array form — text block first, then one image_url block per
// image, then one file block per document.
func contentFor(m llm.Message) any {
	if len(m.Images) == 0 && len(m.Files) == 0 {
		return m.Content
	}
	parts := make([]openAIContentPart, 0, len(m.Images)+len(m.Files)+1)
	if m.Content != "" {
		parts = append(parts, openAIContentPart{Type: "text", Text: m.Content})
	}
	media := 0
	for _, img := range m.Images {
		if img.URL == "" {
			continue
		}
		parts = append(parts, openAIContentPart{
			Type:     "image_url",
			ImageURL: &openAIImageURL{URL: img.URL},
		})
		media++
	}
	for _, f := range m.Files {
		if f.URL == "" {
			continue
		}
		parts = append(parts, openAIContentPart{
			Type: "file",
			File: &openAIFileRef{Filename: f.Filename, FileData: f.URL},
		})
		media++
	}
	if media == 0 {
		// Nothing sendable — keep the plain-string form rather than an
		// array holding only text (some gateways reject array-form
		// content with no media block).
		return m.Content
	}
	return parts
}

func convertTools(tools []llm.ToolDef) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, len(tools))
	for i, t := range tools {
		out[i] = openAITool{Type: "function", Function: openAIToolFn{Name: t.Name, Description: t.Description, Parameters: t.Schema}}
	}
	return out
}

// --- ChatStream ---

// ChatStream implements llm.Provider. The returned channel is always closed.
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	buf, err := json.Marshal(openAIRequest{
		Model: req.Model, Messages: convertMessages(req.Messages),
		MaxTokens: req.MaxTokens, Tools: convertTools(req.Tools),
		Temperature:   req.Temperature,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	bearer := c.apiKey
	if c.tokenSource != nil {
		bearer, err = c.tokenSource(ctx)
		if err != nil {
			return nil, fmt.Errorf("openai: credential: %w", err)
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+bearer)
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	// Self-identify on every lane (see seekUserAgent). The session
	// header is the OpenCode Go lane's routing/prompt-cache contract:
	// one stable value per conversation, so it rides the per-request
	// ChatRequest rather than the client — an absent SessionID (deepseek
	// lane, --no-save, older callers) sends nothing, byte-identical to
	// the pre-opencode wire shape.
	httpReq.Header.Set("User-Agent", seekUserAgent)
	if req.SessionID != "" {
		httpReq.Header.Set("x-opencode-session", req.SessionID)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: status %d: %s", resp.StatusCode, raw)
	}

	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		streamBody(ctx, resp.Body, out)
	}()
	return out, nil
}

func streamBody(ctx context.Context, r io.Reader, out chan<- llm.Event) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	accum := map[int]*toolAccum{}
	var lastFinish string
	var inputTokens, outputTokens, cachedTokens int

	for sc.Scan() {
		select {
		case <-ctx.Done():
			out <- llm.ErrorEvent{Err: ctx.Err()}
			return
		default:
		}
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || string(payload) == "[DONE]" {
			if string(payload) == "[DONE]" {
				break
			}
			continue
		}

		var chunk streamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			out <- llm.ErrorEvent{Err: fmt.Errorf("openai: decode chunk: %w", err)}
			return
		}
		if chunk.Usage != nil {
			inputTokens, outputTokens = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
			cachedTokens = chunk.Usage.cachedTokens()
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != nil && *ch.Delta.Content != "" {
				out <- llm.TextDelta{Delta: *ch.Delta.Content}
			}
			for _, tc := range ch.Delta.ToolCalls {
				a := accum[tc.Index]
				if a == nil {
					a = &toolAccum{}
					accum[tc.Index] = a
				}
				if tc.ID != "" {
					a.id = tc.ID
				}
				if tc.Function != nil {
					if tc.Function.Name != "" {
						a.name = tc.Function.Name
					}
					a.args.WriteString(tc.Function.Arguments)
				}
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				lastFinish = *ch.FinishReason
			}
		}
	}

	// Emit assembled calls in index order. The indices are NOT
	// guaranteed to be contiguous: a gateway that dropped or coalesced
	// one delta (or a strict-schema provider that renumbers after a
	// retry) leaves a hole, and the previous `if a == nil { break }`
	// then discarded every call AFTER the hole — silently, because
	// nothing was ever emitted, which made the turn look like an empty
	// completion (card c_lh6tlq). Sort the keys instead of assuming
	// 0..n-1.
	idx := make([]int, 0, len(accum))
	for i := range accum {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		a := accum[i]
		out <- llm.ToolCallDone{ID: a.id, Name: a.name, Arguments: a.args.String()}
	}
	out <- llm.TurnDone{FinishReason: lastFinish, InputTokens: inputTokens, OutputTokens: outputTokens, CachedInputTokens: cachedTokens}
}
