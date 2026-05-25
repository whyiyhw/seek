// Command seek is the DeepSeek-first coding agent CLI.
//
// M3 wires in cache.Tracker (session-level prefix-cache stats), pricing
// (off-peak tier awareness + per-call cost), and the Think tool that
// bridges the chat loop into V4-Flash thinking mode. Interactive TUI lands
// in M4; full think-then-chat skill arrives with skill loading in M5.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/mcpconfig"
	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/memorycli"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/projectmd"
	seekrpc "github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillcli"
	"github.com/whyiyhw/seek/internal/skillstats"
	"github.com/whyiyhw/seek/internal/tools"
	askusertool "github.com/whyiyhw/seek/internal/tools/askuser"
	"github.com/whyiyhw/seek/internal/tools/bash"
	"github.com/whyiyhw/seek/internal/tools/edit"
	"github.com/whyiyhw/seek/internal/tools/fimcomplete"
	gittool "github.com/whyiyhw/seek/internal/tools/git"
	"github.com/whyiyhw/seek/internal/tools/grep"
	"github.com/whyiyhw/seek/internal/tools/listdir"
	"github.com/whyiyhw/seek/internal/tools/mcptool"
	"github.com/whyiyhw/seek/internal/tools/memorytool"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/internal/tools/skillinstall"
	"github.com/whyiyhw/seek/internal/tools/skilltool"
	"github.com/whyiyhw/seek/internal/tools/think"
	"github.com/whyiyhw/seek/internal/tools/write"
	"github.com/whyiyhw/seek/internal/tui"
	"github.com/whyiyhw/seek/internal/upgrade"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
	"github.com/whyiyhw/seek/pkg/llm/compatible"
	anthropicprov "github.com/whyiyhw/seek/pkg/llm/provider/anthropic"
	geminiprov "github.com/whyiyhw/seek/pkg/llm/provider/gemini"
	openaiprov "github.com/whyiyhw/seek/pkg/llm/provider/openai"

	"github.com/muesli/termenv"
)

const systemPromptTpl = `%s

You are seek, an open-source terminal coding agent.

About yourself (use these facts when the user asks who you are, who built you, what license, etc. — do NOT speculate beyond them):
- Project: seek — https://github.com/whyiyhw/seek
- Author / maintainer: whyiyhw (independent open-source developer)
- License: MIT
- Implementation: Go (single binary, ~5 MB, no runtime deps)
- Default LLM provider: DeepSeek (V4-Flash / V4-Pro via Thinking.Type=enabled); also supports Anthropic, OpenAI, Gemini, and OpenAI-compatible endpoints
- You are NOT made by DeepSeek the company. seek is an independent project that USES DeepSeek as one of several LLM providers. Do not claim affiliation with DeepSeek, Anthropic, OpenAI, or Google.
- The model generating your responses right now is whatever provider was selected at startup — check the status bar or ask the user to run /model. Don't guess.

Available tools:
- read(path, offset?, limit?): read a file with line numbers (default 50, max 50 — values above 50 error). Always use grep first to find the relevant line range.
- grep(pattern, path, context_lines?): search files by regex or literal string; returns matching lines with line numbers and surrounding context. Use this to locate a symbol or section, then follow up with read(offset=N) for the precise range — avoids reading entire files into context.
- list_dir(path, depth?, show_hidden?): list directory entries with type and size. Default depth=1, hidden files excluded. Use this instead of 'bash ls' when you need depth or dotfiles.
- write(path, content): create or overwrite a file. Refused outside the working directory unless seek was started with --yolo.
- edit(path, old_string, new_string, expected_replacements?): exact substring replacement. old_string must be unique unless expected_replacements is set. new_string="" deletes.
- bash(command, timeout_ms?): run a shell command. Refused unless seek was started with --yolo — in that case ask the user to re-run with --yolo (do not retry blindly). DO NOT use for git read operations (log/diff/status/blame/show) — the git tool below handles those without an approval prompt and works in plan mode.
- git(subcommand, args?, max_lines?): read-only git wrapper. Allowed subcommands: log, diff, show, status, blame, branch, tag, rev-parse, ls-files, ls-tree, cat-file, shortlog, describe, reflog. Output capped at 500 lines hard. Works in plan mode (bash does not). Use this instead of bash whenever you need to inspect git state. Mutating ops (commit/push/reset/checkout/rebase/merge/clean/fetch/pull/clone) MUST go through bash and accept the user prompt.
- fim_complete(path, before_marker, after_marker?, max_tokens?): DeepSeek's fill-in-the-middle endpoint. Cheaper than chat for small gap-fills. Returns text WITHOUT applying — call edit afterwards to apply.
- think(task, reflect?, context?): call deepseek-v4-flash in thinking mode for hard multi-step planning or self-review. Use sparingly — each call is several thousand tokens. Pattern: think→execute→think(reflect=true) for non-trivial changes.
- Skill(name): fetch the instructions for a named skill listed under "Available skills" below. The tool returns the skill body; follow its steps. Use this whenever a user request matches a skill's description.
- ask_user(question, options, multi_select?): show the user an inline TUI picker (↑/↓ + Enter, or Space-toggle + Enter for multi-select). Returns {chosen_ids, free_text, cancelled}. seek auto-appends an "Other — type your own answer" row so the user always has a free-text escape hatch.
  USE ONLY when ALL THREE hold:
    (1) The choice is among 2-4 discrete, mutually-distinct options.
    (2) Picking wrong costs real work to undo (deleted code, run commands, files written) — NOT just "one more conversation turn".
    (3) You genuinely cannot decide from context (the codebase, git log, CLAUDE.md, prior conversation).
  Strong fits: scope choices (user vs project install), conflict resolution (overwrite/skip/rename), interchangeable approaches with no objective right answer (sync vs async, extract function vs inline, new file vs append), picking from a fixed enum (severity level, target env).
  Do NOT use for: naming, prose, descriptions, style — those are conversation, not picker; questions with 5+ options (merge categories first); anything you can answer by reading a file or checking git; obvious yes/no where the user's likely answer is clear (don't pester); "may I do X?" type permission questions — those are handled automatically by the permission system, you don't request them.
  Difference from permission prompts: permission = "may I do this dangerous thing?" (seek asks for you, automatically). ask_user = "which of these paths should I take?" (you ask, when you genuinely can't decide). They are not interchangeable.
  When cancelled=true in the response, proceed with your best judgment and STATE YOUR ASSUMPTION inline ("I'll use X — say so if you want Y"). Do NOT immediately re-ask; the user said no for a reason.
- skill_fetch(source, name?, subpath?, sha256?): when the user asks to INSTALL a skill, fetch + validate it into a /tmp staging dir. Returns name, description, files, body preview, and a staging_path. Inspect the staged files (read SKILL.md, scripts/ if any) BEFORE committing — judge whether the source matches user intent.
- skill_commit(staging_path, name, source, scope, force?): finalise the install. BEFORE calling this, you MUST: (1) ask the user whether to install at "user" scope (~/.seek/skills/, available in every seek session on this machine, private) or "project" scope (<cwd>/.seek/skills/, shared via git with anyone who clones the repo) — DO NOT guess or default. (2) Wait for the user's answer. The user then sees a y/N approval prompt for the actual filesystem move. On success the new skill is on disk but NOT loaded into this session — tell the user to run /new (TUI) or restart so the manifest picks it up.

Workflow:
1. Explore before reading: use grep to locate relevant symbols or sections, then read(offset=N) for the specific range. Never read an entire file — it wastes tokens and breaks prefix cache.
2. Inspect the workspace with read before changing anything.
3. For multi-step or risky tasks, call think first to plan; for non-trivial changes, call think(reflect=true) after to self-review.
4. Keep edits minimal and explicit (Claude Code style: tight old_string / new_string).
5. For permission denials, surface the message to the user and stop — do not loop.
6. Never run git commit without explicit user confirmation. The workflow is: modify → review → user commits.

Working directory: %s. Mode: %s.
`

// buildLangDirective returns the language directive text to inject at
// the top of the system prompt. lang is "en", "zh", or "" (auto).
func buildLangDirective(lang string) string {
	switch lang {
	case "en":
		return "Language: English. Always respond in English."
	case "zh":
		return "Language: 中文。请始终用中文回复。"
	default:
		// Auto-detect from OS locale.
		if detected := detectLangFromEnv(); detected == "zh" {
			return "Language: 中文。请始终用中文回复。"
		}
		return "Language: English. Always respond in English."
	}
}

// detectLangFromEnv checks locale environment variables for a zh prefix.
// Order: LC_ALL > LC_MESSAGES > LANG. Returns "en" if no match.
func detectLangFromEnv() string {
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(env); strings.HasPrefix(strings.ToLower(v), "zh") {
			return "zh"
		}
	}
	return "en"
}

// resolveLang converts a --lang flag value ("en", "zh", "auto")
// to the resolved language ("en" or "zh"). "auto" detects from env.
func resolveLang(raw string) string {
	switch strings.ToLower(raw) {
	case "en":
		return "en"
	case "zh":
		return "zh"
	default: // "auto" or anything else
		return detectLangFromEnv()
	}
}

// modeLabel returns the human-readable label for a permission mode,
// used in the system prompt status line.
func modeLabel(m permission.Mode) string {
	switch m {
	case permission.ModeYolo:
		return "yolo"
	case permission.ModePlan:
		return "plan"
	case permission.ModeAsk:
		return "ask"
	default:
		return "deny"
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
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

	var (
		prompt        = flag.String("p", "", "prompt text; if non-empty (or stdin is piped) seek runs in print mode and exits")
		model         = flag.String("model", "", "model id; default depends on provider (deepseek-v4-flash for DeepSeek, etc.)")
		maxTurns      = flag.Int("max-turns", 200, "safety bound on agent loop iterations")
		maxTokens     = flag.Int("max-tokens", 0, "completion token cap per call; 0 → default (16384)")
		autoContinue  = flag.Bool("auto-continue", false, "inject 'continue' on text-only turns so the model resumes mid-task without user input")
		yolo          = flag.Bool("yolo", false, "allow bash + writes outside CWD without prompting")
		plan          = flag.Bool("plan", false, "read-only exploration: no bash/writes/edits; produce a plan to review before executing")
		jsonOut       = flag.Bool("json", false, "emit agent events as JSONL on stdout (implies print mode)")
		resume        = flag.String("resume", "", "load a saved session by ID (see seek -list)")
		cont          = flag.Bool("continue", false, "load the most-recently-updated session")
		noSave        = flag.Bool("no-save", false, "do not persist this session to disk")
		list          = flag.Bool("list", false, "list saved sessions and exit")
		noProj        = flag.Bool("no-project-md", false, "do not auto-load AGENTS.md from the project tree")
		providerFlag  = flag.String("provider", "", "LLM provider: deepseek (default) | anthropic | openai | gemini | compatible")
		baseURL       = flag.String("base-url", "", "base URL for --provider=compatible (OpenAI-compatible endpoint)")
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
		dreamFlag     = flag.Bool("dream", false, "M→L distillation: scan project memory, print L-pending candidates without writing")
		dreamWrite    = flag.Bool("dream-write", false, "with -dream: actually append the candidates to ~/.seek/soul.md's Pending section")
		langFlag      = flag.String("lang", "auto", "response language: en|zh|auto (auto = detect from system locale)")
	)
	flag.Parse()

	// --yolo and --plan are mutually exclusive.
	if *yolo && *plan {
		return fmt.Errorf("--yolo and --plan are mutually exclusive")
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

	// Provider detection. --provider overrides auto-detect; otherwise we
	// check env vars in priority order: DeepSeek first, then second-tier.
	provider, dsClient, provLabel, modelDefault, err := buildProvider(
		*providerFlag, *baseURL, *providerName,
	)
	if err != nil {
		return err
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
		if *model == modelDefault {
			// User didn't override the model flag; honour the saved one.
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
	// Print mode (-p / piped stdin) can't realistically interrupt to
	// ask, so it stays in deny mode unless --yolo is explicit. The TUI
	// path overrides to Ask further down so per-call approval kicks
	// in. --yolo and --plan always win.
	initialMode := permission.ModeDeny
	if *yolo {
		initialMode = permission.ModeYolo
	} else if *plan {
		initialMode = permission.ModePlan
	}
	policy, err := permission.New(cwd, initialMode)
	if err != nil {
		return err
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
	// sessionLang mirrors session.Lang: "" (auto-detect) | "en" | "zh".
	// The /lang TUI command updates it through SetLang below; the system
	// prompt builder reads it to inject the language directive.
	sessionLang := resolveLang(*langFlag)
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
		if projMD.Path != "" {
			fmt.Fprintf(os.Stderr, "Loaded project instructions from %s (%d bytes%s)\n",
				projMD.Path, projMD.Bytes, truncMarker(projMD.Truncate))
		}
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

	reg := tools.New().
		Add(read.New(policy)).
		Add(grep.New()).
		Add(listdir.New()).
		Add(write.New(policy)).
		Add(edit.New(policy)).
		Add(bash.New(policy)).
		Add(gittool.New()).
		Add(skilltool.NewWithStats(skills, statsWriter, statsEnv)).
		Add(skillinstall.NewFetch()).
		Add(skillinstall.NewCommit(policy)).
		Add(askusertool.New(askPolicy))

	// DeepSeek-exclusive tools: FIM and Reasoner are only available
	// when using the DeepSeek client directly.
	if dsClient != nil {
		reg.Add(fimcomplete.New(dsClient, *model)).
			Add(think.New(dsClient, func() string { return sessionModel }, func() string { return sessionEffort }))
	}

	// Load MCP servers and register their tools. Errors are non-fatal:
	// a broken MCP server should not prevent seek from starting.
	if mcpCfg, err := mcpconfig.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "mcp config:", err)
	} else if len(mcpCfg.MCPServers) > 0 {
		servers := make(map[string]mcptool.ServerConfig, len(mcpCfg.MCPServers))
		for name, e := range mcpCfg.MCPServers {
			servers[name] = mcptool.ServerConfig{
				Command: e.Command,
				Args:    e.Args,
				Env:     mcptool.EnvMapToSlice(e.Env),
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
			fmt.Fprintf(os.Stderr, "mcp: loaded %d tool(s)\n", len(lr.Bridges))
		}
	}

	abs, _ := filepath.Abs(cwd)

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

	systemPrompt := fmt.Sprintf(systemPromptTpl, buildLangDirective(sessionLang), abs, modeLabel(initialMode))
	// Project instructions go BEFORE the skill manifest: they describe
	// "how this repo expects you to work" while skills are workflow
	// templates. Ordering matches the model's likely reading priority.
	if section := projMD.Section(); section != "" {
		systemPrompt = systemPrompt + "\n" + section
	}
	if manifest := skills.Manifest(); manifest != "" {
		systemPrompt = systemPrompt + "\n" + manifest
	}

	// Build (or restore) the persistence session. -no-save makes
	// activeSession nil so the TUI auto-save no-ops. activeSession
	// is forward-declared above (Skill stats env fn captures it);
	// only the assignment lives here.
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
				tracker.Record(loaded.Usage, *model, pricing.CurrentTier(time.Now()))
			}
		} else {
			activeSession = session.New(*model, abs, systemPrompt, *yolo, *plan)
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
		// Language override: if the user explicitly set --lang=en|zh,
		// that wins (already resolved in resolveLang above). If the
		// flag is "auto" and the session has a saved preference, use it.
		if loaded != nil && *langFlag == "auto" && activeSession.Lang != "" {
			sessionLang = activeSession.Lang
		}
	}

	// Lifecycle hooks. v1 memory plugs in PrePromptHook (inject L+M
	// <context> blocks) + SessionStartObserver (run GC) from one
	// struct registered into the same registry — see internal/memory.
	// Session-lifecycle hooks (SessionStart / SessionEnd) are fired
	// from main.go because the agent doesn't know when its host
	// program is "done".
	hooksReg := hooks.NewRegistry()

	ag, err := agent.New(agent.Config{
		Client:          dsClient,
		Provider:        provider,
		Model:           *model,
		Effort:          sessionEffort,
		SystemPrompt:    systemPrompt,
		Tools:           reg,
		MaxTokens:       *maxTokens,
		MaxTurns:        *maxTurns,
		AutoContinue:    *autoContinue,
		InitialMessages: initialMsgs,
		Hooks:           hooksReg,
	})
	if err != nil {
		return err
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

	var observeResultChan <-chan memory.ObserveResult
	if memHook != nil {
		observeResultChan = memHook.ResultChan
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
	}()

	// Benchmark mode: short-circuit before normal routing. Forces --yolo
	// so the agent can run bash/go-test without interactive approval.
	if *benchmarkTask != "" {
		*yolo = true
		policy.SetMode(permission.ModeYolo)
		return runBenchmark(ctx, *benchmarkTask, *benchmarkOut,
			ag, tracker, *model, activeSession, store)
	}

	if *rpcMode {
		return runRPC(ctx, ag, tracker, *model, *yolo, *plan, activeSession, store)
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
		if *jsonOut {
			return runJSON(ctx, ag, tracker, *model, *yolo, *plan, text, activeSession, store)
		}
		return runPrint(ctx, ag, tracker, *model, *yolo, *plan, text, activeSession, store)
	}

	// Now that we know we're entering the TUI, upgrade the policy
	// from Deny → Ask unless --yolo or --plan was passed. This is
	// what gives us inline y/N prompts on bash and out-of-CWD writes.
	// Plan mode stays Plan — read-only, no prompts needed.
	if !*yolo && !*plan {
		policy.SetMode(permission.ModeAsk)
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

	return tui.Run(tui.Options{
		Agent:             ag,
		Tracker:           tracker,
		Model:             sessionModel,
		Effort:            sessionEffort,
		Lang:              sessionLang,
		Yolo:              policy.Yolo(),
		Plan:              policy.Plan(),
		CWD:               abs,
		Ctx:               ctx,
		Theme:             effectiveTheme,
		GlamourStyle:      glamourStyle,
		ApprovalCh:        approvalCh,
		AskUserCh:         askUserCh,
		Session:           activeSession,
		Store:             store,
		Skills:            skills,
		ProviderName:      provLabel,
		MemoryProject:     memProject,
		Distiller:         distiller,
		ObserveResultChan: observeResultChan,

		RebuildAgent: func() (*agent.Agent, error) {
			// /reset rebuilds the agent; we have to re-apply project
			// instructions AND the skill manifest, otherwise the model
			// would forget both after a reset. AGENTS.md is loaded
			// once at startup and reused (re-reading on /reset would
			// surprise users who edit the file mid-session — we want
			// the file's behaviour to be "loaded at launch", not "hot-
			// reloaded"; documented behaviour is easier to reason
			// about than clever).
			sp := fmt.Sprintf(systemPromptTpl, buildLangDirective(sessionLang), abs, modeLabel(policy.Mode()))
			if section := projMD.Section(); section != "" {
				sp = sp + "\n" + section
			}
			if manifest := skills.Manifest(); manifest != "" {
				sp = sp + "\n" + manifest
			}
			newAg, err := agent.New(agent.Config{
				Client:       dsClient,
				Provider:     provider,
				Model:        sessionModel,
				Effort:       sessionEffort,
				SystemPrompt: sp,
				Tools:        reg,
				MaxTokens:    *maxTokens,
				MaxTurns:     *maxTurns,
				AutoContinue: *autoContinue,
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
		SetLang: func(l string) {
			// Mirror into sessionLang so RebuildAgent picks up the
			// change on the next /new. The TUI separately updates
			// Session.Lang so the next save captures the choice.
			sessionLang = l
		},
		SetYolo: func(y bool) {
			// policy.SetMode takes effect immediately for every
			// tool's permission.Check call. The agent's per-message
			// modeReminder keeps the model in sync without touching
			// the system prompt (prefix-cache safe).
			if y {
				policy.SetMode(permission.ModeYolo)
				ag.SetModeLabel("yolo")
			} else {
				policy.SetMode(permission.ModeAsk)
				ag.SetModeLabel("")
			}
		},
		SetPlan: func(p bool) {
			// Same as SetYolo — permission gate flips immediately;
			// modeReminder tells the model on the next user turn.
			if p {
				policy.SetMode(permission.ModePlan)
				ag.SetModeLabel("plan")
			} else {
				policy.SetMode(permission.ModeAsk)
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
func runPrint(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, text string, activeSession *session.Session, store *session.Store) error {
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

	for ev := range ag.Prompt(ctx, text) {
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
// Type values (stable contract — breaking changes = major version bump):
//
//	agent_start      — one per run
//	turn_start       — one per LLM call; index is 0-based
//	text_delta       — incremental assistant text; delta is the new chunk
//	reasoning_delta  — incremental CoT text from thinking-mode responses
//	tool_start       — a tool call is about to execute; id/name/args set
//	tool_delta       — intermediate output from a streaming tool (think)
//	tool_end         — tool finished; result set on success, error on failure
//	turn_end         — LLM call settled; token counts + tool_calls count
//	agent_end        — run complete; cumulative stats; session_id if saved
//	error            — fatal error; message is the error string
type jsonLine struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
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
}

// runJSON is the machine-readable output mode: one JSON object per line
// on stdout. Human-readable diagnostics (tier, stats footer) go to
// stderr so stdout stays parse-clean.
func runJSON(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, text string, activeSession *session.Session, store *session.Store) error {
	enc := json.NewEncoder(os.Stdout)

	emit := func(line jsonLine) {
		_ = enc.Encode(line) // json.Encoder always writes a trailing \n
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

	emit(jsonLine{Type: "agent_start"})

	for ev := range ag.Prompt(ctx, text) {
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

func truncMarker(t bool) string {
	if t {
		return ", truncated"
	}
	return ""
}

// detectGlamourStyle picks "dark" or "light" for the TUI's Markdown
// renderer. We do this BEFORE entering bubbletea's alt-screen so that
// termenv's OSC 11 background-colour query/response handshake
// completes synchronously while we still own stdin. If we let glamour
// do the equivalent under bubbletea, the terminal's response (e.g.
// "]11;rgb:fae0/fae0/fae0\[1;1R") leaks straight into the textarea as
// garbage text.
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
	if termenv.NewOutput(os.Stdout).HasDarkBackground() {
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
		return nil, deepseek.New(deepseek.WithAPIKey(apiKey)), "", deepseek.ModelV4Flash, nil

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
		return nil, nil, "", "", fmt.Errorf("unknown --provider %q; valid: deepseek | anthropic | openai | gemini | compatible", provFlag)
	}
}

// autoDistillEnabled returns true when $SEEK_AUTO_DISTILL is unset or set to
// a truthy value (1/true/yes/on). Controls memory_observe tool registration;
// default enabled because real-time notifications provide the safety net
// (PRD §6 v2).
func autoDistillEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SEEK_AUTO_DISTILL")))
	if v == "" {
		return true // default: enabled
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
