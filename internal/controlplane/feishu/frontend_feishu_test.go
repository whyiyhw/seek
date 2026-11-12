//go:build feishu

package feishu

// Tagged-side proof of the D-F1 isolation: this file compiles ONLY
// under `-tags feishu` (run: go test -tags feishu ./internal/
// controlplane/feishu/). The default-side counterproof — that this
// package's default build is just doc.go — lives in internal/
// controlplane/buildtags_test.go and runs in ordinary CI.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/controlplane"
)

// The port seam, at compile time: whatever lands here in M4/M5 must
// remain a ServeSource — the fan-in must not need adapter code.
var _ controlplane.ServeSource[Msg] = New()

// localSource is a second, non-feishu source for the mixed fan-in
// test: same Run shape, different name and ConvID prefix.
type localSource struct {
	name string
	msgs []Msg
	done chan struct{}
}

func (l *localSource) Name() string { return l.name }

func (l *localSource) Run(ctx context.Context, in chan<- Msg) error {
	for _, m := range l.msgs {
		select {
		case in <- m:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	close(l.done)
	<-ctx.Done()
	return nil
}

// TestFake_FanInWithOtherFrontend drives the TAGGED placeholder
// through the real serve fan-in next to another source — same native
// chat id on both, prefix isolation must hold exactly as with the
// untagged fakes in serve_test.go.
func TestFake_FanInWithOtherFrontend(t *testing.T) {
	const per = 5
	fe := New()
	for i := 0; i < per; i++ {
		fe.Queue("chat-1", strings.Repeat("m", i+1))
	}
	other := &localSource{name: "other", done: make(chan struct{})}
	for i := 0; i < per; i++ {
		other.msgs = append(other.msgs, Msg{Conv: "other:chat-1", Text: strings.Repeat("x", i+1)})
	}

	var mu sync.Mutex
	var got []Msg
	handle := func(_ context.Context, m Msg) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- controlplane.Serve(ctx, []controlplane.ServeSource[Msg]{fe, other}, handle, controlplane.ServeConfig{})
	}()

	// The Fake has no pushed-signal hook; wait for the other source's
	// completion signal, then for ALL messages to be dispatched — only
	// then cancel, so no push races the shutdown and loses a message.
	<-other.done
	poll := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2*per {
			break
		}
		if time.Now().After(poll) {
			t.Fatalf("fan-in never dispatched everything: %d of %d", n, 2*per)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown expected, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2*per {
		t.Fatalf("fan-in lost messages: got %d of %d", len(got), 2*per)
	}
	feishuCount, otherCount := 0, 0
	for _, m := range got {
		switch {
		case strings.HasPrefix(m.Conv, "feishu:chat-1"):
			feishuCount++
		case strings.HasPrefix(m.Conv, "other:chat-1"):
			otherCount++
		default:
			t.Errorf("unexpected Conv %q escaped the prefix discipline", m.Conv)
		}
	}
	if feishuCount != per || otherCount != per {
		t.Fatalf("crosstalk or loss: feishu=%d other=%d, want %d each", feishuCount, otherCount, per)
	}
}
