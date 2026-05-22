// Package gemini implements pkg/llm.Provider for Google Gemini via the
// streamGenerateContent SSE endpoint. Zero external dependencies.
package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/whyiyhw/seek/pkg/llm"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com"

// Client satisfies llm.Provider for Google Gemini.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// New returns a Client using apiKey against the live Gemini API.
func New(apiKey string) *Client { return newWithBase(apiKey, defaultBaseURL) }

// newWithBase is used by tests to inject a fake server URL.
func newWithBase(apiKey, baseURL string) *Client {
	return &Client{apiKey: apiKey, baseURL: baseURL, http: &http.Client{Timeout: 5 * time.Minute}}
}

// Name implements llm.Provider.
func (c *Client) Name() string { return "Gemini" }

// ---------------------------------------------------------------------------
// Wire types – request
// ---------------------------------------------------------------------------

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	Tools             []geminiToolList `json:"tools,omitempty"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string          `json:"text,omitempty"`
	FunctionCall     *geminiFuncCall `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResp `json:"functionResponse,omitempty"`
}

type geminiFuncCall struct {
	Name string                 `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiFuncResp struct {
	Name     string                 `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiToolList struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiFuncDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ---------------------------------------------------------------------------
// Wire types – response
// ---------------------------------------------------------------------------

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsage      `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
}

// ---------------------------------------------------------------------------
// ChatStream
// ---------------------------------------------------------------------------

// ChatStream implements llm.Provider. The returned channel is always closed.
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	body, err := buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("gemini: build request: %w", err)
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?key=%s&alt=sse",
		c.baseURL, req.Model, c.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("gemini: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini: http: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini: status %d: %s", resp.StatusCode, raw)
	}

	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		parseStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// buildRequest converts an llm.ChatRequest into the Gemini wire format.
func buildRequest(req llm.ChatRequest) ([]byte, error) {
	gr := geminiRequest{}
	var sysParts []geminiPart

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			sysParts = append(sysParts, geminiPart{Text: m.Content})
		case "tool":
			gr.Contents = append(gr.Contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{{FunctionResponse: &geminiFuncResp{
					Name:     m.ToolName,
					Response: map[string]any{"content": m.Content},
				}}},
			})
		case "assistant":
			gr.Contents = append(gr.Contents, geminiContent{Role: "model", Parts: assistantParts(m)})
		default:
			gr.Contents = append(gr.Contents, geminiContent{
				Role: "user", Parts: []geminiPart{{Text: m.Content}},
			})
		}
	}

	if len(sysParts) > 0 {
		gr.SystemInstruction = &geminiContent{Parts: sysParts}
	}
	if len(req.Tools) > 0 {
		decls := make([]geminiFuncDecl, len(req.Tools))
		for i, t := range req.Tools {
			decls[i] = geminiFuncDecl{Name: t.Name, Description: t.Description, Parameters: t.Schema}
		}
		gr.Tools = []geminiToolList{{FunctionDeclarations: decls}}
	}
	return json.Marshal(gr)
}

// assistantParts converts an assistant message (with optional tool calls) to Gemini parts.
func assistantParts(m llm.Message) []geminiPart {
	var parts []geminiPart
	if m.Content != "" {
		parts = append(parts, geminiPart{Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		args := map[string]any{}
		if tc.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
		}
		parts = append(parts, geminiPart{FunctionCall: &geminiFuncCall{Name: tc.Name, Args: args}})
	}
	if len(parts) == 0 {
		parts = append(parts, geminiPart{Text: ""})
	}
	return parts
}

// parseStream reads an SSE body and sends Events to out until EOF or error.
func parseStream(ctx context.Context, body io.Reader, out chan<- llm.Event) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastUsage *geminiUsage
	var lastFinish string
	var toolIdx int
	var hasFuncCall bool

	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 {
			continue
		}
		var chunk geminiResponse
		if err := json.Unmarshal(payload, &chunk); err != nil {
			out <- llm.ErrorEvent{Err: fmt.Errorf("gemini: decode chunk: %w", err)}
			return
		}
		if chunk.UsageMetadata != nil {
			lastUsage = chunk.UsageMetadata
		}
		for _, cand := range chunk.Candidates {
			if cand.FinishReason != "" {
				lastFinish = cand.FinishReason
			}
			for _, part := range cand.Content.Parts {
				switch {
				case part.FunctionCall != nil:
					hasFuncCall = true
					argsJSON, _ := json.Marshal(part.FunctionCall.Args)
					out <- llm.ToolCallDone{
						ID:        fmt.Sprintf("gemini_%d", toolIdx),
						Name:      part.FunctionCall.Name,
						Arguments: string(argsJSON),
					}
					toolIdx++
				case part.Text != "":
					out <- llm.TextDelta{Delta: part.Text}
				}
			}
		}
	}

	var inTok, outTok int
	if lastUsage != nil {
		inTok, outTok = lastUsage.PromptTokenCount, lastUsage.CandidatesTokenCount
	}
	out <- llm.TurnDone{
		FinishReason: normaliseFinish(lastFinish, hasFuncCall),
		InputTokens:  inTok,
		OutputTokens: outTok,
	}
}

// normaliseFinish maps Gemini finish reasons to canonical lowercase strings.
func normaliseFinish(reason string, hasFuncCall bool) string {
	if hasFuncCall {
		return "tool_calls"
	}
	switch reason {
	case "STOP", "":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	default:
		return "stop"
	}
}
