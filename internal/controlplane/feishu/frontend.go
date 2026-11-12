//go:build feishu

package feishu

// M6a build-tag-isolation placeholder. This file is the designated
// landing spot of the real Feishu frontend (M4: larkws Run with the
// SDK's built-in reconnect; M5: larkim Send with tenant_access_token
// caching — PRD D-F2/D-F3). Until then it carries the Fake that proves
// the D-F1 gate mechanically: this code is unreachable from the
// default build and only exists under `-tags feishu`.
//
// The real frontend will implement controlplane.Frontend (M1a, on
// drive/c_xcy7ff) and thereby ServeSource[controlplane.Inbound]
// structurally; the placeholder keeps the same Run shape against a
// local Msg so the fan-in proof runs without the M1a import.

import (
	"context"
	"fmt"
	"sync"
)

// Msg is the tagged placeholder for controlplane.Inbound (M1a): the
// minimal shape the serve fan-in needs. M4 replaces it wholesale with
// the real type; do not grow it.
type Msg struct {
	Conv string // "<frontend>:<native>" — built here as "feishu:<chat_id>"
	Text string
}

// Fake is the tagged-build placeholder frontend: a queue you stuff by
// hand, drained into the fan-in by Run. It exists to prove that tagged
// code participates in the serve lifecycle under `-tags feishu` and is
// absent from the default build; it never talks to Feishu.
type Fake struct {
	mu   sync.Mutex
	msgs []Msg
}

// New returns the placeholder frontend. Name is "feishu" — the ConvID
// prefix this frontend owns.
func New() *Fake { return &Fake{} }

// Name implements the Frontend/ServeSource port.
func (f *Fake) Name() string { return "feishu" }

// Queue stages one message for Run to push, addressed by the native
// chat id (the "feishu:" prefix is the frontend's own discipline).
func (f *Fake) Queue(native, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, Msg{Conv: "feishu:" + native, Text: text})
}

// Run pushes every staged message into the fan-in (ctx-aware, then
// parks until ctx ends — the transport-shaped lifecycle without the
// transport. The real M4 Run blocks in larkws Start(ctx) here.
func (f *Fake) Run(ctx context.Context, in chan<- Msg) error {
	f.mu.Lock()
	msgs := f.msgs
	f.msgs = nil
	f.mu.Unlock()
	for _, m := range msgs {
		select {
		case in <- m:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	<-ctx.Done()
	return nil
}

// Send is the M5 seam: not implemented before the larkim wiring. It
// fails loud rather than pretending, so nobody ships on a placeholder.
func (f *Fake) Send(_ context.Context, conv, text string) error {
	return fmt.Errorf("feishu: Send not implemented before M5 (conv %q, %d chars)", conv, len(text))
}
