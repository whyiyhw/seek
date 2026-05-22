// Package tools is the in-process tool registry consumed by pkg/agent.
//
// Tools are described once at startup; their wire-format JSON schema is
// frozen on first read (sync.Once) to keep the byte stream stable across
// turns — DeepSeek's prefix cache only hits when the prompt's byte prefix
// is identical, so any non-determinism in tool serialisation kills hit rate
// (PRD §4.8.1).
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Tool is the contract every in-process tool implements.
type Tool interface {
	// Name is the function name the LLM uses to invoke this tool. Must be
	// unique within a Registry and stable across releases.
	Name() string
	// Description tells the LLM when to use this tool. Goes verbatim into
	// the tool schema; keep it short and action-oriented.
	Description() string
	// Schema is the JSON Schema for the tool's arguments object. Must be
	// deterministic — the same Tool instance must return byte-identical
	// JSON every call.
	Schema() json.RawMessage
	// Execute runs the tool. raw is the JSON argument object the LLM
	// produced; tools are responsible for unmarshalling/validating.
	Execute(ctx context.Context, raw json.RawMessage) (string, error)
}

// StreamDelta is one incremental chunk emitted by a StreamingTool while
// it works. Tools without anything live-worthy to say should just
// implement Tool — most do.
//
// Reasoning=true marks the delta as a chain-of-thought trace (e.g. from
// V4 thinking-mode's reasoning_content stream) so the TUI can route it
// to the foldable reasoning region rather than the main answer area.
type StreamDelta struct {
	Delta     string
	Reasoning bool
}

// StreamingTool is an opt-in extension to Tool for tools whose
// execution takes long enough that intermediate output is valuable
// (think: V4 thinking-mode calls, which routinely take 10-60s before
// returning anything from Execute). The agent prefers ExecuteStream
// when the tool implements this interface and falls back to Execute
// otherwise.
//
// ExecuteStream pushes StreamDelta values via the supplied `push`
// callback. push returns a non-nil error when the agent's context has
// been cancelled; the tool should propagate it (`return "", err`)
// rather than ignoring it, otherwise an Esc interrupt won't be felt
// until the underlying stream finishes naturally.
//
// The returned string is the same value Execute would have produced
// for the same model output — it becomes the tool result message in
// history, so streaming vs non-streaming dispatch produces byte-
// identical conversation prefixes (keeps DeepSeek's prefix cache hot).
type StreamingTool interface {
	Tool
	ExecuteStream(ctx context.Context, raw json.RawMessage, push func(StreamDelta) error) (string, error)
}

// ReadOnlyTool is an opt-in marker for tools that only read data and
// never mutate the filesystem, shell, or any external state. The agent
// dispatches a batch of ReadOnlyTools concurrently when all calls in a
// turn implement this interface.
type ReadOnlyTool interface {
	Tool
	ReadOnly() bool
}

// Registry is the set of tools available to an Agent. Build it once at
// startup; it is safe for concurrent reads after the first Wire() call.
type Registry struct {
	tools  []Tool
	byName map[string]Tool

	once   sync.Once
	frozen []deepseek.Tool
}

// New returns an empty registry. Use Add to insert tools.
func New() *Registry {
	return &Registry{byName: map[string]Tool{}}
}

// Add inserts a tool. Panics on duplicate name — duplicates are always a
// programming error.
func (r *Registry) Add(t Tool) *Registry {
	if _, ok := r.byName[t.Name()]; ok {
		panic("tools: duplicate tool name " + t.Name())
	}
	r.tools = append(r.tools, t)
	r.byName[t.Name()] = t
	return r
}

// Lookup returns the tool with the given name, or nil if not registered.
func (r *Registry) Lookup(name string) Tool { return r.byName[name] }

// Wire returns the tool list formatted for DeepSeek's `tools` parameter.
// The result is cached on first call; the slice is sorted by tool name so
// the wire bytes are deterministic regardless of Add() order.
func (r *Registry) Wire() []deepseek.Tool {
	r.once.Do(func() {
		sorted := make([]Tool, len(r.tools))
		copy(sorted, r.tools)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })

		r.frozen = make([]deepseek.Tool, 0, len(sorted))
		for _, t := range sorted {
			r.frozen = append(r.frozen, deepseek.Tool{
				Type: "function",
				Function: deepseek.ToolFunction{
					Name:        t.Name(),
					Description: t.Description(),
					Parameters:  t.Schema(),
				},
			})
		}
	})
	return r.frozen
}

// Names returns registered tool names in sorted order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Errors surfaced when an LLM-generated tool call refers to a tool that is
// not in the registry. Distinguished from execution errors so the agent can
// feed back a clear hint.
var ErrUnknownTool = errors.New("unknown tool")

// UnmarshalStrict decodes raw into v with DisallowUnknownFields, the
// shape every tool's Execute uses for its argument parse. The reason
// it's strict (vs. encoding/json's default of silently dropping
// unknown fields):
//
// LLMs occasionally guess field names — "directory" instead of "path",
// "file" instead of "path", "query" instead of "task". With the loose
// default, those typos drop on the floor and surface as "required
// field X is empty", which doesn't point at the actual mistake. The
// model wastes a turn guessing what to change.
//
// With strict parsing the error becomes `json: unknown field "..."`,
// and we wrap it with the truncated raw input + the list of valid
// fields so the next call gets it right in one shot.
//
// validFields is informational only — used to build the error
// message; not enforced beyond the json struct tags on v.
func UnmarshalStrict(toolName string, raw json.RawMessage, v any, validFields ...string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: bad arguments: %v. Got: %s. Valid fields: %s",
			toolName, err, truncateArgs(string(raw), 200), strings.Join(validFields, ", "))
	}
	return nil
}

// MissingField is the standard "required field X is empty/missing"
// error used after a successful UnmarshalStrict. Same rationale as
// the strict parse: include the raw input + valid field list so the
// model has everything it needs to recover in one turn.
func MissingField(toolName, field string, raw json.RawMessage, validFields ...string) error {
	return fmt.Errorf("%s: %s is required. Got: %s. Valid fields: %s",
		toolName, field, truncateArgs(string(raw), 200), strings.Join(validFields, ", "))
}

func truncateArgs(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Dispatch runs a single tool call and returns its result (or error).
// Lookup-fail is wrapped with ErrUnknownTool so callers can distinguish.
func (r *Registry) Dispatch(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	t := r.Lookup(name)
	if t == nil {
		return "", fmt.Errorf("%w: %s (known: %v)", ErrUnknownTool, name, r.Names())
	}
	return t.Execute(ctx, raw)
}
