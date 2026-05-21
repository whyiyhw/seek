// Package tools is the in-process tool registry consumed by pkg/agent.
//
// Tools are described once at startup; their wire-format JSON schema is
// frozen on first read (sync.Once) to keep the byte stream stable across
// turns — DeepSeek's prefix cache only hits when the prompt's byte prefix
// is identical, so any non-determinism in tool serialisation kills hit rate
// (PRD §4.8.1).
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

// Registry is the set of tools available to an Agent. Build it once at
// startup; it is safe for concurrent reads after the first Wire() call.
type Registry struct {
	tools []Tool
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

// Dispatch runs a single tool call and returns its result (or error).
// Lookup-fail is wrapped with ErrUnknownTool so callers can distinguish.
func (r *Registry) Dispatch(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	t := r.Lookup(name)
	if t == nil {
		return "", fmt.Errorf("%w: %s (known: %v)", ErrUnknownTool, name, r.Names())
	}
	return t.Execute(ctx, raw)
}
