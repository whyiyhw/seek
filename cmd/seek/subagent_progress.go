package main

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/acp"
)

// Subagent progress bridge (paseo / ACP).
//
// A running subagent emits rich events (MessageDelta, ToolExecStart,
// ToolDelta) that the production Runner otherwise discards — it only keeps
// TurnEnd / AgentEnd / ErrorEvent for accounting. The ACP lane wants those
// events live: paseo renders agent_thought_chunk into a collapsible reasoning
// region, which is the ONLY standard session/update variant it will show for
// "process text" (its subagent cards ride a private OMP runtime channel that
// an extends:"acp" provider cannot reach).
//
// The bridge is a per-turn context value, NOT a new StreamingTool and NOT
// agent.EmitEvent:
//   - agent.Tool is marked ReadOnlyTool and dispatched concurrently; making
//     it a StreamingTool would break TestNoToolStreamsAndIsReadOnly and the
//     TUI's single-writer live region.
//   - EmitEvent reads Agent.currentEvents, which is set/cleared by the Prompt
//     goroutine — calling it from a concurrently-dispatched tool would race.
//   - A context sink rides the exact ctx the tool passes to Manager.Spawn;
//     for sync spawns runCtx derives from it, for background spawns runCtx
//     derives from SessionCtx (no sink) — so background and non-ACP lanes
//     are byte-for-byte unchanged.
//
// The sink writes straight to the ACP session/update sender (which serialises
// under the server mutex), so concurrent siblings are safe; each line is
// prefixed with the sub-sid so interleaved progress stays attributable.

// subagentProgressCtxKey is a private context key — no collision with other
// packages' values.
type subagentProgressCtxKey struct{}

// subagentProgress is one condensed progress line from a running subagent.
type subagentProgress struct {
	SubSid string
	Text   string
}

// subagentProgressSink receives progress lines for the turn that installed it.
// A nil sink means "nobody is listening" — every bridge method becomes a
// no-op, so callers never need a nil check of their own.
type subagentProgressSink func(subagentProgress)

// withSubagentProgress installs a sink for one ACP turn. Callers pass the
// returned ctx to Agent.PromptFiles; it flows unchanged through dispatchTool
// into the agent tool and from there into the sync subagent's runCtx.
func withSubagentProgress(ctx context.Context, sink subagentProgressSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, subagentProgressCtxKey{}, sink)
}

// subagentProgressFrom reads the sink installed for this turn, or nil.
func subagentProgressFrom(ctx context.Context) subagentProgressSink {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(subagentProgressCtxKey{}).(subagentProgressSink)
	return sink
}

// newACPProgressSink builds the ACP-facing progress sink for one turn.
// turnCtx is captured (not the derived ctx) so a cancelled turn drops late
// flushes — no frame may land after the termination/response frames.
// sendU serialises writes under the server mutex, so concurrent sibling
// spawns cannot interleave mid-frame.
func newACPProgressSink(turnCtx context.Context, sessionID string, sendU func(acp.SessionUpdate)) subagentProgressSink {
	return func(sp subagentProgress) {
		if turnCtx.Err() != nil {
			return
		}
		sendU(acp.SessionUpdate{SessionID: sessionID, Update: map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": "[" + sp.SubSid + "] " + sp.Text},
		}})
	}
}

// subagentProgressPump coalesces token-level deltas into readable lines.
// One ACP frame per model token would flood the wire and the client's
// reasoning region; flush on newline, on a size threshold, and at tool
// boundaries / run end instead.
type subagentProgressPump struct {
	sink   subagentProgressSink
	subSid string
	buf    strings.Builder
}

// subagentProgressChunk is the soft flush threshold in bytes. Deltas are
// UTF-8 from the model; a cut mid-rune would only show as a replacement
// char in the client, so flush prefers a newline before this boundary.
const subagentProgressChunk = 160

// newSubagentProgressPump returns a pump bound to ctx's sink. A nil sink
// yields a pump whose methods are all no-ops.
func newSubagentProgressPump(ctx context.Context, subSid string) *subagentProgressPump {
	return &subagentProgressPump{sink: subagentProgressFrom(ctx), subSid: subSid}
}

// write accumulates a streaming delta, flushing complete lines as they form.
func (p *subagentProgressPump) write(delta string) {
	if p == nil || p.sink == nil || delta == "" {
		return
	}
	p.buf.WriteString(delta)
	for {
		b := p.buf.String()
		if len(b) < subagentProgressChunk && !strings.Contains(b, "\n") {
			return
		}
		cut := subagentProgressChunk
		if i := strings.IndexByte(b, '\n'); i >= 0 && i+1 < cut {
			cut = i + 1
		}
		if cut > len(b) {
			cut = len(b)
		}
		if cut <= 0 {
			return
		}
		p.flushN(cut)
	}
}

// note emits a discrete event marker (e.g. a tool call) on its own line,
// flushing any pending delta text before it so ordering stays honest.
func (p *subagentProgressPump) note(marker string) {
	if p == nil || p.sink == nil || marker == "" {
		return
	}
	p.flush()
	p.sink(subagentProgress{SubSid: p.subSid, Text: marker})
}

// flush sends whatever text is buffered, if any.
func (p *subagentProgressPump) flush() {
	if p == nil || p.sink == nil || p.buf.Len() == 0 {
		return
	}
	p.flushN(p.buf.Len())
}

func (p *subagentProgressPump) flushN(n int) {
	b := p.buf.String()
	if n > len(b) {
		n = len(b)
	}
	// Back up to a rune boundary on a THRESHOLD cut (n < len(b)): a model
	// delta can be Chinese (3-byte runes), and cutting at an arbitrary byte
	// would split one mid-sequence into a U+FFFD on the wire. b[n] is the
	// first byte left in the buffer; backing up until it starts a rune keeps
	// the emitted prefix whole. A flush at the buffer end (n == len(b)) is
	// already whole — every JSON delta is valid UTF-8 on its own — so this
	// loop deliberately does not run there.
	for n > 1 && n < len(b) && !utf8.RuneStart(b[n]) {
		n--
	}
	line := strings.TrimSpace(b[:n])
	p.buf.Reset()
	p.buf.WriteString(b[n:])
	if line != "" {
		p.sink(subagentProgress{SubSid: p.subSid, Text: line})
	}
}
