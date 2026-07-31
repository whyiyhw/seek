package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeTool is a minimal Tool whose behaviour is fully controlled by the
// test — no filesystem, no network, no provider calls.
type fakeTool struct {
	name   string
	desc   string
	schema json.RawMessage
	fn     func(ctx context.Context, raw json.RawMessage) (string, error)
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return f.desc }
func (f *fakeTool) Schema() json.RawMessage {
	if f.schema != nil {
		return f.schema
	}
	return json.RawMessage(`{"type":"object"}`)
}
func (f *fakeTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if f.fn != nil {
		return f.fn(ctx, raw)
	}
	return "ok:" + f.name, nil
}

// fakeStreamingTool also implements StreamingTool, the opt-in extension
// pkg/agent's dispatchTool type-asserts for (agent.go:915).
type fakeStreamingTool struct {
	fakeTool
	chunks []StreamDelta
}

func (s *fakeStreamingTool) ExecuteStream(ctx context.Context, raw json.RawMessage, push func(StreamDelta) error) (string, error) {
	for _, d := range s.chunks {
		if err := push(d); err != nil {
			return "", err
		}
	}
	return "streamed:" + s.name, nil
}

// fakeReadOnlyTool also implements ReadOnlyTool, the marker pkg/agent's
// allReadOnly type-asserts for before parallel dispatch (agent.go:996).
type fakeReadOnlyTool struct {
	fakeTool
}

func (r *fakeReadOnlyTool) ReadOnly() bool { return true }

// Compile-time contract checks: the agent relies on these assertions
// holding for every tool it dispatches.
var (
	_ Tool          = (*fakeTool)(nil)
	_ StreamingTool = (*fakeStreamingTool)(nil)
	_ ReadOnlyTool  = (*fakeReadOnlyTool)(nil)
)

func TestAddAndLookup(t *testing.T) {
	t.Parallel()
	reg := New()
	a := &fakeTool{name: "alpha", desc: "first tool"}
	b := &fakeTool{name: "beta", desc: "second tool"}
	reg.Add(a).Add(b) // Add returns *Registry for chaining

	if got := reg.Lookup("alpha"); got != a {
		t.Errorf("Lookup(alpha) = %v, want the registered tool", got)
	}
	if got := reg.Lookup("beta"); got != b {
		t.Errorf("Lookup(beta) = %v, want the registered tool", got)
	}
	if got := reg.Lookup("gamma"); got != nil {
		t.Errorf("Lookup(gamma) = %v, want nil for unregistered name", got)
	}
}

func TestAddDuplicatePanics(t *testing.T) {
	t.Parallel()
	reg := New()
	reg.Add(&fakeTool{name: "dup"})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Add with duplicate name did not panic")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "duplicate tool name dup") {
			t.Errorf("panic message = %v, want it to name the duplicate tool", r)
		}
	}()
	reg.Add(&fakeTool{name: "dup"})
}

func TestWire_SortedAndDeterministic(t *testing.T) {
	t.Parallel()
	// Register in reverse alphabetical order — Wire must sort.
	reg := New()
	reg.Add(&fakeTool{name: "zeta", desc: "z desc", schema: json.RawMessage(`{"type":"object","properties":{"z":{"type":"string"}}}`)})
	reg.Add(&fakeTool{name: "alpha", desc: "a desc", schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)})
	reg.Add(&fakeTool{name: "mid", desc: "m desc"})

	wire := reg.Wire()
	if len(wire) != 3 {
		t.Fatalf("Wire() len = %d, want 3", len(wire))
	}
	got := []string{wire[0].Function.Name, wire[1].Function.Name, wire[2].Function.Name}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Wire() order = %v, want sorted %v", got, want)
	}
	for _, tw := range wire {
		if tw.Type != "function" {
			t.Errorf("Wire() type for %s = %q, want \"function\"", tw.Function.Name, tw.Type)
		}
		if tw.Function.Description == "" {
			t.Errorf("Wire() description for %s is empty", tw.Function.Name)
		}
		if len(tw.Function.Parameters) == 0 {
			t.Errorf("Wire() parameters for %s are empty", tw.Function.Name)
		}
	}

	// Repeated calls must return byte-identical output (prefix-cache
	// contract: the prompt prefix must not vary across turns).
	first, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(reg.Wire())
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(first, second) {
		t.Error("Wire() output differs between calls — must be deterministic")
	}

	// Two registries with the same tools in different Add order must
	// produce identical wire bytes.
	other := New()
	other.Add(&fakeTool{name: "mid", desc: "m desc"})
	other.Add(&fakeTool{name: "zeta", desc: "z desc", schema: json.RawMessage(`{"type":"object","properties":{"z":{"type":"string"}}}`)})
	other.Add(&fakeTool{name: "alpha", desc: "a desc", schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)})
	third, err := json.Marshal(other.Wire())
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(first, third) {
		t.Error("identical tools in different Add order produced different wire bytes")
	}
}

// TestWire_FrozenAfterFirstCall pins the caching semantics: Wire() snapshots
// the registry on first call. A tool added later must NOT appear — the agent
// sends Wire()'s output in the request, and a changing schema would break
// the prefix cache. If this behaviour ever needs to change, the cache
// contract in tool.go's doc comment must change with it.
func TestWire_FrozenAfterFirstCall(t *testing.T) {
	t.Parallel()
	reg := New()
	reg.Add(&fakeTool{name: "early"})
	reg.Wire()
	reg.Add(&fakeTool{name: "late"})

	for _, tw := range reg.Wire() {
		if tw.Function.Name == "late" {
			t.Error("tool added after first Wire() call leaked into cached output")
		}
	}
	if len(reg.Wire()) != 1 {
		t.Errorf("Wire() len = %d after late Add, want 1 (frozen snapshot)", len(reg.Wire()))
	}
}

func TestNames_Sorted(t *testing.T) {
	t.Parallel()
	reg := New()
	reg.Add(&fakeTool{name: "zeta"})
	reg.Add(&fakeTool{name: "alpha"})
	got := reg.Names()
	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}

	if got := New().Names(); len(got) != 0 {
		t.Errorf("empty registry Names() = %v, want empty", got)
	}
}

func TestDispatch(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"path":"x.go"}`)
	var gotRaw json.RawMessage
	reg := New()
	reg.Add(&fakeTool{
		name: "read",
		fn: func(ctx context.Context, r json.RawMessage) (string, error) {
			gotRaw = r
			return "contents", nil
		},
	})

	res, err := reg.Dispatch(context.Background(), "read", raw)
	if err != nil {
		t.Fatalf("Dispatch() unexpected error: %v", err)
	}
	if res != "contents" {
		t.Errorf("Dispatch() result = %q, want %q", res, "contents")
	}
	if !bytesEqual(gotRaw, raw) {
		t.Errorf("tool received raw = %s, want the exact bytes %s", gotRaw, raw)
	}

	// Execution errors pass through untouched.
	boom := errors.New("boom")
	reg.Add(&fakeTool{
		name: "fail",
		fn: func(ctx context.Context, r json.RawMessage) (string, error) {
			return "", boom
		},
	})
	if _, err := reg.Dispatch(context.Background(), "fail", raw); !errors.Is(err, boom) {
		t.Errorf("Dispatch() error = %v, want the tool's own error", err)
	}
}

func TestDispatch_UnknownTool(t *testing.T) {
	t.Parallel()
	reg := New()
	reg.Add(&fakeTool{name: "read"})
	reg.Add(&fakeTool{name: "write"})

	_, err := reg.Dispatch(context.Background(), "rm", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Dispatch() on unknown tool returned nil error")
	}
	if !errors.Is(err, ErrUnknownTool) {
		t.Errorf("Dispatch() error = %v, want errors.Is(err, ErrUnknownTool)", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "rm") {
		t.Errorf("error %q does not name the requested tool", msg)
	}
	if !strings.Contains(msg, "known:") || !strings.Contains(msg, "read") || !strings.Contains(msg, "write") {
		t.Errorf("error %q does not list known tools", msg)
	}
}

// TestDispatch_CancelledContext pins the cancellation contract: the
// cancelled context must reach the tool, and the tool's propagated error
// must surface from Dispatch unchanged (AGENTS.md: any ctx-aware path
// gets a ctx.Done test).
func TestDispatch_CancelledContext(t *testing.T) {
	t.Parallel()
	reg := New()
	reg.Add(&fakeTool{
		name: "slow",
		fn: func(ctx context.Context, r json.RawMessage) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reg.Dispatch(ctx, "slow", json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Dispatch() error = %v, want context.Canceled", err)
	}
}

// TestWire_ConcurrentReads verifies the registry's "safe for concurrent
// reads" claim under -race: parallel first-call Wire() from many
// goroutines must not race and must all observe the same bytes.
func TestWire_ConcurrentReads(t *testing.T) {
	reg := New()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		reg.Add(&fakeTool{name: n, desc: n + " desc"})
	}

	const workers = 16
	var wg sync.WaitGroup
	results := make([][]byte, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, err := json.Marshal(reg.Wire())
			results[i] = b
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if !bytesEqual(results[i], results[0]) {
			t.Errorf("worker %d saw different Wire() bytes than worker 0", i)
		}
	}
}

// TestInterfaceContractsSurviveRegistry verifies that Lookup returns the
// concrete tool intact, so the agent's type assertions (StreamingTool /
// ReadOnlyTool) succeed on tools retrieved from the registry.
func TestInterfaceContractsSurviveRegistry(t *testing.T) {
	t.Parallel()
	reg := New()
	st := &fakeStreamingTool{fakeTool: fakeTool{name: "think"}}
	ro := &fakeReadOnlyTool{fakeTool: fakeTool{name: "read"}}
	reg.Add(st).Add(ro)

	gotSt := reg.Lookup("think")
	if _, ok := gotSt.(StreamingTool); !ok {
		t.Error("Lookup(think) lost the StreamingTool interface — agent dispatchTool would fall back to Execute")
	}
	gotRo := reg.Lookup("read")
	if _, ok := gotRo.(ReadOnlyTool); !ok {
		t.Error("Lookup(read) lost the ReadOnlyTool interface — agent allReadOnly would refuse parallel dispatch")
	}
}

func TestUnmarshalStrict_Valid(t *testing.T) {
	t.Parallel()
	type args struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	var v args
	raw := json.RawMessage(`{"path":"x.go","limit":3}`)
	if err := UnmarshalStrict("read", raw, &v, "path", "limit"); err != nil {
		t.Fatalf("UnmarshalStrict() unexpected error: %v", err)
	}
	if v.Path != "x.go" || v.Limit != 3 {
		t.Errorf("UnmarshalStrict() decoded %+v, want {x.go 3}", v)
	}
}

func TestUnmarshalStrict_UnknownField(t *testing.T) {
	t.Parallel()
	type args struct {
		Path string `json:"path"`
	}
	var v args
	err := UnmarshalStrict("read", json.RawMessage(`{"path":"x.go","bogus":1}`), &v, "path")
	if err == nil {
		t.Fatal("UnmarshalStrict() accepted an unknown field")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown field \"bogus\"") {
		t.Errorf("error %q does not name the unknown field", msg)
	}
	if !strings.Contains(msg, "read") {
		t.Errorf("error %q does not name the tool", msg)
	}
	if !strings.Contains(msg, "Valid fields: path") {
		t.Errorf("error %q does not list valid fields", msg)
	}
}

func TestUnmarshalStrict_Malformed(t *testing.T) {
	t.Parallel()
	var v map[string]any
	err := UnmarshalStrict("read", json.RawMessage(`{`), &v, "path")
	if err == nil {
		t.Fatal("UnmarshalStrict() accepted malformed JSON")
	}
	if !strings.Contains(err.Error(), "read: bad arguments") {
		t.Errorf("error %q not wrapped with tool name", err)
	}
}

// TestUnmarshalStrict_TruncatesLongRaw pins the error-message contract:
// raw input longer than 200 chars must be truncated with an ellipsis so
// the model sees the head of its bad call, not a 4KB dump.
func TestUnmarshalStrict_TruncatesLongRaw(t *testing.T) {
	t.Parallel()
	tail := "THE-TAIL-MARKER"
	raw := json.RawMessage(`{"path":"` + strings.Repeat("a", 300) + `","bogus":"` + tail + `"}`)
	// Struct target, not map: DisallowUnknownFields only rejects unknown
	// keys when decoding into a struct; maps accept any key by design.
	var v struct {
		Path string `json:"path"`
	}
	err := UnmarshalStrict("read", raw, &v, "path")
	if err == nil {
		t.Fatal("UnmarshalStrict() accepted unknown field")
	}
	msg := err.Error()
	if !strings.Contains(msg, "…") {
		t.Errorf("error %q not truncated with ellipsis", msg)
	}
	if strings.Contains(msg, tail) {
		t.Error("error message contains the tail of a >200-char raw input — truncation failed")
	}
}

func TestMissingField(t *testing.T) {
	t.Parallel()
	err := MissingField("read", "path", json.RawMessage(`{"limit":5}`), "path", "limit")
	if err == nil {
		t.Fatal("MissingField() returned nil")
	}
	msg := err.Error()
	for _, want := range []string{"read: path is required", `Got: {"limit":5}`, "Valid fields: path, limit"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestTruncateArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 200, "short"},
		{"exactly-ten", 12, "exactly-ten"}, // len == n → unchanged
		{"exactly-ten", 10, "exactly-te…"}, // len > n → cut + ellipsis
		{"", 200, ""},                      // empty input stays empty
		{"abc", 0, "…"},                    // n=0 → just the ellipsis
	}
	for _, c := range cases {
		if got := truncateArgs(c.in, c.n); got != c.want {
			t.Errorf("truncateArgs(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func bytesEqual(a, b []byte) bool {
	return string(a) == string(b)
}
