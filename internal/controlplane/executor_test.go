package controlplane

// The fake seek is this test binary itself, re-exec'd with seek's own
// argv shape (`-p <msg> --json --no-cron-tick [-resume <sid>]`): the
// executor drives it exactly as it drives real seek — same arg
// building, same cmd.Dir, same process-group teardown. TestMain
// intercepts before the testing framework parses flags (flag.Parse
// lives inside m.Run), so `-p` never trips the test flag parser.
// Grandchildren (for the kill-tree test) use the --cp-stub-grandchild
// marker. Behaviour modes come via env so the parent test keeps
// driving the real ExecRequest surface.
//
// Same re-exec idiom as internal/sandbox/sandbox_linux_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- the stub process ----

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "-p" || os.Args[1] == "--cp-stub-grandchild") {
		stubMain()
		// unreachable: stubMain always exits
	}
	os.Exit(m.Run())
}

// stubMain is the fake seek. Env contract (all set by the parent test):
//
//	CP_STUB_DIR       scratch dir: timeline.log / invocations.jsonl / counter / pids
//	CP_STUB_MODE      happy (default) | miderr | badid | hang_tree | partial_exit0
//	CP_STUB_FAIL_FIRST  when set (=miderr|badid), invocation 1 misbehaves per that
//	                      mode and every later invocation is happy
//	CP_STUB_SLEEP_MS  sleep between start/end timeline marks (default 0)
func stubMain() {
	dir := os.Getenv("CP_STUB_DIR")
	if dir == "" {
		os.Exit(3)
	}
	if os.Args[1] == "--cp-stub-grandchild" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}

	msg, sid := stubArgs(os.Args)
	n := stubInvocationN(dir) // one increment per invocation; also the fresh-id source
	mode := os.Getenv("CP_STUB_MODE")
	if ff := os.Getenv("CP_STUB_FAIL_FIRST"); ff != "" && n == 1 {
		mode = ff
	}

	sleepMs := int64(0)
	if v := os.Getenv("CP_STUB_SLEEP_MS"); v != "" {
		fmt.Sscanf(v, "%d", &sleepMs)
	}

	stubTimeline(dir, fmt.Sprintf("start %d %s", time.Now().UnixNano(), msg))
	stubRecordInvocation(dir, os.Args, msg)
	// os.Exit skips defers, so the end mark is written by the wrapper
	// below, not a defer — timeline tests read both marks.
	code := stubBehaviour(mode, dir, msg, sid, n, time.Duration(sleepMs)*time.Millisecond)
	stubTimeline(dir, fmt.Sprintf("end %d %s", time.Now().UnixNano(), msg))
	os.Exit(code)
}

// stubBehaviour prints one mode's stdout/stderr and returns its exit
// code. hang_tree never returns (killed by the executor mid-sleep).
func stubBehaviour(mode, dir, msg, sid string, n int, sleep time.Duration) int {
	switch mode {
	case "badid":
		// Verified real-seek bad-id shape: exit 1, stdout EMPTY,
		// error only on stderr.
		fmt.Fprintln(os.Stderr, "resume "+sid+": session not found")
		return 1
	case "miderr":
		// Verified real-seek mid-run shape: partial events, an error
		// line, no agent_end, non-zero exit.
		fmt.Println(`{"type":"agent_start"}`)
		fmt.Println(`{"type":"turn_start","index":0}`)
		fmt.Println(`{"type":"text_delta","delta":"partial "}`)
		fmt.Println(`{"type":"error","error":"api auth failed"}`)
		return 1
	case "partial_exit0":
		// Contract-violation probe: deltas but no agent_end, clean
		// exit. Still "incomplete" for the triage.
		fmt.Println(`{"type":"agent_start"}`)
		fmt.Println(`{"type":"text_delta","delta":"half an answer"}`)
		return 0
	case "bare_end":
		// agent_end with no deltas and no session id: a completed run
		// that reports neither text nor id (router edge: empty-reply
		// placeholder + session-writeback keeps the stored id).
		fmt.Println(`{"type":"agent_end","turns":0}`)
		return 0
	case "hang_tree":
		// For the timeout/cancel tests: records both PIDs, spawns a
		// grandchild that outlives everything, then hangs. The
		// grandchild inherits our process group, so the executor's
		// group kill must reap both.
		g := exec.Command(os.Args[0], "--cp-stub-grandchild")
		if err := g.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "grandchild:", err)
			return 1
		}
		_ = os.WriteFile(filepath.Join(dir, "pids"),
			[]byte(fmt.Sprintf("%d\n%d\n", os.Getpid(), g.Process.Pid)), 0644)
		time.Sleep(60 * time.Second)
		return 0
	default: // happy
		if sleep > 0 {
			time.Sleep(sleep)
		}
		if sid == "" { // first turn: mint a fresh id from the counter
			sid = fmt.Sprintf("S%d", n)
		} // else: -resume path echoes the SAME id, like real seek
		fmt.Println(`{"type":"agent_start"}`)
		fmt.Println(`{"type":"turn_start","index":0}`)
		fmt.Printf("{\"type\":\"text_delta\",\"delta\":\"Hello \"}\n")
		fmt.Printf("{\"type\":\"text_delta\",\"delta\":\"%s\"}\n", msg)
		fmt.Println(`{"type":"turn_end","index":0,"prompt_tokens":10}`)
		fmt.Printf("{\"type\":\"agent_end\",\"turns\":1,\"session_id\":%q}\n", sid)
		return 0
	}
}

// stubArgs extracts -p's value and -resume's value ("" when absent)
// from a seek-shaped argv.
func stubArgs(argv []string) (prompt, resume string) {
	for i := 1; i < len(argv); i++ {
		switch argv[i] {
		case "-p":
			if i+1 < len(argv) {
				prompt = argv[i+1]
				i++
			}
		case "-resume":
			if i+1 < len(argv) {
				resume = argv[i+1]
				i++
			}
		}
	}
	return prompt, resume
}

func stubTimeline(dir, line string) {
	f, err := os.OpenFile(filepath.Join(dir, "timeline.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

func stubRecordInvocation(dir string, argv []string, msg string) {
	rec := struct {
		Argv      []string `json:"argv"`
		CWD       string   `json:"cwd"`
		Timestamp int64    `json:"ns"`
	}{Argv: argv, CWD: mustGetwd(), Timestamp: time.Now().UnixNano()}
	b, _ := json.Marshal(rec)
	f, err := os.OpenFile(filepath.Join(dir, "invocations.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, string(b))
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

// stubInvocationN returns a monotonically increasing invocation count
// (1-based) via an append-only counter file. Calls from parallel stub
// processes may observe the same number; every test that keys行为 off
// the count runs its stubs sequentially (per-conv serialisation or a
// single Run), where the count is exact.
func stubInvocationN(dir string) int {
	f, err := os.OpenFile(filepath.Join(dir, "counter"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return 1
	}
	f.WriteString("x\n")
	f.Close()
	b, err := os.ReadFile(filepath.Join(dir, "counter"))
	if err != nil {
		return 1
	}
	return strings.Count(string(b), "\n")
}

// ---- parent-side helpers ----

// newStubExecutor wires an Executor whose seek binary is this test
// binary in stub mode, plus the scratch dir the stub logs into.
func newStubExecutor(t *testing.T, mutate func(*ExecutorConfig)) (*Executor, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CP_STUB_DIR", dir)
	t.Setenv("CP_STUB_MODE", "")
	t.Setenv("CP_STUB_FAIL_FIRST", "")
	t.Setenv("CP_STUB_SLEEP_MS", "")
	cfg := ExecutorConfig{SeekPath: os.Args[0], Timeout: 30 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewExecutor(cfg), dir
}

type stubInvocation struct {
	Argv []string `json:"argv"`
	CWD  string   `json:"cwd"`
}

func readInvocations(t *testing.T, dir string) []stubInvocation {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "invocations.jsonl"))
	if err != nil {
		t.Fatalf("read invocations: %v", err)
	}
	var out []stubInvocation
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var inv stubInvocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			t.Fatalf("bad invocation line %q: %v", line, err)
		}
		out = append(out, inv)
	}
	return out
}

// argvHas reports whether inv.Argv contains flag (optionally followed
// by value).
func argvHas(inv stubInvocation, flag, value string) bool {
	for i, a := range inv.Argv {
		if a != flag {
			continue
		}
		if value == "" {
			return true
		}
		return i+1 < len(inv.Argv) && inv.Argv[i+1] == value
	}
	return false
}

type timelineMark struct {
	kind string // start | end
	ns   int64
	tag  string
}

func readTimeline(t *testing.T, dir string) []timelineMark {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "timeline.log"))
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	var out []timelineMark
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m timelineMark
		if _, err := fmt.Sscanf(line, "%s %d", &m.kind, &m.ns); err != nil {
			t.Fatalf("bad timeline line %q: %v", line, err)
		}
		fields := strings.Fields(line)
		if len(fields) == 3 {
			m.tag = fields[2]
		}
		out = append(out, m)
	}
	return out
}

// pidAlive / waitPidGone live in executor_unix_test.go (signal-0
// probe); the Windows fallback skips the liveness assertion.

// ---- the four card states + contract details ----

// State 1: happy first turn — no -resume, fresh session id captured,
// reply assembled from the delta stream, cwd pinned to the bound dir.
func TestExecutor_FirstTurnHappy(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	project := t.TempDir()

	res, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: project, Message: "from stub 1",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Reply != "Hello from stub 1" {
		t.Errorf("reply = %q, want concatenated text_delta stream", res.Reply)
	}
	if res.SessionID != "S1" {
		t.Errorf("session id = %q, want S1 (fresh-minted by the stub)", res.SessionID)
	}
	if res.Attempts != 1 || res.ContextReset {
		t.Errorf("attempts/reset = %d/%v, want 1/false", res.Attempts, res.ContextReset)
	}

	invs := readInvocations(t, dir)
	if len(invs) != 1 {
		t.Fatalf("invocations = %d, want 1", len(invs))
	}
	if invs[0].CWD != project {
		t.Errorf("stub cwd = %q, want bound project dir %q", invs[0].CWD, project)
	}
	for flag, want := range map[string]bool{"-p": true, "--json": true, "--no-cron-tick": true, "-resume": false, "--yolo": false} {
		if got := argvHas(invs[0], flag, ""); got != want {
			t.Errorf("argv flag %s present = %v, want %v (argv=%q)", flag, got, want, invs[0].Argv)
		}
	}
	if !argvHas(invs[0], "-p", "from stub 1") {
		t.Errorf("message not passed via -p: %q", invs[0].Argv)
	}
}

// State 1b: the resume round-trip — second turn passes -resume <sid>
// and seek returns the SAME id (the real contract this plane rides on).
func TestExecutor_ResumeRoundTrip(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	project := t.TempDir()
	req := ExecRequest{Conv: "feishu:oc_1", Dir: project, Message: "m1"}

	first, err := ex.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	req.SessionID = first.SessionID
	req.Message = "m2"
	second, err := ex.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.SessionID != first.SessionID {
		t.Errorf("resume changed session id: %q → %q", first.SessionID, second.SessionID)
	}
	invs := readInvocations(t, dir)
	if len(invs) != 2 {
		t.Fatalf("invocations = %d, want 2", len(invs))
	}
	if !argvHas(invs[1], "-resume", first.SessionID) {
		t.Errorf("second run missing -resume %s: %q", first.SessionID, invs[1].Argv)
	}
	if second.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", second.Attempts)
	}
}

// D-C5 wiring detail: Yolo appends --yolo to the child.
func TestExecutor_YoloFlagPassed(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	if _, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m", Yolo: true,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	invs := readInvocations(t, dir)
	if !argvHas(invs[0], "--yolo", "") {
		t.Errorf("--yolo not passed: %q", invs[0].Argv)
	}
}

// State 2: bad id — resume exits 1 with EMPTY stdout; the executor
// degrades to a first-turn rerun and reports the NEW id.
func TestExecutor_BadIDSelfHeals(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_FAIL_FIRST", "badid")

	res, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), SessionID: "BOGUS", Message: "m",
	})
	if err != nil {
		t.Fatalf("self-heal run: %v", err)
	}
	if res.SessionID == "BOGUS" || res.SessionID == "" {
		t.Errorf("session id = %q, want a fresh id (not the bad one)", res.SessionID)
	}
	if res.Attempts != 2 || !res.ContextReset {
		t.Errorf("attempts/reset = %d/%v, want 2/true", res.Attempts, res.ContextReset)
	}
	invs := readInvocations(t, dir)
	if len(invs) != 2 {
		t.Fatalf("invocations = %d, want 2 (resume attempt + fresh rerun)", len(invs))
	}
	if !argvHas(invs[0], "-resume", "BOGUS") {
		t.Errorf("attempt 1 should carry -resume BOGUS: %q", invs[0].Argv)
	}
	if argvHas(invs[1], "-resume", "") {
		t.Errorf("attempt 2 must rerun as first turn: %q", invs[1].Argv)
	}
}

// State 3: mid-run error — events but no agent_end; the stored
// context is abandoned and the message re-runs as a first turn.
func TestExecutor_MidRunErrorRestartsFresh(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_FAIL_FIRST", "miderr")

	res, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), SessionID: "S_OLD", Message: "m",
	})
	if err != nil {
		t.Fatalf("restart run: %v", err)
	}
	if res.SessionID != "S2" || res.SessionID == "S_OLD" {
		t.Errorf("session id = %q, want fresh S2", res.SessionID)
	}
	if res.Attempts != 2 || !res.ContextReset {
		t.Errorf("attempts/reset = %d/%v, want 2/true", res.Attempts, res.ContextReset)
	}
	invs := readInvocations(t, dir)
	if argvHas(invs[1], "-resume", "") {
		t.Errorf("attempt 2 must abandon the context (no -resume): %q", invs[1].Argv)
	}
}

// Mid-run error on a FIRST turn (no stored session): still retries
// once — a transient API failure shouldn't eat the message — but
// ContextReset stays false (nothing was abandoned).
func TestExecutor_MidRunErrorFirstTurnRetries(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_FAIL_FIRST", "miderr")

	res, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m",
	})
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if res.Attempts != 2 || res.ContextReset {
		t.Errorf("attempts/reset = %d/%v, want 2/false", res.Attempts, res.ContextReset)
	}
}

// A run that emits deltas but exits 0 without agent_end is still
// incomplete — retried as a first turn (contract-violation probe).
func TestExecutor_PartialExitZeroRetries(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_FAIL_FIRST", "partial_exit0")

	res, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), SessionID: "S_OLD", Message: "m",
	})
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if res.Attempts != 2 || !res.ContextReset || res.SessionID != "S2" {
		t.Errorf("got attempts=%d reset=%v sid=%q, want 2/true/S2", res.Attempts, res.ContextReset, res.SessionID)
	}
}

// Both attempts fail: the surfaced error carries the JSONL error line.
func TestExecutor_BothAttemptsFail(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_MODE", "miderr")

	_, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m",
	})
	if err == nil {
		t.Fatal("double failure must surface an error")
	}
	if !strings.Contains(err.Error(), "api auth failed") {
		t.Errorf("error should carry the runJSON error line, got: %v", err)
	}
}

// State 4: timeout — the run dies at the deadline, the process TREE
// (stub + grandchild) is killed, nothing leaks past the grace.
func TestExecutor_TimeoutKillsTree(t *testing.T) {
	ex, dir := newStubExecutor(t, func(c *ExecutorConfig) { c.Timeout = 400 * time.Millisecond })
	t.Setenv("CP_STUB_MODE", "hang_tree")

	start := time.Now()
	_, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m",
	})
	if !errors.Is(err, ErrExecTimeout) {
		t.Fatalf("err = %v, want ErrExecTimeout", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("timeout handling took %v — kill/grace path too slow", el)
	}

	b, rerr := os.ReadFile(filepath.Join(dir, "pids"))
	if rerr != nil {
		t.Fatalf("stub never recorded pids: %v", rerr)
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		t.Fatalf("pid file fields = %q", fields)
	}
	var stubPID, childPID int
	fmt.Sscanf(fields[0], "%d", &stubPID)
	fmt.Sscanf(fields[1], "%d", &childPID)
	waitPidGone(t, stubPID, 2*time.Second)
	waitPidGone(t, childPID, 2*time.Second)
}

// Caller cancellation (/cancel) mid-run: same kill path, distinct
// message, tree still reaped.
func TestExecutor_CancelKillsProcess(t *testing.T) {
	ex, dir := newStubExecutor(t, func(c *ExecutorConfig) { c.Timeout = 30 * time.Second })
	t.Setenv("CP_STUB_MODE", "hang_tree")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := ex.Run(ctx, ExecRequest{Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m"})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want cancelled", err)
	}

	b, _ := os.ReadFile(filepath.Join(dir, "pids"))
	fields := strings.Fields(string(b))
	var stubPID int
	fmt.Sscanf(fields[0], "%d", &stubPID)
	waitPidGone(t, stubPID, 2*time.Second)
}

// A /cancel while QUEUED behind a running sibling of the same conv
// aborts without spawning anything.
func TestExecutor_CancelWhileQueuedNeverStarts(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_SLEEP_MS", "500")
	project := t.TempDir()

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := ex.Run(context.Background(), ExecRequest{Conv: "feishu:oc_1", Dir: project, Message: "m1"}); err != nil {
			t.Errorf("first run: %v", err)
		}
	})
	time.Sleep(100 * time.Millisecond) // let m1 take the conv gate

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := ex.Run(ctx, ExecRequest{Conv: "feishu:oc_1", Dir: project, Message: "m2"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued run err = %v, want ctx deadline", err)
	}
	wg.Wait()

	invs := readInvocations(t, dir)
	if len(invs) != 1 {
		t.Errorf("invocations = %d, want 1 (queued run must not start)", len(invs))
	}
}

// ---- concurrency shape (D-C4) ----

// Same conv: strictly serial — run 2 starts only after run 1 ends.
func TestExecutor_SameConvSerializes(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_SLEEP_MS", "250")
	project := t.TempDir()

	var wg sync.WaitGroup
	for _, msg := range []string{"m1", "m2"} {
		wg.Add(1)
		go func(m string) {
			defer wg.Done()
			if _, err := ex.Run(context.Background(), ExecRequest{Conv: "feishu:oc_1", Dir: project, Message: m}); err != nil {
				t.Errorf("run %s: %v", m, err)
			}
		}(msg)
	}
	wg.Wait()

	marks := readTimeline(t, dir)
	if len(marks) != 4 {
		t.Fatalf("timeline marks = %d, want 4", len(marks))
	}
	// One possible order: m1 start, m1 end, m2 start, m2 end.
	byTag := map[string][]timelineMark{}
	for _, m := range marks {
		byTag[m.tag] = append(byTag[m.tag], m)
	}
	if len(byTag) != 2 {
		t.Fatalf("expected two runs in timeline, got %v", byTag)
	}
	for tag, ms := range byTag {
		if len(ms) != 2 || ms[0].kind != "start" || ms[1].kind != "end" {
			t.Fatalf("tag %s marks malformed: %+v", tag, ms)
		}
	}
	// The serialisation assertion: the later start is after the
	// earlier end, no matter which message won the race.
	var firstEnd, lastStart int64
	for _, ms := range byTag {
		if firstEnd == 0 || ms[1].ns < firstEnd {
			firstEnd = ms[1].ns
		}
		if ms[0].ns > lastStart {
			lastStart = ms[0].ns
		}
	}
	if lastStart < firstEnd {
		t.Errorf("runs overlapped: second start %d < first end %d", lastStart, firstEnd)
	}
}

// Different convs run in parallel: the two runs' timeline intervals
// must overlap (a serialised pair would have zero overlap). Overlap,
// not wall clock, is the assertion — stub process startup (~0.5s
// under -race) would drown a wall-clock signal.
func TestExecutor_CrossConvParallel(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_SLEEP_MS", "400")
	project := t.TempDir()

	var wg sync.WaitGroup
	for _, conv := range []string{"feishu:oc_1", "feishu:oc_2"} {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			if _, err := ex.Run(context.Background(), ExecRequest{Conv: c, Dir: project, Message: "m"}); err != nil {
				t.Errorf("run %s: %v", c, err)
			}
		}(conv)
	}
	wg.Wait()

	marks := readTimeline(t, dir)
	if len(marks) != 4 {
		t.Fatalf("timeline marks = %d, want 4", len(marks))
	}
	var starts, ends []int64
	for _, m := range marks {
		if m.kind == "start" {
			starts = append(starts, m.ns)
		} else {
			ends = append(ends, m.ns)
		}
	}
	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("timeline malformed: %+v", marks)
	}
	// overlap = earlier end − later start; > 0 means concurrent.
	var laterStart, earlierEnd = starts[0], ends[0]
	if starts[1] > laterStart {
		laterStart = starts[1]
	}
	if ends[1] < earlierEnd {
		earlierEnd = ends[1]
	}
	if overlap := earlierEnd - laterStart; overlap <= 0 {
		t.Errorf("runs did not overlap (later start %d, earlier end %d) — convs serialised", laterStart, earlierEnd)
	}
}

// Global cap: with MaxParallel=2 across four convs, never more than
// two runs are alive at once, and all four complete.
func TestExecutor_GlobalCapQueues(t *testing.T) {
	ex, dir := newStubExecutor(t, func(c *ExecutorConfig) { c.MaxParallel = 2 })
	t.Setenv("CP_STUB_SLEEP_MS", "300")
	project := t.TempDir()

	var wg sync.WaitGroup
	for _, conv := range []string{"feishu:oc_1", "feishu:oc_2", "feishu:oc_3", "feishu:oc_4"} {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			if _, err := ex.Run(context.Background(), ExecRequest{Conv: c, Dir: project, Message: "m"}); err != nil {
				t.Errorf("run %s: %v", c, err)
			}
		}(conv)
	}
	wg.Wait()

	marks := readTimeline(t, dir)
	if len(marks) != 8 {
		t.Fatalf("timeline marks = %d, want 8", len(marks))
	}
	events := make([]timelineMark, len(marks))
	copy(events, marks)
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j].ns < events[j-1].ns; j-- {
			events[j], events[j-1] = events[j-1], events[j]
		}
	}
	live, max := 0, 0
	for _, m := range events {
		switch m.kind {
		case "start":
			live++
			if live > max {
				max = live
			}
		case "end":
			live--
		}
	}
	if max > 2 {
		t.Errorf("max concurrent runs = %d, want <= 2", max)
	}
	if max < 2 {
		t.Errorf("max concurrent runs = %d, want 2 (parallelism never happened)", max)
	}
}

// ---- request validation + failure plumbing ----

func TestExecutor_ValidatesRequest(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	cases := []ExecRequest{
		{Dir: "/tmp", Message: "m"},
		{Conv: "feishu:oc_1", Message: "m"},
		{Conv: "feishu:oc_1", Dir: "/tmp"},
	}
	for i, req := range cases {
		if _, err := ex.Run(context.Background(), req); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestExecutor_MissingBinary(t *testing.T) {
	ex, _ := newStubExecutor(t, func(c *ExecutorConfig) { c.SeekPath = "/nonexistent/seek-m3-test" })
	_, err := ex.Run(context.Background(), ExecRequest{Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m"})
	if err == nil {
		t.Fatal("missing binary must error")
	}
	if !strings.Contains(err.Error(), "/nonexistent/seek-m3-test") {
		t.Errorf("error should name the seek path: %v", err)
	}
}

// Empty stdout + non-zero exit on a FIRST turn (no -resume) is a
// deterministic boot failure — no self-heal rerun.
func TestExecutor_FirstTurnBootFailureNoRetry(t *testing.T) {
	ex, dir := newStubExecutor(t, func(c *ExecutorConfig) {})
	t.Setenv("CP_STUB_MODE", "badid") // no -resume passed: stub still exits 1 with empty stdout

	res, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m",
	})
	if err == nil {
		t.Fatal("boot failure must error")
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on deterministic failure)", res.Attempts)
	}
	if len(readInvocations(t, dir)) != 1 {
		t.Errorf("invocations != 1 — a retry happened anyway")
	}
}

// ---- parser unit tests ----

func TestParseSeekJSONL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		reply   string
		sid     string
		sawEnd  bool
		errLine string
		lines   int
	}{
		{
			name:   "happy stream",
			in:     `{"type":"agent_start"}` + "\n" + `{"type":"text_delta","delta":"Hi "}` + "\n" + `{"type":"reasoning_delta","delta":"thinking","reasoning":true}` + "\n" + `{"type":"text_delta","delta":"there"}` + "\n" + `{"type":"agent_end","turns":1,"session_id":"abc"}` + "\n",
			reply:  "Hi there",
			sid:    "abc",
			sawEnd: true,
			lines:  5,
		},
		{
			name:   "crlf tolerated",
			in:     "{\"type\":\"agent_end\",\"session_id\":\"x\"}\r\n",
			sid:    "x",
			sawEnd: true,
			lines:  1,
		},
		{
			name:   "malformed line skipped",
			in:     "not json\n" + `{"type":"text_delta","delta":"ok"}` + "\n",
			reply:  "ok",
			lines:  1,
			sawEnd: false,
		},
		{
			name:    "error line captured",
			in:      `{"type":"agent_start"}` + "\n" + `{"type":"error","error":"boom"}` + "\n",
			errLine: "boom",
			lines:   2,
		},
		{
			name: "empty",
			in:   "",
		},
		{
			name:   "agent_end without session id",
			in:     `{"type":"agent_end"}` + "\n",
			sawEnd: true,
			lines:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSeekJSONL([]byte(tc.in))
			if got.reply != tc.reply {
				t.Errorf("reply = %q, want %q", got.reply, tc.reply)
			}
			if got.sessionID != tc.sid {
				t.Errorf("sid = %q, want %q", got.sessionID, tc.sid)
			}
			if got.sawAgentEnd != tc.sawEnd {
				t.Errorf("sawAgentEnd = %v, want %v", got.sawAgentEnd, tc.sawEnd)
			}
			if got.errLine != tc.errLine {
				t.Errorf("errLine = %q, want %q", got.errLine, tc.errLine)
			}
			if got.lines != tc.lines {
				t.Errorf("lines = %d, want %d", got.lines, tc.lines)
			}
		})
	}
}
