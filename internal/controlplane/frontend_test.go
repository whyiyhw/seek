package controlplane

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeFrontend is the M1 stand-in for a real channel adapter: it
// pushes canned Inbounds through Run and records Sends. The M2/M3
// plane-core tests reuse this shape — a fake that satisfies the port
// with zero SDK is itself evidence the port is frontend-agnostic
// (PRD §6). The compile-time assertion below pins the contract.
type fakeFrontend struct {
	name     string
	inbounds []Inbound // pushed by Run, in order

	mu    sync.Mutex
	sent  []Reply          // recorded Send payloads, keyed by conv
	convs []ConvID         // Send call order
	errOn map[ConvID]error // optional per-conv Send failure
}

var _ Frontend = (*fakeFrontend)(nil)

func (f *fakeFrontend) Name() string { return f.name }

// Run pushes every canned inbound, then blocks until ctx is done —
// the real contract: Run returning early would tell the plane the
// frontend is down.
func (f *fakeFrontend) Run(ctx context.Context, in chan<- Inbound) error {
	for _, msg := range f.inbounds {
		select {
		case in <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeFrontend) Send(ctx context.Context, conv ConvID, r Reply) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errOn != nil {
		if err, ok := f.errOn[conv]; ok {
			return err
		}
	}
	f.convs = append(f.convs, conv)
	f.sent = append(f.sent, r)
	return nil
}

func (f *fakeFrontend) sends() []Reply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Reply(nil), f.sent...)
}

// TestFrontend_PortContract exercises the port the way the plane will:
// Run fans inbounds into the shared channel, the plane answers via
// Send, the fake records it. No SDK anywhere — frontend-agnosticism
// made executable.
func TestFrontend_PortContract(t *testing.T) {
	fe := &fakeFrontend{name: "fake", inbounds: []Inbound{
		{Conv: NewConvID("fake", "c1"), Text: "hello", DirectAt: true, EventID: "e1"},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan Inbound, 4)
	go func() {
		if err := fe.Run(ctx, in); err != nil && ctx.Err() == nil {
			t.Errorf("Run: %v", err)
		}
	}()
	defer cancel()

	var got Inbound
	select {
	case got = <-in:
	case <-time.After(2 * time.Second):
		t.Fatal("no inbound arrived via Run")
	}
	if got.Conv != NewConvID("fake", "c1") || got.Text != "hello" || !got.DirectAt {
		t.Fatalf("inbound round-trip mismatch: %+v", got)
	}

	if err := fe.Send(ctx, got.Conv, Reply{Text: "hi back"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if s := fe.sends(); len(s) != 1 || s[0].Text != "hi back" {
		t.Fatalf("fake recorded sends = %+v, want one 'hi back'", s)
	}

	// ctx cancellation must unblock Run (the plane shuts down through it).
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignored ctx cancellation")
	}
}

func TestNewConvID(t *testing.T) {
	tests := []struct {
		name, frontend, native, want string
	}{
		{"plain", "feishu", "oc_abc", "feishu:oc_abc"},
		{"native may contain colon", "feishu", "oc_a:b", "feishu:oc_a:b"},
		{"empty frontend", "", "oc", ""},
		{"empty native", "feishu", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewConvID(tt.frontend, tt.native); got != ConvID(tt.want) {
				t.Errorf("NewConvID(%q, %q) = %q, want %q", tt.frontend, tt.native, got, tt.want)
			}
		})
	}
}

func TestConvID_Split(t *testing.T) {
	// Malformed ids (empty part / no separator) yield "" from BOTH
	// accessors — that empty string is the validity signal Put checks.
	tests := []struct {
		id              ConvID
		wantFE, wantNat string
	}{
		{"feishu:oc_abc", "feishu", "oc_abc"},
		{"feishu:oc_a:b", "feishu", "oc_a:b"}, // first-separator split
		{"feishu:", "", ""},                   // empty native → malformed
		{":oc_abc", "", ""},                   // empty frontend → malformed
		{"oc_abc", "", ""},                    // no separator → malformed
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.id), func(t *testing.T) {
			if fe := tt.id.FrontendName(); fe != tt.wantFE {
				t.Errorf("FrontendName() = %q, want %q", fe, tt.wantFE)
			}
			if nat := tt.id.Native(); nat != tt.wantNat {
				t.Errorf("Native() = %q, want %q", nat, tt.wantNat)
			}
		})
	}
}

// TestConvID_CrossFrontendNoCollision pins the reason the prefix
// exists (D-C1): the same native chat id on two frontends must be two
// distinct conversation keys.
func TestConvID_CrossFrontendNoCollision(t *testing.T) {
	a := NewConvID("feishu", "oc_1")
	b := NewConvID("telegram", "oc_1")
	if a == b {
		t.Fatalf("conv ids collided across frontends: %q", a)
	}
	m := map[ConvID]int{a: 1, b: 2}
	if len(m) != 2 {
		t.Fatalf("map keyed by ConvID collapsed entries: %v", m)
	}
	if a.FrontendName() != "feishu" || b.FrontendName() != "telegram" {
		t.Fatalf("prefix routing lookup broken: %q / %q", a.FrontendName(), b.FrontendName())
	}
}
