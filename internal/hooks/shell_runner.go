// ShellRunner is the user-configurable shell-hook implementation of
// the Registry interfaces (PRD v3 pillar B / feature-shell-hooks).
//
// It implements PreToolUseHook + the four observer interfaces in one
// struct so a single Registry.Register call wires everything. Per PRD
// §3.2:
//
//   - pre_tool   → PreToolUseHook (exit code ≠ 0 = deny, stdout = reason)
//   - post_tool  → PostToolUseObserver (no return value; result logged)
//   - pre_prompt → PreTurnObserver (deliberately NOT PrePromptHook —
//     PRD §2.1 + §3.2 forbid shell stdout from entering the prompt
//     byte sequence)
//   - session_start → SessionStartObserver
//   - session_end   → SessionEndObserver
//
// Note: ShellRunner deliberately lives in package hooks (not
// internal/hooksconfig) because it implements the Registry interfaces
// defined here. The CONFIG loading (toml parse, sha256, trust, audit
// log) lives in internal/hooksconfig — this file only consumes
// hooksconfig.Config and runs commands.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/hooksconfig"
)

const (
	// envPrefix is the wire-format prefix every shell hook env var
	// starts with. Per PRD §3.3.
	envPrefix = "SEEK_"

	// resultBudget caps how many bytes of tool result we hand to a
	// post_tool hook via SEEK_TOOL_RESULT. Matches PRD §3.3.
	resultBudget = 4096

	// truncationMarker is appended to SEEK_TOOL_RESULT when the
	// original result exceeded resultBudget. Documented wire format
	// — third-party tooling may match on it.
	truncationMarker = " [truncated]"
)

// ShellRunner ties a hooksconfig.Config to the Registry interfaces.
// It is the only file in the codebase that calls `exec.CommandContext(
// "bash", "-c", ...)` for hooks.
//
// The runner is stateless across calls except for:
//
//   - cfg: the merged hook config snapshot, set at construction time.
//     ShellRunner is NOT hot-reloadable on purpose — a config change
//     ought to be visible at startup so users see startup warnings
//     for any newly-broken hook, and so cache-relevant decisions
//     (which hooks were active for this turn) are stable.
//
//   - sessionID / projectID / projectPath / version: stamped at
//     SessionStart for use in environment variables.
//
//   - audit log: a thread-safe AuditLog handle used by every dispatch
//     path. Nil-safe — when nil the audit writes are no-ops.
type ShellRunner struct {
	cfg   hooksconfig.Config
	audit *hooksconfig.AuditLog

	// runtime stamps, populated by OnSessionStart (and constructor
	// for tests / non-TUI launches). Protected by mu because
	// pre_tool can fire concurrently with session_end shutdown.
	mu          sync.RWMutex
	version     string
	sessionID   string
	projectID   string
	projectPath string

	// onStatus is an optional callback for surfacing per-hook events
	// (denials, timeouts, slow-hook warnings) to the TUI status bar.
	// Nil-safe.
	onStatus func(StatusEvent)

	// now / exec are seams for tests so they can drive time forward
	// without sleeping and exec without actually shelling out.
	now     func() time.Time
	execCmd func(ctx context.Context, command, cwd string, env []string) (stdout string, exitCode int, err error)
}

// StatusEvent is the payload of the onStatus callback. Carries enough
// for the TUI to render "hook denied" / "hook slow" / "hook timeout"
// banners without re-querying the audit log. Wire format INSIDE the
// process — fields can change between minor versions; the audit log
// JSON is the public wire surface.
type StatusEvent struct {
	Event      string // pre_tool / post_tool / ...
	Hook       string // hook name
	Tool       string // tool name (pre/post_tool only)
	Denied     bool
	TimedOut   bool
	DurationMs int64
	Reason     string // free-text (stdout for deny; bash error for fail)
}

// Option configures a ShellRunner at construction.
type Option func(*ShellRunner)

// WithAuditLog plugs in the audit writer. nil means "no auditing".
func WithAuditLog(a *hooksconfig.AuditLog) Option {
	return func(r *ShellRunner) { r.audit = a }
}

// WithStatusCallback wires status events to the TUI. nil = no UI.
func WithStatusCallback(fn func(StatusEvent)) Option {
	return func(r *ShellRunner) { r.onStatus = fn }
}

// WithVersion stamps the SEEK_VERSION env var. Optional.
func WithVersion(v string) Option {
	return func(r *ShellRunner) { r.version = v }
}

// WithProjectContext sets the project metadata used in env vars before
// the first SessionStart. Useful for non-TUI launches (CLI -p) where
// we want hooks to see consistent project context.
func WithProjectContext(projectID, projectPath string) Option {
	return func(r *ShellRunner) {
		r.projectID = projectID
		r.projectPath = projectPath
	}
}

// WithExecutor swaps the bash exec for tests.
func WithExecutor(fn func(ctx context.Context, command, cwd string, env []string) (string, int, error)) Option {
	return func(r *ShellRunner) { r.execCmd = fn }
}

// WithClock swaps time.Now for tests.
func WithClock(fn func() time.Time) Option {
	return func(r *ShellRunner) { r.now = fn }
}

// NewShellRunner constructs a runner from a merged config. Call
// Register(runner) once on the agent's Registry. nil-safe Config
// (empty Config{}) → all dispatch paths no-op without erroring.
func NewShellRunner(cfg hooksconfig.Config, opts ...Option) *ShellRunner {
	r := &ShellRunner{cfg: cfg, now: time.Now, execCmd: defaultExec}
	for _, o := range opts {
		o(r)
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.execCmd == nil {
		r.execCmd = defaultExec
	}
	return r
}

// HasHooks reports whether at least one event in cfg has at least one
// (non-skipped) hook. Used by main.go to decide whether to even
// Register — saves a Registry slot when the user has no hooks.
func (r *ShellRunner) HasHooks() bool {
	if r == nil {
		return false
	}
	for _, h := range r.cfg.All() {
		if h.SkipReason == "" {
			return true
		}
	}
	return false
}

// Config returns the runner's snapshot. Used by `seek hooks list` so
// the CLI reads the same merged view the runner dispatches against.
func (r *ShellRunner) Config() hooksconfig.Config { return r.cfg }

// ---- env var building ----

// baseEnv builds the hook process environment.
//
// Deliberately NOT scrubbed through internal/childenv, unlike the bash
// tool / MCP / LSP spawn paths. Those run code seek does not control —
// model-chosen commands and third-party binaries. A hook command is the
// USER's own shell snippet from their own settings file: same trust
// level as their .bashrc, and the whole point of a hook is often to
// reach a credentialed service (post to a webhook, call gh, hit an
// internal API). Scrubbing here would break that with no attacker
// removed from the picture — the person who wrote the hook already has
// the environment.
//
// If this ever changes, it needs a passthrough channel first (the bash
// tool has config.BashEnvPassthrough); silently withholding variables
// from user-authored hooks is a worse failure than the leak it prevents.
func (r *ShellRunner) baseEnv(event string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	env := os.Environ()
	env = append(env,
		envPrefix+"VERSION="+r.version,
		envPrefix+"SESSION_ID="+r.sessionID,
		envPrefix+"PROJECT_ID="+r.projectID,
		envPrefix+"PROJECT_PATH="+r.projectPath,
		envPrefix+"EVENT="+event,
	)
	return env
}

// argsJSON renders json.RawMessage to a single-line compact string for
// SEEK_TOOL_ARGS_JSON. Per PRD §3.3 ("紧凑、单行"). Returns "" for nil
// input — the env var contract says missing = empty string.
func argsJSON(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	// Re-encode to guarantee a single-line compact form even if the
	// model produced indented JSON.
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		// Pass through opaquely — better than dropping the field.
		return strings.TrimSpace(string(args))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return strings.TrimSpace(string(args))
	}
	return string(b)
}

func truncatedResult(result string) string {
	if len(result) <= resultBudget {
		return result
	}
	return result[:resultBudget] + truncationMarker
}

// ---- dispatch helpers ----

// runHook is the single point that shells out. It applies the per-hook
// timeout, captures stdout (used as deny reason for pre_tool), and
// writes the audit row. Returns (stdout, exitCode, timedOut).
//
// Callers MUST short-circuit on h.SkipReason before reaching here —
// the responsibility lives at each event entry point because the
// "what does skip mean for THIS event" decision differs (pre_tool
// skips silently, observer paths log it). Reaching this function with
// SkipReason set is a programming error; we still defend-in-depth by
// auditing and returning a benign code.
func (r *ShellRunner) runHook(ctx context.Context, h hooksconfig.Hook, tool string, env []string) (string, int, bool) {
	if h.SkipReason != "" {
		r.writeAudit(hooksconfig.AuditEntry{
			Event:    h.Event,
			Hook:     h.Name,
			Tool:     tool,
			ExitCode: 0, // success-equivalent so observer paths don't trip
			Reason:   "skipped: " + h.SkipReason,
		})
		return "", 0, false
	}
	timeout := time.Duration(h.EffectiveTimeoutMs()) * time.Millisecond
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r.mu.RLock()
	cwd := r.projectPath
	r.mu.RUnlock()
	if cwd == "" {
		if d, err := os.Getwd(); err == nil {
			cwd = d
		}
	}
	start := r.now()
	stdout, code, err := r.execCmd(cctx, h.Command, cwd, env)
	dur := r.now().Sub(start)
	timedOut := cctx.Err() == context.DeadlineExceeded
	reason := ""
	if timedOut {
		reason = fmt.Sprintf("timeout after %dms", h.EffectiveTimeoutMs())
	} else if err != nil && code == 0 {
		// exec.Command errors that didn't translate to an exit code
		// (e.g. bash missing) — flag distinctly so audit consumers
		// can tell "exit 0 fine" from "exec failed".
		reason = err.Error()
		code = -1
	}
	r.writeAudit(hooksconfig.AuditEntry{
		Event:      h.Event,
		Hook:       h.Name,
		Tool:       tool,
		DurationMs: dur.Milliseconds(),
		ExitCode:   code,
		Denied:     h.Event == hooksconfig.EventPreTool && code != 0,
		Reason:     reason,
	})
	return stdout, code, timedOut
}

func (r *ShellRunner) writeAudit(e hooksconfig.AuditEntry) {
	if r.audit == nil {
		return
	}
	r.mu.RLock()
	e.SessionID = r.sessionID
	r.mu.RUnlock()
	if e.TS == "" {
		e.TS = r.now().UTC().Format(time.RFC3339)
	}
	_ = r.audit.Append(e)
}

func (r *ShellRunner) emitStatus(ev StatusEvent) {
	if r.onStatus == nil {
		return
	}
	r.onStatus(ev)
}

// ---- PreToolUseHook ----

// OnPreToolUse runs every matching pre_tool hook in declaration order.
// First non-zero exit OR timeout short-circuits with Deny set to a
// pasted-together explanation (PRD §3.2). Hooks that don't match the
// tool name are skipped. Errors that aren't "bash returned non-zero"
// — i.e., exec failures like bash missing — surface as deny too with
// a reason indicating the exec failure (defensive: a broken bash on
// PATH shouldn't silently let traffic through past a hook the user
// believes is gating).
func (r *ShellRunner) OnPreToolUse(ctx context.Context, in PreToolUseIn) (PreToolUseOut, error) {
	for _, h := range r.cfg.PreTool {
		if !h.MatchTool(in.Name) {
			continue
		}
		if h.SkipReason != "" {
			// Static-check failed at startup. PRD §3.4: failed hooks
			// are skipped, not converted to deny — the user's startup
			// banner already warned them. Record an audit row noting
			// the skip and move on (next hook still runs).
			r.writeAudit(hooksconfig.AuditEntry{
				Event:    hooksconfig.EventPreTool,
				Hook:     h.Name,
				Tool:     in.Name,
				ExitCode: -1,
				Reason:   "skipped: " + h.SkipReason,
			})
			continue
		}
		env := r.baseEnv(hooksconfig.EventPreTool)
		env = append(env,
			envPrefix+"TOOL_NAME="+in.Name,
			envPrefix+"TOOL_ARGS_JSON="+argsJSON(in.Args),
		)
		stdout, code, timedOut := r.runHook(ctx, h, in.Name, env)
		if timedOut {
			msg := fmt.Sprintf("hook %q timed out after %dms — treating as deny", h.Name, h.EffectiveTimeoutMs())
			r.emitStatus(StatusEvent{
				Event:      hooksconfig.EventPreTool,
				Hook:       h.Name,
				Tool:       in.Name,
				Denied:     true,
				TimedOut:   true,
				DurationMs: int64(h.EffectiveTimeoutMs()),
				Reason:     msg,
			})
			return PreToolUseOut{Deny: msg}, nil
		}
		if code != 0 {
			reason := strings.TrimSpace(stdout)
			if reason == "" {
				reason = fmt.Sprintf("hook %q exited with code %d", h.Name, code)
			}
			r.emitStatus(StatusEvent{
				Event:  hooksconfig.EventPreTool,
				Hook:   h.Name,
				Tool:   in.Name,
				Denied: true,
				Reason: reason,
			})
			return PreToolUseOut{Deny: reason}, nil
		}
	}
	return PreToolUseOut{}, nil
}

// ---- PostToolUseObserver ----

// OnPostToolUse runs every matching post_tool hook. Observer-only:
// stdout / exit code go to the audit log, NEVER into the prompt
// history (PRD §2.1 hard rule). Failure here is silent by design —
// post_tool is the "tell something about what happened" event, not a
// gate.
func (r *ShellRunner) OnPostToolUse(ctx context.Context, ev PostToolUseEvent) {
	for _, h := range r.cfg.PostTool {
		if !h.MatchTool(ev.Name) {
			continue
		}
		env := r.baseEnv(hooksconfig.EventPostTool)
		exitOK := "1"
		if ev.Err != nil {
			exitOK = "0"
		}
		env = append(env,
			envPrefix+"TOOL_NAME="+ev.Name,
			envPrefix+"TOOL_ARGS_JSON="+argsJSON(ev.Args),
			envPrefix+"TOOL_RESULT="+truncatedResult(ev.Result),
			envPrefix+"TOOL_EXIT_OK="+exitOK,
		)
		_, code, timedOut := r.runHook(ctx, h, ev.Name, env)
		if timedOut || code != 0 {
			r.emitStatus(StatusEvent{
				Event:    hooksconfig.EventPostTool,
				Hook:     h.Name,
				Tool:     ev.Name,
				TimedOut: timedOut,
				Reason:   fmt.Sprintf("exit=%d timedOut=%v", code, timedOut),
			})
		}
	}
}

// ---- PreTurnObserver (pre_prompt) ----

// OnPreTurn fires before the model sees a new user turn. Deliberately
// observer-only — see file-level comment. Hook stdout never reaches
// the prompt because there's no plumbing in PreTurnObserver for it.
func (r *ShellRunner) OnPreTurn(ctx context.Context, ev PreTurnEvent) {
	for _, h := range r.cfg.PrePrompt {
		env := r.baseEnv(hooksconfig.EventPrePrompt)
		_, code, timedOut := r.runHook(ctx, h, "", env)
		if timedOut || code != 0 {
			r.emitStatus(StatusEvent{
				Event:    hooksconfig.EventPrePrompt,
				Hook:     h.Name,
				TimedOut: timedOut,
				Reason:   fmt.Sprintf("exit=%d timedOut=%v", code, timedOut),
			})
		}
	}
}

// ---- SessionStartObserver ----

// OnSessionStart stamps runtime metadata onto the runner so future
// hook dispatches see SEEK_SESSION_ID etc., then runs session_start
// hooks.
func (r *ShellRunner) OnSessionStart(ctx context.Context, ev SessionStartEvent) {
	r.mu.Lock()
	r.sessionID = ev.ID
	if r.projectPath == "" {
		r.projectPath = ev.CWD
	}
	r.mu.Unlock()
	for _, h := range r.cfg.SessionStart {
		env := r.baseEnv(hooksconfig.EventSessionStart)
		_, code, timedOut := r.runHook(ctx, h, "", env)
		if timedOut || code != 0 {
			r.emitStatus(StatusEvent{
				Event:    hooksconfig.EventSessionStart,
				Hook:     h.Name,
				TimedOut: timedOut,
				Reason:   fmt.Sprintf("exit=%d timedOut=%v", code, timedOut),
			})
		}
	}
}

// ---- SessionEndObserver ----

// OnSessionEnd runs the session_end hooks then closes the audit log.
// Per PRD §3.4 we run synchronously (so the audit write happens
// before process exit). Audit close is best-effort.
func (r *ShellRunner) OnSessionEnd(ctx context.Context, ev SessionEndEvent) {
	for _, h := range r.cfg.SessionEnd {
		env := r.baseEnv(hooksconfig.EventSessionEnd)
		_, code, timedOut := r.runHook(ctx, h, "", env)
		if timedOut || code != 0 {
			r.emitStatus(StatusEvent{
				Event:    hooksconfig.EventSessionEnd,
				Hook:     h.Name,
				TimedOut: timedOut,
				Reason:   fmt.Sprintf("exit=%d timedOut=%v", code, timedOut),
			})
		}
	}
}

// Close flushes the audit log. Optional — main.go calls this in
// `defer`, but the tests use it to assert log content.
func (r *ShellRunner) Close() error {
	if r == nil {
		return nil
	}
	return r.audit.Close()
}

// ---- default exec ----

// defaultExec runs `bash -c <command>` with the given cwd / env and
// returns (stdout, exitCode, error). exitCode follows POSIX
// convention: 0 on success, 1..255 from the process, -1 when exec
// itself failed (returned in err).
//
// Note: stderr is captured but discarded — only stdout becomes the
// deny reason for pre_tool. Stderr lands in the audit "reason" field
// only on exec failure. This matches the PRD's explicit contract
// ("stdout 进 deny reason") and avoids accidentally surfacing a hook
// author's noisy logging as the LLM-facing denial text.
func defaultExec(ctx context.Context, command, cwd string, env []string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	cmd.Env = env
	// Empty stdin: PRD §3.4 ("stdin：空"). Without this, exec inherits
	// the parent's stdin which on TUI is the terminal — a hook that
	// reads stdin would block the agent loop indefinitely.
	cmd.Stdin = strings.NewReader("")
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &limitedWriter{w: &stdoutBuf, max: 64 * 1024}
	cmd.Stderr = &limitedWriter{w: &stderrBuf, max: 64 * 1024}
	err := cmd.Run()
	if err == nil {
		return strings.TrimRight(stdoutBuf.String(), "\n"), 0, nil
	}
	// Context cancelled (timeout) → bubble up; the caller checks
	// ctx.Err() and surfaces "timeout" specifically.
	if ctx.Err() != nil {
		return strings.TrimRight(stdoutBuf.String(), "\n"), -1, ctx.Err()
	}
	// ExitError carries an explicit exit code.
	if ee, ok := err.(*exec.ExitError); ok {
		return strings.TrimRight(stdoutBuf.String(), "\n"), ee.ExitCode(), nil
	}
	// Anything else (bash missing, fork failed) → -1 + raw err.
	return strings.TrimRight(stdoutBuf.String(), "\n"), -1, err
}

// limitedWriter caps the bytes captured from a hook so a
// runaway-stdout hook can't blow process memory. Excess bytes are
// silently dropped — by the time we hit the limit the user's hook is
// already pathological.
type limitedWriter struct {
	w   io.Writer
	max int
	n   int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n >= l.max {
		return len(p), nil
	}
	room := l.max - l.n
	if len(p) > room {
		n, _ := l.w.Write(p[:room])
		l.n += n
		return len(p), nil
	}
	n, err := l.w.Write(p)
	l.n += n
	return n, err
}
