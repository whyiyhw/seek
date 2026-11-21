package controlplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
// Yolo mirrors the per-binding D-C5 switch (--yolo on the child);
// Approval is the optional decision router (nil → the child keeps print
// mode's deny-everything default).
type ExecRequest struct {
	Conv      string
	Dir       string
	SessionID string
	Message   string
	Yolo      bool

	// Approval, when non-nil, turns on the child's --approval-stdin
	// channel (W5 card c_n37tnr): every approval_request line the child
	// emits is registered here with a delivery handle, and the plane
	// writes the human's decision back into that child's stdin keyed by
	// req_id. nil keeps the pre-W5 byte shape of the child's argv AND
	// its deny-by-default posture — a frontend without an approval
	// surface (fake) is unaffected.
	Approval ApprovalRouter
}

// ApprovalRouter is the exec-side half of the approval round-trip: the
// per-run object the executor registers request ids on and withdraws
// leftovers through. The web frontend implements it (webfrontend_approval.go,
// RunApprovals); the plane's Frontend port stays untouched — this is a
// sibling interface, upcast at the wiring site like StreamSender.
type ApprovalRouter interface {
	// Route installs deliver as the handler for reqID's decision. Called
	// from the stdout reader BEFORE the same event is emitted, so a card
	// is never visible without a live delivery handle.
	Route(reqID string, deliver func(decision string))
	// Cancel withdraws every card this run filed that nobody decided —
	// called when an attempt ends (the child is gone; a leftover card
	// could only be answered into a dead pipe). Idempotent, and it
	// forgets the ids so a self-heal rerun starts clean.
	Cancel()
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
//
// Run is the no-observer entry point; it delegates to RunStream
// (feature-web-frontend.md §2.4 seam 2).
func (e *Executor) Run(ctx context.Context, req ExecRequest) (ExecResult, error) {
	return e.RunStream(ctx, req, nil)
}

// RunEvent is one line of the stable `seek -p --json` JSONL contract
// (cmd/seek main.go jsonLine — the field set mirrors it 1:1 and grows
// additively with that contract). Named + exported so frontends can
// render the stream without re-parsing raw bytes. Consumers branch on
// Type; all other fields are type-specific, per the jsonLine contract
// comment.
type RunEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	// agent_start only: model id the run is bound to (the JSONL
	// contract's agent_start.model, W0.3 — pricing consumers turn
	// turn_end usage into $ with it, feature-web-frontend §2.5)
	Model string `json:"model,omitempty"`
	// text_delta / reasoning_delta / tool_delta
	Delta     string `json:"delta,omitempty"`
	Reasoning bool   `json:"reasoning,omitempty"`
	// tool_start / tool_delta / tool_end
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	// error field for tool_end and error events
	Error string `json:"error,omitempty"`
	// turn_end / agent_end token accounting
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	CacheHitTokens   int `json:"cache_hit_tokens,omitempty"`
	ToolCalls        int `json:"tool_calls,omitempty"`
	// agent_end only
	Turns     int    `json:"turns,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// approval_request / approval_cancel only (W5 card c_n37tnr, the
	// additive plane seam): a print-mode child surfacing a permission
	// decision to a human. ReqID keys the whole round-trip (the child
	// waits on it, the web approval hub files the card under it, the
	// decision comes back keyed by it); Kind/Summary/Command/Diff
	// mirror permission.Action's decision-relevant subset plus Display's
	// rendering hints. The child emits these under `-p --json
	// --approval-stdin` (cmd/seek/approval_stdin.go); a stub that
	// speaks the same lines exercises the same path.
	ReqID   string `json:"req_id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Summary string `json:"summary,omitempty"`
	Command string `json:"command,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// RunStream is Run with an optional per-line observer: emit sees each
// parseable JSONL line AS IT ARRIVES (line-buffered stdout read),
// before the subprocess exits. It carries the FULL jsonLine contract —
// tool_start/tool_end/turn_end/agent_end, not just text_delta — so the
// web status bar and the P2 panel share one data source; filtering
// (e.g. reasoning out of the UI) is the consumer's job. A self-heal
// rerun restarts the stream: a consumer sees a fresh agent_start after
// the failed attempt's error event.
//
// Red line: emit is read-only. Events are passed by value and the
// folded outcome is computed independently of the callback, so emit
// can neither observe nor corrupt the final ExecResult. Run(req) ≡
// RunStream(ctx, req, nil); aggregation and self-heal semantics are
// exactly Run's.
func (e *Executor) RunStream(ctx context.Context, req ExecRequest, emit func(RunEvent)) (ExecResult, error) {
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
	first, err := e.attempt(ctx, req, req.SessionID != "", emit)
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
	second, err2 := e.attempt(ctx, req, false, emit)
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
// passed, emit is the optional per-line observer (nil from the Run
// path). stdout is streamed line by line through the streamSink — the
// same fold parseSeekJSONL performs — so emit sees lines as they land
// while the outcome aggregation stays byte-identical to the batch
// reader. See RunStream for the self-heal policy around its outcomes.
//
// With req.Approval set the child also gains --approval-stdin and a
// stdin PIPE (os.Pipe, not io.Pipe: kernel-buffered writes must not
// block the plane's RPC handler, and closing the write end on the way
// out turns a late decision into EPIPE instead of a hung HTTP request).
// Every approval_request line is registered with the run's router
// BEFORE it is emitted — the card must never be visible without a live
// delivery handle — and the handle writes one decision line back keyed
// by req_id.
func (e *Executor) attempt(ctx context.Context, req ExecRequest, resume bool, emit func(RunEvent)) (attemptOutcome, error) {
	args := []string{"-p", req.Message, "--json", "--no-cron-tick"}
	if resume && req.SessionID != "" {
		args = append(args, "-resume", req.SessionID)
	}
	if req.Yolo {
		args = append(args, "--yolo")
	}
	if req.Approval != nil {
		args = append(args, "--approval-stdin")
	}

	// Same ctx-derived deadline for caller cancellation (/cancel) and
	// the per-attempt timeout: both land in the kill-tree path.
	runCtx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()

	cmd := exec.Command(e.cfg.SeekPath, args...)
	cmd.Dir = req.Dir
	prepareChild(cmd) // platform: process group / kill-tree setup

	// stdout streams through an io.Pipe into a line reader. cmd.Stdout
	// must stay a plain io.Writer, NOT StdoutPipe: StdoutPipe forbids
	// calling Wait before reads complete, and the timeout/kill pattern
	// below needs Wait running concurrently. io.Pipe is synchronous —
	// when cmd.Wait returns, os/exec's copy goroutine has handed every
	// byte to our reader, so closing pw afterwards surfaces EOF and
	// flushes a possibly unterminated final line (same "parse the
	// trailing partial" semantics the batch reader had).
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	var stderr lockedBuf
	cmd.Stderr = &stderr

	var (
		stdinW *os.File // decision line writer; nil without an approval router
		stdinR *os.File // child's end of that pipe, closed with the attempt
		decMu  sync.Mutex
	)
	closeApprovalPipe := func() {
		decMu.Lock()
		defer decMu.Unlock()
		if stdinW == nil {
			return
		}
		stdinW.Close()
		stdinR.Close()
		stdinW, stdinR = nil, nil // a late decision is a no-op, never a double close
	}
	if req.Approval != nil {
		r, w, err := os.Pipe()
		if err != nil {
			pr.Close()
			pw.Close()
			return attemptOutcome{retryable: false},
				fmt.Errorf("controlplane: exec: approval pipe: %w", err)
		}
		stdinR, stdinW = r, w
		// A plain *os.File: os/exec hands the fd to the child directly
		// instead of pumping it from a goroutine.
		cmd.Stdin = r
	}
	writeDecision := func(reqID, decision string) {
		b, err := json.Marshal(struct {
			Type     string `json:"type"`
			ReqID    string `json:"req_id"`
			Decision string `json:"decision"`
		}{"approval_decision", reqID, decision})
		if err != nil {
			return
		}
		decMu.Lock()
		defer decMu.Unlock()
		if stdinW == nil { // attempt already settled: the child is gone
			return
		}
		_, _ = stdinW.Write(append(b, '\n'))
	}

	// Wrap the observer: register the delivery handle first, then pass
	// the event on. Emit's red line still holds — the wrapped callback
	// adds side effects, never result state.
	var obs func(RunEvent)
	if req.Approval != nil {
		router := req.Approval
		obs = func(ev RunEvent) {
			if ev.Type == "approval_request" && ev.ReqID != "" {
				id := ev.ReqID
				router.Route(id, func(d string) { writeDecision(id, d) })
			}
			if emit != nil {
				emit(ev)
			}
		}
	} else {
		obs = emit
	}

	sink := &streamSink{emit: obs}

	if startErr := cmd.Start(); startErr != nil {
		pr.Close()
		pw.Close()
		closeApprovalPipe()
		// Deterministic (binary missing, bad Dir) — a rerun changes
		// nothing. Named with the seek path so the reply says it.
		return attemptOutcome{retryable: false}, fmt.Errorf("controlplane: exec %s: %w", e.cfg.SeekPath, startErr)
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		br := bufio.NewReader(pr)
		for {
			line, err := br.ReadString('\n')
			if len(line) > 0 {
				sink.line(line)
			}
			if err != nil {
				return
			}
		}
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	// settle runs once Wait has returned: the child is gone and every
	// stdout byte is already inside the reader (synchronous pipe), so
	// closing pw lets the reader see EOF, fold the trailing partial
	// line if any, and exit before the outcome snapshot. The approval
	// pipe closes here too — that is what unblocks (with EPIPE) a
	// decision racing the child's exit, and what lets the parent reap
	// the fd.
	settle := func() seekOutcome {
		pw.Close()
		closeApprovalPipe()
		<-readerDone
		return sink.snapshot()
	}
	// One exit path for both success and failure: withdraw the cards
	// this attempt filed (nobody decided them; the child is gone) so a
	// browser can never be left with a prompt whose pipe is closed. The
	// router keeps its own id list, so a plain run never touches it and
	// a self-heal rerun starts with a clean slate. The pipe teardown is
	// deferred as well as called from settle: the kill-grace expiry path
	// walks away without settling, and a leaked fd pair must not outlive
	// the attempt. Both calls are idempotent (nil-ed under decMu).
	defer closeApprovalPipe()
	if req.Approval != nil {
		defer req.Approval.Cancel()
	}

	var waitErr error
	killed := false
	var out seekOutcome
	select {
	case waitErr = <-waitDone:
		out = settle()
	case <-runCtx.Done():
		killed = true
		killTree(cmd)
		select {
		case waitErr = <-waitDone:
			out = settle()
		case <-time.After(execKillWaitGrace):
			waitErr = errExecWaitGrace
			// Pipes still held by a Setsid'd grandchild (seek's own
			// bash tool does this) can block the pipe copy forever;
			// same accepted posture as internal/tools/bash — the
			// SIGKILL is already out, report and move on. The reader
			// may deliver more lines after we walk away; the snapshot
			// takes the sink's lock so the race is contained.
			out = sink.snapshot()
		}
	}

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

// seekOutcome is the distilled JSONL of one run. replyBuf accumulates
// the non-reasoning text_delta stream; snapshot materialises it into
// reply so the field stays a plain value.
type seekOutcome struct {
	reply       string
	sessionID   string
	sawAgentEnd bool
	errLine     string // the runJSON `error` event, if any
	lines       int    // parseable JSONL lines seen (0 = empty stdout)

	replyBuf strings.Builder
}

// fold aggregates one parsed line — the SINGLE aggregation step shared
// by the batch reader (parseSeekJSONL) and the streaming sink, so the
// two cannot drift.
func (o *seekOutcome) fold(ev RunEvent) {
	o.lines++
	switch ev.Type {
	case "text_delta":
		if !ev.Reasoning {
			o.replyBuf.WriteString(ev.Delta)
		}
	case "agent_end":
		o.sawAgentEnd = true
		o.sessionID = ev.SessionID
	case "error":
		o.errLine = ev.Error
	}
}

// parseRunEvent decodes one JSONL line into a RunEvent. ok=false on
// malformed JSON — the caller skips it (uncounted, unemitted), so a
// seek bug cannot lose a completed run's agent_end.
func parseRunEvent(line string) (RunEvent, bool) {
	var ev RunEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return RunEvent{}, false
	}
	return ev, true
}

// streamSink folds the live JSONL stream into the same seekOutcome the
// batch parser produces, handing each parseable line to emit as it
// lands. The mutex exists for the kill-grace path only: the reader may
// still be folding when the attempt walks away with a snapshot (the
// normal path joins the reader via its done channel first).
type streamSink struct {
	emit func(RunEvent)
	mu   sync.Mutex
	out  seekOutcome
}

// line handles one raw stdout line (delimiter included): empty and
// malformed lines are skipped exactly like parseSeekJSONL. fold runs
// under the lock; emit fires after it, off the lock, with a value copy
// — the callback can neither deadlock the reader nor touch final
// state (the RunStream red line).
func (s *streamSink) line(raw string) {
	line := strings.TrimRight(raw, "\r\n")
	if line == "" {
		return
	}
	ev, ok := parseRunEvent(line)
	if !ok {
		return
	}
	s.mu.Lock()
	s.out.fold(ev)
	s.mu.Unlock()
	if s.emit != nil {
		s.emit(ev)
	}
}

// snapshot returns the aggregated outcome with reply materialised.
func (s *streamSink) snapshot() seekOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.out // copies replyBuf's bookkeeping; only String() is called on the copy, which is copy-safe
	out.reply = s.out.replyBuf.String()
	return out
}

// parseSeekJSONL walks the -p --json stdout. Malformed lines are
// skipped (a seek bug must not lose a completed run's agent_end);
// `lines` counts only parseable ones so "empty stdout" stays
// distinguishable from "garbage stdout" for the triage above.
// Implemented on the same streamSink fold the live reader uses.
func parseSeekJSONL(data []byte) seekOutcome {
	s := &streamSink{}
	for line := range strings.SplitSeq(string(data), "\n") {
		s.line(line)
	}
	return s.snapshot()
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
