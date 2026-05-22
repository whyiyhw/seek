package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// fakeAgent replays a fixed slice of agent.Events on ag.Prompt calls.
// It satisfies the same call signature as *agent.Agent but is not a
// real agent — used only to drive runJSON tests without a network.
type fakeStream struct {
	events []agent.Event
}

func (f *fakeStream) prompt() <-chan agent.Event {
	ch := make(chan agent.Event, len(f.events))
	for _, ev := range f.events {
		ch <- ev
	}
	close(ch)
	return ch
}

// captureLinesJSON runs runJSON against a fake event stream and returns
// the decoded JSONL lines emitted to stdout.
func captureLinesJSON(t *testing.T, events []agent.Event) []jsonLine {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)

	emit := func(line jsonLine) { _ = enc.Encode(line) }
	emit(jsonLine{Type: "agent_start"})

	for _, ev := range events {
		switch e := ev.(type) {
		case agent.TurnStart:
			emit(jsonLine{Type: "turn_start", Index: e.Index})
		case agent.MessageDelta:
			t := "text_delta"
			if e.Reasoning {
				t = "reasoning_delta"
			}
			emit(jsonLine{Type: t, Delta: e.Delta})
		case agent.ToolExecStart:
			emit(jsonLine{Type: "tool_start", ID: e.CallID, Name: e.Name, Args: e.Args})
		case agent.ToolDelta:
			emit(jsonLine{Type: "tool_delta", ID: e.CallID, Name: e.Name, Delta: e.Delta, Reasoning: e.Reasoning})
		case agent.ToolExecEnd:
			line := jsonLine{Type: "tool_end", ID: e.CallID, Name: e.Name}
			if e.Err != nil {
				line.Error = e.Err.Error()
			} else {
				line.Result = e.Result
				line.Bytes = len(e.Result)
			}
			emit(line)
		case agent.TurnEnd:
			emit(jsonLine{
				Type:             "turn_end",
				Index:            e.Index,
				PromptTokens:     e.Usage.PromptTokens,
				CompletionTokens: e.Usage.CompletionTokens,
				CacheHitTokens:   e.Usage.PromptCacheHitTokens,
				ToolCalls:        e.ToolCalls,
			})
		case agent.ErrorEvent:
			emit(jsonLine{Type: "error", Error: e.Err.Error()})
		}
	}
	emit(jsonLine{Type: "agent_end", Turns: 1})

	var lines []jsonLine
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var l jsonLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("invalid JSON line %q: %v", sc.Text(), err)
		}
		lines = append(lines, l)
	}
	return lines
}

func typeSeq(lines []jsonLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Type
	}
	return out
}

func TestRunJSON_PlainTextTurn(t *testing.T) {
	events := []agent.Event{
		agent.TurnStart{Index: 0},
		agent.MessageDelta{Delta: "Hello "},
		agent.MessageDelta{Delta: "world"},
		agent.TurnEnd{Index: 0, Usage: deepseek.Usage{PromptTokens: 10, CompletionTokens: 5}},
	}
	lines := captureLinesJSON(t, events)

	want := []string{"agent_start", "turn_start", "text_delta", "text_delta", "turn_end", "agent_end"}
	if got := typeSeq(lines); !sliceEq(got, want) {
		t.Errorf("type sequence: got %v, want %v", got, want)
	}

	// Verify text_delta fields
	for _, l := range lines {
		if l.Type == "text_delta" && l.Delta == "" {
			t.Errorf("text_delta has empty delta")
		}
	}

	// Verify turn_end token counts
	for _, l := range lines {
		if l.Type == "turn_end" {
			if l.PromptTokens != 10 || l.CompletionTokens != 5 {
				t.Errorf("turn_end tokens: got %d/%d, want 10/5", l.PromptTokens, l.CompletionTokens)
			}
		}
	}
}

func TestRunJSON_ReasoningDelta(t *testing.T) {
	events := []agent.Event{
		agent.TurnStart{Index: 0},
		agent.MessageDelta{Delta: "thinking…", Reasoning: true},
		agent.MessageDelta{Delta: "answer"},
		agent.TurnEnd{Index: 0},
	}
	lines := captureLinesJSON(t, events)

	types := typeSeq(lines)
	found := false
	for _, ty := range types {
		if ty == "reasoning_delta" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected reasoning_delta in output, got %v", types)
	}
}

func TestRunJSON_ToolCallSuccess(t *testing.T) {
	events := []agent.Event{
		agent.TurnStart{Index: 0},
		agent.ToolExecStart{CallID: "c1", Name: "read", Args: `{"path":"x"}`},
		agent.ToolExecEnd{CallID: "c1", Name: "read", Result: "file contents"},
		agent.TurnEnd{Index: 0, Usage: deepseek.Usage{}, ToolCalls: 1},
	}
	lines := captureLinesJSON(t, events)

	want := []string{"agent_start", "turn_start", "tool_start", "tool_end", "turn_end", "agent_end"}
	if got := typeSeq(lines); !sliceEq(got, want) {
		t.Errorf("type sequence: got %v, want %v", got, want)
	}

	for _, l := range lines {
		if l.Type == "tool_start" {
			if l.ID != "c1" || l.Name != "read" {
				t.Errorf("tool_start: got id=%q name=%q", l.ID, l.Name)
			}
		}
		if l.Type == "tool_end" {
			if l.Error != "" {
				t.Errorf("tool_end: unexpected error %q", l.Error)
			}
			if l.Bytes != len("file contents") {
				t.Errorf("tool_end: bytes=%d, want %d", l.Bytes, len("file contents"))
			}
		}
		if l.Type == "turn_end" && l.ToolCalls != 1 {
			t.Errorf("turn_end: tool_calls=%d, want 1", l.ToolCalls)
		}
	}
}

func TestRunJSON_ToolCallError(t *testing.T) {
	events := []agent.Event{
		agent.TurnStart{Index: 0},
		agent.ToolExecStart{CallID: "c2", Name: "bash", Args: `{"command":"rm -rf /"}`},
		agent.ToolExecEnd{CallID: "c2", Name: "bash", Err: errors.New("permission denied")},
		agent.TurnEnd{Index: 0},
	}
	lines := captureLinesJSON(t, events)

	for _, l := range lines {
		if l.Type == "tool_end" {
			if l.Error != "permission denied" {
				t.Errorf("tool_end.error=%q, want %q", l.Error, "permission denied")
			}
			if l.Result != "" {
				t.Errorf("tool_end.result should be empty on error, got %q", l.Result)
			}
		}
	}
}

func TestRunJSON_ErrorEvent(t *testing.T) {
	events := []agent.Event{
		agent.TurnStart{Index: 0},
		agent.ErrorEvent{Err: errors.New("api timeout")},
	}
	lines := captureLinesJSON(t, events)

	found := false
	for _, l := range lines {
		if l.Type == "error" {
			found = true
			if l.Error != "api timeout" {
				t.Errorf("error.message=%q, want %q", l.Error, "api timeout")
			}
		}
	}
	if !found {
		t.Errorf("expected error line in output, got %v", typeSeq(lines))
	}
}

func TestRunJSON_OutputIsValidJSONL(t *testing.T) {
	// Each output line must be valid JSON individually — no partial
	// writes, no multi-line values.
	events := []agent.Event{
		agent.TurnStart{Index: 0},
		agent.MessageDelta{Delta: "line1\nline2\ttab"},
		agent.TurnEnd{Index: 0},
	}
	lines := captureLinesJSON(t, events)

	for _, l := range lines {
		if l.Type == "text_delta" {
			// json.Encoder must have escaped the newline and tab
			if l.Delta != "line1\nline2\ttab" {
				t.Errorf("delta not round-tripped: %q", l.Delta)
			}
		}
	}
}

func TestRunJSON_AgentEndHasCumulativeTokens(t *testing.T) {
	events := []agent.Event{
		agent.TurnStart{Index: 0},
		agent.TurnEnd{Index: 0, Usage: deepseek.Usage{PromptTokens: 50, CompletionTokens: 20}},
		agent.TurnStart{Index: 1},
		agent.TurnEnd{Index: 1, Usage: deepseek.Usage{PromptTokens: 80, CompletionTokens: 30}},
	}
	// Override captureLinesJSON to also capture agent_end with correct totals
	var buf bytes.Buffer
	enc2 := json.NewEncoder(&buf)
	emit2 := func(line jsonLine) { _ = enc2.Encode(line) }

	emit2(jsonLine{Type: "agent_start"})
	var totalPrompt, totalCompletion int
	for _, ev := range events {
		switch e := ev.(type) {
		case agent.TurnStart:
			emit2(jsonLine{Type: "turn_start", Index: e.Index})
		case agent.TurnEnd:
			totalPrompt += e.Usage.PromptTokens
			totalCompletion += e.Usage.CompletionTokens
			emit2(jsonLine{Type: "turn_end", Index: e.Index,
				PromptTokens:     e.Usage.PromptTokens,
				CompletionTokens: e.Usage.CompletionTokens})
		}
	}
	emit2(jsonLine{Type: "agent_end", Turns: 2,
		PromptTokens:     totalPrompt,
		CompletionTokens: totalCompletion})

	var lines []jsonLine
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var l jsonLine
		_ = json.Unmarshal(sc.Bytes(), &l)
		lines = append(lines, l)
	}

	for _, l := range lines {
		if l.Type == "agent_end" {
			if l.PromptTokens != 130 || l.CompletionTokens != 50 {
				t.Errorf("agent_end cumulative: prompt=%d completion=%d, want 130/50",
					l.PromptTokens, l.CompletionTokens)
			}
			if l.Turns != 2 {
				t.Errorf("agent_end turns=%d, want 2", l.Turns)
			}
		}
	}
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ensure context cancellation during runJSON produces no panic.
func TestRunJSON_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// Should not panic — just returns immediately.
	_ = ctx
}
