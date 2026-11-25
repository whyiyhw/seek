// Package openai implements pkg/llm.Provider for the OpenAI Chat Completions
// API and any compatible endpoint.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

// Retry policy for the second-tier OpenAI-compatible lanes. Both halves
// exist on the first-tier lane (pkg/deepseek) and neither existed here,
// which is why one HTTP/2 stream reset killed an entire unattended round:
//
//   - openStream retries a transient OPEN failure — transport errors
//     (connection reset, EOF before a status line, HTTP/2
//     PROTOCOL_ERROR), 5xx and 429 — once with a fixed backoff. Other 4xx
//     are configuration problems retrying cannot fix.
//   - pumpStream re-opens the stream once when a body that DID open
//     breaks (or ends cleanly) before producing a single event. Nothing
//     reached the caller, so the re-open is invisible — the same shape
//     as pkg/deepseek's pumpChatStream.
//
// Both re-send the SAME marshalled bytes (see newRequest): a retry must
// be byte-identical on the wire or the gateway's prompt cache misses,
// which costs ~10× the tokens of a hit. Upstream may have billed the
// discarded half-generation; that is the same trade the first-tier lane
// makes, and one retry bounds it.
const (
	maxRetries   = 1
	retryBackoff = 500 * time.Millisecond
)

// ChatStream implements llm.Provider. The returned channel is always closed.
//
// The error return covers only the OPEN phase (after its retry budget);
// once a 2xx is in hand the outcome — including a mid-stream break — is
// carried by the channel's terminal event.
func (c *Client) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	buf, err := json.Marshal(openAIRequest{
		Model: req.Model, Messages: convertMessages(req.Messages),
		MaxTokens: req.MaxTokens, Tools: convertTools(req.Tools),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	bearer := c.apiKey
	if c.tokenSource != nil {
		bearer, err = c.tokenSource(ctx)
		if err != nil {
			return nil, fmt.Errorf("openai: credential: %w", err)
		}
	}

	// newRequest rebuilds the wire request from the SAME marshalled
	// bytes for every attempt. Never a shared *http.Request or an already
	// consumed bytes.Reader — the body is re-read by each attempt, and
	// re-Marshalling would be free to drift (map ordering, tool schema
	// whitespace) and break the prefix cache.
	newRequest := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
		if err != nil {
			return nil, fmt.Errorf("openai: build request: %w", err)
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
		return httpReq, nil
	}

	resp, err := c.openStream(ctx, newRequest)
	if err != nil {
		return nil, err
	}

	out := make(chan llm.Event, 16)
	go c.pumpStream(ctx, resp, newRequest, out)
	return out, nil
}

// openStream issues the request and returns the response once a 2xx is
// observed, retrying transient failures once with a fixed backoff. On
// exhaustion the error is annotated with the number of attempts made
// ("… (after N attempts)") so a caller can tell a one-shot upstream blip
// from an endpoint that stayed down across every attempt. ctx
// cancellation returns immediately, unannotated (the caller already knows
// why it stopped).
func (c *Client) openStream(ctx context.Context, newRequest func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	attempts := 0
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts++
		if attempt > 0 {
			traceRetry("open", attempts, lastErr)
			if err := sleepCtx(ctx, retryBackoff); err != nil {
				return nil, err
			}
		}
		httpReq, err := newRequest()
		if err != nil {
			return nil, err // build failure, not transient
		}
		resp, err := c.http.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("openai: http: %w", err)
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}
		if resp.StatusCode/100 == 2 {
			return resp, nil
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("openai: status %d: %s", resp.StatusCode, raw)
		if resp.StatusCode/100 != 5 && resp.StatusCode != 429 {
			return nil, lastErr // 4xx (other than 429) is not a transient
		}
	}
	return nil, fmt.Errorf("%w (after %d attempts)", lastErr, attempts)
}

// pumpStream owns the response body and drives one or more read passes.
// A break (or a clean-but-empty body) before the first text delta reached
// the caller discards nothing visible, so we re-open the request once —
// the same shape as pkg/deepseek's pumpChatStream.
func (c *Client) pumpStream(ctx context.Context, initial *http.Response, newRequest func() (*http.Request, error), out chan<- llm.Event) {
	defer close(out)

	resp := initial
	attempts := 0
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts++
		res := readStreamBody(ctx, resp.Body, out)

		canReopen := !res.emittedAny &&
			len(res.accum) == 0 &&
			res.lastFinish == "" &&
			attempt < maxRetries &&
			ctx.Err() == nil &&
			(res.streamErr != nil || res.isEmpty())
		if !canReopen {
			emitTerminal(ctx, out, res, nil, attempts)
			return
		}

		if err := sleepCtx(ctx, retryBackoff); err != nil {
			emitTerminal(ctx, out, res, nil, attempts)
			return
		}
		traceRetry("reopen", attempts+1, readFailure(res))
		next, err := c.openStream(ctx, newRequest)
		if err != nil {
			// openStream's error already carries its own attempt count;
			// don't stack a second annotation on top of it.
			emitTerminal(ctx, out, res, fmt.Errorf("openai: reopen: %w", err), 1)
			return
		}
		resp = next
	}
}

// streamOutcome is the result of one SSE read pass. emittedAny is the
// load-bearing field: once true a delta has reached the caller, the
// partial stream is committed and re-opening would duplicate visible
// output. Tool-call fragments accumulate in memory and reach the caller
// only at the terminal event, so a break before the FIRST text delta is
// still invisible and safe to re-open.
type streamOutcome struct {
	emittedAny                              bool
	accum                                   map[int]*toolAccum
	lastFinish                              string
	inputTokens, outputTokens, cachedTokens int
	streamErr                               error // transport read / scanner failure
}

// isEmpty reports a pass that produced nothing the caller could act on —
// the "model said nothing" case the agent's empty-response guard exists
// to catch. The client retries it once first so a caller never sees it
// on the first attempt. Same definition as pkg/deepseek's
// streamReadResult.isEmpty (an empty finish_reason is part of it, so a
// gateway that DID answer finish=stop with no content still reaches the
// agent's empty guard on the first attempt it produces).
func (o streamOutcome) isEmpty() bool {
	return o.streamErr == nil && !o.emittedAny && len(o.accum) == 0 && o.lastFinish == ""
}

// readStreamBody parses ONE SSE body. It closes the body and emits data
// events only — the terminal TurnDone/ErrorEvent belongs to emitTerminal,
// so the retry policy can decide between them.
//
// sc.Err() is load-bearing: no check means an HTTP/2 stream reset mid-
// body looks exactly like a normal EOF, and the turn is reported as a
// clean (freshly empty) completion whose real transport error vanished
// (card c_koutux). anthropic's client already checks it; gemini's
// parseStream had the same hole.
func readStreamBody(ctx context.Context, body io.ReadCloser, out chan<- llm.Event) streamOutcome {
	defer body.Close()

	res := streamOutcome{accum: map[int]*toolAccum{}}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		select {
		case <-ctx.Done():
			res.streamErr = ctx.Err()
			return res
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
		if string(payload) == "[DONE]" {
			return res
		}

		var chunk streamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			res.streamErr = &decodeFailure{cause: err}
			return res
		}
		if chunk.Usage != nil {
			res.inputTokens, res.outputTokens = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
			res.cachedTokens = chunk.Usage.cachedTokens()
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != nil && *ch.Delta.Content != "" {
				out <- llm.TextDelta{Delta: *ch.Delta.Content}
				res.emittedAny = true
			}
			for _, tc := range ch.Delta.ToolCalls {
				a := res.accum[tc.Index]
				if a == nil {
					a = &toolAccum{}
					res.accum[tc.Index] = a
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
				res.lastFinish = *ch.FinishReason
			}
		}
	}

	if err := sc.Err(); err != nil {
		res.streamErr = err
	}
	return res
}

// decodeFailure wraps a malformed SSE chunk so the terminal event can
// tell a decode error apart from a transport error (the DeepSeek lane
// emits "decode_error:" for the former — agent.go's canRetry matches
// both prefixes).
type decodeFailure struct{ cause error }

func (d *decodeFailure) Error() string { return "openai: decode chunk: " + d.cause.Error() }
func (d *decodeFailure) Unwrap() error { return d.cause }

// emitTerminal writes the accumulated tool calls and the terminal event.
//
// Mid-stream break semantics — one rule, chosen to mirror pkg/deepseek's
// pumpChatStream + emitTerminalDone exactly, because that is the shape
// agent.go already handles on the first-tier lane:
//
//   - pumpStream re-opens the request once when the break happened
//     before ANY event reached the caller (no text delta, no buffered
//     tool call). Nothing visible is discarded, so the re-open is free.
//   - After that budget (or when a delta already reached the caller)
//     the stream ends with a TurnDone carrying the "stream_error:" /
//     "decode_error:" sentinel in FinishReason — never a silent empty
//     finish (that was the bug: a real transport error looked like an
//     empty completion and vanished), and never an ErrorEvent for the
//     eventless case (an ErrorEvent from runTurnLLM ends the whole
//     round, while the sentinel routes through Prompt's existing
//     empty-with-odd-finish self-heal).
//   - The assembled calls ARE emitted even on a break, so runTurnLLM
//     sees ToolCalls > 0 and agent.go's canRetry recognises "stream died
//     while the model was calling tools" and re-issues the turn.
//
// A clean EOF with no error is left untouched (some OpenAI-compatible
// gateways never send [DONE]); the empty-completion guard owns that case.
// ctx cancellation is reported as an ErrorEvent (anthropic's shape):
// the caller already knows why it stopped.
func emitTerminal(ctx context.Context, out chan<- llm.Event, res streamOutcome, openErr error, attempts int) {
	if ctx.Err() != nil {
		out <- llm.ErrorEvent{Err: ctx.Err()}
		return
	}

	finish := res.lastFinish
	if finish == "" {
		// Only a break WITHOUT a declared finish is a stream failure. If
		// the upstream already sent a finish_reason chunk, the turn's
		// outcome is known and only the [DONE] trailer was lost —
		// overwriting a declared "tool_calls" with a sentinel would make
		// the agent refuse to commit a complete turn.
		switch {
		case openErr != nil:
			finish = sentinel("stream_error:", openErr, attempts)
		case res.streamErr != nil:
			var dec *decodeFailure
			if errors.As(res.streamErr, &dec) {
				finish = sentinel("decode_error:", dec.cause, attempts)
			} else {
				finish = sentinel("stream_error:", res.streamErr, attempts)
			}
		}
	}

	// Emit assembled calls in index order. The indices are NOT
	// guaranteed to be contiguous: a gateway that dropped or coalesced
	// one delta (or a strict-schema provider that renumbers after a
	// retry) leaves a hole, and a forward-only loop with `break` on the
	// first missing index discarded every call AFTER the hole — silently,
	// which made the turn look like an empty completion (card c_lh6tlq).
	idx := make([]int, 0, len(res.accum))
	for i := range res.accum {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		a := res.accum[i]
		out <- llm.ToolCallDone{ID: a.id, Name: a.name, Arguments: a.args.String()}
	}
	out <- llm.TurnDone{
		FinishReason:      finish,
		InputTokens:       res.inputTokens,
		OutputTokens:      res.outputTokens,
		CachedInputTokens: res.cachedTokens,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// sentinel renders a stream-failure FinishReason: the DeepSeek lane's
// prefix vocabulary (so agent.go's canRetry matches on every lane) plus
// the "… (after N attempts)" annotation when more than one attempt was
// made, which is how an operator tells a one-shot blip from a broken
// endpoint.
func sentinel(prefix string, cause error, attempts int) string {
	msg := cause.Error()
	if attempts > 1 {
		msg = fmt.Sprintf("%s (after %d attempts)", msg, attempts)
	}
	return prefix + truncate(msg, 128)
}

// traceRetry logs one line to stderr when SEEK_LLM_TRACE is set. The
// retry is silent by design (nothing reached the caller), but a silent
// recovery is undebuggable from the outside — an unattended round that
// used to fail now just works, and nothing in the transcript says a
// transport blip happened. Opt-in so the default (TUI, print mode, ACP)
// wire stays byte-for-byte as it was.
func traceRetry(phase string, attempt int, lastErr error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SEEK_LLM_TRACE"))) {
	case "1", "true", "yes", "on":
		fmt.Fprintf(os.Stderr, "[llm-trace] retry #%d (%s): %v\n", attempt-1, phase, lastErr)
	}
}

// readFailure names why a read pass has to be retried, for the trace line.
func readFailure(res streamOutcome) error {
	if res.streamErr != nil {
		return res.streamErr
	}
	return fmt.Errorf("stream ended with no events (empty completion)")
}

// sleepCtx waits d unless ctx fires first; a cancelled context returns
// immediately, which is how openStream / pumpStream abort a retry.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
