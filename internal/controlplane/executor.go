package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// M3 executor: the seek subprocess driver. Everything above this file
// (routing, commands) is M2; everything below it is a plain child
// process speaking the stable `seek -p --json` JSONL contract
// (cmd/seek/main.go jsonLine — §6 verified ①). Design + decisions:
// docs/prd/feature-remote-control-plane.md D-C3/D-C4.
//
// NOTE: this file deliberately does not reference the M1a type layer
// (ConvID / Binding / BindingStore) — it landed on a parallel branch
// and the two merge later (verified: clean merge, both test suites
// green on the merged tree). ExecRequest.Conv is the same key as
// ConvID, spelled string; the M2 router bridges (`string(convID)` →
// Conv) and owns the binding.session_id writeback (ExecResult.
// SessionID exists precisely so the router can Put it).

// Defaults for ExecutorConfig zero fields. MaxParallel mirrors PRD
// D-C4 ("全局并发上限，默认 ~4"); Timeout is per subprocess ATTEMPT,
// not per message — a self-heal rerun gets its own budget.
const (
	defaultExecMaxParallel = 4
	defaultExecTimeout     = 10 * time.Minute
	execKillWaitGrace      = 5 * time.Second
)

// ErrExecTimeout reports a subprocess attempt killed by the per-run
// timeout. The plane surfaces it to the conversation as a run failure;
// it never retries automatically (bounded cost: PRD D-C4's 超时 kill
// is terminal).
var ErrExecTimeout = errors.New("controlplane: seek run timed out")

// ExecRequest is one message to run through seek in a bound project
// directory. Conv keys the per-conversation serialisation queue (the
// same value as the inbound's ConvID, as a plain string); SessionID is
// the binding's stored session — empty means first turn (no -resume);
// Yolo mirrors the per-binding D-C5 switch (--yolo on the child).
type ExecRequest struct {
	Conv      string
	Dir       string
	SessionID string
	Message   string
	Yolo      bool
}

// ExecResult is what one executed message produced.
type ExecResult struct {
	// Reply is the concatenated text_delta stream — what the run
	// answered. Empty on total failure.
	Reply string
	// SessionID is the agent_end.session_id of the successful run:
	// what the router writes back into the binding. Empty when the
	// run completed without one (--no-save style edge) — the router
	// keeps the previously stored id in that case.
	SessionID string
	// Attempts counts subprocess runs spent (1 normally, 2 when a
	// self-heal rerun happened).
	Attempts int
	// ContextReset reports that the stored session was abandoned and
	// the message re-ran as a first turn (bad id / mid-run error).
	ContextReset bool
}

// ExecutorConfig tunes an Executor. Zero fields fill from the defaults
// above; the M1b config wiring maps plane config onto this struct.
type ExecutorConfig struct {
	SeekPath    string        // seek binary; "" → "seek" (PATH lookup)
	Timeout     time.Duration // per attempt; <=0 → defaultExecTimeout
	MaxParallel int           // global concurrent runs; <=0 → default
}

// Executor runs seek subprocesses for the plane: same conversation
// serialised, different conversations in parallel, globally capped
// (D-C4). Safe for concurrent use.
type Executor struct {
	cfg   ExecutorConfig
	sem   chan struct{} // global concurrency cap
	mu    sync.Mutex    // guards convs
	convs map[string]*convGate
}

// NewExecutor builds an Executor. Call it once per serve process; the
// per-conv gates and the global semaphore live for its lifetime.
func NewExecutor(cfg ExecutorConfig) *Executor {
	if cfg.SeekPath == "" {
		cfg.SeekPath = "seek"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultExecTimeout
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = defaultExecMaxParallel
	}
	return &Executor{
		cfg:   cfg,
		sem:   make(chan struct{}, cfg.MaxParallel),
		convs: make(map[string]*convGate),
	}
}

// MaxParallel reports the effective global concurrency cap (post
// defaulting). Read accessor for the serve CLI's startup log, so the
// "max_concurrency unset → default" fallback is observable without a
// second copy of the default constant.
func (e *Executor) MaxParallel() int { return e.cfg.MaxParallel }

// convGate is the per-conversation serialisation lock: a
// buffered-channel mutex that can be acquired ctx-aware (a /cancel
// while queued for the conv aborts instead of waiting).
type convGate struct {
	gate    chan struct{}
	waiters int
}

func (e *Executor) acquireConv(ctx context.Context, conv string) (func(), error) {
	e.mu.Lock()
	cg := e.convs[conv]
	if cg == nil {
		cg = &convGate{gate: make(chan struct{}, 1)}
		cg.gate <- struct{}{} // start unlocked
		e.convs[conv] = cg
	}
	cg.waiters++
	e.mu.Unlock()

	select {
	case <-cg.gate:
	case <-ctx.Done():
		e.mu.Lock()
		cg.waiters--
		if cg.waiters <= 0 {
			delete(e.convs, conv)
		}
		e.mu.Unlock()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			cg.gate <- struct{}{}
			e.mu.Lock()
			cg.waiters--
			if cg.waiters <= 0 {
				delete(e.convs, conv)
			}
			e.mu.Unlock()
		})
	}, nil
}

// Run executes one message: serialise on Conv, take a global slot,
// spawn `seek -p <msg> --json --no-cron-tick [-resume <sid>] [--yolo]`
// with cmd.Dir=req.Dir, parse the JSONL, and self-heal per D-C3 —
//
//   - success (agent_end)          → ExecResult with SessionID.
//   - non-zero exit, empty stdout  → the -resume id is bad (verified:
//     seek exits 1 before emitting anything) → rerun as first turn.
//   - no agent_end, partial stdout → mid-run error; the session file
//     may exist but its id never reaches stdout → abandon the context
//     and rerun as first turn (accepted boundary).
//   - timeout / cancel             → kill the process tree, fail, no
//     retry (bounded cost).
//
// The rerun happens at most once; two failed attempts surface the
// error of the second.
//
// --no-cron-tick: main.go fires a cron auto-tick goroutine on every
// startup unless this flag opts out — the flag's own comment names
// "CI / scripted invocations", which is exactly what the plane is.
// Without it every inbound message would re-trigger cron jobs that the
// OS scheduler already owns (v5 zero-daemon doctrine).
func (e *Executor) Run(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if req.Conv == "" {
		return ExecResult{}, errors.New("controlplane: exec: empty Conv")
	}
	if req.Dir == "" {
		return ExecResult{}, errors.New("controlplane: exec: empty Dir")
	}
	if strings.TrimSpace(req.Message) == "" {
		return ExecResult{}, errors.New("controlplane: exec: empty Message")
	}

	release, err := e.acquireConv(ctx, req.Conv)
	if err != nil {
		return ExecResult{}, err
	}
	defer release()

	// Conv lock first, global slot second: a conversation waiting on
	// the global cap must not hold a slot another conversation could
	// use (that inverts D-C4's intent).
	select {
	case e.sem <- struct{}{}:
	case <-ctx.Done():
		return ExecResult{}, ctx.Err()
	}
	defer func() { <-e.sem }()

	res := ExecResult{}
	first, err := e.attempt(ctx, req, req.SessionID != "")
	res.Attempts = 1
	if err == nil {
		res.Reply, res.SessionID = first.reply, first.sessionID
		return res, nil
	}
	if !first.retryable {
		return res, err
	}

	// Self-heal: rerun as a first turn, abandoning the stored session.
	res.Attempts = 2
	res.ContextReset = req.SessionID != ""
	second, err2 := e.attempt(ctx, req, false)
	if err2 != nil {
		return res, err2
	}
	res.Reply, res.SessionID = second.reply, second.sessionID
	return res, nil
}

// attemptOutcome is one subprocess attempt distilled: the parsed run
// on success (err == nil), and on failure whether a first-turn rerun
// could plausibly help plus the error to surface if it doesn't.
type attemptOutcome struct {
	reply     string
	sessionID string
	retryable bool
}

// attempt spawns one seek run; resume selects whether -resume <sid> is
// passed. See Run for the self-heal policy around its outcomes.
func (e *Executor) attempt(ctx context.Context, req ExecRequest, resume bool) (attemptOutcome, error) {
	args := []string{"-p", req.Message, "--json", "--no-cron-tick"}
	if resume && req.SessionID != "" {
		args = append(args, "-resume", req.SessionID)
	}
	if req.Yolo {
		args = append(args, "--yolo")
	}

	// Same ctx-derived deadline for caller cancellation (/cancel) and
	// the per-attempt timeout: both land in the kill-tree path.
	runCtx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()

	cmd := exec.Command(e.cfg.SeekPath, args...)
	cmd.Dir = req.Dir
	prepareChild(cmd) // platform: process group / kill-tree setup

	var stdout, stderr lockedBuf
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil // /dev/null: -p reads the flag, never stdin

	if startErr := cmd.Start(); startErr != nil {
		// Deterministic (binary missing, bad Dir) — a rerun changes
		// nothing. Named with the seek path so the reply says it.
		return attemptOutcome{retryable: false}, fmt.Errorf("controlplane: exec %s: %w", e.cfg.SeekPath, startErr)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var waitErr error
	killed := false
	select {
	case waitErr = <-waitDone:
	case <-runCtx.Done():
		killed = true
		killTree(cmd)
		select {
		case waitErr = <-waitDone:
		case <-time.After(execKillWaitGrace):
			// Pipes still held by a Setsid'd grandchild (seek's own
			// bash tool does this) can block the pipe copy forever;
			// same accepted posture as internal/tools/bash — the
			// SIGKILL is already out, report and move on.
			waitErr = errExecWaitGrace
		}
	}

	out := parseSeekJSONL(stdout.snapshot())

	if killed {
		// Both timeout and /cancel are terminal, non-retryable; the
		// message names which one so the conversation reply can too.
		// ctx.Err() != nil means the CALLER's ctx fired (cancel or its
		// own deadline) — /cancel semantics; only a parent ctx that is
		// still alive while runCtx expired is our per-attempt timeout.
		if ctx.Err() != nil {
			return attemptOutcome{retryable: false}, errors.New("controlplane: seek run cancelled")
		}
		return attemptOutcome{retryable: false}, fmt.Errorf("%w after %s", ErrExecTimeout, e.cfg.Timeout)
	}
	if out.sawAgentEnd {
		// Success by contract — even with a non-zero exit (not
		// produced by today's seek; salvaged if it ever is).
		return attemptOutcome{reply: out.reply, sessionID: out.sessionID}, nil
	}

	// No agent_end: the run is incomplete. Triage per D-C3 —
	//   partial stdout (error event or deltas) → mid-run failure;
	//   empty stdout + non-zero exit + we used -resume → bad id;
	// both heal by rerunning as a first turn. Empty stdout on a
	// first-turn attempt is a deterministic boot failure (config,
	// binary) — rerunning the identical command can't help.
	exitDesc := ""
	if waitErr != nil {
		exitDesc = waitErr.Error()
	}
	retryable := out.lines > 0 || (resume && waitErr != nil)
	if retryable {
		detail := out.errLine
		if detail == "" {
			detail = strings.TrimSpace(tail(string(stderr.snapshot()), 400))
		}
		return attemptOutcome{retryable: true}, fmt.Errorf(
			"controlplane: seek run incomplete (exit=%q, lines=%d): %s", exitDesc, out.lines, detail)
	}
	return attemptOutcome{retryable: false}, fmt.Errorf(
		"controlplane: seek run failed (exit=%q): %s", exitDesc, strings.TrimSpace(tail(string(stderr.snapshot()), 400)))
}

// errExecWaitGrace mirrors internal/tools/bash: the kill fired but the
// pipe copy didn't drain within execKillWaitGrace.
var errExecWaitGrace = errors.New("controlplane: seek process did not exit after kill")

// seekOutcome is the distilled JSONL of one run.
type seekOutcome struct {
	reply       string
	sessionID   string
	sawAgentEnd bool
	errLine     string // the runJSON `error` event, if any
	lines       int    // parseable JSONL lines seen (0 = empty stdout)
}

// parseSeekJSONL walks the -p --json stdout. Malformed lines are
// skipped (a seek bug must not lose a completed run's agent_end);
// `lines` counts only parseable ones so "empty stdout" stays
// distinguishable from "garbage stdout" for the triage above.
func parseSeekJSONL(data []byte) seekOutcome {
	var out seekOutcome
	var reply strings.Builder
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			Delta     string `json:"delta"`
			Reasoning bool   `json:"reasoning"`
			SessionID string `json:"session_id"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		out.lines++
		switch ev.Type {
		case "text_delta":
			if !ev.Reasoning {
				reply.WriteString(ev.Delta)
			}
		case "agent_end":
			out.sawAgentEnd = true
			out.sessionID = ev.SessionID
		case "error":
			out.errLine = ev.Error
		}
	}
	out.reply = reply.String()
	return out
}

// lockedBuf is a mutex-guarded bytes.Buffer: os/exec pipe-copy
// goroutines may still be writing after the kill-grace expiry while
// the main goroutine snapshots the tail for the error message. Same
// shape as internal/tools/bash's lockedBuffer.
type lockedBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return []byte(b.buf.String())
}

// tail returns the last n bytes of s (the whole string when shorter).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
