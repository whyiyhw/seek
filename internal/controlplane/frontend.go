// Package controlplane is seek's remote control plane: a long-running
// serve process that makes seek reachable from any chat channel and
// routes each conversation to a bound project directory.
//
// The plane is frontend-agnostic. Everything in this package talks to
// the Frontend port only; concrete channels (Feishu is the first) live
// behind their own build tags so the default seek binary stays stdlib-
// only. Design + decisions: docs/prd/feature-remote-control-plane.md.
//
// M1 scope (this file): the type layer — Frontend / Inbound / Reply /
// ConvID. Routing (M2), the seek subprocess executor (M3) and the
// serve lifecycle (M6) layer on top; pkg/agent and the agent loop are
// never touched — the plane drives seek by exec'ing it, nothing else.
package controlplane

import "context"

// Frontend is the pluggable ingress of the remote control plane. It
// turns one external channel (chat app / web / voice) into a stream of
// normalised inbound messages and delivers the plane's replies back to
// the conversation they came from. The plane core (routing, bindings,
// execution) knows frontends only through this port; Feishu is the
// first implementation (D-C1).
//
// Run and Send are deliberately separate methods: chat channels are
// naturally half-duplex at the transport level (Feishu receives over a
// long-lived WS but sends via HTTPS REST; Telegram long-polls but sends
// via the bot API), so the port must not assume one bidirectional
// connection (§1.3).
//
// Implementations must be safe for concurrent use: the plane calls Run
// from one goroutine per frontend but Send from executor goroutines.
type Frontend interface {
	// Name returns the stable channel identifier ("feishu"), used in
	// logs, config and as the ConvID prefix. Must be non-empty and
	// constant for the lifetime of the frontend.
	Name() string

	// Run blocks receiving events for the channel, normalising each
	// one into an Inbound and pushing it down `in`. It owns its own
	// transport including reconnects; it returns only on ctx.Done or
	// terminal transport failure. Returning an error means the plane
	// should treat the frontend as down (and may rebuild it).
	Run(ctx context.Context, in chan<- Inbound) error

	// Send delivers one reply back to the conversation identified by
	// conv. Best-effort with a single retry — the same posture as
	// internal/routines webhook delivery. A failed Send drops the
	// reply (the next inbound still works); it must never panic the
	// executor.
	Send(ctx context.Context, conv ConvID, r Reply) error
}

// ConvID is the frontend-agnostic conversation key:
// "<frontend>:<native-id>", e.g. "feishu:oc_abc". The frontend prefix
// keeps bindings and serialisation queues from colliding across
// channels — the same native chat id on two frontends is two distinct
// conversations (D-C1). Construct via NewConvID; parse via FrontendName
// / Native.
type ConvID string

// convSep separates the frontend name from the channel-native id.
const convSep = ":"

// NewConvID builds "<frontend>:<native>". Both parts must be non-empty;
// an empty part yields "" so an invalid id cannot silently become a
// binding key (callers treat "" as absent).
func NewConvID(frontend, native string) ConvID {
	if frontend == "" || native == "" {
		return ""
	}
	return ConvID(frontend + convSep + native)
}

// FrontendName returns the frontend prefix of the id, or "" when the id
// is malformed (missing or empty prefix — e.g. ":oc_1", "oc_1", "").
func (c ConvID) FrontendName() string {
	fe, _, ok := c.split()
	if !ok {
		return ""
	}
	return fe
}

// Native returns the channel-native conversation id (everything after
// the first separator — native ids may themselves contain ':'), or ""
// when the id is malformed.
func (c ConvID) Native() string {
	_, native, ok := c.split()
	if !ok {
		return ""
	}
	return native
}

// split parses "<frontend>:<native>" and reports whether both parts
// are present. First-separator split: the native id may contain ':' but
// the frontend name may not.
func (c ConvID) split() (frontend, native string, ok bool) {
	if c == "" {
		return "", "", false
	}
	for i := 0; i < len(c); i++ {
		if c[i] == convSep[0] {
			fe, nat := string(c[:i]), string(c[i+1:])
			if fe == "" || nat == "" {
				return "", "", false
			}
			return fe, nat, true
		}
	}
	return "", "", false
}

// Inbound is one normalised user message pushed by a Frontend's Run.
// All plane routing decisions key off Conv; the other fields are
// advisory metadata the plane consumes (EventID for dedup, DirectAt
// for the trigger filter) or passes through for future use (Sender —
// unused in single-user v1, D-C5).
type Inbound struct {
	Conv     ConvID // frontend-agnostic conversation key
	Text     string // user message (command or prompt)
	Sender   string // opaque sender id (single-user v1: unused)
	EventID  string // channel-native event id, dedup key
	DirectAt bool   // should trigger: p2p true / group @bot true / group chatter false
}

// Reply is the plane's answer to one Inbound, delivered via
// Frontend.Send. v1 is plain text; the frontend handles chunking.
// Rich cards / streaming updates are future work (§8).
type Reply struct {
	Text string
}
