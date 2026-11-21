// Command seek is the DeepSeek-first coding agent CLI.
//
// M3 wires in cache.Tracker (session-level prefix-cache stats), pricing
// (off-peak tier awareness + per-call cost), and the Think tool that
// bridges the chat loop into V4-Flash thinking mode. Interactive TUI lands
// in M4; full think-then-chat skill arrives with skill loading in M5.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/agentscli"
	"github.com/whyiyhw/seek/internal/agenttypes"
	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/assets"
	"github.com/whyiyhw/seek/internal/autopilot"
	"github.com/whyiyhw/seek/internal/bgjob"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/checkpoint"
	"github.com/whyiyhw/seek/internal/checkpointcli"
	"github.com/whyiyhw/seek/internal/clipimage"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/controlplane"
	"github.com/whyiyhw/seek/internal/fileroute"
	"github.com/whyiyhw/seek/internal/fsobserve"
	"github.com/whyiyhw/seek/internal/goal"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/hookscli"
	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/internal/i18n"
	"github.com/whyiyhw/seek/internal/keymap"
	"github.com/whyiyhw/seek/internal/keyscli"
	"github.com/whyiyhw/seek/internal/lspclient"
	"github.com/whyiyhw/seek/internal/mcpconfig"
	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/memorycli"
	"github.com/whyiyhw/seek/internal/opencatalog"
	"github.com/whyiyhw/seek/internal/opencodeauth"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/projectmd"
	"github.com/whyiyhw/seek/internal/routines"
	"github.com/whyiyhw/seek/internal/routinescli"
	seekrpc "github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/sandbox"
	"github.com/whyiyhw/seek/internal/servecli"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/sessioncli"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillcli"
	"github.com/whyiyhw/seek/internal/skilldistill"
	"github.com/whyiyhw/seek/internal/skillmgr"
	"github.com/whyiyhw/seek/internal/skillstats"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/suggester"
	"github.com/whyiyhw/seek/internal/sysprompt"
	"github.com/whyiyhw/seek/internal/tools"
	agenttool "github.com/whyiyhw/seek/internal/tools/agent"
	askusertool "github.com/whyiyhw/seek/internal/tools/askuser"
	"github.com/whyiyhw/seek/internal/tools/bash"
	"github.com/whyiyhw/seek/internal/tools/edit"
	"github.com/whyiyhw/seek/internal/tools/enterworktree"
	"github.com/whyiyhw/seek/internal/tools/exitworktree"
	"github.com/whyiyhw/seek/internal/tools/fimcomplete"
	gittool "github.com/whyiyhw/seek/internal/tools/git"
	"github.com/whyiyhw/seek/internal/tools/grep"
	"github.com/whyiyhw/seek/internal/tools/listdir"
	"github.com/whyiyhw/seek/internal/tools/mcptool"
	"github.com/whyiyhw/seek/internal/tools/memorytool"
	"github.com/whyiyhw/seek/internal/tools/monitor"
	plantool "github.com/whyiyhw/seek/internal/tools/plan"
	"github.com/whyiyhw/seek/internal/tools/propose"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/internal/tools/references"
	"github.com/whyiyhw/seek/internal/tools/skillinstall"
	"github.com/whyiyhw/seek/internal/tools/skilltool"
	"github.com/whyiyhw/seek/internal/tools/think"
	"github.com/whyiyhw/seek/internal/tools/wakeup"
	"github.com/whyiyhw/seek/internal/tools/webfetch"
	"github.com/whyiyhw/seek/internal/tools/websearch"
	"github.com/whyiyhw/seek/internal/tools/write"
	"github.com/whyiyhw/seek/internal/tui"
	"github.com/whyiyhw/seek/internal/upgrade"
	"github.com/whyiyhw/seek/internal/verifier"
	"github.com/whyiyhw/seek/internal/worktree"
	"github.com/whyiyhw/seek/internal/worktreecli"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
	"github.com/whyiyhw/seek/pkg/llm/compatible"
	anthropicprov "github.com/whyiyhw/seek/pkg/llm/provider/anthropic"
	geminiprov "github.com/whyiyhw/seek/pkg/llm/provider/gemini"
	openaiprov "github.com/whyiyhw/seek/pkg/llm/provider/openai"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-isatty"
)

// System prompt assembly (template literal, Compose, ModeLabel)
// lives in internal/sysprompt. Two callers consume it: cmd/seek (this
// file, root agent) and internal/subagent (v5 柱 G, subagents). PRD
// docs/prd/archive/feature-subagent.md §3.6.1 specifies the composition rules
// and the byte-determinism invariant that keeps DeepSeek's prefix
// cache hot.

// ----- v3 checkpoint glue -----

// checkpointStderrSink routes Manager warnings to os.Stderr — the
// default for non-TUI launches. TUI swaps to its own buffered sink
// via Options when ready.
type checkpointStderrSink struct{}

func (checkpointStderrSink) Warn(m string) { fmt.Fprintln(os.Stderr, m) }

// checkpointSnapshotter is the adapter the write/edit tools see.
// Lives here so internal/tools/{write,edit} don't have to import
// internal/checkpoint (the dependency would also fight tests that
// pass nil snapshotters).
type checkpointSnapshotter struct{ m *checkpoint.Manager }

func (c checkpointSnapshotter) SnapshotFile(path, tool, callID string) error {
	return c.m.SnapshotFile(path, tool, callID)
}
func (c checkpointSnapshotter) FinaliseSnapshot(path string, after []byte) error {
	return c.m.FinaliseSnapshot(path, after)
}

// checkpointHook adapts *checkpoint.Manager to the hooks observer
// interfaces. Register once on the hooks.Registry; the dispatcher
// detects PreTurn / SessionEnd by interface satisfaction.
type checkpointHook struct{ m *checkpoint.Manager }

func (h *checkpointHook) OnPreTurn(ctx context.Context, ev hooks.PreTurnEvent) {
	h.m.OnPreTurn(ctx, nil)
}
func (h *checkpointHook) OnSessionEnd(ctx context.Context, ev hooks.SessionEndEvent) {
	h.m.OnSessionEnd(ctx, nil)
}

func main() {
	// v7 柱 O: if this is a Landlock trampoline re-exec (Linux only), lock
	// the process down and exec the wrapped command before anything else —
	// no config load, no flag parse. No-op on every other invocation/OS.
	sandbox.RunTrampolineIfRequested()
	if err := run(); err != nil {
		// -tools with unknown names: the error text (unknown list +
		// available list) was already printed to stderr by
		// applyToolsWhitelist; exit 2 without the "seek:" prefix so CI
		// can tell "bad flag value" from "system failure" — same
		// contract as `seek keys` (keyscli.ErrUsage).
		if errors.Is(err, errToolsUsage) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
}

// helpRequested reports whether help was asked for: `help` as the first
// word (the verb-alias convention every subcommand CLI here follows),
// or -h/--help anywhere before the "--" terminator. Deliberately a flat
// scan: `-p -h` reads as help here where flag.Parse would take -h as
// -p's value — a prompt of literally "-h" is not a case worth
// preserving, and every other reading of the token means help. Tokens
// flag itself special-cases (-help, --h) still reach flag.Usage via
// flag.Parse: exit 0, text on stderr.
func helpRequested(args []string) bool {
	for i, a := range args {
		if a == "--" {
			return false
		}
		if (i == 0 && a == "help") || a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

// printUsage is `seek -h` / `seek --help` / `seek help`: the verb
// surface FIRST — run() dispatches those ahead of flag.Parse, so the
// flag listing alone hides them, which is exactly how seek came to look
// like a flags-only CLI — then the standard flag listing. Each verb's
// own flags live in `<command> help`; keep the one-liners here honest.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `seek — terminal coding agent: bare `+"`seek`"+` opens the TUI, the verbs below do everything else

Usage:
  seek [flags]                 interactive TUI (print mode with -p / -json)
  seek <command> [args]        run a command; <command> help has its detail

Commands:
  serve        remote control plane; --frontend=web also serves the browser workbench
  acp          Agent Client Protocol over stdio (IDE integration)
  cron         schedule prompts to run unattended (create / list / delete / tick / config)
  skill        skill lifecycle (install / update / list / stats / create / drafts)
  memory       project memory (list / show / search / add / archive)
  agents       typed-agent definitions (list / check / create / remove)
  hooks        shell hooks (list / check / trust / audit)
  worktree     seek-owned git worktrees (list / gc)
  session      export a transcript to markdown (export)
  checkpoint   file checkpoints (list / restore / prune); undo / redo are aliases
  keys         keybinding config (list / check / actions)
  auth         browser login for a provider lane (opencode)
  goal         unattended main-agent loop:   seek goal run "<goal>"
  autopilot    unattended orchestration:     seek autopilot run "<goal>"

Flags:
`)
	// PrintDefaults follows CommandLine's output; point it at w for the
	// duration and put it back so a later flag.Parse error still lands
	// on stderr (main() never runs twice, tests might).
	prev := flag.CommandLine.Output()
	flag.CommandLine.SetOutput(w)
	defer flag.CommandLine.SetOutput(prev)
	flag.PrintDefaults()
}

func run() error {
	// Skill subcommand surface (PRD v2 §5.1). Dispatched ahead of
	// every global flag and provider/session probe so `seek skill
	// install ./foo` doesn't need API keys, doesn't load sessions,
	// doesn't touch ~/.seek/projects/. The first positional arg is
	// the discriminator — flag.Parse() would already have consumed
	// it if we waited.
	if len(os.Args) >= 2 && os.Args[1] == "skill" {
		return skillcli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) >= 2 && os.Args[1] == "memory" {
		return memorycli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek agents ...` — typed-agent definitions (feature-
	// orchestration.md §5, M-O2.3): list / check (read-only views of
	// the registry) and create / remove (the write path — the only
	// legal writers under the agents/ scopes are internal/agentsmgr
	// via these verbs). Same rationale as skill/memory: no API keys,
	// no session, no agent runtime.
	if len(os.Args) >= 2 && os.Args[1] == "agents" {
		return agentscli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek session export` — transcript JSONL → human-readable
	// markdown. Same rationale as skill/memory: export needs neither
	// API keys nor the agent runtime, and must not collide with the
	// top-level flags. The markdown renderer lives in
	// internal/sessionexport (pure, reusable) so the serve web panel
	// can call it directly when a transcript download button lands.
	// `seek auth opencode` — browser device login for the OpenCode Go
	// lane, stored at ~/.seek/opencode-auth.json. Dispatched early for
	// the same reason as the other verbs: it needs no provider probe,
	// no session and no project, and it must work when the lane is
	// currently unauthenticated (which is the whole point).
	if len(os.Args) >= 2 && os.Args[1] == "auth" {
		return opencodeauth.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) >= 2 && os.Args[1] == "session" {
		return sessioncli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// v3 checkpoint subcommand (PRD docs/prd/archive/feature-checkpoint.md §4.1).
	// Dispatched ahead of flag.Parse so the user can pass --session etc.
	// without colliding with the top-level binary flags.
	if len(os.Args) >= 2 && os.Args[1] == "checkpoint" {
		return checkpointcli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) >= 2 && os.Args[1] == "undo" {
		return checkpointcli.RunUndo(os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) >= 2 && os.Args[1] == "redo" {
		return checkpointcli.RunRedo(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek hooks ...` — list / check / trust / audit. Dispatched
	// before global flag parse for the same reason as `skill`:
	// hooks queries don't need API keys, sessions, or a project
	// directory. See PRD docs/prd/archive/feature-shell-hooks.md §4.1.
	if len(os.Args) >= 2 && os.Args[1] == "hooks" {
		return hookscli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek worktree ...` — list / gc. Same rationale as hooks:
	// worktree queries don't need API keys, sessions, or full
	// seek runtime, so they short-circuit ahead of flag.Parse to
	// stay invokable from CI cleanup scripts. PRD docs/prd/
	// feature-subagent.md §3.8 + §9 v0.6.x dot.
	if len(os.Args) >= 2 && os.Args[1] == "worktree" {
		return worktreecli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek cron ...` — create / list / delete / run / tick. Same
	// rationale: cron registry mutations + the OS-scheduler-fired
	// tick don't need the full seek runtime. tick especially is
	// invoked every minute by launchd / systemd / cron / Task
	// Scheduler; short-circuit keeps that cheap. PRD docs/prd/
	// feature-routines.md §4.1.
	if len(os.Args) >= 2 && os.Args[1] == "cron" {
		return routinescli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek keys ...` — list / check / actions. Same rationale: keymap
	// queries don't need API keys, sessions, or a project directory.
	// PRD §5.1 wants exit code 2 for validation errors (so CI can
	// distinguish "bad config" from "system failure"); keyscli signals
	// this via ErrUsage which we unwrap and route to os.Exit(2) here.
	// See PRD docs/prd/archive/feature-tui-ergonomics.md §5.1.
	if len(os.Args) >= 2 && os.Args[1] == "keys" {
		if err := keyscli.Run(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			if errors.Is(err, keyscli.ErrUsage) {
				os.Exit(2)
			}
			return err
		}
		return nil
	}
	// `seek serve` — remote control plane (docs/prd/feature-remote-
	// control-plane.md). Dispatched ahead of the agent runtime for the
	// same reason as cron: the plane never loads a session or touches
	// the TUI. The plane owns its exit-code contract (docs/serve/
	// README.md): 0 clean drain / 1 work dropped / 2 frontend-layer or
	// bad config. controlplane.ExitCode is the single mapping — servecli
	// has already printed the error, so don't re-print (main() would
	// prefix "seek:" and flatten the code to 1).
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		if err := servecli.Run(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			os.Exit(controlplane.ExitCode(err))
		}
		return nil
	}

	var (
		prompt        = flag.String("p", "", "prompt text; if non-empty (or stdin is piped) seek runs in print mode and exits")
		model         = flag.String("model", "", "model id; default depends on provider (deepseek-flash for DeepSeek, etc.)")
		maxTurns      = flag.Int("max-turns", 200, "safety bound on agent loop iterations")
		maxTokens     = flag.Int("max-tokens", 0, "completion token cap per call; 0 → effort-linked default (16k; 64k under /effort max)")
		autoContinue  = flag.Bool("auto-continue", false, "inject 'continue' on text-only turns so the model resumes mid-task without user input")
		yolo          = flag.Bool("yolo", false, "allow bash + writes outside CWD without prompting")
		plan          = flag.Bool("plan", false, "read-only exploration: no bash/writes/edits; produce a plan to review before executing")
		jsonOut       = flag.Bool("json", false, "emit agent events as JSONL on stdout (implies print mode)")
		resume        = flag.String("resume", "", "load a saved session by ID (see seek -list)")
		cont          = flag.Bool("continue", false, "load the most-recently-updated session")
		noSave        = flag.Bool("no-save", false, "do not persist this session to disk")
		noSuggest     = flag.Bool("no-suggest", false, "disable v4 柱 D suggested-reply (predictor + UI placeholder + calibration injection); equivalent to suggest_reply=false in ~/.seek/config.json")
		noCronTick    = flag.Bool("no-cron-tick", false, "skip the startup cron auto-tick (cron jobs still fire via `seek cron tick` or OS scheduler)")
		approvalStdin = flag.Bool("approval-stdin", false, "print mode (--json) only: surface permission prompts as approval_request lines on stdout and read the decision lines from stdin (the remote control plane's approval channel)")
		list          = flag.Bool("list", false, "list saved sessions and exit")
		noProj        = flag.Bool("no-project-md", false, "do not auto-load AGENTS.md from the project tree")
		providerFlag  = flag.String("provider", "", "LLM provider: deepseek (default) | anthropic | openai | gemini | glm | opencode | compatible")
		baseURL       = flag.String("base-url", "", "base URL for --provider=compatible; overrides the default endpoint for --provider=glm / --provider=opencode")
		providerName  = flag.String("provider-name", "Compatible", "display name for --provider=compatible")
		themeFlag     = flag.String("theme", "auto", "color theme: auto|dark|light")
		rpcMode       = flag.Bool("rpc", false, "run as a JSON-RPC 2.0 server over stdio (for IDE integrations)")
		benchmarkTask = flag.String("benchmark", "", "run a benchmark task (self-hosting | fim-patch) and report metrics")
		benchmarkOut  = flag.String("benchmark-out", "", "write benchmark JSON report to this file (default: stdout)")
		showVersion   = flag.Bool("version", false, "print version info and exit")
		upgradeFlag   = flag.Bool("upgrade", false, "download the latest release from GitHub and replace this binary")
		upgradeForce  = flag.Bool("upgrade-force", false, "with -upgrade: proceed even when the current build is a dev build (overwrites local builds)")
		upgradeDryRun = flag.Bool("upgrade-dry-run", false, "with -upgrade: download + verify checksum but do not replace the binary")
		upgradeCheck  = flag.Bool("upgrade-check", false, "check for a newer release on GitHub and print the result; never modifies the binary")
		installFlag   = flag.Bool("install", false, "add seek to the user PATH (Windows)")
		dreamFlag     = flag.Bool("dream", false, "M→L distillation: scan project memory, print L-pending candidates without writing")
		dreamWrite    = flag.Bool("dream-write", false, "with -dream: actually append the candidates to ~/.seek/soul.md's Pending section")
		// v3 柱 A: keep the per-session file checkpoint directory
		// past SessionEnd. Default off — file checkpoints are
		// "this-session-only" by design (see feature-checkpoint
		// PRD §3.2). Power users debugging across resumes set this.
		keepCheckpoints = flag.Bool("keep-checkpoints", false, "preserve <session>/checkpoints/ across session end (default: cleaned)")
		// v8·T1 bare-core comparison lane: freeze the session to an
		// explicit tool whitelist. Read once here; the tools array is
		// part of the request prefix, so it cannot change mid-session.
		toolsFlag = flag.String("tools", "", "comma-separated tool-name whitelist (e.g. read,edit,write,bash,grep); MCP tools attach only when listed by registered name. Empty or absent = full default registry. Unknown names exit 2 with the available list. Frozen at startup")
	)

	// Top-level help runs BEFORE flag.Parse but AFTER the flag set is
	// registered, so the listing below carries both halves: the verbs
	// (dispatched above) and the flags (defaults from the block just
	// closed). flag.CommandLine is ExitOnError — its built-in -h exits
	// 0 with the text on stderr and shows only the flags, which is how
	// seek came to read as a flags-only CLI while serve & co. stayed
	// invisible. A requested help goes to stdout (the skillcli/keyscli
	// posture); flag.Usage below keeps the malformed-flag path on
	// stderr.
	if helpRequested(os.Args[1:]) {
		printUsage(os.Stdout)
		return nil
	}

	// Malformed flags: the flag package prints "flag provided but not
	// defined: -x" and then calls this — same stream (stderr) so the two
	// halves stay together. User-requested help never reaches here: the
	// helpRequested branch above already took it to stdout.
	flag.Usage = func() { printUsage(flag.CommandLine.Output()) }
	flag.Parse()

	// --yolo and --plan are mutually exclusive.
	if *yolo && *plan {
		return fmt.Errorf("--yolo and --plan are mutually exclusive")
	}

	// --approval-stdin belongs to the JSONL print lane only: it turns the
	// child's stdout into an event stream carrying approval_request lines
	// and its stdin into the decision channel (W5 c_n37tnr). Asking for it
	// in the TUI or plain-print lanes is a caller bug — say so instead of
	// silently ignoring it.
	if *approvalStdin && !*jsonOut {
		return fmt.Errorf("--approval-stdin requires --json (the approval channel is part of the JSONL contract)")
	}

	// -version / -upgrade short-circuit before any provider / session
	// machinery is touched: these subcommands don't need API keys.
	if *showVersion {
		fmt.Println(tui.VersionString())
		return nil
	}
	if *upgradeFlag || *upgradeDryRun || *upgradeForce {
		return runUpgrade(*upgradeForce, *upgradeDryRun)
	}
	if *upgradeCheck {
		return runUpgradeCheck()
	}

	// -install short-circuit before any provider machinery.
	if *installFlag {
		return runInstall()
	}

	// Best-effort cleanup of a stale ".old" file left by a previous
	// Windows upgrade. No-op on Unix.
	if exe, err := os.Executable(); err == nil {
		upgrade.CleanupStaleOld(exe)
	}

	// Validate --theme before doing anything else.
	switch strings.ToLower(*themeFlag) {
	case "auto", "dark", "light":
	default:
		return fmt.Errorf("--theme must be auto, dark, or light (got %q)", *themeFlag)
	}

	// Session store is needed for -list / -resume / -continue and for
	// auto-save. Construct early so we can short-circuit on -list
	// before hitting the API-key check.
	store, err := session.NewStore()
	if err != nil {
		return err
	}

	if *list {
		return printSessionList(store)
	}

	// `seek acp` lane marker, hoisted to the very top of the shared
	// init (it used to live just before initialPref) because the ACP
	// provider/model pins below must land BEFORE buildProvider runs.
	// flag.Parse treats "acp" as a positional and stops there, so no
	// command-line flag can ever reach this lane — same story as
	// SEEK_ACP_YOLO (feature-paseo-provider.md §3.3–3.4).
	acpLane := len(os.Args) >= 2 && os.Args[1] == "acp"
	if acpLane {
		// Startup pins for the lane's provider/model. Precedence is
		// flag (unreachable here, the guard is belt-and-braces) > env >
		// buildProvider auto-detect. Embedding hosts (paseo provider
		// entries) set these per entry the same way they pin
		// ZCODE_PROVIDER on the zcode bridge. A pinned provider also
		// suppresses the first-run wizard below — a headless spawn must
		// never sit in an interactive paste prompt.
		if *providerFlag == "" {
			if p, _ := acp.ProviderModelFromEnv(os.Getenv); p != "" {
				*providerFlag = p
			}
		}
		if *model == "" {
			if _, m := acp.ProviderModelFromEnv(os.Getenv); m != "" {
				*model = m
			}
		}
	}

	// First-run setup wizard. Only fires when there's truly no auth
	// anywhere (env + config both empty) AND stdin is a TTY (otherwise
	// scripts get an honest error instead of a hung interactive prompt).
	// User flags like --provider don't suppress the wizard — if the
	// chosen provider's key isn't anywhere, buildProvider would error
	// out anyway, and an interactive paste is friendlier than a 1-liner
	// "X is not set" line.
	if *providerFlag == "" && shouldTriggerWizard() {
		// The wizard runs before the SIGINT-bound ctx is established
		// (that lives further down, after auth is resolved). Using a
		// background ctx is fine — the wizard itself is driven by
		// bufio.Scanner, and pingDeepSeek derives its own 10s timeout.
		if _, werr := runSetupWizard(context.Background(), os.Stdin, os.Stderr); werr != nil {
			return werr
		}
	}

	// First-run PATH nudge on Windows TUI launches — runs after the setup
	// wizard but before agent/session machinery so the user isn't kept
	// waiting through a full init only to hit an stdin prompt.
	if willUseTUI(*jsonOut, *prompt, *benchmarkTask, *rpcMode, *dreamFlag) {
		if err := maybeWindowsPATHPrompt(); err != nil {
			return err
		}
	}

	// Provider detection. --provider overrides auto-detect; otherwise we
	// check env vars in priority order: DeepSeek first, then second-tier.
	provider, dsClient, provLabel, modelDefault, err := buildProvider(
		*providerFlag, *baseURL, *providerName,
	)
	if err != nil {
		return err
	}

	// OpenCode Go lane: seed the model catalogue from the disk cache and
	// kick a background refresh. Both are best-effort — every failure
	// path leaves the compiled baseline (pricing.OpenCodeGoModels, already
	// installed as Rows()) in place, and the goroutine never blocks
	// startup or an inference call. The catalogue feeds menus, the cost
	// display and the /compact threshold only; it is not part of the
	// system prompt, so a refresh can't disturb the prefix cache.
	if provLabel == "OpenCode" {
		initOpenCatalog()
	}

	// Resolve which session we're operating on. Priority:
	//   -resume <id>  → load that session (error if missing)
	//   -continue     → load latest (error if no sessions yet)
	//   else          → new session, fresh history
	var loaded *session.Session
	if *resume != "" {
		loaded, err = store.Load(*resume)
		if err != nil {
			return fmt.Errorf("resume %s: %w", *resume, err)
		}
	} else if *cont {
		loaded, err = store.Latest()
		if err != nil {
			return fmt.Errorf("continue: %w", err)
		}
		if loaded == nil {
			return fmt.Errorf("continue: no saved sessions in %s", store.Dir())
		}
	}
	// Defensive repair: older sessions written before the orphan-
	// tool_calls fix may have a trailing assistant tool_calls message
	// with no matching tool results. The API rejects that on the next
	// turn — repair drops the offending tail so the user can continue
	// instead of being permanently locked out of the session.
	if loaded != nil {
		if n := loaded.Repair(); n > 0 {
			fmt.Fprintf(os.Stderr,
				"session %s: repaired %d trailing message(s) with orphan tool_calls — re-ask your last question if needed\n",
				loaded.ID, n)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// If we loaded a session, its Model/Yolo override the flag defaults
	// (sessions are sticky — resuming with different settings would be
	// surprising). The flags can still override explicitly: set them
	// AFTER -resume on the command line and they win.
	if loaded != nil {
		if *model == "" {
			// User didn't pass --model; honour the saved one. The
			// comparison is against "" (the flag's default), NOT
			// modelDefault — comparing against modelDefault made this
			// branch dead code and silently downgraded every -resume
			// to the provider default (a vision session → flash →
			// "This model does not support image"; a V4-Pro session →
			// flash, losing thinking). See docs/pitfalls.md.
			*model = loaded.Model
		}
		if !*yolo {
			// Inherit yolo state from the saved session if user didn't
			// pass --yolo explicitly.
			*yolo = loaded.Yolo
		}
		if !*plan {
			// Inherit plan state from the saved session if user didn't
			// pass --plan explicitly.
			*plan = loaded.Plan
		}
	}
	// acpLane is declared near the top of the shared init (before
	// buildProvider) so the env pins land in time; the actual ACP
	// short-circuit sits further down, after the agent is up.
	// Print mode (-p / piped stdin) can't realistically interrupt to
	// ask, so it stays in Deny preference unless --yolo is explicit.
	// The TUI path overrides to Ask further down so per-call approval
	// kicks in. --yolo wins on pref; --plan enters the plan workflow
	// (they're orthogonal so could in principle co-exist; cmdYolo/
	// cmdPlan enforce mutual exclusion at the UI layer).
	//
	// ACP lane (D-2, docs/prd/archive/feature-paseo-provider.md §3.3): flags
	// NEVER reach it — flag.Parse stops at the "acp" positional — so
	// --yolo is inexpressible and pref would stay Deny (bash denied
	// across the board → unattended lanes can't work). The escape
	// hatch is the SEEK_ACP_YOLO env var, resolved HERE rather than at
	// the acp short-circuit so sysprompt.Compose's Mode label matches
	// the gate (a late SetPref would leave "Mode: deny" in the prompt
	// and the model wouldn't even try bash). Env-scoped and gated on
	// acpLane: a stray SEEK_ACP_YOLO in the shell must not silently
	// yolo the TUI. Same "no human to ask" posture as goal/autopilot.
	// Resolved once, consumed twice: initialPref here, and the
	// no-remote bash guard further down (goalBash + subagent runner
	// wiring). The guard is a property of the yolo lane specifically —
	// a Deny ACP session (Zed) keeps the plain permission gate.
	//
	// W5 c_n37tnr child half: --approval-stdin (--json only) makes print
	// mode's prompts answerable from stdin, so the pref has to be Ask —
	// resolved HERE for the same reason SEEK_ACP_YOLO is: the system
	// prompt's Mode label is composed below and must agree with the gate.
	acpYolo := acpLane && acp.YoloFromEnv(os.Getenv)
	initialPref := permission.PrefDeny
	if *yolo || acpYolo {
		initialPref = permission.PrefYolo
	} else if *approvalStdin && *jsonOut {
		initialPref = permission.PrefAsk
	}
	initialWorkflow := permission.WorkflowNone
	if *plan {
		initialWorkflow = permission.WorkflowPlanAnalyze
	}
	policy, err := permission.New(cwd, initialPref)
	if err != nil {
		return err
	}
	if initialWorkflow != permission.WorkflowNone {
		policy.SetWorkflow(initialWorkflow)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// -dream short-circuits before session / agent / TUI setup. Needs a
	// DeepSeek client only (the thinking-mode call uses the Chat API);
	// second-tier providers don't currently expose a non-streaming Chat
	// on the llm.Provider interface, so -dream is DeepSeek-only for now.
	if *dreamFlag {
		if dsClient == nil {
			return fmt.Errorf("-dream requires a DeepSeek API key (DEEPSEEK_API_KEY); set one or use --provider=deepseek explicitly")
		}
		return runDream(ctx, dsClient, *dreamWrite)
	}

	if *model == "" {
		*model = modelDefault
	}
	sessionModel := *model
	// sessionEffort mirrors session.Effort: "" (model default) | "high" |
	// "max". The /effort TUI command updates it through SetEffort below;
	// the think tool reads it through its effortFunc closure so its
	// bumped-by-one-level rule (see think.bumpEffort) reflects the
	// session-level choice at call time.
	// Default is "max" — the deepest reasoning level.
	var sessionEffort = "max"
	tracker := cache.New()

	// Project-level AGENTS.md, if present. Walks up from cwd. Failures
	// (permission denied on a real file) are reported but non-fatal —
	// the rest of seek still works without project instructions.
	var projMD projectmd.Result
	if !*noProj {
		pm, perr := projectmd.Load(cwd)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "project-md:", perr)
		}
		projMD = pm
	}

	// Load skills before the system prompt is rendered — the manifest
	// is appended below. Errors are non-fatal: a malformed user skill
	// shouldn't lock the agent out of running.
	skills, skillStats, _ := skill.Load(skill.LoadOptions{ProjectDir: cwd})
	for _, err := range skillStats.Errors {
		fmt.Fprintln(os.Stderr, "skills:", err)
	}
	if summary := skillStats.FormatLoadSummary(); summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}

	// Typed agent definitions (feature-orchestration.md P2-a + P2-c):
	// loaded ONCE here — no hot reload, the same per-process freeze
	// the skill manifest breaks out of only via explicit
	// /skill_commit paths. Custom types are listed by /agents types
	// and, since P2-b, spawnable through the Manager (AgentTypes below
	// + the D13 gate in internal/subagent/custom.go). Since P2-c the
	// config `agents` 段 rides in as the routing-override layer
	// between the file scopes and the built-ins (内置 < config <
	// agents/*.md), and the `agent` tool schema's subagent_type enum
	// is generated from this registry (agenttool.CacheTypesSchema —
	// M-O2.2, D12's per-process byte freeze). Errors non-fatal, same
	// posture as skills.
	agentCfg, _ := config.Load()
	var configAgents map[string]agenttypes.RouteOverride
	if len(agentCfg.Agents) > 0 {
		configAgents = make(map[string]agenttypes.RouteOverride, len(agentCfg.Agents))
		for name, ac := range agentCfg.Agents {
			configAgents[name] = agenttypes.RouteOverride{
				Provider: ac.Provider,
				Model:    ac.Model,
				Effort:   ac.Effort,
				MaxTurns: ac.MaxTurns,
			}
		}
	}
	agentTypes, agentTypeStats, _ := agenttypes.Load(agenttypes.LoadOptions{
		ProjectDir:   cwd,
		ConfigAgents: configAgents,
	})
	for _, err := range agentTypeStats.Errors {
		fmt.Fprintln(os.Stderr, "agents:", err)
	}
	if agentTypes.CustomCount() > 0 {
		fmt.Fprintf(os.Stderr, "Loaded %d custom agent type(s) — /agents types\n", agentTypes.CustomCount())
	}
	// Freeze the dynamic `agent` tool schema from the registry BEFORE
	// any tool registration can read it (D12: build once per process,
	// package var cache). With no files + empty config this generates
	// the built-ins-only schema — same enum members as before P2-c.
	agenttool.CacheTypesSchema(agentTypes.List())

	// memProject + activeSession are forward-declared so the Skill tool's
	// stats EnvFn can read them by reference — both are set further
	// down (memProject by memory.LoadOrCreate, activeSession by
	// session.New / Store.Load). Until those run the env fn returns
	// empty strings, which skillstats omits from the JSONL anyway.
	var memProject *memory.Project
	var activeSession *session.Session

	// Wire the skill call-stats writer (PRD v2 §4.3). Failure to
	// resolve the path is non-fatal — we just disable stats for this
	// session rather than refusing to start.
	var statsWriter *skillstats.Writer
	if path, err := paths.UserSkillStats(); err == nil {
		statsWriter = skillstats.New(path)
	}
	statsEnv := func() skilltool.Env {
		env := skilltool.Env{
			Model:    *model,
			Provider: provLabel,
		}
		if activeSession != nil {
			env.SessionID = activeSession.ID
		}
		if memProject != nil {
			env.ProjectID = memProject.ID
		}
		return env
	}

	// askPolicy holds the callback for ask_user. Constructed here
	// (before tool registration) so askusertool.New can capture it;
	// the actual channel + SetAskFn wiring happens later once ctx
	// and the TUI options are ready.
	askPolicy := askuser.New(askuser.ModeAsk)

	// Resolve the absolute project root early — the checkpoint
	// Manager (constructed next) needs it, and downstream code
	// (memory init, system prompt rendering) re-uses the same
	// value via the variable.
	abs, _ := filepath.Abs(cwd)

	// v3 checkpoint Manager (feature-checkpoint.md). Constructed
	// BEFORE tool registration so write.WithSnapshotter / edit
	// .WithSnapshotter can bind. Built with the session id we
	// either inherit (resume) or pre-generate (fresh). The pre-
	// generation only commits a string — the actual session
	// header is written later by store.Save.
	//
	// --no-save disables checkpoint entirely (ephemeral run; no
	// session id means no scoped storage path). Same intent as
	// --no-save's memory / session opt-outs.
	var ckMgr *checkpoint.Manager
	if !*noSave {
		sid := ""
		if loaded != nil {
			sid = loaded.ID
		} else {
			// Construct the session early so we get a stable ID
			// for the checkpoint scope. activeSession is reused
			// below — the fresh-session branch at line ~870 only
			// runs when loaded == nil AND activeSession is nil,
			// so this pre-emptive construction is safe.
			activeSession = session.New(*model, abs, "", *yolo, *plan)
			sid = activeSession.ID
		}
		ckMgr = checkpoint.New(checkpoint.Config{
			SessionID:  sid,
			ProjectAbs: abs,
			CWD:        abs,
			Sink:       checkpointStderrSink{},
			KeepOnExit: *keepCheckpoints,
		})
		// Hook into the permission gate so destructive actions
		// fire MaybeCreateGit before they hit the filesystem.
		policy.SetOnDestructive(func(a permission.Action) {
			ckMgr.MaybeCreateGit(ctx, a)
		})
	}

	// v5 柱 G worktree Manager (feature-subagent.md §3.8).
	// Constructed unconditionally — Manager itself doesn't probe
	// for git availability (mirrors checkpoint.New's lazy
	// failure model). If the user's cwd isn't a git working
	// tree, enter_worktree's first git invocation surfaces a
	// clean "fatal: not a git repository" via the wire-format
	// failure path. Always-construct keeps the tool list byte-
	// stable across project types (non-git users still see the
	// tool in the schema list, which the prefix cache wants).
	//
	// PruneDiscarded fires at startup as a best-effort GC of
	// 48h-old rescue stashes (PRD §3.8). PruneOrphans is the
	// age-based fallback for sessions that died before
	// exit_worktree — without it the ref/worktree lives forever
	// (2026-11-14: six June zombies). Failure of either doesn't
	// block startup — the worst case is leftover refs the next
	// `seek worktree gc` reaps.
	wtMgr, wtErr := worktree.NewManager(abs)
	if wtErr != nil {
		fmt.Fprintln(os.Stderr, "worktree:", wtErr)
	} else {
		if _, perr := wtMgr.PruneDiscarded(ctx, 48*time.Hour); perr != nil {
			fmt.Fprintln(os.Stderr, "worktree: prune discarded:", perr)
		}
		if _, oerr := wtMgr.PruneOrphans(ctx, worktree.DefaultOrphanAge, func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}); oerr != nil {
			fmt.Fprintln(os.Stderr, "worktree: prune orphans:", oerr)
		}
	}

	// Background-job manager for `bash run_in_background` + the monitor
	// tool (v6 柱 K). Session-scoped: Shutdown kills every survivor when
	// run() returns so no background process is ever orphaned (PRD §3, D5).
	bgMgr := bgjob.New()
	defer bgMgr.Shutdown()

	// Language-server manager for the references tool (v6 柱 L). Like
	// bgMgr it's session-scoped: servers are bound to ctx (the session,
	// NOT a turn) and Shutdown kills them when run() returns, so no gopls
	// is orphaned (PRD feature-lsp.md §4 D4).
	lspRoot := policy.CWD()
	if lspRoot == "" {
		lspRoot, _ = os.Getwd()
	}
	lspMgr := lspclient.New(lspRoot, ctx)
	defer lspMgr.Shutdown()

	// `seek goal run` drives the MAIN agent unattended (loops until a
	// condition is met), so — like autopilot's subagents — its bash must
	// deny remote-mutating ops (no `git push` / `gh pr` while you're away).
	// Detected here (before the registry) because the main bash tool is
	// built inline below; autopilot guards SUBagent bash instead.
	goalMode := len(os.Args) >= 2 && os.Args[1] == "goal"
	// bashEnvKeep opts specific variables back through the credential
	// scrub every model-issued command runs under (internal/childenv).
	// Read here rather than at the later config.Load() because the bash
	// tool is a value type — options must be set before reg.Add copies it.
	bashCfg, _ := config.Load()
	// View-layer language: resolved ONCE here and stored as the i18n
	// package default — the same per-session discipline as
	// sysprompt.Header.Date (never recomputed per turn; /lang is the
	// only sanctioned runtime swap). Reads SEEK_LANG > config >
	// LC_ALL/LANG > en.
	i18n.SetDefault(i18n.New(i18n.Resolve(bashCfg.Language)))
	// Same load serves the read tool's limits (max_limit /
	// whole_read_bytes).
	readCfg := bashCfg
	bashEnvKeep := bashCfg.BashEnvPassthrough
	goalBash := bash.New(policy).WithBackground(bgMgr).WithEnvPassthrough(bashEnvKeep)
	// No-remote guard for the unattended lanes: goal and SEEK_ACP_YOLO
	// (docs/prd/archive/feature-paseo-provider.md §3.3) both run yolo with no
	// human to ask — yolo opens LOCAL edit/shell only; push/PR-class
	// remote mutations stay blocked (double insurance with the drive
	// cards' own "never push" sandbox discipline). The lane label rides
	// the refusal so an ACP session (where a human IS present) is not
	// told it is goal mode (c_ajza7r). Both flags cannot be true at once
	// (goalMode/acpYolo key off mutually exclusive argv[1]); ACP is
	// checked first so the ordering is explicit if that ever changes.
	if goalMode || acpYolo {
		lane := autopilot.LaneGoal
		if acpYolo {
			lane = autopilot.LaneACP
		}
		goalBash = goalBash.WithDeny(laneGuard(lane))
	}

	// Repeat-call guard (one counter per session). Wrapped around the
	// tools whose repetition is both common and expensive: a failing
	// command re-run unchanged, or the same file re-read after it fell
	// out of attention. Deliberately NOT applied to ask_user (asking the
	// same question twice is legitimate), write/edit (a second identical
	// write is idempotent, not a loop), or the plan/skill tools (their
	// own state machines already reject nonsense repeats).
	repeats := tools.NewRepeatGuard()
	guarded := func(t tools.Tool) tools.Tool { return tools.WithRepeatGuard(t, repeats) }

	// Blind-overwrite guard (one observation set per session). `read`
	// records what the model has seen, `edit` refreshes it after a
	// successful match-and-replace, and `write` refuses to replace a file
	// that is unseen or changed on disk since it was read.
	observed := fsobserve.New()

	reg := tools.New().
		Add(guarded(read.New(policy).
			WithLimits(readCfg.ReadMaxLimit(), readCfg.ReadWholeReadBytes()).
			WithObserver(observed))).
		Add(references.New(lspMgr)).
		Add(guarded(grep.New())).
		Add(guarded(listdir.New())).
		Add(write.New(policy).WithSnapshotter(checkpointSnapshotter{m: ckMgr}).WithObserver(observed)).
		Add(edit.New(policy).WithSnapshotter(checkpointSnapshotter{m: ckMgr}).WithObserver(observed)).
		Add(guarded(goalBash)).
		Add(monitor.New(bgMgr)).
		Add(guarded(gittool.New())).
		Add(skilltool.NewWithStats(skills, statsWriter, statsEnv)).
		Add(skillinstall.NewFetch()).
		Add(skillinstall.NewCommit(policy)).
		Add(askusertool.New(askPolicy))

	// v5 柱 G worktree tools — only register when Manager
	// constructed successfully. nil Manager would force the
	// tools to panic at New(); skipping registration gives
	// non-git environments a cleaner schema (no enter_worktree
	// in the LLM's tool list at all, no model confusion about
	// "why does this fail every time I try it").
	if wtMgr != nil {
		reg.Add(enterworktree.New(wtMgr)).
			Add(exitworktree.New(wtMgr))
	}

	// v5 柱 H schedule_wakeup tool — the model's surface over
	// internal/routines for one-shot future-time wake-ups
	// (feature-routines.md §3.7). Registration gated on Store
	// availability: a read-only ~/.seek/ (rare but possible
	// e.g. on locked-down CI runners) means we can't persist
	// jobs, and the tool would just fail every call — cleaner
	// to omit it from the registry entirely so the model
	// doesn't even see it as an option.
	if cronStore, err := routines.OpenStore(); err == nil {
		reg.Add(wakeup.New(cronStore))
	} else {
		fmt.Fprintln(os.Stderr, "wakeup: cron store unavailable, schedule_wakeup tool not registered:", err)
	}

	// webfetch: opt-out via SEEK_NO_WEBFETCH for air-gapped /
	// privacy-sensitive sessions. SEEK_WEBFETCH_ALLOW_HTTP opens
	// the http:// scheme for internal docs sites. See PRD
	// docs/prd/feature-webfetch.md.
	if !envBoolTrue("SEEK_NO_WEBFETCH") {
		wfOpts := webfetch.DefaultOptions()
		if envBoolTrue("SEEK_WEBFETCH_ALLOW_HTTP") {
			wfOpts.AllowHTTP = true
		}
		reg.Add(guarded(webfetch.New(wfOpts)))
	}

	// DeepSeek-exclusive tools: FIM and Reasoner are only available
	// when using the DeepSeek client directly.
	if dsClient != nil {
		reg.Add(fimcomplete.New(dsClient, *model)).
			Add(think.New(dsClient, func() string { return sessionModel }, func() string { return sessionEffort }))
		// web_search rides the standalone Search API — same DeepSeek
		// key / base URL as chat (config.KeyFor deepseek), billed per
		// successful call into the tracker so the status bar's $ total
		// grows without a token turn. Opt-out mirrors SEEK_NO_WEBFETCH
		// for air-gapped / privacy-sensitive sessions.
		if !envBoolTrue("SEEK_NO_WEBSEARCH") {
			reg.Add(guarded(websearch.New(dsClient, tracker)))
		}
	}

	// Load MCP servers and register their tools. Errors are non-fatal:
	// a broken MCP server should not prevent seek from starting.
	//
	// mcpSection feeds sysprompt.Header.MCPSection (the fifth prompt
	// segment). Assembled ONCE here, after LoadServers, and never
	// recomputed — per-session static, same discipline as sessionDate
	// below: MCP servers load at startup and never change mid-session,
	// and a mutating prompt segment would invalidate the prefix cache.
	// Empty when no MCP tools loaded → zero prompt bytes, so no-MCP
	// users keep a byte-identical prefix (the sysprompt golden locks
	// this). Also empty under an explicit `"mcp_prompt": false` — tools
	// still register, only the guidance segment goes away (the A2 eval
	// arm). Note: the -tools whitelist narrows the registry later and
	// may drop MCP tools this section still names — same static-prose
	// posture as rootTpl (which documents fim_complete even on non-
	// DeepSeek providers); the section describes what loaded, the
	// request's tools array is the callable truth.
	var mcpSection string
	if mcpCfg, err := mcpconfig.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "mcp config:", err)
	} else if len(mcpCfg.MCPServers) > 0 {
		servers := make(map[string]mcptool.ServerConfig, len(mcpCfg.MCPServers))
		for name, e := range mcpCfg.MCPServers {
			servers[name] = mcptool.ServerConfig{
				Command:  e.Command,
				Args:     e.Args,
				Env:      mcptool.EnvMapToSlice(e.Env),
				NoPrefix: !e.UsePrefix(),
			}
		}
		existing := make(map[string]bool, len(reg.Names()))
		for _, n := range reg.Names() {
			existing[n] = true
		}
		lr := mcptool.LoadServers(ctx, servers, existing)
		for _, e := range lr.Errors {
			fmt.Fprintln(os.Stderr, "mcp:", e)
		}
		for _, b := range lr.Bridges {
			reg.Add(b)
		}
		if len(lr.Bridges) > 0 {
			mcpSection = mcpSectionFromBridges(lr.Bridges, mcpCfg)
			fmt.Fprintf(os.Stderr, "mcp: loaded %d tool(s)\n", len(lr.Bridges))
		}
	}

	// M layer + L layer. Without session persistence (--no-save) we
	// also skip memory persistence — they share the "this is a
	// throwaway run" intent. Load failures are non-fatal: a broken
	// or read-only ~/.seek/projects/ degrades the session (no memory
	// injection, no recall, no remember) but should not block startup.
	// memProject is forward-declared above (the Skill stats env fn
	// captures it by reference); only the assignment lives here.
	var memSoul *memory.Soul
	if !*noSave {
		if proj, err := memory.LoadOrCreate(abs); err != nil {
			fmt.Fprintln(os.Stderr, "memory:", err)
		} else {
			memProject = proj
		}
		if soul, err := memory.LoadSoul(); err != nil {
			fmt.Fprintln(os.Stderr, "memory soul:", err)
		} else {
			memSoul = soul
		}
		reg.Add(memorytool.NewRecall(memProject)).
			Add(memorytool.NewRemember(memProject, policy)).
			Add(memorytool.NewArchive(memProject)).
			Add(memorytool.NewAmend(memProject))
	}

	// sessionDate is computed ONCE here and threaded through every
	// system-prompt assembly for the life of the process (the Compose
	// below, the /new rebuild closure, and the subagent Manager). It is
	// deliberately NOT recomputed per turn: the date sits in the prompt
	// prefix, so a value that ticked over at midnight (or per request)
	// would invalidate DeepSeek's prefix cache on every turn. Held fixed,
	// the prefix stays byte-identical and the cache holds. A new process
	// (-resume on a later day) picks up the new date — an accepted
	// invalidation point, same as Cwd/skill-manifest changes.
	sessionDate := time.Now().Format("Monday, 2006-01-02")

	// Project instructions go BEFORE the skill manifest inside Compose:
	// they describe "how this repo expects you to work" while skills are
	// workflow templates. Ordering matches the model's likely reading
	// priority.
	systemPrompt := sysprompt.Compose(sysprompt.Header{
		Cwd:            abs,
		ProjectSection: projMD.Section(),
		SkillManifest:  skills.Manifest(),
		MCPSection:     mcpSection,
		Date:           sessionDate,
	}, sysprompt.ModeLabel(initialPref, initialWorkflow))

	// Build (or restore) the persistence session. -no-save makes
	// activeSession nil so the TUI auto-save no-ops. activeSession
	// is forward-declared above (Skill stats env fn captures it);
	// only the assignment lives here.
	//
	// v3 checkpoint: a fresh activeSession may already exist —
	// pre-constructed earlier so the checkpoint Manager could
	// bind a stable session id. In that case we just back-fill
	// the SystemPrompt that wasn't known at construction time.
	var initialMsgs []deepseek.Message
	if !*noSave {
		if loaded != nil {
			activeSession = loaded
			initialMsgs = loaded.Messages
			// Replay accumulated stats into the tracker so the status
			// bar shows cumulative figures, not just this run's. The
			// session file stores only aggregate Usage (no per-turn
			// breakdown), so we attribute the whole thing to the
			// current model+tier — an approximation when a session
			// spans multiple models or tier transitions. Subsequent
			// turns are priced accurately at their own (model, tier).
			if loaded.Usage.TotalTokens > 0 {
				tracker.SetBase(loaded.Usage, *model, pricing.CurrentTier(time.Now()))
			}
		} else if activeSession == nil {
			activeSession = session.New(*model, abs, systemPrompt, *yolo, *plan)
		} else {
			// Pre-constructed by the checkpoint Manager bootstrap;
			// back-fill the SystemPrompt now that it's known.
			activeSession.SystemPrompt = systemPrompt
		}
		// A resumed session may carry an /effort selection from the prior
		// run — restore it before the agent is built so the very first
		// turn after --continue honours the user's choice.
		// NB: guard on loaded, not activeSession — activeSession is always
		// non-nil (either loaded or a fresh session.New), so checking it
		// would overwrite the "max" default with the empty string from a
		// brand-new session (see commit <fill>).
		if loaded != nil {
			sessionEffort = activeSession.Effort
		}

	}

	// Lifecycle hooks. v1 memory plugs in PrePromptHook (inject L+M
	// <context> blocks) + SessionStartObserver (run GC) from one
	// struct registered into the same registry — see internal/memory.
	// Session-lifecycle hooks (SessionStart / SessionEnd) are fired
	// from main.go because the agent doesn't know when its host
	// program is "done".
	hooksReg := hooks.NewRegistry()

	// v3 柱 A: register the checkpoint Manager as a SessionEnd /
	// PreTurn observer so file-checkpoint state is cleaned up on
	// shutdown and per-turn git-checkpoint arming refreshes each
	// turn. Wrapped in a small adapter so the Manager's specific
	// signatures match the hooks Observer interfaces.
	if ckMgr != nil {
		hooksReg.Register(&checkpointHook{m: ckMgr})
	}

	// v3 pillar B — user-configurable shell hooks. Gate() reads
	// ~/.seek/hooks.toml (no trust prompt) and <project>/.seek/hooks.toml
	// (trust-on-first-visit + sha256-change re-prompt), then merges +
	// static-checks. The CLI/print paths run without a TrustPrompt so
	// project hooks stay dormant until the user trusts them from a
	// TUI session; this matches the PRD §3.5 "no bash before trust"
	// guarantee — the only way `bash -c` runs is via the StaticCheck
	// inside Gate (`bash -n`, safe) and via ShellRunner at dispatch
	// time AFTER Register, which only happens when HasHooks is true.
	// v3 柱 C — user-customisable keybindings. Load BEFORE constructing
	// the TUI Options so the loaded KeyMap (or default fallback) is
	// available at Options.Keymap. Warnings from a malformed
	// keybindings.toml go to stderr; the file is silently ignored if
	// absent (common case). PRD docs/prd/archive/feature-tui-ergonomics.md §4.4.
	userKeymapPath, _ := paths.UserKeybindings()
	userKeymap, _ := keymap.Load(userKeymapPath, os.Stderr)

	userHooksPath, _ := paths.UserHooksToml()
	projectHooksPath := paths.ProjectHooksToml(abs)
	trustPath, _ := paths.TrustedProjectsJSON()
	auditPath, _ := paths.HooksAuditLog()
	trustStore, err := hooksconfig.NewTrustStore(trustPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hooks:", err)
	}
	// stdinTrustPrompt asks the user y/N before any project-level
	// `bash -c` can fire. Detects TTY internally — piped/non-TTY
	// stdin auto-refuses with a friendly warning rather than hang.
	// Per PRD §3.5: this is the ONLY thing standing between a freshly
	// cloned repo's hooks.toml and arbitrary shell execution; we run
	// it BEFORE constructing ShellRunner so the contract "no `bash -c`
	// before trust" holds even when the file is malicious.
	hookCfg, hookWarnings := hooksconfig.Gate(
		userHooksPath, projectHooksPath, abs, trustStore,
		newStdinTrustPrompt(),
		hooksconfig.DefaultSyntaxChecker,
	)
	for _, w := range hookWarnings {
		fmt.Fprintln(os.Stderr, w)
	}
	var shellRunner *hooks.ShellRunner
	if !hookCfg.IsEmpty() {
		auditLog, alerr := hooksconfig.NewAuditLog(auditPath)
		if alerr != nil {
			fmt.Fprintln(os.Stderr, "hooks: audit log:", alerr)
		}
		shellRunner = hooks.NewShellRunner(hookCfg,
			hooks.WithAuditLog(auditLog),
			hooks.WithVersion(tui.VersionString()),
			hooks.WithProjectContext(paths.ProjectID(abs), abs),
		)
		if shellRunner.HasHooks() {
			hooksReg.Register(shellRunner)
		}
	}

	// v4 柱 D suggested-reply: a single switch (`--no-suggest` CLI flag
	// trumps `suggest_reply: true|false` in ~/.seek/config.json; absent
	// config field defaults to enabled). When disabled, both the
	// Predictor and the InjectCalibration message-preparer are nil —
	// `agent.runTurn*` no-ops the PrepareMessages hook on nil, and the
	// TUI suggester goroutine is gated on Options.Suggester != nil.
	// PRD docs/prd/archive/feature-suggested-reply.md §4.7.
	appCfg, _ := config.Load()
	suggestEnabled := !*noSuggest && appCfg.SuggestReplyEnabled() && dsClient != nil
	// Model safety check (feature-deepseek-app-api.md §5): config-only
	// opt-in, read ONCE here — no in-session switch, because the value
	// rides every request body and a mid-session flip would change all
	// subsequent requests (same discipline as language / the retired
	// server_tools switch). DeepSeek lane only; other providers ignore it.
	safetyCheck := appCfg.SafetyCheckLevel()
	var predictor *suggester.Predictor
	var prepareMessages func([]deepseek.Message) []deepseek.Message
	if suggestEnabled {
		predictor = suggester.New(dsClient)
		prepareMessages = suggester.InjectCalibration
	}

	// v5 柱 G subagent orchestrator (feature-subagent PRD §3.7).
	// Subagent persistence (index + transcript dir) needs an
	// activeSession; in --no-save we skip subagent wiring entirely
	// so the LLM can't spawn what we can't track. The `agent` tool
	// is simply absent from the registry in that mode.
	//
	// Closures: ParentSidFn reads activeSession.ID live so /compact
	// (forks a new session) is reflected; SkillManifestFn reads
	// skills.Manifest() live so skill_commit + /new shows up in
	// the next subagent's system prompt; ParentToolNamesFn reads
	// reg.Names() live so plan/propose tools added below are
	// available to subagents that want them (e.g. plan template).
	// Declared outside the !*noSave block so the TUI Options
	// below can pass the same pointer (or nil) without scoping
	// gymnastics. /agents handler nil-checks it.
	// v7 柱 N: `seek autopilot run "<goal>"` — unattended orchestration.
	// Detected here (after the full agent/subagent/worktree wiring is
	// available) rather than as a cheap early subcommand. In this mode
	// every subagent gets the no-remote bash guard for the whole run.
	// The SEEK_ACP_YOLO lane shares the guard: subagents spawnable
	// there must not push either (PRD paseo §3.3).
	autopilotMode := len(os.Args) >= 2 && os.Args[1] == "autopilot"
	autopilotGoal := ""
	if autopilotMode && len(os.Args) >= 3 && os.Args[2] == "run" {
		autopilotGoal = strings.TrimSpace(strings.Join(os.Args[3:], " "))
	}
	// Lane label for the no-remote guard mounted on every subagent bash in
	// this run; "" = no guard. A label rather than a func so the child
	// wiring passes the REAL lane into the refusal (c_ajza7r: under
	// SEEK_ACP_YOLO the old wording claimed "autopilot runs unattended"
	// while a human was present and had asked for the push). ACP first —
	// the two modes are mutually exclusive today (argv[1]), order makes
	// the precedence explicit rather than accidental.
	autopilotGuardLane := ""
	if acpYolo {
		autopilotGuardLane = autopilot.LaneACP
	} else if autopilotMode {
		autopilotGuardLane = autopilot.LaneAutopilot
	}

	var subagentMgr *subagent.Manager
	var bgNotifier *subagent.Notifier
	if !*noSave && activeSession != nil {
		var smerr error
		subagentMgr, smerr = subagent.NewManager(subagent.ManagerOpts{
			ProjectAbsPath: abs,
			ParentTracker:  tracker,
			ParentPolicy:   policy,
			ParentSidFn: func() string {
				if activeSession == nil {
					return ""
				}
				return activeSession.ID
			},
			ProjectSectionFn:  func() string { return projMD.Section() },
			SkillManifestFn:   func() string { return skills.Manifest() },
			ParentToolNamesFn: func() []string { return reg.Names() },
			// Static, not a closure: the date is fixed at session start
			// (unlike project/skill which hot-reload), and must be byte-
			// identical to the parent's prompt so the subagent header
			// matches. See sessionDate above.
			SessionDate: sessionDate,
			// Same static discipline: MCP servers load once at startup,
			// so the subagent inherits the parent's MCPSection bytes
			// verbatim (§3.6.1 inheritance, fifth segment).
			MCPSection: mcpSection,
			// Background spawns bind to the process/session ctx so a
			// parent turn returning (Esc) can never kill them — the
			// 柱 K bgjob ctx-discipline lifted to agent level
			// (feature-orchestration PRD §4.1 D1).
			SessionCtx: ctx,
			// P2-b D13: custom file-defined agent types become
			// spawnable through the gate (unknown tools degrade to
			// the general-purpose toolset with the startup warning
			// below; everything else fails the spawn loudly). P2-c:
			// EVERY definition routes through the typed registry —
			// built-ins included (byte-identical to the compile-time
			// path), config clones bring their routing knobs. The
			// `agent` tool schema enum is generated from the same
			// registry at startup (agenttool.CacheTypesSchema above).
			AgentTypes: agentTypes,
			Runner: buildSubagentRunner(subagentRunnerOpts{
				client:    dsClient,
				provider:  provider,
				parentReg: reg,
				// Children share the parent's hook surface EXCEPT the
				// background Notifier (parent-only drain point). Lazy
				// per-spawn resolution so hooks registered after Manager
				// construction (memory hook) still reach children.
				hooksFn: func() *hooks.Registry {
					return hooksReg.CloneWithout(bgNotifier)
				},
				maxTokens:     *maxTokens,
				maxTurns:      *maxTurns,
				getModel:      func() string { return sessionModel },
				getEffort:     func() string { return sessionEffort },
				routes:        buildSubagentRoutes(dsClient, *baseURL),
				bashGuardLane: autopilotGuardLane,
				bashSandbox:   autopilotMode,
				bashEnvKeep:   bashEnvKeep,
				bgMgr:         bgMgr,
			}),
		})
		if smerr != nil {
			return fmt.Errorf("subagent manager: %w", smerr)
		}
		reg.Add(agenttool.New(subagentMgr, wtMgr))

		// Background-completion delivery (feature-orchestration PRD
		// §4.1 D2): the Notifier drains pending background notices
		// into the PARENT Prompt as <context> Prepend messages. It
		// lives on the parent registry only — children get it
		// stripped via the CloneWithout closure above.
		bgNotifier = subagent.NewNotifier(subagentMgr)
		hooksReg.Register(bgNotifier)

		// Clean-exit contract: cancel every still-active subagent
		// (background children write `failed reason=canceled`
		// terminal events) and wait for those events to land before
		// the process tears down — no started-without-terminal
		// residue for the orphan scanner.
		defer subagentMgr.Shutdown()

		// On startup, scan the index for `started`-without-terminal
		// sub_sids (subagents whose owning seek process crashed
		// mid-run). Marks them `orphaned` so /agents doesn't show
		// stale "active" rows after a crash recover. Best-effort —
		// failure here doesn't block startup; the panel just shows
		// the stale row until next clean shutdown.
		if indexPath, perr := paths.SubagentsIndex(abs); perr == nil {
			if _, oerr := subagent.OrphanRecover(indexPath); oerr != nil {
				fmt.Fprintln(os.Stderr, "subagent: orphan recover:", oerr)
			}
		}

		// D3 resume 回灌 (M-O1.3): rehydrate the delivery queue for
		// THIS session's background children that reached a terminal
		// state in a prior process without their notice ever being
		// ingested (completed-not-notified; orphaned/failed get a
		// one-line status note). Runs AFTER OrphanRecover so crash
		// residue folds to orphaned before selection. Delivery rides
		// the normal path — the Notifier prepends them as <context>
		// messages on the FIRST Prompt after resume and appends the
		// `notified` marker, so repeated resumes never re-inject.
		// No-op for fresh sessions (the ParentSid filter finds
		// nothing) and when nothing is pending.
		if n, rerr := subagentMgr.RecoverNotices(); rerr != nil {
			fmt.Fprintln(os.Stderr, "subagent: notice recover:", rerr)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "subagent: %d background notice(s) from a prior session — delivered on your next message\n", n)
		}
	}

	// v7 柱 N: autopilot takes over before the TUI/print dispatch.
	// runAutopilot nil-checks the manager/client and returns a usage error
	// for an empty goal, so this is safe even with --no-save / missing key
	// (and never silently falls through to the TUI).
	if autopilotMode {
		mdl := *model
		if mdl == "" {
			mdl = modelDefault
		}
		return runAutopilot(ctx, dsClient, mdl, subagentMgr, wtMgr, autopilotGoal)
	}

	// v5 柱 H auto-tick: fire any due cron jobs as a side effect
	// of starting seek interactively. Detached goroutine so the
	// agent loop doesn't wait for cron subprocesses. Errors
	// stderr-only; cron tick is best-effort and never blocks the
	// foreground path.
	//
	// Why side-effect-on-startup at all: most users won't bother
	// to install a launchd / systemd / cron job to call
	// `seek cron tick` every minute. Hooking it to every
	// interactive launch makes routines work out of the box —
	// "I open seek, my morning report runs". OS scheduler is
	// still the right answer for "fire while seek isn't open";
	// auto-tick is the right answer for "fire when seek IS open".
	//
	// --no-cron-tick opts out for CI / scripted invocations
	// where the side effect would surprise. The early-dispatch
	// CLI subcommands (skill / memory / checkpoint / hooks /
	// worktree / cron itself) return before reaching this point,
	// so `seek cron tick` doesn't trigger an inner auto-tick
	// (no infinite recursion risk).
	//
	// The ACP headless lane skips as well (PRD M1, §2.3): its
	// processes are short-lived spawns (paseo probes / Zed
	// sessions) that kill the detached tick goroutine mid-run —
	// a half-fired cron job risk for zero benefit. And this
	// can't ride the flag: flag.Parse stops at the "acp"
	// positional, so --no-cron-tick is inexpressible in that
	// lane; acpLane gates independently.
	if cronAutoTickEnabled(*noCronTick, acpLane) {
		go func() {
			cronStore, err := routines.OpenStore()
			if err != nil {
				fmt.Fprintln(os.Stderr, "cron auto-tick: open store:", err)
				return
			}
			if _, err := routines.Tick(ctx, cronStore, routines.TickOptions{Webhook: routinescli.WebhookDispatcherFromConfig()}); err != nil {
				fmt.Fprintln(os.Stderr, "cron auto-tick:", err)
			}
		}()
	}

	// sessionIDFn feeds agent.Config.SessionID — ChatRequest.SessionID,
	// emitted as x-opencode-session by the openai client (OpenCode Go
	// lane's per-conversation routing/prompt-cache contract; inert on
	// every other lane). Live read, not a captured value: /compact
	// forks the session under a new ID while this agent object lives
	// on — same pattern as planBridge.sessionID below. "" under
	// --no-save (no session) sends no header.
	sessionIDFn := func() string {
		if activeSession == nil {
			return ""
		}
		return activeSession.ID
	}

	ag, err := agent.New(agent.Config{
		Client:          dsClient,
		Provider:        provider,
		Model:           *model,
		Effort:          sessionEffort,
		SafetyCheck:     safetyCheck,
		SystemPrompt:    systemPrompt,
		Tools:           reg,
		MaxTokens:       *maxTokens,
		MaxTurns:        *maxTurns,
		AutoContinue:    *autoContinue,
		InitialMessages: initialMsgs,
		Hooks:           hooksReg,
		PrepareMessages: prepareMessages,
		SessionID:       sessionIDFn,
		// feature-vision: resolve Asset-referenced images into data
		// URLs at send time. Wired whenever the project assets dir is
		// computable; nil only in pathological setups, where an
		// unresolved image fails the request loudly at marshal time.
		ImageLoader: imageAssetsLoader(abs),
		// feature-deepseek-app-api F4: the document sibling — lazy
		// upload / expiry re-upload of file_id from the asset library.
		FileResolver: fileResolver(abs, dsClient),
	})
	if err != nil {
		return err
	}

	// The submit-time image router, shared by all three input entries
	// (print, TUI, ACP) so capability decisions are byte-identical.
	vroute := newVisionRouter(abs)

	// The submit-time document router, same three entries
	// (feature-deepseek-app-api F3). dsClient is nil on non-DeepSeek
	// lanes — the router then degrades every reference to an in-band
	// lane note instead of uploading.
	froute := fileroute.New(dsClient, abs, tracker)

	// Register the plan + propose tools AFTER agent.New so both Sinks
	// can route into ag.EmitEvent. Same deferred-registration pattern
	// as memorytool.NewObserve below (the Registry's tool list isn't
	// frozen until Wire() runs on the first Prompt). The plan tool is
	// constructed first because the propose sink Seeds it on approval
	// — both tools share that *plan.Tool pointer for the program's
	// lifetime. See PRD docs/prd/feature-plan-mode.md for the full
	// plan-mode flow.
	bridge := &planBridge{
		getAgent:        func() *agent.Agent { return ag },
		policy:          policy,
		projectAbs:      abs,
		artifactEnabled: !*noSave,
		sessionID: func() string {
			// activeSession may flip on /compact (forked session
			// with new ID) — close over the variable, not the
			// current value, so the next Approved picks up the
			// live ID. Empty string in --no-save mode signals "do
			// not write artifacts" to Approved's gate (defensive;
			// artifactEnabled above is the primary gate).
			if activeSession == nil {
				return ""
			}
			return activeSession.ID
		},
	}
	planTool := plantool.New(bridge)
	bridge.planTool = planTool
	reg.Add(planTool)
	reg.Add(propose.New(askPolicy, bridge))

	// On resume, rebuild the plan task list from the transcript so the
	// TUI shows the user where they left off. Reconstruction here only
	// populates `restoredPlanSteps` for the initial tui.Options seed;
	// the planTool itself is also Restored so subsequent plan() calls
	// from the model see the correct in-memory state. Reconstruction
	// is a pure-read scan — no events emitted (no agent Prompt active
	// yet, EmitEvent would no-op anyway).
	var (
		restoredPlanSteps  []agent.PlanStep
		restoredPlanCurIdx = -1
	)
	if loaded != nil && len(initialMsgs) > 0 {
		steps, cur := plantool.ReconstructFromTranscript(initialMsgs)
		if len(steps) > 0 {
			planTool.Restore(steps, cur)
			restoredPlanSteps = make([]agent.PlanStep, len(steps))
			for i, st := range steps {
				restoredPlanSteps[i] = agent.PlanStep{Text: st.Text, Status: string(st.Status)}
			}
			restoredPlanCurIdx = cur
		}
	}

	// Register the memory hook AFTER agent.New so the M5.7 auto-distill
	// HistoryProvider can close over ag.Messages. The Registry stores a
	// pointer and dispatches at call-time, so deferring registration
	// past agent.New is safe — no events fire until NotifySessionStart
	// below.
	var memHook *memory.Hook
	if memProject != nil || memSoul != nil {
		memHook = &memory.Hook{
			Project:    memProject,
			Soul:       memSoul,
			ResultChan: make(chan memory.ObserveResult, 20),
		}
		if memProject != nil && dsClient != nil {
			memHook.Distiller = &memory.Distiller{Client: dsClient}
			memHook.HistoryProvider = ag.Messages
			memHook.Dreamer = &memory.Dreamer{Client: dsClient}

			// Register memory_observe tool gated on $SEEK_AUTO_DISTILL.
			// Default (unset / 1/true/yes/on) → registered so the model
			// can save decisions in real time. Set to 0/false/no/off to
			// disable (PRD §6 v2).
			if autoDistillEnabled() {
				enqueue := memHook.ObserveEnqueue()
				reg.Add(memorytool.NewObserve(memProject, enqueue))
			}
		}
		hooksReg.Register(memHook)
	}

	// M-O3.2 pre_prompt_inject: the authorized shell stdout-injection
	// face (feature-hooks-injection §4.5 / orchestration §6 D17).
	// Registered AFTER the in-process PrePrompt hooks (background
	// notifier, memory) so its <context type="hook"> messages land
	// behind them in the turn's frontier batch — the PRD's "in-process
	// hooks 之后" ordering. ShellRunner itself stays registered early
	// for the pre_tool deny gate; this separate face is what lets both
	// orderings hold at once. Children inherit it via the
	// CloneWithout-on-bgNotifier closure (inject hooks fire per child
	// Prompt too — same shared-surface semantics as pre_tool).
	if shellRunner != nil && shellRunner.HasInjectHooks() {
		hooksReg.Register(shellRunner.InjectHook())
	}

	// v8·T1 `-tools` whitelist (bare-core comparison lane). Runs here —
	// after the LAST reg.Add (memory_observe above; the D13 gate below
	// documents why this is the final-composition point) and before the
	// first Prompt freezes the registry via Wire(). The registry is
	// mutated in place, so every consumer that already holds the pointer
	// (agent.Config.Tools, RebuildAgent, ParentToolNamesFn → subagent
	// child registries) sees the filtered set with no extra wiring.
	// MCP bridges are loaded further up and simply drop out of the
	// registry unless listed by registered name — the "load, then
	// filter" order is what makes an MCP tool name validatable.
	if err := applyToolsWhitelist(*toolsFlag, reg, loaded, activeSession); err != nil {
		return err
	}

	// P2-b D13 startup gate + P2-c config layer: validate every
	// non-built-in definition against the FINAL parent registry —
	// plan/propose land above, MCP / env-gated tools (webfetch,
	// fim_complete, think…) vary the registry earlier, so this pass
	// must run after the last reg.Add. File-scope types
	// (project/user) get the full D13 check — unknown tool names are
	// config errors: warn here (the loud half of D13); at spawn the
	// affected type degrades to the general-purpose toolset — never
	// silently narrowed. Config-layer clones carry only routing knobs
	// on top of a built-in (tools are the built-in's own, already
	// sane), so they get the provider/model lane check only. Provider
	// and workflow errors are warned about too and hard-fail the
	// spawn with the same message when the type is actually used.
	if agentTypes != nil && agentTypes.CustomCount() > 0 {
		for _, def := range agentTypes.List() {
			switch def.Origin {
			case agenttypes.OriginBuiltin:
				continue
			case agenttypes.OriginConfig:
				if def.Provider != "" || def.Model != "" {
					if _, err := subagent.ResolveRoute(def.Provider, def.Model); err != nil {
						fmt.Fprintf(os.Stderr, "agents: %s: %v\n", def.Source, err)
					}
				}
			default:
				if err := subagent.ValidateCustomType(def, reg.Names()); err != nil {
					fmt.Fprintf(os.Stderr, "agents: %s: %v\n", def.Source, err)
				}
			}
		}
	}

	var observeResultChan <-chan memory.ObserveResult
	if memHook != nil {
		observeResultChan = memHook.ResultChan
	}

	// Background-subagent wake signal for the TUI loop (nil when the
	// Manager isn't wired — --no-save — the wake handler gates on it).
	var subagentWakeCh <-chan struct{}
	if subagentMgr != nil {
		subagentWakeCh = subagentMgr.WakeCh()
	}

	// skill auto-distillation (feature-skill-distill M-SD2): the
	// SessionStart background batch. Off unless $SEEK_AUTO_SKILL_DISTILL
	// is truthy — the hook itself re-checks on every start, so the env
	// can be flipped without relaunching wiring. Registered whenever the
	// reasoner half exists (DeepSeek client + loaded skills); a nil-ish
	// hook would no-op anyway, but keeping the Registry's Stack honest
	// about what COULD run is worth the branch.
	if dsClient != nil && skills != nil {
		autoDistiller := &skilldistill.Distiller{Client: dsClient}
		if v := os.Getenv(skilldistill.EnvSkillDistillModel); v != "" {
			autoDistiller.Model = v // PRD §7: unattended quality-first switch to Pro
		}
		var autoMIndex []skilldistill.MEntry
		if memProject != nil {
			for _, e := range memProject.Index() {
				autoMIndex = append(autoMIndex, skilldistill.MEntry{Name: e.Name, Tagline: e.Tagline})
			}
		}
		projDir, perr := paths.ProjectDir(abs)
		if perr == nil {
			hooksReg.Register(&skilldistill.AutoHook{
				Distiller:  autoDistiller,
				Skills:     skills,
				MIndex:     autoMIndex,
				Drafts:     &skillmgr.DraftStore{},
				Stats:      statsWriter,
				Sessions:   skilldistill.SessionStore{Store: store},
				ProjectID:  paths.ProjectID(abs),
				ProjectDir: projDir,
			})
		}
	}

	var sessionID string
	if activeSession != nil {
		sessionID = activeSession.ID
	}
	hooksReg.NotifySessionStart(ctx, hooks.SessionStartEvent{
		ID:      sessionID,
		Model:   *model,
		CWD:     abs,
		Resumed: loaded != nil,
	})
	defer func() {
		hooksReg.NotifySessionEnd(context.Background(), hooks.SessionEndEvent{
			ID:    sessionID,
			Usage: tracker.Cumulative(),
		})
		// Flush the shell-hook audit log on the way out. SessionEnd
		// observers in ShellRunner write one final audit row; Close
		// makes sure the file's fsync isn't lost to a fast exit.
		if shellRunner != nil {
			_ = shellRunner.Close()
		}
	}()

	// Benchmark mode: short-circuit before normal routing. Forces --yolo
	// so the agent can run bash/go-test without interactive approval.
	if *benchmarkTask != "" {
		*yolo = true
		policy.SetPref(permission.PrefYolo)
		return runBenchmark(ctx, *benchmarkTask, *benchmarkOut,
			ag, tracker, *model, activeSession, store)
	}

	if *rpcMode {
		return runRPC(ctx, ag, tracker, *model, *yolo, *plan, activeSession, store)
	}

	// v7 柱 P: `seek acp` → speak the Agent Client Protocol over stdio.
	// feature-vision: pasted image blocks route through the same
	// submit-time vision router as the TUI (native attach / switch note).
	// feature-deepseek-app-api F3: document references in the prompt
	// text route through the same submit-time file router as the TUI.
	// Permission posture (incl. SEEK_ACP_YOLO) was already resolved into
	// initialPref above — nothing to flip here. The model menu is built
	// from the startup lane (*model already carries the env pin / loaded
	// session's choice at this point); nil = freeform model, no dropdown.
	if acpLane {
		return runACP(ctx, acpOptions{
			ag:     ag,
			vroute: newVisionRouter(abs),
			froute: froute,
			policy: policy,
			menu:   acpModelMenu(*model, openCatalogRows()),
			sess:   activeSession,
			store:  store,
		})
	}

	// `seek goal run "<condition>"` → headless single-agent loop-until-met
	// (M-goal.3). Uses the same Driver as the TUI's /goal, with a real
	// agent Worker. Cron-composable (M-goal.4).
	if goalMode {
		cond := ""
		if len(os.Args) >= 3 && os.Args[2] == "run" {
			cond = strings.TrimSpace(strings.Join(os.Args[3:], " "))
		}
		// Unattended loop → elevate to yolo so the agent can actually edit/
		// run bash without per-action approval (there's no human to ask).
		// LOCAL only: the bash no-remote guard wired above still blocks
		// push/PR, so an overnight goal can't leave the machine. Same
		// safety posture as autopilot.
		policy.SetPref(permission.PrefYolo)
		return runGoal(ctx, ag, dsClient, sessionModel, cond)
	}

	// Route: --rpc → JSON-RPC 2.0 server; -json / -p / piped stdin → print; otherwise TUI.
	if *jsonOut || *prompt != "" || stdinIsPiped() {
		text, err := resolvePrompt(*prompt)
		if err != nil {
			return err
		}
		if text == "" {
			return fmt.Errorf("empty prompt (pass -p or pipe text on stdin)")
		}
		// feature-vision: route image references at submit time —
		// vision model attaches bytes natively, anything else gets an
		// in-band switch-model note. Never errors (M-V.2).
		text, imageParts := vroute.route(ag.Model(), text)
		// feature-deepseek-app-api F3: route document references at
		// the same station — copy-at-submit + eager upload + parse
		// fee, every failure in-band. Never errors.
		text, fileParts := froute.Route(ctx, ag.Model(), text)
		if *jsonOut {
			// W5 c_n37tnr child half: --approval-stdin turns print mode's
			// silence into a decision channel. The relay emits
			// approval_request lines on the same JSONL stream and blocks
			// on the decision line the host writes to stdin; "a" grants
			// the per-Kind session allowlist, everything else is the
			// usual ErrDenied tool result. The policy's PrefAsk was
			// resolved with initialPref above (before the system prompt
			// was composed, so the mode label matches the gate).
			var relay *approvalRelay
			if *approvalStdin {
				if *prompt == "" {
					return fmt.Errorf("--approval-stdin needs -p <prompt>: stdin carries decision lines, not the prompt")
				}
				if *yolo {
					// --yolo never consults askFn; say so instead of
					// silently leaving the channel dark.
					fmt.Fprintln(os.Stderr, "seek: --approval-stdin ignored under --yolo (nothing is gated)")
				}
				relay = newApprovalRelay(os.Stdin)
				relay.grant = policy.SetAlwaysAllow
				policy.SetAskFn(relay.askFn(ctx))
			}
			return runJSON(ctx, ag, tracker, *model, *yolo, *plan, text, imageParts, fileParts, activeSession, store, relay)
		}
		return runPrint(ctx, ag, tracker, *model, *yolo, *plan, text, imageParts, fileParts, activeSession, store)
	}

	// Now that we know we're entering the TUI, upgrade the policy
	// from Deny → Ask unless --yolo was passed. This is what gives
	// us inline y/N prompts on bash and out-of-CWD writes. --plan
	// doesn't change pref (it sets the workflow); upgrading pref to
	// Ask here is correct for --plan too — plan-execute uses Ask for
	// per-call gating after approval.
	if !*yolo {
		policy.SetPref(permission.PrefAsk)
	}

	// Approval channel: askFn pushes a request, blocks on its reply.
	// Buffered so a slow TUI doesn't deadlock a fast tool dispatcher
	// (the agent loop is sequential today, but the buffer is cheap).
	approvalCh := make(chan permission.ApprovalRequest, 4)
	policy.SetAskFn(func(a permission.Action) bool {
		resp := make(chan bool, 1)
		select {
		case approvalCh <- permission.ApprovalRequest{Action: a, Reply: resp}:
		case <-ctx.Done():
			return false
		}
		select {
		case ok := <-resp:
			return ok
		case <-ctx.Done():
			return false
		}
	})

	// ask_user channel: same pattern as approval, but the reply
	// type is the structured askuser.Answer (chosen ids OR free
	// text OR cancelled) rather than a bool. Buffer 4 mirrors the
	// approval channel — never has more than one in flight today,
	// the buffer is a defence against future parallelism. askPolicy
	// itself was constructed earlier (before tool registration) so
	// the askuser tool could capture it; SetAskFn registers the
	// real callback here, now that ctx + askUserCh are in scope.
	askUserCh := make(chan askuser.Request, 4)
	askPolicy.SetAskFn(func(q askuser.Question) askuser.Answer {
		resp := make(chan askuser.Answer, 1)
		select {
		case askUserCh <- askuser.Request{Question: q, Reply: resp}:
		case <-ctx.Done():
			return askuser.Answer{Cancelled: true}
		}
		select {
		case ans := <-resp:
			return ans
		case <-ctx.Done():
			return askuser.Answer{Cancelled: true}
		}
	})

	// v2 batch channel: parallel to askUserCh but carries a Batch +
	// receives a []Answer. ask_user v2 (柱 I) tool uses AskBatch
	// which goes through this path; the TUI renders a multi-question
	// stack instead of a single picker. Buffer 4 matches the single-
	// question channel.
	askBatchCh := make(chan askuser.BatchRequest, 4)
	askPolicy.SetAskBatchFn(func(b askuser.Batch) []askuser.Answer {
		resp := make(chan []askuser.Answer, 1)
		// Defensive cancelled payload sized to the batch — every
		// ctx.Done() / channel-close path returns this so the
		// caller never sees a nil slice (which would index-panic
		// in the tool layer's result builder).
		cancelled := make([]askuser.Answer, len(b.Questions))
		for i := range cancelled {
			cancelled[i] = askuser.Answer{Cancelled: true}
		}
		select {
		case askBatchCh <- askuser.BatchRequest{Batch: b, Reply: resp}:
		case <-ctx.Done():
			return cancelled
		}
		select {
		case ans := <-resp:
			return ans
		case <-ctx.Done():
			return cancelled
		}
	})

	sessionModel = *model

	// Resolve the effective theme for the TUI.
	effectiveTheme := strings.ToLower(*themeFlag)
	glamourStyle := detectGlamourStyle(effectiveTheme)
	// If auto, resolve to the concrete dark/light value.
	if effectiveTheme == "auto" {
		effectiveTheme = glamourStyle
	}

	// /distill needs both the Project (where saved candidates land)
	// and a Distiller (the thinking-mode round-trip). The Distiller is only
	// constructible when we have a DeepSeek client — second-tier
	// providers don't currently expose a Chat method on the same
	// interface, so /distill is DeepSeek-only for now.
	var distiller *memory.Distiller
	if memProject != nil && dsClient != nil {
		distiller = &memory.Distiller{Client: dsClient}
	}

	// The skill segment of /distill (feature-skill-distill M-SD1) is
	// same-shaped: needs the DeepSeek client for extraction and the
	// loaded skill registry for the L0/L2 dedup context. nil on
	// second-tier providers → /distill silently stays M-only.
	var skillDistiller *skilldistill.Distiller
	if dsClient != nil && skills != nil {
		skillDistiller = &skilldistill.Distiller{Client: dsClient}
	}

	// 柱 M interactive push: notify a configured webhook when a long
	// interactive turn finishes. Reuses the cron push dispatcher; the
	// duration gate (default 60s) comes from config. nil dispatcher when
	// no push_webhooks are set, so this is a no-op for most users.
	sessionNotifySeconds := 60
	if cfg, cerr := config.Load(); cerr == nil {
		sessionNotifySeconds = cfg.SessionNotifySecondsOrDefault()
	}

	// Auto-compact trigger: config's `auto_compact` section (or
	// $SEEK_AUTO_COMPACT for one launch), resolved per model at call time
	// so a /model switch moves a fraction-configured trigger with the
	// window. Loaded once — the section is read at startup like every
	// other standing posture; the ENV override is re-read on each call so
	// a shell that exports it always wins.
	autoCompactCfg, _ := config.Load()
	autoCompactAt := autoCompactCfg.AutoCompactThreshold

	// /goal judge (M-goal.2): a cheap per-turn "is the condition met?" call.
	// Uses the session model for now (guaranteed valid); a dedicated cheap
	// goal.judge_model lands with the headless path. nil on non-DeepSeek
	// providers → /goal reports "unavailable" rather than crashing.
	var goalJudge goal.Judge
	if dsClient != nil {
		goalJudge = goal.NewDeepSeekJudge(dsClient, sessionModel)
	}
	// A resumed session may carry an unfinished /goal (M-goal.3); re-arm it.
	resumeGoal := ""
	if activeSession != nil {
		resumeGoal = activeSession.Goal
	}
	// Clipboard image paste (M-imgpaste.2): Ctrl+V grabs an image off the OS
	// clipboard to a temp PNG, which the vision input router then attaches
	// natively (feature-vision; the OCR-era pipeline is gone). The grabber
	// command comes from SEEK_CLIPBOARD_IMAGE_CMD (else the platform default);
	// clipimage.Grab degrades to ErrNoImage/ErrNoGrabber → text paste.
	grabImage := func(gctx context.Context) (string, error) {
		var cmd []string
		if v := strings.TrimSpace(os.Getenv("SEEK_CLIPBOARD_IMAGE_CMD")); v != "" {
			cmd = strings.Fields(v)
		}
		dir := os.TempDir()
		if c, err := paths.Cache(); err == nil {
			dir = filepath.Join(c, "clipboard")
		}
		return clipimage.Grab(gctx, clipimage.Options{Command: cmd, CacheDir: dir})
	}

	return tui.Run(tui.Options{
		Agent:                 ag,
		Tracker:               tracker,
		Model:                 sessionModel,
		Effort:                sessionEffort,
		ExpandInput:           vroute.route,
		ExpandFiles:           froute.Route,
		GoalJudge:             goalJudge,
		ResumeGoal:            resumeGoal,
		GrabImage:             grabImage,
		Yolo:                  policy.Yolo(),
		Plan:                  policy.Plan(),
		PlanSteps:             restoredPlanSteps,
		PlanCurrentIdx:        restoredPlanCurIdx,
		RevokePlanPreApproval: func() { policy.SetPreApproved(false) },
		CWD:                   abs,
		Ctx:                   ctx,
		Theme:                 effectiveTheme,
		GlamourStyle:          glamourStyle,
		ApprovalCh:            approvalCh,
		AskUserCh:             askUserCh,
		AskUserBatchCh:        askBatchCh,
		Session:               activeSession,
		Store:                 store,
		Checkpoint:            ckMgr,
		Subagents:             subagentMgr,
		Worktrees:             wtMgr,
		Keymap:                userKeymap,
		Suggester:             predictor,
		Skills:                skills,
		AgentTypes:            agentTypes,
		Webhook:               routinescli.WebhookDispatcherFromConfig(),
		SessionNotifySeconds:  sessionNotifySeconds,
		AutoCompactAt:         autoCompactAt,
		ProviderName:          provLabel,
		OpenCodeModels:        openCatalogRows,
		RefreshOpenCodeModels: refreshOpenCatalog,
		MemoryProject:         memProject,
		Distiller:             distiller,
		SkillDistiller:        skillDistiller,
		ObserveResultChan:     observeResultChan,
		SubagentWake:          subagentWakeCh,

		RebuildAgent: func() (*agent.Agent, error) {
			// /reset rebuilds the agent; we have to re-apply project
			// instructions AND the skill manifest, otherwise the model
			// would forget both after a reset. AGENTS.md is loaded
			// once at startup and reused (re-reading on /reset would
			// surprise users who edit the file mid-session — we want
			// the file's behaviour to be "loaded at launch", not "hot-
			// reloaded"; documented behaviour is easier to reason
			// about than clever).
			//
			// Skills, however, ARE hot-reloaded here so that newly
			// installed skills (via skill_commit) appear in the system
			// prompt manifest after /new without requiring a full restart.
			if freshSkills, _, lerr := skill.Load(skill.LoadOptions{ProjectDir: cwd}); lerr == nil && freshSkills != nil {
				skills = freshSkills
			}
			sp := sysprompt.Compose(sysprompt.Header{
				Cwd:            abs,
				ProjectSection: projMD.Section(),
				SkillManifest:  skills.Manifest(),
				MCPSection:     mcpSection,
				Date:           sessionDate,
			}, sysprompt.ModeLabel(policy.Pref(), policy.Workflow()))
			newAg, err := agent.New(agent.Config{
				Client:       dsClient,
				Provider:     provider,
				Model:        sessionModel,
				Effort:       sessionEffort,
				SafetyCheck:  safetyCheck,
				SystemPrompt: sp,
				Tools:        reg,
				MaxTokens:    *maxTokens,
				MaxTurns:     *maxTurns,
				AutoContinue: *autoContinue,
				SessionID:    sessionIDFn,
			})
			if err != nil {
				return nil, err
			}
			// Keep the closure-captured ag in sync so SetYolo /
			// SetPlan callbacks update the live agent's
			// ModeLabel, not a stale copy.
			ag = newAg
			// Rebuilt system prompt already has the correct mode
			// label — clear per-message reminder.
			ag.SetModeLabel("")
			return newAg, nil
		},
		SetModel: func(m string) { sessionModel = m },
		SetEffort: func(e string) {
			// Mirror into the closures the think tool and agent.Config
			// builds read from. The TUI separately calls Agent.SetEffort
			// on the live agent so the change is visible on the very
			// next prompt without a /reset / RebuildAgent.
			sessionEffort = e
		},
		SetYolo: func(y bool) {
			// policy.SetPref takes effect immediately for every
			// tool's permission.Check call. The agent's per-message
			// modeReminder keeps the model in sync without touching
			// the system prompt (prefix-cache safe).
			if y {
				policy.SetPref(permission.PrefYolo)
				ag.SetModeLabel("yolo")
			} else {
				policy.SetPref(permission.PrefAsk)
				ag.SetModeLabel("")
			}
		},
		// The approval prompt's "[a] always: <kind>" answer — a per-Kind
		// session grant, NOT a yolo escalation. SetPref/SetWorkflow clear
		// it (see permission.Policy.alwaysAllow).
		AlwaysAllowKind: policy.SetAlwaysAllow,
		SetPlan: func(p bool) {
			// /plan toggles the Workflow axis (not Pref). Entering
			// /plan starts in the analyze substate (plan mode v2;
			// PRD §2.4). The legacy "plan" label still works in
			// modeReminder for back-compat.
			if p {
				policy.SetWorkflow(permission.WorkflowPlanAnalyze)
				ag.SetModeLabel("plan-analyze")
			} else {
				policy.SetWorkflow(permission.WorkflowNone)
				ag.SetModeLabel("")
			}
		},
		SetPlanSubstate: func(s string) {
			// Driven by propose tool events flowing through
			// applyAgentEvent. "execute" moves the workflow to
			// PlanExecute (pref is untouched — typically Ask, so
			// per-call gating applies unless preApproved is set by
			// plan-tool start/complete). "" is symmetric with
			// SetPlan(false).
			switch s {
			case "execute":
				policy.SetWorkflow(permission.WorkflowPlanExecute)
				ag.SetModeLabel("plan-execute")
			case "analyze":
				policy.SetWorkflow(permission.WorkflowPlanAnalyze)
				ag.SetModeLabel("plan-analyze")
			case "":
				policy.SetWorkflow(permission.WorkflowNone)
				ag.SetModeLabel("")
			}
		},
	})
}

// stdinIsPiped returns true when stdin is not a terminal (i.e. data is
// being piped or redirected in). When false, seek launches the TUI.
func stdinIsPiped() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice == 0
}

// runPrint preserves the M3 print-mode behaviour: stream to stdout,
// tool indicators + stats footer to stderr. Suitable for piping.
// The session is saved after every TurnEnd so a crash or interrupt
// mid-run preserves progress up to the last completed turn.
func runPrint(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, text string, images []deepseek.ImagePart, files []deepseek.FilePart, activeSession *session.Session, store *session.Store) error {
	tier := pricing.CurrentTier(time.Now())
	nextTier, nextAt := pricing.NextTransition(time.Now())
	fmt.Fprintf(os.Stderr, "\x1b[2mtier: %s → next %s at %s\x1b[0m\n",
		pricing.TierLabel(tier),
		pricing.TierLabel(nextTier),
		nextAt.In(pricing.Shanghai).Format("2006-01-02 15:04 MST"))

	start := time.Now()
	var (
		firstByte time.Duration
		gotFirst  bool
		turns     int
		toolCalls int
	)

	// saveTurn snapshots the current agent state to disk. Called after
	// each TurnEnd so an interrupt preserves progress. Failures are
	// warnings only — the answer already printed to stdout.
	saveTurn := func() {
		if activeSession == nil || store == nil {
			return
		}
		activeSession.Messages = ag.Messages()
		activeSession.Turns = turns
		activeSession.ToolCalls = toolCalls
		activeSession.Usage = tracker.Cumulative()
		activeSession.Model = model
		activeSession.Yolo = yolo
		activeSession.Plan = plan
		// In non-TUI runs (runPrint / runJSON / runRPC) there is no
		// /effort command, so the agent's Effort is constant for the
		// lifetime of the process. Reading it off the agent keeps the
		// helper signature stable instead of growing another param.
		activeSession.Effort = ag.Effort()
		if err := store.Save(activeSession); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save session after turn %d: %v\n", turns, err)
		}
	}

	for ev := range ag.PromptFiles(ctx, text, images, files) {
		switch e := ev.(type) {
		case agent.MessageDelta:
			if !gotFirst {
				firstByte = time.Since(start)
				gotFirst = true
			}
			if e.Reasoning {
				fmt.Fprint(os.Stderr, "\x1b[2m"+e.Delta+"\x1b[0m")
			} else {
				fmt.Print(e.Delta)
			}

		case agent.ToolExecStart:
			fmt.Fprintf(os.Stderr, "\n\x1b[36m[tool] → %s %s\x1b[0m\n", e.Name, truncate(e.Args, 200))

		case agent.ToolExecEnd:
			if e.Err != nil {
				fmt.Fprintf(os.Stderr, "\x1b[31m[tool] ← %s ERROR: %v\x1b[0m\n", e.Name, e.Err)
			} else {
				fmt.Fprintf(os.Stderr, "\x1b[36m[tool] ← %s (%d bytes)\x1b[0m\n", e.Name, len(e.Result))
			}

		case agent.TurnEnd:
			tracker.Record(e.Usage, model, pricing.CurrentTier(time.Now()))
			turns++
			toolCalls += e.ToolCalls
			saveTurn()

		case agent.AgentEnd:
			// turns/toolCalls already accumulated via TurnEnd above.

		case agent.ErrorEvent:
			fmt.Println()
			return e.Err
		}
	}

	fmt.Println()
	c := tracker.Cumulative()
	// Cumulative cost is summed from per-turn locked-in amounts in the
	// tracker, not re-derived from cumulative tokens at the current
	// (model, tier). Matters when the session straddled a /model
	// switch or the 00:30/08:30 tier boundary; see internal/cache doc.
	cost := tracker.CumulativeCost()

	fmt.Fprintf(os.Stderr, "\n--- seek stats ---\n")
	fmt.Fprintf(os.Stderr, "yolo:         %v\n", yolo)
	fmt.Fprintf(os.Stderr, "plan:         %v\n", plan)
	fmt.Fprintf(os.Stderr, "model:        %s\n", model)
	fmt.Fprintf(os.Stderr, "tier:         %s\n", pricing.TierLabel(tier))
	fmt.Fprintf(os.Stderr, "turns:        %d\n", turns)
	fmt.Fprintf(os.Stderr, "tool calls:   %d\n", toolCalls)
	fmt.Fprintf(os.Stderr, "ttfb:         %s\n", firstByte.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "elapsed:      %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "prompt tok:   %d (cache hit %d / miss %d, ratio %s)\n",
		c.PromptTokens, c.PromptCacheHitTokens, c.PromptCacheMissTokens, deepseek.FormatHitRatio(c))
	fmt.Fprintf(os.Stderr, "completion:   %d tok\n", c.CompletionTokens)
	fmt.Fprintf(os.Stderr, "est. cost:    %s (saved ~%d input tok via cache)\n",
		pricing.FormatCost(cost), tracker.SavedTokens())

	if activeSession != nil {
		fmt.Fprintf(os.Stderr, "session:      %s (--resume to continue)\n", activeSession.ID)
	}
	return nil
}

// jsonLine is the flat envelope for every JSONL event. Fields are
// omitempty so absent data doesn't clutter the output. Consumers should
// branch on Type; all other fields are type-specific.
//
// The field set is as stable as the Type values: ADDING a field (e.g.
// agent_start's model) is non-breaking — consumers decode into their
// own structs and ignore unknown keys — while removing or repurposing
// one is a major-version break, same discipline as the Type list below.
//
// Type values (stable contract — breaking changes = major version bump):
//
//	agent_start      — one per run; model names the model bound to this run
//	turn_start       — one per LLM call; index is 0-based
//	text_delta       — incremental assistant text; delta is the new chunk
//	reasoning_delta  — incremental CoT text from thinking-mode responses
//	tool_start       — a tool call is about to execute; id/name/args set
//	tool_delta       — intermediate output from a streaming tool (think)
//	tool_end         — tool finished; result set on success, error on failure
//	turn_end         — LLM call settled; token counts + tool_calls count
//	agent_end        — run complete; cumulative stats; session_id if saved
//	error            — fatal error; message is the error string
//	approval_request — a gated action awaits a human decision; req_id keys
//	                   the round-trip, kind/summary/command/diff describe it
//	                   (W5 c_n37tnr, `--approval-stdin` only: the decision
//	                   line arrives on stdin — cmd/seek/approval_stdin.go)
//	approval_cancel  — that request is withdrawn (run ended first); req_id
type jsonLine struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	// agent_start only: model id the run is bound to (pricing consumers
	// need it to turn turn_end usage into $ — feature-web-frontend §2.5)
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
	// approval_request / approval_cancel only (W5 c_n37tnr; the field set
	// mirrors controlplane.RunEvent 1:1 — additive, consumers ignore
	// unknown keys). ReqID keys the decision line that comes back on
	// stdin under --approval-stdin.
	ReqID   string `json:"req_id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Summary string `json:"summary,omitempty"`
	Command string `json:"command,omitempty"`
	Diff    string `json:"diff,omitempty"`
}

// runJSON is the machine-readable output mode: one JSON object per line
// on stdout. Human-readable diagnostics (tier, stats footer) go to
// stderr so stdout stays parse-clean.
//
// approval is the OPTIONAL approval relay of --approval-stdin (W5 card
// c_n37tnr): nil keeps print mode's exact previous behaviour (deny, no
// extra lines). Non-nil wires the relay's emitter to this file's one
// encoder — two encoders on stdout could interleave a card line inside
// an event line — and withdraws any still-pending card when the run
// ends so the browser never keeps a throwaway prompt up.
func runJSON(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, text string, images []deepseek.ImagePart, files []deepseek.FilePart, activeSession *session.Session, store *session.Store, approval *approvalRelay) error {
	enc := json.NewEncoder(os.Stdout)

	// One mutex for every write to stdout: json.Encoder is not safe for
	// concurrent use, and with --approval-stdin a card line is emitted
	// from the TOOL goroutine (the askFn) while the main loop is folding
	// the event stream — an interleaved half-line would corrupt both.
	var outMu sync.Mutex
	emit := func(line jsonLine) {
		outMu.Lock()
		defer outMu.Unlock()
		_ = enc.Encode(line) // json.Encoder always writes a trailing \n
	}
	if approval != nil {
		approval.emit = emit
		defer approval.CancelPending()
	}

	var (
		turns     int
		toolCalls int
	)

	saveTurn := func() {
		if activeSession == nil || store == nil {
			return
		}
		activeSession.Messages = ag.Messages()
		activeSession.Turns = turns
		activeSession.ToolCalls = toolCalls
		activeSession.Usage = tracker.Cumulative()
		activeSession.Model = model
		activeSession.Yolo = yolo
		activeSession.Plan = plan
		// In non-TUI runs (runPrint / runJSON / runRPC) there is no
		// /effort command, so the agent's Effort is constant for the
		// lifetime of the process. Reading it off the agent keeps the
		// helper signature stable instead of growing another param.
		activeSession.Effort = ag.Effort()
		if err := store.Save(activeSession); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save session after turn %d: %v\n", turns, err)
		}
	}

	emit(jsonLine{Type: "agent_start", Model: model})

	for ev := range ag.PromptFiles(ctx, text, images, files) {
		switch e := ev.(type) {

		case agent.TurnStart:
			emit(jsonLine{Type: "turn_start", Index: e.Index})

		case agent.MessageDelta:
			t := "text_delta"
			if e.Reasoning {
				t = "reasoning_delta"
			}
			emit(jsonLine{Type: t, Delta: e.Delta})

		case agent.ToolExecStart:
			emit(jsonLine{Type: "tool_start", ID: e.CallID, Name: e.Name, Args: e.Args})

		case agent.ToolDelta:
			emit(jsonLine{Type: "tool_delta", ID: e.CallID, Name: e.Name, Delta: e.Delta, Reasoning: e.Reasoning})

		case agent.ToolExecEnd:
			line := jsonLine{Type: "tool_end", ID: e.CallID, Name: e.Name}
			if e.Err != nil {
				line.Error = e.Err.Error()
			} else {
				line.Result = e.Result
				line.Bytes = len(e.Result)
			}
			emit(line)

		case agent.TurnEnd:
			tracker.Record(e.Usage, model, pricing.CurrentTier(time.Now()))
			turns++
			toolCalls += e.ToolCalls
			emit(jsonLine{
				Type:             "turn_end",
				Index:            e.Index,
				PromptTokens:     e.Usage.PromptTokens,
				CompletionTokens: e.Usage.CompletionTokens,
				CacheHitTokens:   e.Usage.PromptCacheHitTokens,
				ToolCalls:        e.ToolCalls,
			})
			saveTurn()

		case agent.ErrorEvent:
			emit(jsonLine{Type: "error", Error: e.Err.Error()})
			return e.Err
		}
	}

	c := tracker.Cumulative()
	end := jsonLine{
		Type:             "agent_end",
		Turns:            turns,
		ToolCalls:        toolCalls,
		PromptTokens:     c.PromptTokens,
		CompletionTokens: c.CompletionTokens,
		CacheHitTokens:   c.PromptCacheHitTokens,
	}
	if activeSession != nil {
		end.SessionID = activeSession.ID
	}
	emit(end)
	return nil
}

// runRPC starts a JSON-RPC 2.0 server over stdin/stdout. The server accepts
// requests for agent/prompt, agent/info, and session/list methods. Suitable
// for IDE integrations and scripted automation that need more control than the
// simple -p / --json modes.
func runRPC(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, activeSession *session.Session, store *session.Store) error {
	fmt.Fprintf(os.Stderr, "seek rpc: listening on stdin (JSON-RPC 2.0)\n")
	srv := seekrpc.New(ag, tracker, store, activeSession, model, yolo)
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}

// printSessionList renders the saved-sessions inventory to stdout
// for -list. Tabular but plain-text (no lipgloss) so it pipes cleanly
// into grep / awk.
func printSessionList(store *session.Store) error {
	infos, loadErrs, err := store.List()
	if err != nil {
		return err
	}
	for _, le := range loadErrs {
		fmt.Fprintf(os.Stderr, "warning: skipped unreadable session: %v\n", le)
	}
	if len(infos) == 0 {
		fmt.Println("no saved sessions in", store.Dir())
		return nil
	}
	fmt.Printf("%-25s  %-22s  %-10s  %5s  %5s  %s\n",
		"ID", "UPDATED (UTC)", "MODEL", "TURNS", "TOOLS", "PARENT")
	for _, s := range infos {
		parent := "-"
		if s.ParentID != "" {
			parent = s.ParentID
		}
		fmt.Printf("%-25s  %-22s  %-10s  %5d  %5d  %s\n",
			s.ID,
			s.UpdatedAt.Format("2006-01-02 15:04:05"),
			truncate(s.Model, 10),
			s.Turns,
			s.ToolCalls,
			parent)
	}
	return nil
}

// tuiOCRExpander / ocrOptions were removed with 柱 Q (feature-vision
// M-V.0): the TUI's ExpandInput hook now routes through visionRouter.

// imageAssetsLoader returns the send-time Asset→data-URL loader for
// the project's image store (feature-vision D2/D5). It is handed to
// the agent (Config.ImageLoader) and runs on every DeepSeek-path
// request alongside StripReasoningContent.
func imageAssetsLoader(projectAbs string) deepseek.ImageLoader {
	dir, err := paths.ProjectAssets(projectAbs)
	if err != nil {
		return nil
	}
	return func(asset string) (string, error) {
		return assets.Load(dir, asset)
	}
}

// fileAssetsLoader returns the send-time Asset→bytes loader for the
// project's document store (feature-deepseek-app-api F4) — the file
// sibling of imageAssetsLoader. Paired with fileResolver below: the
// loader supplies the ONLY durable copy for a re-upload (the Files API
// cannot re-download originals), the client supplies the upload.
func fileAssetsLoader(projectAbs string) deepseek.FileLoader {
	dir, err := paths.ProjectAssets(projectAbs)
	if err != nil {
		return nil
	}
	return func(asset string) ([]byte, error) {
		return assets.LoadDoc(dir, asset)
	}
}

// fileResolver builds the send-time document resolver (F4). One
// instance per session: it carries the process-scoped transport cache
// that keeps an expired file_id from being re-parsed (and re-billed) on
// every turn. A nil client (non-DeepSeek lane) still gets a resolver:
// live ids pass through, unhealable parts degrade to in-band notes.
func fileResolver(projectAbs string, client *deepseek.Client) *deepseek.FileResolver {
	load := fileAssetsLoader(projectAbs)
	if load == nil {
		return nil // pathological setup: unresolved parts fail loudly at marshal time, as with images
	}
	var up deepseek.FileUploader
	if client != nil {
		up = client
	}
	return deepseek.NewFileResolver(load, up)
}

// runAutopilot drives the v7 柱 N unattended loop: decompose the goal,
// fan out to no-remote-guarded worktree subagents, aggregate, push the
// summary. The subagents are already guarded (bashGuardLane set on their
// manager for the whole autopilot process), so they produce LOCAL commits
// only — the user reviews + pushes in the morning.
func runAutopilot(ctx context.Context, client *deepseek.Client, model string, mgr *subagent.Manager, wtMgr *worktree.Manager, goal string) error {
	if goal == "" {
		return fmt.Errorf("usage: seek autopilot run \"<goal>\"")
	}
	if client == nil {
		return fmt.Errorf("autopilot requires a model provider — set an API key (seek setup)")
	}
	if mgr == nil {
		return fmt.Errorf("autopilot requires a saved session in a git repo (unavailable with --no-save or outside a git project)")
	}
	dec := autopilot.NewDeepSeekDecomposer(client, model)
	// model is also the lane the worktree subagents run on (they
	// inherit the parent session's), so the fleet prices each
	// outcome's tokens with it (v8 柱 R R1). The same lane judges
	// each completed task (v8 柱 R R2): the verifier is built here,
	// on demand for this run — no resident anything.
	fleet := autopilot.NewFleet(mgr, wtMgr, model, verifier.New(client, model))
	drv := autopilot.New(dec, fleet, autopilot.Caps{Timeout: 30 * time.Minute})

	fmt.Fprintf(os.Stderr, "autopilot: planning + fanning out for %q …\n", goal)
	rep, err := drv.Run(ctx, goal)
	if err != nil {
		return fmt.Errorf("autopilot: %w", err)
	}
	fmt.Println(rep.Body())
	// Best-effort mobile push (柱 M reuse) so an unattended run reaches you.
	if dispatch := routinescli.WebhookDispatcherFromConfig(); dispatch != nil {
		dispatch(ctx, rep.Event(), rep.Title(), rep.Body())
	}
	return nil
}

// acpBackend adapts seek's agent to the Agent Client Protocol (v7 柱 P).
// One seek process = one ACP session (MVP); Prompt streams the agent's
// event channel out as ACP session/update notifications.
type acpBackend struct {
	ag     *agent.Agent
	route  visionRouter     // feature-vision: submit-time image router
	froute fileroute.Router // feature-deepseek-app-api F3: submit-time document router
	policy *permission.Policy
	// sess + store are the persistence pair behind session/load and the
	// sessionId session/new hands out. nil/zero under --no-save, where
	// loadSession is not advertised. sess is the live session POINTER
	// main's closures (subagent parent-sid, x-opencode-session, the
	// checkpoint manager) captured — LoadSession mutates it in place so
	// those closures keep describing the conversation actually running.
	sess  *session.Session
	store *session.Store
	// menu is the configOptions model select (nil ⇒ the client filters
	// the dropdown away, pre-C behaviour). Built once at startup from
	// the startup lane; SetConfigOption rewrites CurrentValue in place.
	// Only touched from the transport's serial read loop (dispatch), so
	// no lock — prompt goroutines never read it.
	menu []acp.ConfigOption
	// laneFor builds a provider lane on demand — a closure over
	// buildProvider so the switch path and the startup path share one
	// source of truth for keys/endpoints/defaults.
	laneFor func(provider string) (llm.Provider, *deepseek.Client, string, error)
	// sessionID is the single live session ("one seek process = one
	// session"); anchored by NewSession / LoadSession, checked by
	// SetConfigOption.
	sessionID string
}

func (b *acpBackend) Initialize(p acp.InitializeParams) acp.InitializeResult {
	return acp.InitializeResult{
		ProtocolVersion: p.ProtocolVersion,
		AgentCapabilities: acp.AgentCapabilities{
			// loadSession is a promise that a sessionId survives the
			// process, so it is only made when a transcript store exists
			// (not under --no-save). ACP clients gate their whole
			// persistence/continue UX on this one boolean: advertising it
			// with nothing behind it turns "continue this conversation"
			// into a hard error at the client.
			LoadSession: b.store != nil && b.sess != nil,
			// Advertise image so editors (Zed) offer image attachment;
			// on a vision model the bytes attach natively, otherwise an
			// in-band note tells the model to suggest switching.
			PromptCapabilities: acp.PromptCapabilities{Image: true},
		},
	}
}

// NewSession anchors the session's working directory to p.Cwd
// (feature-paseo-provider.md §2.2 D-1): headless ACP clients (paseo)
// spawn seek without a cwd, so the project root only arrives here.
// acp.AnchorSession chdirs the process AND re-anchors the shared
// policy, so read/edit/write/bash (policy-anchored) and
// git/grep/list_dir (process-anchored) all follow.
//
// The returned sessionId is seek's DURABLE transcript id, not the
// project name it used to be: ACP's own contract is that the id a
// client gets here is what it later hands back to `session/load`
// ("The ID of the session to load"), and paseo persists exactly this
// value as its native session id. A project-name id could never be
// loaded — it names no file and changes when the client opens a
// different directory. With --no-save there is no durable id, so the
// project name stays (and Initialize stops advertising loadSession).
// Since C, the result also carries the model configOptions menu —
// a non-nil menu is what makes ACP clients render the dropdown.
func (b *acpBackend) NewSession(p acp.NewSessionParams) (acp.NewSessionResult, error) {
	anchor, err := acp.AnchorSession(p.Cwd, b.policy)
	if err != nil {
		return acp.NewSessionResult{}, err
	}
	id := anchor.Name
	if b.sess != nil {
		// One seek process = one live session, so a second session/new
		// re-anchors the existing one rather than minting another. Keep
		// the transcript's CWD honest when the anchor moved.
		b.sess.CWD = anchor.Abs
		if b.sess.ID != "" {
			id = b.sess.ID
		}
	}
	b.sessionID = id
	fmt.Fprintf(os.Stderr, "acp: session anchored cwd=%s project=%s id=%s\n", anchor.Abs, anchor.ProjectID, id)
	return acp.NewSessionResult{SessionID: id, ConfigOptions: b.menu}, nil
}

// LoadSession resumes a stored session (session/load): load, re-anchor,
// restore the lane and history, then replay the transcript as the
// session/update sequence the client would have seen live.
//
// The live session pointer is MUTATED IN PLACE (`*b.sess = *loaded`)
// rather than reassigned: main's closures — the subagent parent-sid, the
// x-opencode-session id, the checkpoint manager — all captured the
// original pointer, and reassigning would leave them describing the
// previous conversation while the agent runs the loaded one.
func (b *acpBackend) LoadSession(p acp.LoadSessionParams, sendU func(acp.SessionUpdate)) (acp.LoadSessionResult, error) {
	if b.store == nil || b.sess == nil {
		return acp.LoadSessionResult{}, errors.New("session/load: persistence is off (--no-save) — no transcript to load")
	}
	if p.SessionID == "" {
		return acp.LoadSessionResult{}, errors.New("session/load: empty sessionId")
	}
	loaded, err := b.store.Load(p.SessionID)
	if err != nil {
		// A missing transcript is the EXPECTED shape for conversations
		// started before durable ids existed: session/new used to hand
		// out the project directory name (so paseo holds ids like
		// "seek" that index no file), and the ACP lane did not persist
		// at all — there is nothing on disk to resume whatever the id
		// says. Surface that as guidance; the raw ENOENT reads like a
		// broken install.
		if errors.Is(err, os.ErrNotExist) {
			return acp.LoadSessionResult{}, fmt.Errorf(
				"session/load %s: no stored transcript. Sessions created before session/load shipped (ids were project names) were never persisted and cannot be resumed — start a new conversation; it will resume fine",
				p.SessionID)
		}
		return acp.LoadSessionResult{}, fmt.Errorf("session/load %s: %w", p.SessionID, err)
	}
	// Same repair -resume runs: a transcript cut mid-tool-call would
	// otherwise 400 the next request and lock the session out.
	if n := loaded.Repair(); n > 0 {
		fmt.Fprintf(os.Stderr, "acp: session %s: repaired %d trailing message(s) with orphan tool_calls\n", loaded.ID, n)
	}
	anchor, err := acp.AnchorSession(p.Cwd, b.policy)
	if err != nil {
		return acp.LoadSessionResult{}, err
	}
	// Restore the lane the session was last using. ResolveRoute infers
	// the provider from the model id (the registry is keyed by wire id);
	// a freeform id belonging to no lane keeps the current lane and just
	// re-points the model, which is what `-resume` of such a session does.
	if loaded.Model != "" && loaded.Model != b.ag.Model() {
		if route, rerr := subagent.ResolveRoute("", loaded.Model); rerr == nil {
			provider, dsClient, _, lerr := b.laneFor(route.Provider)
			if lerr != nil {
				return acp.LoadSessionResult{}, fmt.Errorf("session/load: restore lane %s: %w", route.Provider, lerr)
			}
			b.ag.SetLane(provider, dsClient, loaded.Model)
		} else {
			b.ag.SetModel(loaded.Model)
		}
		b.setMenuCurrent(loaded.Model)
	}

	// Swap the live session in place (see the doc comment) and reseed the
	// agent. Both must happen BEFORE the replay notifications go out: the
	// client renders the history and may prompt immediately after the
	// response resolves.
	*b.sess = *loaded
	b.sess.CWD = anchor.Abs
	originalID := loaded.ID
	b.sessionID = originalID
	b.ag.Reset(loaded.Messages)

	for _, u := range acpReplayUpdates(originalID, loaded.Messages, anchor.Abs) {
		sendU(u)
	}
	fmt.Fprintf(os.Stderr, "acp: session loaded id=%s messages=%d cwd=%s\n", originalID, len(loaded.Messages), anchor.Abs)
	return acp.LoadSessionResult{ConfigOptions: b.menu}, nil
}

// setMenuCurrent re-points the model dropdown at a model id after a
// session/load. Ids are curated-per-lane, so the first option whose model
// half matches wins; an unlisted model leaves the menu untouched (the
// dropdown then shows a lane the session isn't on — a lie the client can
// only avoid by re-reading session/load's configOptions, which is why
// they are returned).
func (b *acpBackend) setMenuCurrent(model string) {
	for _, opt := range b.menu {
		if opt.ID != "model" {
			continue
		}
		for _, v := range opt.Options {
			if _, m, ok := strings.Cut(v.Value, ":"); ok && m == model {
				b.menu[0].CurrentValue = v.Value
				return
			}
		}
	}
}

// SetConfigOption applies a model-dropdown pick. configId "model" is
// the only option seek serves; value ids are "<provider>:<model>"
// (acpModelMenu). The transport rejects the request while a prompt is
// in flight (internal/acp dispatch — see the happens-before note
// there), so the lane swap below races nothing.
func (b *acpBackend) SetConfigOption(p acp.SetConfigOptionParams) (acp.SetConfigOptionResult, error) {
	if b.sessionID == "" || p.SessionID != b.sessionID {
		return acp.SetConfigOptionResult{}, fmt.Errorf("unknown session %q", p.SessionID)
	}
	if p.ConfigID != "model" {
		return acp.SetConfigOptionResult{}, fmt.Errorf("unknown config id %q (only \"model\" is served)", p.ConfigID)
	}
	if len(b.menu) != 1 || b.menu[0].ID != "model" {
		return acp.SetConfigOptionResult{}, fmt.Errorf("model selection unavailable on this session (no model menu was served)")
	}
	// Only advertised values are accepted — a client echoing a stale or
	// hand-crafted id gets a loud error instead of a lane that dies at
	// the next API call.
	known := false
	for _, o := range b.menu[0].Options {
		if o.Value == p.Value {
			known = true
			break
		}
	}
	if !known {
		return acp.SetConfigOptionResult{}, fmt.Errorf("unknown model value %q", p.Value)
	}
	prov, model, ok := strings.Cut(p.Value, ":")
	if !ok {
		return acp.SetConfigOptionResult{}, fmt.Errorf("malformed model value %q (want \"provider:model\")", p.Value)
	}
	provider, dsClient, _, err := b.laneFor(prov)
	if err != nil {
		return acp.SetConfigOptionResult{}, fmt.Errorf("switch to %s: %w", prov, err)
	}
	b.ag.SetLane(provider, dsClient, model)
	b.menu[0].CurrentValue = p.Value
	fmt.Fprintf(os.Stderr, "acp: model switched provider=%s model=%s\n", prov, model)
	return acp.SetConfigOptionResult{ConfigOptions: b.menu}, nil
}

// acpLaneModels is the ACP model dropdown's curated table — the
// multi-provider analogue of the TUI /model picker's list (internal/tui
// commands.go knownModelsForProvider). Deliberately a separate table,
// same decoupling as the TUI's: dropdown rows and terminal picker rows
// serve different surfaces and evolve at their own pace. Keep the model
// IDs in lockstep with that list (and pricing's constants) when either
// changes — the dropdown must never offer an id the lane can't serve.
var acpLaneModels = []struct {
	provider string
	models   []struct{ id, label string }
}{
	{"deepseek", []struct{ id, label string }{
		{deepseek.ModelV41Flash, "deepseek-flash (V4.1-Flash)"},
	}},
	{"anthropic", []struct{ id, label string }{
		{"claude-sonnet-4-20250514", "claude-sonnet-4 (Anthropic)"},
		{"claude-3-5-sonnet-20241022", "claude-3.5-sonnet (Anthropic)"},
	}},
	{"openai", []struct{ id, label string }{
		{"gpt-4o", "gpt-4o (OpenAI)"},
		{"gpt-4o-mini", "gpt-4o-mini (OpenAI)"},
	}},
	{"gemini", []struct{ id, label string }{
		{"gemini-2.0-flash", "gemini-2.0-flash (Gemini)"},
		{"gemini-1.5-pro", "gemini-1.5-pro (Gemini)"},
	}},
	{"glm", []struct{ id, label string }{
		{pricing.ModelGLM53, "glm-5.3 (GLM)"},
	}},
	{"opencode", opencodeACPMenuModels(pricing.OpenCodeGoModels)},
}

// laneHasCredential reports whether a curated lane has anything to
// authenticate with: a static key for any lane, or — for opencode — a
// stored browser login. Same presence gate as buildProvider, so a lane
// never appears in the ACP dropdown and then fails on the first prompt.
func laneHasCredential(cfg config.Config, provider string) bool {
	if config.KeyFor(cfg, provider) != "" {
		return true
	}
	if provider == "opencode" {
		if cred, ok := opencodeauth.Load(); ok && cred.Valid() {
			return true
		}
	}
	return false
}

// opencodeACPMenuModels expands a Go-lane catalogue into dropdown rows.
// The ACP label keeps the pre-existing "<id> (OpenCode Go)" shape — the
// dropdown's job is picking an id, and the same ids appear on other
// lanes' labels the same way. Callers pass the live rows when the
// catalogue has refreshed (see acpModelMenu) or pricing.OpenCodeGoModels
// for the compiled baseline.
func opencodeACPMenuModels(rows []pricing.OpenCodeGoModel) []struct{ id, label string } {
	out := make([]struct{ id, label string }, 0, len(rows))
	for _, m := range rows {
		out = append(out, struct{ id, label string }{m.ID, m.ID + " (OpenCode Go)"})
	}
	return out
}

// acpModelMenu builds the session/new configOptions model select.
// Providers appear only when their key resolves (config.KeyFor — same
// presence gate as buildSubagentRoutes), value ids are
// "<provider>:<model>" so they're unique across providers and can be
// split back without a lookup table. currentValue is matched by the
// STARTUP model id alone — the curated ids are unique across lanes, so
// the provider name isn't needed (and buildProvider's auto-detect
// result isn't visible out here). A startup model outside the curated
// set (freeform --model / env pin) yields a nil menu: a select with a
// currentValue it doesn't list would lie to the dropdown, and the
// client-side fallback is simply no dropdown (pre-C behaviour).
//
// openCodeRows, when non-empty, replaces the compiled Go-lane rows —
// that is the refreshed catalogue (opencatalog), which can carry ids the
// binary has never heard of and can drop ones the vendor removed. A
// startup model that the refreshed list no longer offers therefore
// yields nil, same as any other unlisted model.
func acpModelMenu(currentModel string, openCodeRows []pricing.OpenCodeGoModel) []acp.ConfigOption {
	cfg, _ := config.Load()
	var opts []acp.ConfigSelectOption
	current := ""
	for _, lane := range acpLaneModels {
		if !laneHasCredential(cfg, lane.provider) {
			continue
		}
		models := lane.models
		if lane.provider == "opencode" && len(openCodeRows) > 0 {
			models = opencodeACPMenuModels(openCodeRows)
		}
		for _, m := range models {
			opts = append(opts, acp.ConfigSelectOption{
				Value: lane.provider + ":" + m.id,
				Name:  m.label,
			})
			if m.id == currentModel {
				current = lane.provider + ":" + m.id
			}
		}
	}
	if len(opts) == 0 || current == "" {
		return nil
	}
	return []acp.ConfigOption{{
		ID:           "model",
		Name:         "Model",
		Category:     "model",
		Type:         "select",
		CurrentValue: current,
		Options:      opts,
	}}
}

// buildACPPromptText assembles the prompt seek's agent actually sees from
// an ACP session/prompt: the text blocks, plus one in-band marker block
// per image content block — native attach on vision models, a
// switch-model note otherwise (feature-vision M-V.2, replacing the
// 柱 Q OCR path). Document references in the text then route through
// the file router (feature-deepseek-app-api F3) — copy-at-submit +
// eager upload, every failure in-band. Corrupt base64 / unhandled
// mimeType degrade silently (the block is skipped). The upload needs
// a context, so this is the one impure seam; it still touches no agent
// state and stays unit-testable.
func buildACPPromptText(ctx context.Context, model string, p acp.PromptParams, vroute visionRouter, froute fileroute.Router) (string, []deepseek.ImagePart, []deepseek.FilePart) {
	text := p.PromptText()
	var parts []deepseek.ImagePart
	for _, blk := range p.ImageBlocks() {
		data, err := base64.StdEncoding.DecodeString(blk.Data)
		if err != nil {
			continue
		}
		ext := imageExtForMime(blk.MimeType)
		if ext == "" {
			continue
		}
		block, part := vroute.routeBytes(model, "pasted-image"+ext, data, ext)
		if text != "" {
			text += "\n\n"
		}
		text += block
		if part.Asset != "" {
			parts = append(parts, part)
		}
	}
	text, files := froute.Route(ctx, model, text)
	return text, parts, files
}

// imageExtForMime maps an ACP image block's mimeType to a file extension
// the OCR engine recognizes; "" means "not an image type we handle" → the
// caller skips that block. M-P.5.
func imageExtForMime(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tiff"
	case "image/heic":
		return ".heic"
	default:
		return ""
	}
}

func (b *acpBackend) Prompt(ctx context.Context, p acp.PromptParams, sendU func(acp.SessionUpdate)) (acp.PromptResult, error) {
	text, images, files := buildACPPromptText(ctx, b.ag.Model(), p, b.route, b.froute)
	if text == "" {
		return acp.PromptResult{StopReason: "end_turn"}, nil
	}
	// cwd makes tool-call locations absolute (schema contract: "the
	// absolute file path"), resolved once per turn — the session cwd was
	// anchored at session/new (D-1) so this is the project root.
	cwd, _ := os.Getwd()
	for ev := range b.ag.PromptFiles(ctx, text, images, files) {
		if e, ok := ev.(agent.ErrorEvent); ok {
			b.saveTurn()
			return acp.PromptResult{}, e.Err
		}
		if u, ok := acpUpdate(p.SessionID, cwd, ev); ok {
			sendU(u)
		}
		// Persist per turn, exactly like runPrint: a client that kills
		// the process mid-conversation (paseo closes stdin on tab close)
		// must still find everything up to the last completed turn, and
		// session/load is only meaningful because this happens.
		// Per-turn, not per-event: the transcript is a whole-file
		// atomic rewrite, so saving on every delta would rewrite the
		// file hundreds of times a turn.
		if _, ok := ev.(agent.TurnEnd); ok {
			b.saveTurn()
		}
	}
	b.saveTurn()
	if ctx.Err() != nil {
		return acp.PromptResult{StopReason: "cancelled"}, nil
	}
	return acp.PromptResult{StopReason: "end_turn"}, nil
}

// saveTurn snapshots the live session to disk. Best-effort: a failed
// write is a warning, never a turn failure — the answer already went to
// the client, and losing a transcript must not lose the conversation.
// No-op under --no-save (either pointer is nil there).
func (b *acpBackend) saveTurn() {
	if b.sess == nil || b.store == nil {
		return
	}
	b.sess.Messages = b.ag.Messages()
	b.sess.Model = b.ag.Model()
	if err := b.store.Save(b.sess); err != nil {
		fmt.Fprintf(os.Stderr, "acp: warning: session save failed: %v\n", err)
	}
}

// acpUpdate maps one agent event to an ACP session/update payload. ok is
// false for events that don't surface to the client (turn bookkeeping);
// reasoning streams surface on the thought channel (agent_thought_chunk,
// opened by user decision 2026-11-19). Pure + table-testable
// (acp_test.go) so the mapping is verified without a live Zed client.
// cwd (the anchored session root) makes tool-call locations absolute
// per the schema contract.
//
// Tool calls ride the full protocol surface (2026-11-18, "放出去"):
// name + kind (icon/UI treatment), locations (client follow-along),
// rawInput/rawOutput (collapsible originals; the detail carrier — see
// acpToolContent for why text content blocks are never sent), and
// content as edit-only diff blocks. Wire shapes reverse-engineered
// from the @agentclientprotocol/sdk schema + paseo's own bundle.
func acpUpdate(sessionID, cwd string, ev agent.Event) (acp.SessionUpdate, bool) {
	switch e := ev.(type) {
	case agent.MessageDelta:
		if e.Reasoning {
			// The model's chain-of-thought rides the protocol's thought
			// channel (agent_thought_chunk), NOT as message content —
			// clients render it as a separate collapsible region. Surfaced
			// by user decision 2026-11-19 ("开放"); the prior posture
			// dropped it entirely. Trade-off accepted: CoT now reaches
			// client-side logs.
			return acp.SessionUpdate{SessionID: sessionID, Update: map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"content":       map[string]any{"type": "text", "text": e.Delta},
			}}, true
		}
		return acp.SessionUpdate{SessionID: sessionID, Update: map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": e.Delta},
		}}, true
	case agent.ToolDelta:
		// The think tool streams both its chain-of-thought (Reasoning)
		// and its answer text — both are thought-channel material: the
		// process is the point ("过程"), and the final answer already
		// lands in the tool call's rawOutput when it completes.
		return acp.SessionUpdate{SessionID: sessionID, Update: map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content":       map[string]any{"type": "text", "text": e.Delta},
		}}, true
	case agent.ToolExecStart:
		u := map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    e.CallID,
			"title":         acpToolTitle(e.Name, e.Args),
			"name":          e.Name,
			"kind":          acpToolKind(e.Name),
			"status":        "in_progress",
		}
		if locs := acpToolLocations(e.Args, cwd); locs != nil {
			u["locations"] = locs
		}
		if e.Args != "" {
			u["rawInput"] = acpRawValue(acpTruncate(e.Args, acpRawCap))
		}
		return acp.SessionUpdate{SessionID: sessionID, Update: u}, true
	case agent.ToolExecEnd:
		status := "completed"
		if e.Err != nil {
			status = "failed"
		}
		u := map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    e.CallID,
			"status":        status,
		}
		if content := acpToolContent(e.Name, e.Args, e.Result, e.Err); content != nil {
			u["content"] = content
		}
		out := e.Result
		if e.Err != nil {
			out = e.Err.Error()
		}
		if out != "" {
			u["rawOutput"] = acpRawValue(acpTruncate(out, acpRawCap))
		}
		return acp.SessionUpdate{SessionID: sessionID, Update: u}, true
	}
	return acp.SessionUpdate{}, false
}

// Byte cap for ACP tool-call raw payloads. Tool results can be a whole
// file; the wire copy exists for the client's collapsible card, not for
// re-transporting the file — the model still receives the full result
// through the normal tool-result message, so this cap never touches the
// prompt path (prefix-cache unfazed).
const acpRawCap = 16 << 10

// acpTruncate cuts s to at most max bytes on a rune boundary, appending
// an ellipsis marker when it cut anything. Rune-safe so the JSON wire
// copy never carries a broken UTF-8 sequence.
func acpTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…[truncated]"
}

// acpRawValue coerces a wire payload to the shape clients render best:
// paseo's own mock agent (the known-good pattern against its UI) sends
// rawInput/rawOutput as JSON OBJECTS, which its detail panel
// pretty-prints into the "Input"/"Output" sections. Anything that
// parses as JSON rides as the parsed value; plain text stays a string.
// Truncation happens BEFORE the call, so a cut JSON string that no
// longer parses simply degrades to the truncated-string form.
func acpRawValue(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}

// acpToolTitle builds the human title from the tool's args — the one
// line a client shows collapsed. A bare tool name ("read") says
// nothing; "read notes.txt" or "bash go test ./..." does. Extraction
// is best-effort per known arg names, degrading to the bare name.
func acpToolTitle(name, args string) string {
	var a map[string]any
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return name
	}
	arg := func(k string) string {
		s, _ := a[k].(string)
		return s
	}
	var what string
	switch name {
	case "read", "list_dir", "write", "edit", "fim_complete":
		what = arg("path")
	case "grep":
		if p := arg("pattern"); p != "" {
			what = p
		}
	case "bash":
		what = arg("command")
	case "git":
		if s := arg("subcommand"); s != "" {
			what = s
		}
	case "think":
		what = arg("task")
	case "webfetch":
		what = arg("url")
	case "monitor":
		what = arg("command")
	}
	if what == "" {
		return name
	}
	if len(what) > 80 {
		what = what[:80] + "…"
	}
	return name + " " + what
}

// acpToolKind maps seek tool names to ACP ToolKind values (clients pick
// icons / UI treatment by them). Default "other" covers memory_*,
// worktree, subagent and anything new — the map only needs the names a
// client would treat specially.
func acpToolKind(name string) string {
	switch name {
	case "read", "list_dir", "references":
		return "read"
	case "grep":
		return "search"
	case "edit", "write", "fim_complete":
		return "edit"
	case "bash", "git", "monitor":
		return "execute"
	case "think", "plan", "propose":
		return "think"
	case "webfetch":
		return "fetch"
	default:
		return "other"
	}
}

// acpToolLocations derives the follow-along location(s) from a tool's
// args: any tool carrying a "path" (read/edit/write/list_dir/grep/…)
// gets one entry, made absolute against cwd (schema: "the absolute
// file path"); read's "offset" (1-based line) rides as the line hint.
// Args without a path (bash, think, …) and unparseable args get nil —
// fail-soft, the location is UX sugar, never correctness.
func acpToolLocations(args, cwd string) []map[string]any {
	var a struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil || a.Path == "" {
		return nil
	}
	p := a.Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	loc := map[string]any{"path": p}
	if a.Offset > 0 {
		loc["line"] = a.Offset
	}
	return []map[string]any{loc}
}

// acpToolContent builds the tool_call_update content blocks — diff
// blocks for successful edits ONLY. Diff has no conflicting
// discriminator field, so it validates against every SDK generation;
// it is the one content variant that reaches clients.
//
// Text content blocks are NOT SENT AT ALL, and that is load-bearing
// (2026-11-19, user-reported stuck spinners): paseo's runtime zod is
// `content: z.array(zToolCallContent)` with NO skip-invalid wrapper —
// one item failing the (never-satisfiable) text union makes the ENTIRE
// tool_call_update fail validation, so status:"completed" and
// rawOutput never land and the call spins forever. A bad content item
// doesn't degrade to "no preview", it poisons the whole update. Result
// detail rides rawOutput (acpRawValue) for every tool; failures carry
// their error text there too. Do not reintroduce text blocks without a
// client that provably parses them.
func acpToolContent(name, args, result string, err error) []map[string]any {
	if name != "edit" || err != nil {
		return nil
	}
	var a struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if jerr := json.Unmarshal([]byte(args), &a); jerr != nil || a.Path == "" {
		return nil
	}
	return []map[string]any{{
		"type":    "diff",
		"path":    a.Path,
		"oldText": a.OldString, // empty old_string is invalid for edit anyway; block renders as new content
		"newText": a.NewString,
	}}
}

// runGoal drives the headless `/goal` loop (M-goal.3): a single agent works
// across turns toward a completion condition, judged each turn by a cheap
// DeepSeek call, until met / capped / stalled. Progress goes to stderr; the
// final report to stdout (cron/webhook-friendly).
func runGoal(ctx context.Context, ag *agent.Agent, client *deepseek.Client, model, condition string) error {
	if condition == "" {
		return fmt.Errorf("usage: seek goal run \"<condition>\"")
	}
	if client == nil {
		return fmt.Errorf("goal requires a DeepSeek provider — set an API key (seek setup)")
	}
	drv := goal.New(goal.NewAgentWorker(ag), goal.NewDeepSeekJudge(client, model), goal.Caps{Timeout: 30 * time.Minute})
	drv.OnTurn = func(tl goal.TurnLog) {
		mark := "…"
		if tl.Met {
			mark = "✓"
		}
		fmt.Fprintf(os.Stderr, "goal: turn %d (tools %d) %s %s\n", tl.Turn, tl.ToolCalls, mark, tl.Reason)
	}
	fmt.Fprintf(os.Stderr, "goal: working toward %q …\n", condition)
	rep, err := drv.Run(ctx, condition)
	if err != nil {
		return fmt.Errorf("goal: %w", err)
	}
	status := "stopped"
	event := "goal.stopped"
	if rep.Met {
		status, event = "met", "goal.completed"
	}
	fmt.Printf("goal %s (%s) — %d turn(s), %d tok\n", status, rep.Stop, rep.Turns, rep.Tokens)
	if rep.Reason != "" {
		fmt.Printf("  %s\n", rep.Reason)
	}
	// Best-effort mobile push (柱 M reuse) so an unattended cron goal reaches
	// you — wake up to "goal met", not a silent exit. nil dispatcher when no
	// push_webhooks configured, so this is a no-op for most users.
	if dispatch := routinescli.WebhookDispatcherFromConfig(); dispatch != nil {
		title := fmt.Sprintf("seek goal %s — %s", status, condition)
		body := fmt.Sprintf("%s (%s) after %d turn(s), %d tok\n%s", status, rep.Stop, rep.Turns, rep.Tokens, rep.Reason)
		dispatch(ctx, event, title, body)
	}
	return nil
}

// acpOptions bundles what runACP needs. A struct rather than another
// five-argument signature: session/load added the persistence pair, and
// every field here is "the thing main already owns, handed over once".
type acpOptions struct {
	ag     *agent.Agent
	vroute visionRouter
	froute fileroute.Router
	policy *permission.Policy
	menu   []acp.ConfigOption
	// sess/store back session/load + the durable sessionId. Both nil
	// under --no-save, which is what makes Initialize stop advertising
	// loadSession.
	sess  *session.Session
	store *session.Store
}

// runACP serves the Agent Client Protocol over stdio (v7 柱 P) so any ACP
// editor (Zed, …) can drive seek. stdout is the protocol channel — all
// logging goes to stderr. The shared permission policy the whole tool
// registry was built on is re-anchored by session/new to the client's
// cwd (D-1). A nil menu hides the client's model dropdown;
// session/set_config_option swaps the agent's lane between turns. When a
// session store is present the lane also serves session/load, resuming a
// past conversation by transcript id.
func runACP(ctx context.Context, o acpOptions) error {
	var r io.Reader = os.Stdin
	var w io.Writer = os.Stdout
	// Opt-in protocol trace for debugging the Zed (or any ACP client)
	// handshake: SEEK_ACP_LOG=/path records raw incoming ("<<") and
	// outgoing (">>") JSON-RPC. Off by default — no overhead/noise. The
	// real stdio stream is untouched; only a tee copy is tagged + logged.
	if lp := strings.TrimSpace(os.Getenv("SEEK_ACP_LOG")); lp != "" {
		if f, err := os.OpenFile(lp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			defer f.Close()
			wd, _ := os.Getwd()
			fmt.Fprintf(f, "\n=== seek acp start cwd=%s pid=%d ===\n", wd, os.Getpid())
			r = io.TeeReader(os.Stdin, &taggedWriter{w: f, tag: "<< "})
			w = io.MultiWriter(os.Stdout, &taggedWriter{w: f, tag: ">> "})
		}
	}
	return acp.NewServer(r, w, &acpBackend{
		ag:     o.ag,
		route:  o.vroute,
		froute: o.froute,
		policy: o.policy,
		menu:   o.menu,
		// Same construction path as the startup lane: buildProvider owns
		// key resolution and per-provider endpoints; "" baseURL keeps the
		// pinned defaults (a passthrough --base-url can't reach this
		// lane anyway — flags never parse past "acp").
		laneFor: func(provider string) (llm.Provider, *deepseek.Client, string, error) {
			p, c, _, modelDefault, err := buildProvider(provider, "", "")
			return p, c, modelDefault, err
		},
		sess:  o.sess,
		store: o.store,
	}).Serve(ctx)
}

// taggedWriter prefixes each write with tag, for the SEEK_ACP_LOG trace
// only. Best-effort: inner write errors are swallowed so tracing can never
// disturb the live protocol stream, and it always reports a full write so
// io.TeeReader / io.MultiWriter stay happy.
type taggedWriter struct {
	w   io.Writer
	tag string
}

func (t *taggedWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write([]byte(t.tag))
	_, _ = t.w.Write(p)
	if n := len(p); n == 0 || p[n-1] != '\n' {
		_, _ = t.w.Write([]byte("\n"))
	}
	return len(p), nil
}

func resolvePrompt(flagPrompt string) (string, error) {
	if flagPrompt != "" {
		return flagPrompt, nil
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Don't split a multi-byte character at the boundary.
	// The loop caps at 3 iterations (max continuation bytes in a 4-byte rune).
	b := []byte(s[:n])
	for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return string(b) + "…"
}

// detectGlamourStyle picks "dark" or "light" for the TUI's Markdown
// renderer. We do this BEFORE entering bubbletea's alt-screen so that
// lipgloss v2's OSC 11 background-colour query/response handshake
// completes synchronously while we still own stdin. If we let glamour
// do the equivalent under bubbletea, the terminal's response (e.g.
// "]11;rgb:fae0/fae0/fae0\[1;1R") leaks straight into the textarea as
// garbage text.
//
// stdin must be a real terminal for the query — lipgloss v2's Windows
// path opens CONIN$ when it isn't, switches the console to raw mode,
// and waits for an OSC reply that a non-interactive console never
// sends; its 2s CancelReader timeout does not interrupt ReadConsole on
// every console host, so the process hangs forever (go test under a
// piped-stdin shell died exactly here). The TUI needs a tty stdin
// anyway; without one, skip the query and assume dark — the same
// fallback HasDarkBackground itself uses on query failure.
//
// --theme overrides the detection. SEEK_STYLE=dark|light is a fallback
// when --theme=auto (the default).
func detectGlamourStyle(theme string) string {
	if theme == "dark" || theme == "light" {
		return theme
	}
	if v := os.Getenv("SEEK_STYLE"); v != "" {
		return v
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
		return "dark"
	}
	if lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		return "dark"
	}
	return "light"
}

// runUpgrade resolves the latest GitHub release, downloads the asset
// for this platform, verifies its sha256 against the release's
// checksums.txt, and atomically replaces this binary. dryRun stops
// after the checksum verification step.
func runUpgrade(force, dryRun bool) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	_, err := upgrade.Run(ctx, upgrade.Options{
		Owner:    "whyiyhw",
		Repo:     "seek",
		Current:  tui.VersionString(),
		AllowDev: force,
		DryRun:   dryRun,
		Stderr:   os.Stderr,
		Stdout:   os.Stdout,
	})
	// ErrAlreadyLatest is not a failure; the orchestrator already
	// printed a friendly note to stderr.
	if err == upgrade.ErrAlreadyLatest {
		return nil
	}
	return err
}

// runUpgradeCheck prints whether a newer release is available, without
// downloading or modifying anything. Exits 0 in both "up to date" and
// "newer available" cases; non-zero only on transport / parse errors.
func runUpgradeCheck() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	rel, err := upgrade.Check(ctx, upgrade.Options{
		Owner:   "whyiyhw",
		Repo:    "seek",
		Current: tui.VersionString(),
	})
	if err != nil {
		return err
	}
	if rel == nil {
		fmt.Printf("seek is up to date (%s)\n", tui.VersionString())
		return nil
	}
	fmt.Printf("seek update available: %s → %s\n", tui.VersionString(), rel.TagName)
	fmt.Println("Run `seek -upgrade` to install.")
	return nil
}

// buildProvider selects and constructs the LLM provider based on the
// --provider flag, env vars, and ~/.seek/config.json (in that order).
// Returns:
//
//	provider    llm.Provider — non-nil for second-tier providers
//	dsClient    *deepseek.Client — non-nil for DeepSeek (first-class path)
//	provLabel   string — human name for TUI banner ("" = DeepSeek, no banner)
//	modelDefault string — sensible default model for the chosen provider
//
// Auth resolution (per provider): the canonical env var beats the
// config-file entry — see config.KeyFor. That order means CI and
// short-lived `KEY=... seek` invocations always win over what got
// written to disk by a previous setup wizard.
func buildProvider(provFlag, baseURLFlag, provName string) (
	provider llm.Provider, dsClient *deepseek.Client, provLabel, modelDefault string, err error,
) {
	// Load config once; ignore parse errors here so a malformed file
	// degrades to "env-only" rather than blocking startup. (Save() is
	// the place that aggressively reports config issues.)
	cfg, _ := config.Load()

	// Determine effective provider name. Order:
	//   1. --provider flag (explicit user intent)
	//   2. DeepSeek if its key is anywhere (env or config)
	//   3. Second-tier whose key is set, if no DeepSeek key
	//   4. cfg.DefaultProvider (the setup wizard writes this)
	//   5. "deepseek" as final fallback (errors out later if no key)
	if provFlag == "" {
		deepseekHas := config.KeyFor(cfg, "deepseek") != ""
		switch {
		case deepseekHas:
			provFlag = "deepseek"
		case config.KeyFor(cfg, "anthropic") != "":
			provFlag = "anthropic"
		case config.KeyFor(cfg, "openai") != "":
			provFlag = "openai"
		case config.KeyFor(cfg, "gemini") != "":
			provFlag = "gemini"
		case config.KeyFor(cfg, "glm") != "":
			provFlag = "glm"
		case cfg.DefaultProvider != "":
			provFlag = cfg.DefaultProvider
		default:
			provFlag = "deepseek"
		}
	}

	switch provFlag {
	case "deepseek":
		apiKey := config.KeyFor(cfg, "deepseek")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no DeepSeek API key — set DEEPSEEK_API_KEY or run seek once to use the setup wizard")
		}
		return nil, deepseek.New(deepseek.WithAPIKey(apiKey)), "", deepseek.ModelV41Flash, nil

	case "anthropic":
		apiKey := config.KeyFor(cfg, "anthropic")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no Anthropic API key — set ANTHROPIC_API_KEY or save one via the setup wizard")
		}
		return anthropicprov.New(apiKey), nil, "Anthropic", "claude-sonnet-4-20250514", nil

	case "openai":
		apiKey := config.KeyFor(cfg, "openai")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no OpenAI API key — set OPENAI_API_KEY or save one via the setup wizard")
		}
		return openaiprov.New(apiKey), nil, "OpenAI", "gpt-4o", nil

	case "gemini":
		apiKey := config.KeyFor(cfg, "gemini")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no Gemini API key — set GEMINI_API_KEY or save one via the setup wizard")
		}
		return geminiprov.New(apiKey), nil, "Gemini", "gemini-2.0-flash", nil

	case "glm":
		// Zhipu GLM lane (open.bigmodel.cn). Both endpoints speak OpenAI
		// Chat Completions — including streaming tool_calls deltas — so
		// the lane rides the compatible adapter with a pinned default
		// base URL. Endpoint precedence: providers.glm.base_url /
		// GLM_BASE_URL > --base-url > glmBaseURL. The override matters
		// because Zhipu bills a Coding Plan subscription ONLY on
		// /api/coding/paas/v4: a coding-plan key pointed at the default
		// /api/paas/v4 path charges the recharge balance (or fails with
		// error 1113 「余额不足或无可用资源包」). Expected degradation vs
		// DeepSeek: no FIM, no cache metadata, reasoning_content deltas
		// are silently skipped by the openai client's wire types.
		apiKey := config.KeyFor(cfg, "glm")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no GLM API key — set GLM_API_KEY or add providers.glm.api_key to ~/.seek/config.json")
		}
		return newGLMProvider(cfg, apiKey, baseURLFlag), nil, "GLM", "glm-5.3", nil

	case "opencode":
		// OpenCode Go subscription lane (opencode.ai/inference/go):
		// a flat $10/month plan over an open-weight + frontier model
		// pool, billed per request window (5h/week/month) — never per
		// token. OpenAI Chat-Completions wire, so it rides the
		// compatible adapter like GLM. The Go endpoint's client
		// contract: a self-identifying User-Agent plus a stable
		// x-opencode-session header per conversation — both injected by
		// the openai client from ChatRequest.SessionID; omitting the
		// session header is a hard 400 MissingSessionID (verified
		// 2026-11-18), and a generic UA is the documented per-token
		// degradation path. Default model deepseek-v4.1-flash was
		// confirmed in GET /v1/models (public): recommended_api
		// chat-completions, tools + reasoning (effort low/high/max),
		// 1M context / 384K output. The menu offers the whole
		// Chat-Completions slice of the Go pool (pricing.OpenCodeGoModels
		// — 20 ids, of which 4 are the time-limited zero-price rows:
		// the docs define "free" as an opencode-go model with no input
		// price, same gateway and key, just unmetered). --base-url
		// overrides the endpoint.
		//
		// Credential precedence: an explicit API key (env or config)
		// always wins over the stored browser login — an operator who
		// pinned a key must not be silently switched onto OAuth by a
		// login performed earlier. With neither, the error names both
		// routes instead of just the env var.
		if apiKey := config.KeyFor(cfg, "opencode"); apiKey != "" {
			return newOpenCodeProvider(cfg, apiKey, baseURLFlag), nil, "OpenCode", "deepseek-v4.1-flash", nil
		}
		provider, err := newOpenCodeOAuthProvider(cfg, baseURLFlag)
		if err != nil {
			return nil, nil, "", "", fmt.Errorf("no OpenCode credentials — run `seek auth opencode` (browser login) or set OPENCODE_API_KEY: %w", err)
		}
		return provider, nil, "OpenCode", "deepseek-v4.1-flash", nil

	case "compatible":
		// Compatible endpoints don't have a canonical env var, so we
		// accept OPENAI_API_KEY or DEEPSEEK_API_KEY (common shapes for
		// vLLM/Ollama deployments) before checking config under the
		// provider's display name.
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("DEEPSEEK_API_KEY")
		}
		if apiKey == "" {
			apiKey = config.KeyFor(cfg, provName)
		}
		if baseURLFlag == "" {
			return nil, nil, "", "", fmt.Errorf("--base-url is required for --provider=compatible")
		}
		return compatible.New(apiKey, baseURLFlag, provName), nil, provName, "", nil

	default:
		return nil, nil, "", "", fmt.Errorf("unknown --provider %q; valid: deepseek | anthropic | openai | gemini | glm | opencode | compatible", provFlag)
	}
}

// glmBaseURL is the default endpoint for --provider=glm: Zhipu's
// pay-as-you-go OpenAI-compatible Chat Completions path. It is NOT the
// Coding Plan endpoint — a subscription is billed only on
// glmCodingPlanBaseURL; a coding-plan key sent to paas/v4 charges the
// recharge balance, or fails with error 1113 when that balance is empty.
// The default stays pay-as-you-go (the majority of keys are pay-as-you-
// go; flipping it would break them in the opposite direction) and
// Coding Plan users override per lane via providers.glm.base_url /
// GLM_BASE_URL / --base-url. Full chat-completions path, matching the
// openai client's expectation that baseURL is the POST target.
const glmBaseURL = "https://open.bigmodel.cn/api/paas/v4/chat/completions"

// glmCodingPlanBaseURL is Zhipu's GLM Coding Plan subscription endpoint
// (one of the three OpenAI-compatible Base URLs Zhipu documents:
// /api/anthropic, /api/coding/paas/v4, /api/v1). Not wired as a default —
// named here so docs, the setup wizard tip, and future 1113 diagnostics
// all point at one string. Value for providers.glm.base_url / GLM_BASE_URL.
const glmCodingPlanBaseURL = "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"

// glmEndpoint resolves the effective GLM POST target. Precedence is
// per-provider override > --base-url flag > compiled default:
//
//	providers.glm.base_url / GLM_BASE_URL  ← the Coding Plan preset
//	--base-url                             ← one-off / proxy
//	glmBaseURL (pay-as-you-go)             ← the majority
//
// The config-file/env layer deliberately beats the flag: it is the
// per-provider statement of intent written once, while --base-url is a
// generic override shared by every lane. Pure function so the precedence
// is unit-testable without a live endpoint.
func glmEndpoint(cfg config.Config, baseURLFlag string) string {
	base := glmBaseURL
	if baseURLFlag != "" {
		base = baseURLFlag
	}
	if override := config.BaseURLFor(cfg, "glm"); override != "" {
		base = override
	}
	return base
}

// newGLMProvider is the single construction point for the GLM lane:
// startup (buildProvider), ACP laneFor, and subagent routes all reach
// glmEndpoint through here, so no path can disagree about the endpoint
// (a divergent child lane would silently bill the recharge balance).
func newGLMProvider(cfg config.Config, apiKey, baseURLFlag string) llm.Provider {
	return compatible.New(apiKey, glmEndpoint(cfg, baseURLFlag), "GLM")
}

// opencodeBaseURL is the default endpoint for --provider=opencode: the
// OpenCode Go plan's inference gateway, OpenAI-compatible. Same
// full-POST-target shape as glmBaseURL. Verified 2026-11-18 against
// GET https://opencode.ai/inference/go/v1/models (public, no key): each
// chat model carries api.chat-completions.base_url =
// "https://opencode.ai/inference/go/openai/v1", so the POST target is
// that base plus /chat/completions. Two aliases also route (docs table
// lists /inference/openai/v1/chat/completions, and the older
// /inference/go/v1/chat/completions) — keep the /go/openai one so the
// request is scoped to the Go plan, which is what the lane bills
// against. --base-url overrides.
const opencodeBaseURL = "https://opencode.ai/inference/go/openai/v1/chat/completions"

// openCatalog is the OpenCode lane's live model catalogue: the compiled
// baseline (pricing.OpenCodeGoModels) unless a cache or a refresh says
// otherwise. nil for every other lane — readers must treat nil as "use
// the compiled list". Populated once by initOpenCatalog; the background
// refresh installs new snapshots under the catalogue's own lock.
var openCatalog *opencatalog.Catalogue

// initOpenCatalog seeds the process-wide catalogue at startup: a usable
// disk snapshot first (so a warm start is offline-complete), then one
// keyed refresh in the background. Every failure is silent by design —
// the compiled baseline is already installed, and a menu that lags the
// vendor by a release is better than a startup that waits on HTTP.
func initOpenCatalog() {
	openCatalog = opencatalog.New()
	if !opencatalog.Enabled() {
		return
	}
	if snap, ok := opencatalog.LoadCached(); ok && snap.Usable(time.Now()) {
		openCatalog.Install(snap)
	}
	cfg, err := config.Load()
	if err != nil {
		return
	}
	// Same credential precedence as buildProvider: an explicit key wins
	// over the stored browser login.
	if key := config.KeyFor(cfg, "opencode"); key != "" {
		openCatalog.APIKey = key
	} else if cred, ok := opencodeauth.Load(); ok && cred.Valid() {
		openCatalog.Bearer = opencodeCatalogBearer
	} else {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = openCatalog.Refresh(ctx, opencatalog.DefaultClient())
	}()
}

// openCatalogRows is the single reader the ACP dropdown and the TUI
// picker share. nil (lane inactive) means "fall back to the compiled
// list", which both surfaces already do.
func openCatalogRows() []pricing.OpenCodeGoModel {
	if openCatalog == nil {
		return nil
	}
	return openCatalog.Rows()
}

// refreshOpenCatalog backs the TUI's `/model refresh`: a synchronous
// fetch (the caller runs it inside a tea.Cmd) that installs and caches
// the new list. Errors are returned verbatim — that surface has room to
// show them.
func refreshOpenCatalog(ctx context.Context) (int, error) {
	switch {
	case openCatalog == nil:
		return 0, fmt.Errorf("opencode catalogue: lane not active")
	case !opencatalog.Enabled():
		return 0, fmt.Errorf("opencode catalogue: disabled by SEEK_OPENCODE_CATALOG")
	case openCatalog.APIKey == "" && openCatalog.Bearer == nil:
		return 0, fmt.Errorf("opencode catalogue: not logged in — run `seek auth opencode`")
	}
	return openCatalog.Refresh(ctx, opencatalog.DefaultClient())
}

// opencodeCatalogBearer adapts the browser login to the catalogue's
// credential hook: both fetch a rotating bearer and the workspace header.
func opencodeCatalogBearer(ctx context.Context) (string, string, error) {
	return opencodeauth.Token(ctx, opencodeauth.New())
}

// newOpenCodeOAuthProvider builds the lane's client from the stored
// browser login (`seek auth opencode`): a self-refreshing bearer plus the
// x-org-id header. The header is static per login — the workspace id does
// not rotate with token refreshes — and omitting it is the difference
// between 200 and 403 "Workspace selection is required".
func newOpenCodeOAuthProvider(cfg config.Config, baseURLFlag string) (llm.Provider, error) {
	cred, ok := opencodeauth.Load()
	if !ok || !cred.Valid() {
		return nil, opencodeauth.ErrNoCredentials
	}
	if cred.OrgID == "" {
		return nil, fmt.Errorf("stored login has no workspace — re-run `seek auth opencode`")
	}
	c := compatible.New("", opencodeEndpoint(cfg, baseURLFlag), "OpenCode")
	c.SetHeader("x-org-id", cred.OrgID)
	c.SetTokenSource(func(ctx context.Context) (string, error) {
		token, _, err := opencodeauth.Token(ctx, opencodeauth.New())
		return token, err
	})
	return c, nil
}

// opencodeEndpoint resolves the lane's POST target with the same
// precedence as glmEndpoint: env (OPENCODE_BASE_URL) / providers.opencode
// .base_url > --base-url > the compiled default. baseURLFlag alone used
// to be honoured here while config-file overrides were silently dropped —
// the GLM lane already grew this exit after its own children diverged.
func opencodeEndpoint(cfg config.Config, baseURLFlag string) string {
	base := opencodeBaseURL
	if baseURLFlag != "" {
		base = baseURLFlag
	}
	if override := config.BaseURLFor(cfg, "opencode"); override != "" {
		base = override
	}
	return base
}

// newOpenCodeProvider is the single construction point for the OpenCode
// lane: startup (buildProvider), ACP laneFor and subagent routes all
// reach opencodeEndpoint through here, so a proxy / mirror override
// can't apply to a parent and miss its children.
func newOpenCodeProvider(cfg config.Config, apiKey, baseURLFlag string) llm.Provider {
	return compatible.New(apiKey, opencodeEndpoint(cfg, baseURLFlag), "OpenCode")
}

// mcpSectionFromBridges assembles the sysprompt MCPSection content (the
// fifth prompt segment) from the bridges LoadServers produced. It is
// the single assembly path (MCP-D3): the copy itself lives in
// mcptool.PromptSection — tier-1 mcp.json "hint" verbatim, tier-2
// built-in curated copy, tier-3 bare listing — so this layer only maps
// bridges onto mcptool.ServerInfo. Tools keep bridge registration order
// (the same order the model sees in the tools array); PromptSection
// sorts servers by name, so mcp.json map iteration order never reaches
// the prompt bytes.
//
// Empty string when no bridges loaded, or when mcp.json sets an
// explicit `"mcp_prompt": false`: tools stay registered (the A2
// "tool exists, no guidance" eval arm), only the prompt guidance is
// suppressed.
func mcpSectionFromBridges(bridges []*mcptool.Bridge, cfg mcpconfig.Config) string {
	if len(bridges) == 0 || !cfg.PromptEnabled() {
		return ""
	}
	toolsByServer := make(map[string][]mcptool.ToolInfo, len(bridges))
	names := make([]string, 0, len(bridges))
	for _, b := range bridges {
		name := b.Server()
		if _, seen := toolsByServer[name]; !seen {
			names = append(names, name)
		}
		toolsByServer[name] = append(toolsByServer[name], mcptool.ToolInfo{
			Name:        b.Name(),
			Description: b.Description(),
			Schema:      b.Schema(),
		})
	}
	servers := make([]mcptool.ServerInfo, 0, len(names))
	for _, name := range names {
		servers = append(servers, mcptool.ServerInfo{
			Name:  name,
			Hint:  cfg.MCPServers[name].Hint,
			Tools: toolsByServer[name],
		})
	}
	return mcptool.PromptSection(servers)
}

// cronAutoTickEnabled is the single gate for the startup cron auto-tick
// goroutine (v5 柱 H). False when --no-cron-tick opts out, or when the
// ACP headless lane runs: those processes are short-lived spawns (paseo
// probes / Zed sessions) that kill the detached tick goroutine mid-run —
// half-fired cron job risk, zero benefit (docs/prd/archive/feature-paseo-provider.md
// §2.3). The two axes are independent because --no-cron-tick cannot
// substitute for acpLane: flag.Parse stops at the "acp" positional, so
// the flag never reaches that lane.
func cronAutoTickEnabled(noCronTickFlag, acpLane bool) bool {
	return !noCronTickFlag && !acpLane
}

// errToolsUsage is the sentinel behind `-tools` exit code 2: the
// human-readable complaint (unknown names + available list) is printed
// to stderr by applyToolsWhitelist BEFORE returning this, so main()
// routes it straight to os.Exit(2) without the "seek:" prefix — same
// shape as keyscli.ErrUsage.
var errToolsUsage = errors.New("tools usage")

// parseToolsFlag splits the comma-separated -tools value into clean
// tool names: surrounding whitespace trimmed, empty segments dropped,
// duplicates collapsed (first occurrence keeps order). Empty input
// (absent flag, "", or "," ) yields nil — the "no whitelist, full
// default registry" contract.
func parseToolsFlag(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(v, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// applyToolsWhitelist implements the v8·T1 `-tools` flag against the
// FINAL registry composition (call it after the last reg.Add):
//
//   - non-empty value: every listed name must be registered (MCP tools
//     validate by their registered name); unknown names print the
//     offender list + available list to stderr and return errToolsUsage
//     → exit 2. Survivors narrow the registry in place.
//
//   - resume: if the loaded session recorded a tool set (session.Tools)
//     that differs from what this run will use, warn once on stderr —
//     the tools array is part of the request prefix, so this is exactly
//     one accepted cache invalidation, mirroring the -resume-new-day
//     posture. Sessions saved before the field existed have no record;
//     we skip the warning rather than guess.
//
//   - bookkeeping: the live session's Tools field is (re)written so
//     saves carry the current set and the NEXT resume compares against
//     it. Under --no-save there is no session to record — nothing to do.
func applyToolsWhitelist(flagVal string, reg *tools.Registry, resumed, active *session.Session) error {
	names := parseToolsFlag(flagVal)
	if len(names) > 0 {
		avail := reg.Names()
		known := make(map[string]bool, len(avail))
		for _, n := range avail {
			known[n] = true
		}
		var unknown []string
		for _, n := range names {
			if !known[n] {
				unknown = append(unknown, n)
			}
		}
		if len(unknown) > 0 {
			fmt.Fprintf(os.Stderr, "-tools: unknown tool(s): %s\n", strings.Join(unknown, ", "))
			fmt.Fprintf(os.Stderr, "available tools: %s\n", strings.Join(avail, ", "))
			return errToolsUsage
		}
		reg.Whitelist(names)
	}
	if active == nil {
		return nil
	}
	final := reg.Names()
	if resumed != nil && len(resumed.Tools) > 0 && !equalToolSets(resumed.Tools, final) {
		fmt.Fprintln(os.Stderr, "tools: 工具集变了，缓存会失效一次")
	}
	active.Tools = final
	return nil
}

// equalToolSets compares two tool-name collections as sets. Both sides
// come from reg.Names() in practice (sorted), but the session header
// round-trips through JSON — compare by membership so an order change
// can never fabricate a "tool set changed" warning.
func equalToolSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, n := range a {
		set[n] = true
	}
	for _, n := range b {
		if !set[n] {
			return false
		}
	}
	return true
}
