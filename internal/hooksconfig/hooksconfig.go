// Package hooksconfig parses, validates, and merges the user/project
// hooks.toml files that configure seek's shell-hook subsystem (PRD v3
// pillar B / feature-shell-hooks).
//
// Why a dedicated package: configuration ingestion is the entire surface
// where user-provided strings can hit seek's process — wrap it in one
// place so the trust gate, sha256 fingerprinting, syntax check, and
// merge order are all auditable from one file.
//
// What lives here:
//
//   - File schema (Hook / Config) and TOML decoding via
//     github.com/BurntSushi/toml.
//   - Load() / LoadProject() readers that turn one file path into a
//     validated Config; ENOENT is NOT an error — having no hooks is the
//     steady state.
//   - Merge() that joins user + project configs in PRD-mandated order
//     (project first, user second — see PRD §3.1).
//   - Static syntax validation via `bash -n` (PRD §3.4) so hooks with
//     broken shell never get a chance to run.
//   - Sha256Hex() for the trust-on-change flow.
//
// What does NOT live here: the actual execution of `bash -c` lives in
// internal/hooks/shell_runner.go. trust prompting / audit log writing
// live in trust.go / audit.go in this package but as separate units.
// This file is purely about turning bytes into a validated in-memory
// data structure.
package hooksconfig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Source identifies which file a hook came from. Used by `seek hooks
// list` to annotate each row and by Merge to assign ordering. Wire
// format (the string form) is human-facing in CLI output, so any
// rename here is a UX change.
type Source string

const (
	SourceUser    Source = "user"
	SourceProject Source = "project"
)

// Event names. Wire format — these strings appear in toml keys, in
// `seek hooks check --event`, and in audit log JSON. NEVER change the
// spelling once shipped without a migration story.
const (
	EventPreTool      = "pre_tool"
	EventPostTool     = "post_tool"
	EventPrePrompt    = "pre_prompt"
	EventSessionStart = "session_start"
	EventSessionEnd   = "session_end"

	// P3 observer-only events (feature-orchestration §6 D15 /
	// feature-hooks-injection §3.3). No deny surface: timeout and
	// non-zero exit only audit + status; `pre_tool` above remains the
	// ONLY event whose exit code can veto anything.
	EventSubagentStart = "subagent_start"
	EventSubagentStop  = "subagent_stop"
	EventStop          = "stop"
	EventPreCompact    = "pre_compact"
	EventNotification  = "notification"

	// EventPrePromptInject (M-O3.2 / feature-hooks-injection §3.4+§4.5)
	// is the one EXCEPTION to "shell stdout never enters the prompt":
	// an explicitly opt-in per-hook authorization whose stdout is
	// wrapped in a <context type="hook"> user message and injected at
	// the frontier via PrePromptHook.Prepend — the same single seam the
	// memory subsystem uses. The authorization the user signs is a
	// CONTENT-trust decision (let an external process's output reach
	// the model's view), not a cache one: channel D is byte-safe by
	// construction (§2.3 commit-once). Failure (non-zero exit /
	// timeout) skips that hook's injection — it never blocks the turn.
	EventPrePromptInject = "pre_prompt_inject"
)

// Byte budgets for EventPrePromptInject stdout (feature-hooks-injection
// §4.5): default conservative because injected bytes are committed to
// history permanently (token cost, not cache cost); ceiling hard-stops
// a runaway hook from flooding the transcript every turn.
const (
	DefaultMaxBytes   = 2048
	MaxBytesCeiling   = 8192
)

// DefaultTimeoutMs is the per-hook wall-clock timeout if the toml
// entry omits timeout_ms. Per PRD §3.4.
const DefaultTimeoutMs = 5000

// Match is the per-tool selector for pre_tool / post_tool hooks.
// `Tool` is a glob in the simple sense: "*" matches every tool name;
// any other value is an exact-string match. v3 deliberately does NOT
// support shell-style glob (the PRD §2 "不做什么" list explicitly defers
// `tool_args_match` to v4) — single-name + "*" is enough to cover the
// motivating use cases (kubectl deny, edit-only lint) without inviting
// permission-bypass-via-glob surprises.
type Match struct {
	Tool string `toml:"tool"`
}

// Hook is one entry from hooks.toml after decoding + merge. The Source
// + Event fields are filled by the loader (toml doesn't carry them),
// the rest map directly to toml keys.
type Hook struct {
	// Name is the human-readable identifier shown in status bar /
	// audit log / `seek hooks list`. Required.
	Name string `toml:"name"`

	// Command is the shell string passed to `bash -c`. Required.
	Command string `toml:"command"`

	// Match scopes pre_tool / post_tool hooks to a single tool or
	// "*" for all. Ignored for non-tool events. Empty Match{} means
	// match-all.
	Match Match `toml:"match"`

	// TimeoutMs caps wall-clock duration. <=0 means use default
	// (DefaultTimeoutMs). The pre_tool path treats timeout as deny
	// (PRD §3.4); observer paths just log it.
	TimeoutMs int `toml:"timeout_ms"`

	// MaxBytes caps how many stdout bytes a pre_prompt_inject hook may
	// contribute to the prompt (feature-hooks-injection §4.5). Excess
	// is hard-truncated and marked " [truncated]". Meaningful ONLY for
	// pre_prompt_inject; ignored by every other event. <=0 means
	// DefaultMaxBytes; values above MaxBytesCeiling clamp to it.
	MaxBytes int `toml:"max_bytes"`

	// SkipReason, when non-empty, marks this hook as static-check
	// failed (bash -n rejected the command). Such hooks are kept in
	// the Config so `seek hooks list` can show them with the failure
	// reason, but ShellRunner skips them at dispatch time. Wire-format
	// hook layer ALWAYS checks SkipReason before invoking.
	SkipReason string `toml:"-"`

	// Source + Event are populated by Load/Merge; never written by
	// the user.
	Source Source `toml:"-"`
	Event  string `toml:"-"`
}

// EffectiveTimeoutMs returns TimeoutMs if positive, otherwise the
// package default. Centralised so call sites can't disagree.
func (h Hook) EffectiveTimeoutMs() int {
	if h.TimeoutMs > 0 {
		return h.TimeoutMs
	}
	return DefaultTimeoutMs
}

// EffectiveMaxBytes returns MaxBytes if positive, clamped to
// MaxBytesCeiling; <=0 means DefaultMaxBytes. Centralised so call sites
// can't disagree (mirrors EffectiveTimeoutMs).
func (h Hook) EffectiveMaxBytes() int {
	if h.MaxBytes <= 0 {
		return DefaultMaxBytes
	}
	if h.MaxBytes > MaxBytesCeiling {
		return MaxBytesCeiling
	}
	return h.MaxBytes
}

// MatchTool returns true when this hook should fire for the given tool
// name. Empty / "*" matches everything; any other value is exact match.
// Tool-event hooks (pre_tool, post_tool) are the only callers; other
// events ignore Match entirely.
func (h Hook) MatchTool(toolName string) bool {
	t := strings.TrimSpace(h.Match.Tool)
	if t == "" || t == "*" {
		return true
	}
	return t == toolName
}

// Config holds the decoded hooks.toml content. Fields are arrays so
// declaration order (user-meaningful: PRD §3.1 says "按声明顺序串行")
// survives the round-trip. Tag names mirror the schema in the PRD §3.1
// example.
type Config struct {
	PreTool      []Hook `toml:"pre_tool"`
	PostTool     []Hook `toml:"post_tool"`
	PrePrompt    []Hook `toml:"pre_prompt"`
	SessionStart []Hook `toml:"session_start"`
	SessionEnd   []Hook `toml:"session_end"`

	// P3 observer-only events (feature-orchestration §6 D15). Same
	// Hook schema; Match/timeout carry over unchanged. Match is
	// meaningless for these (no tool name in scope) and is ignored at
	// dispatch — kept uniform so the loader / list / trust machinery
	// needs no per-event special cases.
	SubagentStart []Hook `toml:"subagent_start"`
	SubagentStop  []Hook `toml:"subagent_stop"`
	Stop          []Hook `toml:"stop"`
	PreCompact    []Hook `toml:"pre_compact"`
	Notification  []Hook `toml:"notification"`

	// PrePromptInject is the authorized stdout-injection event
	// (M-O3.2 / feature-hooks-injection §4.5). Opt-in, no defaults.
	// Match is meaningless here (ignored at dispatch, same as the
	// observer events); MaxBytes carries the per-hook byte budget.
	PrePromptInject []Hook `toml:"pre_prompt_inject"`
}

// IsEmpty reports whether this Config registers any hooks at all.
// Useful so the wiring layer can skip ShellRunner registration entirely
// when both user and project files are missing.
func (c Config) IsEmpty() bool {
	return len(c.PreTool) == 0 && len(c.PostTool) == 0 && len(c.PrePrompt) == 0 &&
		len(c.SessionStart) == 0 && len(c.SessionEnd) == 0 &&
		len(c.SubagentStart) == 0 && len(c.SubagentStop) == 0 &&
		len(c.Stop) == 0 && len(c.PreCompact) == 0 && len(c.Notification) == 0 &&
		len(c.PrePromptInject) == 0
}

// All flattens the Config into a single slice for callers that want to
// iterate hooks regardless of event. Order = PreTool, PostTool,
// PrePrompt, SessionStart, SessionEnd, then the P3 observer events —
// matches the order a `seek hooks list` would print and is stable
// across runs. Each Hook's .Event field carries the original event so
// the consumer can dispatch.
func (c Config) All() []Hook {
	out := make([]Hook, 0,
		len(c.PreTool)+len(c.PostTool)+len(c.PrePrompt)+len(c.SessionStart)+len(c.SessionEnd)+
			len(c.SubagentStart)+len(c.SubagentStop)+len(c.Stop)+len(c.PreCompact)+len(c.Notification)+
			len(c.PrePromptInject))
	out = append(out, c.PreTool...)
	out = append(out, c.PostTool...)
	out = append(out, c.PrePrompt...)
	out = append(out, c.SessionStart...)
	out = append(out, c.SessionEnd...)
	out = append(out, c.SubagentStart...)
	out = append(out, c.SubagentStop...)
	out = append(out, c.Stop...)
	out = append(out, c.PreCompact...)
	out = append(out, c.Notification...)
	out = append(out, c.PrePromptInject...)
	return out
}

// ErrEmptyHook is returned by Load when a hook is missing required
// fields (name / command). Callers display the message verbatim in
// startup warnings.
var ErrEmptyHook = errors.New("hooksconfig: hook missing required name or command")

// Load reads and decodes one hooks.toml file. The src argument
// records which side (user / project) the file came from — every Hook
// in the returned Config inherits it.
//
// ENOENT returns (Config{}, nil) — having no hooks file at all is the
// steady state and never an error.
//
// Decode errors return the underlying toml error; the caller should
// surface it to stderr but otherwise treat it as "no hooks loaded"
// (failure-degrade-not-block per umbrella §2.5).
func Load(path string, src Source) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("hooksconfig: open %s: %w", path, err)
	}
	defer f.Close()
	return Decode(f, src)
}

// Decode is the io.Reader variant of Load — handy for tests so they
// can pass in-memory bytes without touching a tempdir. Same contract:
// ENOENT-like errors are the caller's problem; this function only
// handles toml syntax.
func Decode(r io.Reader, src Source) (Config, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return Config{}, fmt.Errorf("hooksconfig: read: %w", err)
	}
	return DecodeBytes(body, src)
}

// DecodeBytes is the []byte variant. Used by Gate when it has already
// read the file (to compute sha256) and wants to decode without a
// second open.
func DecodeBytes(body []byte, src Source) (Config, error) {
	var c Config
	if err := toml.Unmarshal(body, &c); err != nil {
		return Config{}, fmt.Errorf("hooksconfig: parse: %w", err)
	}
	stampSourceEvent(&c.PreTool, src, EventPreTool)
	stampSourceEvent(&c.PostTool, src, EventPostTool)
	stampSourceEvent(&c.PrePrompt, src, EventPrePrompt)
	stampSourceEvent(&c.SessionStart, src, EventSessionStart)
	stampSourceEvent(&c.SessionEnd, src, EventSessionEnd)
	stampSourceEvent(&c.SubagentStart, src, EventSubagentStart)
	stampSourceEvent(&c.SubagentStop, src, EventSubagentStop)
	stampSourceEvent(&c.Stop, src, EventStop)
	stampSourceEvent(&c.PreCompact, src, EventPreCompact)
	stampSourceEvent(&c.Notification, src, EventNotification)
	stampSourceEvent(&c.PrePromptInject, src, EventPrePromptInject)
	return c, nil
}

func stampSourceEvent(hooks *[]Hook, src Source, event string) {
	for i := range *hooks {
		(*hooks)[i].Source = src
		(*hooks)[i].Event = event
	}
}

// Merge combines a project-level config and a user-level config into
// the order that ShellRunner will dispatch them in. PRD §3.1 says
// "项目级先执行，用户级后执行" — that ordering survives here. Within each
// side, declaration order is preserved.
//
// Note: the project config goes first ON PURPOSE — for `pre_tool` deny
// short-circuiting, the team's policy should fire before the user's
// personal audit hook, otherwise a slow user-level audit hook would
// run on traffic the project policy would have denied.
func Merge(project, user Config) Config {
	return Config{
		PreTool:       append(append([]Hook{}, project.PreTool...), user.PreTool...),
		PostTool:      append(append([]Hook{}, project.PostTool...), user.PostTool...),
		PrePrompt:     append(append([]Hook{}, project.PrePrompt...), user.PrePrompt...),
		SessionStart:  append(append([]Hook{}, project.SessionStart...), user.SessionStart...),
		SessionEnd:    append(append([]Hook{}, project.SessionEnd...), user.SessionEnd...),
		SubagentStart: append(append([]Hook{}, project.SubagentStart...), user.SubagentStart...),
		SubagentStop:  append(append([]Hook{}, project.SubagentStop...), user.SubagentStop...),
		Stop:          append(append([]Hook{}, project.Stop...), user.Stop...),
		PreCompact:    append(append([]Hook{}, project.PreCompact...), user.PreCompact...),
		Notification:  append(append([]Hook{}, project.Notification...), user.Notification...),
		PrePromptInject: append(append([]Hook{}, project.PrePromptInject...), user.PrePromptInject...),
	}
}

// Validate walks every hook and confirms the required fields are
// present. Returns the first failure as an error (so callers can print
// it as a startup warning) AND, in the *Config receiver variant, marks
// individual hooks with SkipReason so list-style commands can still
// show them annotated.
//
// Static `bash -n` checking is NOT done here — that's StaticCheck
// below, separated because it shells out and Validate is pure.
func Validate(c Config) error {
	for _, h := range c.All() {
		if strings.TrimSpace(h.Name) == "" || strings.TrimSpace(h.Command) == "" {
			return fmt.Errorf("%w: source=%s event=%s name=%q",
				ErrEmptyHook, h.Source, h.Event, h.Name)
		}
	}
	return nil
}

// SyntaxChecker is the seam StaticCheck uses to invoke `bash -n`. The
// real implementation calls exec.Command; tests override it so they
// don't need a working bash on the test machine. Returning a non-nil
// error MUST mean "bash rejected the syntax" — wrapping a missing-bash
// error here would mean every hook gets skipped on Windows, which is
// the OPPOSITE of what we want (Windows users with WSL bash should
// have their hooks validated; Windows users without bash get a single
// startup warning from CheckBashAvailable + all hooks running anyway is
// safe because exec.Command will surface the bash-missing error per-call).
type SyntaxChecker func(command string) error

// DefaultSyntaxChecker shells out to `bash -n -c <command>` and
// returns nil iff bash accepts the syntax. Output (stderr from bash)
// is returned in the error so the user sees what the syntax problem
// was.
func DefaultSyntaxChecker(command string) error {
	cmd := exec.Command("bash", "-n", "-c", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// StaticCheck runs the given syntax checker (use DefaultSyntaxChecker
// in production) against every hook in c and stamps SkipReason on the
// ones that fail. Returns the list of warnings as human-readable
// strings so the wiring layer can print them to stderr at startup.
//
// Per PRD §3.4: failed hooks are kept in the config (so `seek hooks
// list` can show "[skipped: <reason>]") but ShellRunner won't dispatch
// them.
func StaticCheck(c *Config, chk SyntaxChecker) []string {
	if chk == nil {
		chk = DefaultSyntaxChecker
	}
	var warnings []string
	check := func(hooks []Hook) {
		for i := range hooks {
			if hooks[i].Command == "" {
				continue
			}
			if err := chk(hooks[i].Command); err != nil {
				hooks[i].SkipReason = err.Error()
				warnings = append(warnings, fmt.Sprintf(
					"hooks: skipping %s/%s %q: %v",
					hooks[i].Source, hooks[i].Event, hooks[i].Name, err))
			}
		}
	}
	check(c.PreTool)
	check(c.PostTool)
	check(c.PrePrompt)
	check(c.SessionStart)
	check(c.SessionEnd)
	check(c.SubagentStart)
	check(c.SubagentStop)
	check(c.Stop)
	check(c.PreCompact)
	check(c.Notification)
	check(c.PrePromptInject)
	return warnings
}

// HookSummary describes one merged hook for `seek hooks list` and the
// /hooks TUI command. Kept structurally separate from Hook so the CLI
// formatter doesn't accidentally print internal fields (SkipReason
// when empty, etc.).
type HookSummary struct {
	Source     Source
	Event      string
	Name       string
	Command    string
	Tool       string // "*" or specific tool name
	TimeoutMs  int
	SkipReason string
	// MaxBytes is the effective stdout byte budget; meaningful only
	// for pre_prompt_inject rows (0 elsewhere, matching EffectiveMaxBytes
	// callers that check the event first).
	MaxBytes int
}

// Summarize returns a stable-ordered slice of HookSummary for display.
// Order = Event group (pre_tool → session_end, then the P3 observer
// events, pre_prompt_inject last) then declaration order within each
// event, with project hooks before user hooks (matching dispatch
// order).
func Summarize(c Config) []HookSummary {
	groups := []struct {
		event string
		hooks []Hook
	}{
		{EventPreTool, c.PreTool},
		{EventPostTool, c.PostTool},
		{EventPrePrompt, c.PrePrompt},
		{EventSessionStart, c.SessionStart},
		{EventSessionEnd, c.SessionEnd},
		{EventSubagentStart, c.SubagentStart},
		{EventSubagentStop, c.SubagentStop},
		{EventStop, c.Stop},
		{EventPreCompact, c.PreCompact},
		{EventNotification, c.Notification},
		{EventPrePromptInject, c.PrePromptInject},
	}
	var out []HookSummary
	for _, g := range groups {
		for _, h := range g.hooks {
			tool := strings.TrimSpace(h.Match.Tool)
			if tool == "" {
				tool = "*"
			}
			maxBytes := 0
			if g.event == EventPrePromptInject {
				maxBytes = h.EffectiveMaxBytes()
			}
			out = append(out, HookSummary{
				Source:     h.Source,
				Event:      g.event,
				Name:       h.Name,
				Command:    h.Command,
				Tool:       tool,
				TimeoutMs:  h.EffectiveTimeoutMs(),
				SkipReason: h.SkipReason,
				MaxBytes:   maxBytes,
			})
		}
	}
	return out
}

// SortByName sorts a HookSummary slice alphabetically by name. Used by
// `seek hooks list --by-name` (optional surface) — keeping this here
// means the CLI doesn't have to know the field name.
func SortByName(s []HookSummary) {
	sort.SliceStable(s, func(i, j int) bool {
		return s[i].Name < s[j].Name
	})
}
