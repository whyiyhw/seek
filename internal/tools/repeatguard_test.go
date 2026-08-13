package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// rgTool is a plain Tool: no optional interfaces.
type rgTool struct {
	name    string
	result  string
	err     error
	calls   int
	lastRaw string
	mu      sync.Mutex
}

func (f *rgTool) Name() string            { return f.name }
func (f *rgTool) Description() string     { return "fake" }
func (f *rgTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *rgTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	f.mu.Lock()
	f.calls++
	f.lastRaw = string(raw)
	f.mu.Unlock()
	return f.result, f.err
}

type rgReadOnly struct{ *rgTool }

func (rgReadOnly) ReadOnly() bool { return true }

type rgStreaming struct{ *rgTool }

func (f rgStreaming) ExecuteStream(_ context.Context, raw json.RawMessage, push func(StreamDelta) error) (string, error) {
	_ = push(StreamDelta{Delta: "chunk"})
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.result, f.err
}

type rgStreamReadOnly struct{ rgStreaming }

func (rgStreamReadOnly) ReadOnly() bool { return true }

func rgExec(t *testing.T, tool Tool, args string) (string, error) {
	t.Helper()
	return tool.Execute(context.Background(), json.RawMessage(args))
}

func TestRepeatGuard_QuietBelowThreshold(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "bash", result: "output"}, g)

	for i := 1; i <= 2; i++ {
		out, err := rgExec(t, tool, `{"command":"go build"}`)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "repeat-guard") {
			t.Errorf("call %d: got a reminder before the threshold: %q", i, out)
		}
	}
}

func TestRepeatGuard_FiresAtEachThreshold(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "bash", result: "output"}, g)

	var fired []int
	for i := 1; i <= 9; i++ {
		out, err := rgExec(t, tool, `{"command":"go build"}`)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "repeat-guard") {
			fired = append(fired, i)
		}
	}
	want := []int{3, 5, 8}
	if len(fired) != len(want) {
		t.Fatalf("reminders fired on calls %v, want %v", fired, want)
	}
	for i := range want {
		if fired[i] != want[i] {
			t.Fatalf("reminders fired on calls %v, want %v", fired, want)
		}
	}
}

func TestRepeatGuard_EscalatesWording(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "bash", result: "output"}, g)

	var third, eighth string
	for i := 1; i <= 8; i++ {
		out, _ := rgExec(t, tool, `{"command":"go build"}`)
		switch i {
		case 3:
			third = out
		case 8:
			eighth = out
		}
	}
	if !strings.Contains(third, "change the arguments") && !strings.Contains(third, "change the arguments or approach") {
		t.Errorf("3rd-call reminder should be gentle guidance, got: %q", third)
	}
	if !strings.Contains(eighth, "stuck in a loop") {
		t.Errorf("8th-call reminder should be blunt, got: %q", eighth)
	}
}

func TestRepeatGuard_DifferentArgsCountSeparately(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "read", result: "contents"}, g)

	// Reading twelve different files is healthy work, not a loop.
	for i := 0; i < 12; i++ {
		out, err := rgExec(t, tool, `{"path":"file`+string(rune('a'+i))+`.go"}`)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "repeat-guard") {
			t.Fatalf("distinct arguments triggered a repeat reminder on call %d: %q", i, out)
		}
	}
}

func TestRepeatGuard_KeyOrderDoesNotDefeatIt(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "grep", result: "hit"}, g)

	// Same call, three different key orderings — the model does not emit
	// stable key order, so a byte-comparison key would never fire.
	orders := []string{
		`{"pattern":"foo","path":"."}`,
		`{"path":".","pattern":"foo"}`,
		`{"pattern":"foo","path":"."}`,
	}
	var last string
	for _, o := range orders {
		last, _ = rgExec(t, tool, o)
	}
	if !strings.Contains(last, "repeat-guard") {
		t.Errorf("reordered keys defeated the guard; got: %q", last)
	}
}

func TestRepeatGuard_WhitespaceDoesNotDefeatIt(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "grep", result: "hit"}, g)

	for _, o := range []string{
		`{"pattern":"foo"}`,
		`{ "pattern" : "foo" }`,
		"{\n  \"pattern\": \"foo\"\n}",
	} {
		if _, err := rgExec(t, tool, o); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := rgExec(t, tool, `{"pattern":"foo"}`)
	_ = out
	// 3rd call already fired; assert by re-running to 5 and checking.
	out, _ = rgExec(t, tool, `{"pattern":"foo"}`)
	if !strings.Contains(out, "repeat-guard") {
		t.Errorf("whitespace variations defeated the guard; got: %q", out)
	}
}

// TestRepeatGuard_ReminderSurvivesOnErrorPath is the load-bearing case:
// the archetypal loop is a command that keeps FAILING, and pkg/agent's
// buildToolResultMsg discards the result string whenever the error is
// non-nil. A reminder attached only to the result would vanish exactly
// when it is needed.
func TestRepeatGuard_ReminderSurvivesOnErrorPath(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "bash", err: errors.New("exit status 1")}, g)

	var lastErr error
	for i := 1; i <= 3; i++ {
		_, lastErr = rgExec(t, tool, `{"command":"go build"}`)
	}
	if lastErr == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(lastErr.Error(), "repeat-guard") {
		t.Errorf("reminder missing from the error path: %q", lastErr)
	}
	if !strings.Contains(lastErr.Error(), "exit status 1") {
		t.Errorf("original error text lost: %q", lastErr)
	}
}

func TestRepeatGuard_ErrorWrappingPreservesErrorsIs(t *testing.T) {
	sentinel := errors.New("denied")
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "write", err: sentinel}, g)

	var lastErr error
	for i := 1; i <= 3; i++ {
		_, lastErr = rgExec(t, tool, `{"path":"x"}`)
	}
	if !errors.Is(lastErr, sentinel) {
		t.Errorf("errors.Is broken by the guard's wrapping: %v", lastErr)
	}
}

// TestRepeatGuard_ErrorPrefixStillMatches guards the wire-format
// convention: seek parses some result/error strings by prefix, so the
// reminder must be appended AFTER the original text, never spliced in.
func TestRepeatGuard_ErrorPrefixStillMatches(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "bash", err: errors.New("[denied] no")}, g)

	var lastErr error
	for i := 1; i <= 3; i++ {
		_, lastErr = rgExec(t, tool, `{"command":"x"}`)
	}
	if !strings.HasPrefix(lastErr.Error(), "[denied]") {
		t.Errorf("reminder broke prefix matching: %q", lastErr)
	}
}

func TestRepeatGuard_PreviewIsTruncated(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "write", result: "ok"}, g)

	big := `{"content":"` + strings.Repeat("x", 4000) + `"}`
	var out string
	for i := 1; i <= 3; i++ {
		out, _ = rgExec(t, tool, big)
	}
	if !strings.Contains(out, "truncated") {
		t.Error("large argument blob was not truncated in the reminder")
	}
	if len(out) > 2000 {
		t.Errorf("reminder echoed %d bytes back at the model; should be bounded", len(out))
	}
}

func TestRepeatGuard_EmptyResultStillCarriesReminder(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "memory_observe", result: ""}, g)

	var out string
	for i := 1; i <= 3; i++ {
		out, _ = rgExec(t, tool, `{"note":"x"}`)
	}
	if !strings.Contains(out, "repeat-guard") {
		t.Errorf("reminder lost on a tool that returns empty by design: %q", out)
	}
}

func TestRepeatGuard_MalformedArgsStillCounted(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "bash", result: "ok"}, g)

	var out string
	for i := 1; i <= 3; i++ {
		out, _ = rgExec(t, tool, `{not json`)
	}
	if !strings.Contains(out, "repeat-guard") {
		t.Errorf("malformed arguments escaped loop detection: %q", out)
	}
}

func TestRepeatGuard_NilGuardIsPassthrough(t *testing.T) {
	inner := &rgTool{name: "bash", result: "ok"}
	if got := WithRepeatGuard(inner, nil); got != Tool(inner) {
		t.Error("WithRepeatGuard(t, nil) should return the tool unchanged")
	}
}

// The interface-preservation tests below are the reason WithRepeatGuard
// is four types. Losing ReadOnlyTool turns parallel read batches
// sequential; losing StreamingTool kills live output — both silent.
func TestWithRepeatGuard_PreservesReadOnly(t *testing.T) {
	wrapped := WithRepeatGuard(rgReadOnly{&rgTool{name: "read"}}, NewRepeatGuard())
	ro, ok := wrapped.(ReadOnlyTool)
	if !ok {
		t.Fatal("wrapped tool no longer implements ReadOnlyTool — parallel dispatch would silently stop")
	}
	if !ro.ReadOnly() {
		t.Error("ReadOnly() = false, want true")
	}
}

func TestWithRepeatGuard_PreservesStreaming(t *testing.T) {
	wrapped := WithRepeatGuard(rgStreaming{&rgTool{name: "think", result: "done"}}, NewRepeatGuard())
	st, ok := wrapped.(StreamingTool)
	if !ok {
		t.Fatal("wrapped tool no longer implements StreamingTool — live output would silently stop")
	}
	var got []string
	out, err := st.ExecuteStream(context.Background(), json.RawMessage(`{}`), func(d StreamDelta) error {
		got = append(got, d.Delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "chunk" {
		t.Errorf("deltas = %v, want [chunk]", got)
	}
	if out != "done" {
		t.Errorf("result = %q, want done", out)
	}
}

func TestWithRepeatGuard_PreservesBothInterfaces(t *testing.T) {
	wrapped := WithRepeatGuard(
		rgStreamReadOnly{rgStreaming{&rgTool{name: "agent", result: "done"}}},
		NewRepeatGuard())
	if _, ok := wrapped.(ReadOnlyTool); !ok {
		t.Error("lost ReadOnlyTool")
	}
	if _, ok := wrapped.(StreamingTool); !ok {
		t.Error("lost StreamingTool")
	}
}

func TestWithRepeatGuard_PlainToolGainsNoInterfaces(t *testing.T) {
	// A plain tool must NOT accidentally become read-only: that would
	// hand write/bash to the concurrent dispatch batch.
	wrapped := WithRepeatGuard(&rgTool{name: "write"}, NewRepeatGuard())
	if _, ok := wrapped.(ReadOnlyTool); ok {
		t.Error("plain tool wrongly reports ReadOnlyTool — write would be dispatched concurrently")
	}
	if _, ok := wrapped.(StreamingTool); ok {
		t.Error("plain tool wrongly reports StreamingTool")
	}
}

func TestWithRepeatGuard_ForwardsIdentity(t *testing.T) {
	inner := &rgTool{name: "bash"}
	wrapped := WithRepeatGuard(inner, NewRepeatGuard())
	if wrapped.Name() != "bash" || wrapped.Description() != "fake" {
		t.Error("decorator did not forward Name/Description")
	}
	if string(wrapped.Schema()) != `{"type":"object"}` {
		t.Errorf("decorator did not forward Schema: %s", wrapped.Schema())
	}
}

func TestRepeatGuard_StreamingPathCounts(t *testing.T) {
	g := NewRepeatGuard()
	wrapped := WithRepeatGuard(rgStreaming{&rgTool{name: "think", result: "done"}}, g)
	st := wrapped.(StreamingTool)

	var out string
	var err error
	for i := 1; i <= 3; i++ {
		out, err = st.ExecuteStream(context.Background(), json.RawMessage(`{"q":"same"}`),
			func(StreamDelta) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(out, "repeat-guard") {
		t.Errorf("streaming path did not count repeats: %q", out)
	}
}

func TestRepeatGuard_ConcurrentUse(t *testing.T) {
	g := NewRepeatGuard()
	tool := WithRepeatGuard(&rgTool{name: "read", result: "ok"}, g)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = rgExec(t, tool, `{"path":"same.go"}`)
		}()
	}
	wg.Wait()

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.counts) != 1 {
		t.Fatalf("counts has %d keys, want 1", len(g.counts))
	}
	for _, n := range g.counts {
		if n != 50 {
			t.Errorf("count = %d, want 50 (lost increments under concurrency)", n)
		}
	}
}
