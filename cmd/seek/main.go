// Command seek is the DeepSeek-first coding agent CLI.
//
// M3 wires in cache.Tracker (session-level prefix-cache stats), pricing
// (off-peak tier awareness + per-call cost), and the Think tool that
// bridges the chat loop into deepseek-reasoner. Interactive TUI lands in
// M4; full reasoner-then-chat skill arrives with skill loading in M5.
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
	"syscall"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/mcpconfig"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/projectmd"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/tools/bash"
	"github.com/whyiyhw/seek/internal/tools/edit"
	"github.com/whyiyhw/seek/internal/tools/fimcomplete"
	"github.com/whyiyhw/seek/internal/tools/grep"
	"github.com/whyiyhw/seek/internal/tools/listdir"
	"github.com/whyiyhw/seek/internal/tools/mcptool"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/internal/tools/skilltool"
	"github.com/whyiyhw/seek/internal/tools/think"
	"github.com/whyiyhw/seek/internal/tools/write"
	"github.com/whyiyhw/seek/internal/tui"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
	"github.com/whyiyhw/seek/pkg/llm/compatible"
	anthropicprov "github.com/whyiyhw/seek/pkg/llm/provider/anthropic"
	geminiprov "github.com/whyiyhw/seek/pkg/llm/provider/gemini"
	openaiprov "github.com/whyiyhw/seek/pkg/llm/provider/openai"

	"github.com/muesli/termenv"
)

const systemPromptTpl = `You are seek, a DeepSeek-powered coding agent.

Available tools:
- read(path, offset?, limit?): read a file with line numbers. Always pass limit when reading an unfamiliar file; use grep first to find the relevant line range.
- grep(pattern, path, context_lines?): search files by regex or literal string; returns matching lines with line numbers and surrounding context. Use this to locate a symbol or section, then follow up with read(offset, limit) for the precise range — avoids reading entire files into context.
- list_dir(path, depth?, show_hidden?): list directory entries with type and size. Default depth=1, hidden files excluded. Use this instead of 'bash ls' when you need depth or dotfiles.
- write(path, content): create or overwrite a file. Refused outside the working directory unless seek was started with --yolo.
- edit(path, old_string, new_string, expected_replacements?): exact substring replacement. old_string must be unique unless expected_replacements is set. new_string="" deletes.
- bash(command, timeout_ms?): run a shell command. Refused unless seek was started with --yolo — in that case ask the user to re-run with --yolo (do not retry blindly).
- fim_complete(path, before_marker, after_marker?, max_tokens?): DeepSeek's fill-in-the-middle endpoint. Cheaper than chat for small gap-fills. Returns text WITHOUT applying — call edit afterwards to apply.
- think(task, reflect?, context?): call deepseek-reasoner for hard multi-step planning or self-review. Use sparingly — each call is several thousand tokens. Pattern: think→execute→think(reflect=true) for non-trivial changes.
- Skill(name): fetch the instructions for a named skill listed under "Available skills" below. The tool returns the skill body; follow its steps. Use this whenever a user request matches a skill's description.

Workflow:
1. Explore before reading: use grep to locate relevant symbols or sections, then read(offset, limit) for the specific range. Never read an entire file without a limit unless every line is needed.
2. Inspect the workspace with read before changing anything.
3. For multi-step or risky tasks, call think first to plan; for non-trivial changes, call think(reflect=true) after to self-review.
4. Keep edits minimal and explicit (Claude Code style: tight old_string / new_string).
5. For permission denials, surface the message to the user and stop — do not loop.

Working directory: %s. --yolo: %v.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		prompt       = flag.String("p", "", "prompt text; if non-empty (or stdin is piped) seek runs in print mode and exits")
		model        = flag.String("model", "", "model id; default depends on provider (deepseek-chat for DeepSeek, etc.)")
		maxTurns     = flag.Int("max-turns", 8, "safety bound on agent loop iterations")
		yolo         = flag.Bool("yolo", false, "allow bash + writes outside CWD without prompting")
		jsonOut      = flag.Bool("json", false, "emit agent events as JSONL on stdout (implies print mode)")
		resume       = flag.String("resume", "", "load a saved session by ID (see seek -list)")
		cont         = flag.Bool("continue", false, "load the most-recently-updated session")
		noSave       = flag.Bool("no-save", false, "do not persist this session to disk")
		list         = flag.Bool("list", false, "list saved sessions and exit")
		noProj       = flag.Bool("no-project-md", false, "do not auto-load AGENTS.md from the project tree")
		providerFlag = flag.String("provider", "", "LLM provider: deepseek (default) | anthropic | openai | gemini | compatible")
		baseURL      = flag.String("base-url", "", "base URL for --provider=compatible (OpenAI-compatible endpoint)")
		providerName = flag.String("provider-name", "Compatible", "display name for --provider=compatible")
	)
	flag.Parse()

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
	}
	// Print mode (-p / piped stdin) can't realistically interrupt to
	// ask, so it stays in deny mode unless --yolo is explicit. The TUI
	// path overrides to Ask further down so per-call approval kicks
	// in. --yolo always wins.
	initialMode := permission.ModeDeny
	if *yolo {
		initialMode = permission.ModeYolo
	}
	policy, err := permission.New(cwd, initialMode)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *model == "" {
		*model = modelDefault
	}
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

	reg := tools.New().
		Add(read.New()).
		Add(grep.New()).
		Add(listdir.New()).
		Add(write.New(policy)).
		Add(edit.New(policy)).
		Add(bash.New(policy)).
		Add(skilltool.New(skills))

	// DeepSeek-exclusive tools: FIM and Reasoner are only available
	// when using the DeepSeek client directly.
	if dsClient != nil {
		reg.Add(fimcomplete.New(dsClient, *model)).
			Add(think.New(dsClient))
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
	systemPrompt := fmt.Sprintf(systemPromptTpl, abs, *yolo)
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
	// activeSession nil so the TUI auto-save no-ops.
	var activeSession *session.Session
	var initialMsgs []deepseek.Message
	if !*noSave {
		if loaded != nil {
			activeSession = loaded
			initialMsgs = loaded.Messages
			// Replay accumulated stats into the tracker so the status
			// bar shows cumulative figures, not just this run's.
			if loaded.Usage.TotalTokens > 0 {
				tracker.Record(loaded.Usage)
			}
		} else {
			activeSession = session.New(*model, abs, systemPrompt, *yolo)
		}
	}

	ag, err := agent.New(agent.Config{
		Client:          dsClient,
		Provider:        provider,
		Model:           *model,
		SystemPrompt:    systemPrompt,
		Tools:           reg,
		MaxTurns:        *maxTurns,
		InitialMessages: initialMsgs,
	})
	if err != nil {
		return err
	}

	// Route: -json / -p / piped stdin → print mode; otherwise TUI.
	if *jsonOut || *prompt != "" || stdinIsPiped() {
		text, err := resolvePrompt(*prompt)
		if err != nil {
			return err
		}
		if text == "" {
			return fmt.Errorf("empty prompt (pass -p or pipe text on stdin)")
		}
		if *jsonOut {
			return runJSON(ctx, ag, tracker, *model, *yolo, text, activeSession, store)
		}
		return runPrint(ctx, ag, tracker, *model, *yolo, text, activeSession, store)
	}

	// Now that we know we're entering the TUI, upgrade the policy
	// from Deny → Ask unless --yolo was passed. This is what gives us
	// inline y/N prompts on bash and out-of-CWD writes.
	if !*yolo {
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

	sessionModel := *model

	return tui.Run(tui.Options{
		Agent:        ag,
		Tracker:      tracker,
		Model:        sessionModel,
		Yolo:         policy.Yolo(),
		CWD:          abs,
		Ctx:          ctx,
		GlamourStyle: detectGlamourStyle(),
		ApprovalCh:   approvalCh,
		Session:      activeSession,
		Store:        store,
		Skills:       skills,
		ProviderName: provLabel,

		RebuildAgent: func() (*agent.Agent, error) {
			// /reset rebuilds the agent; we have to re-apply project
			// instructions AND the skill manifest, otherwise the model
			// would forget both after a reset. AGENTS.md is loaded
			// once at startup and reused (re-reading on /reset would
			// surprise users who edit the file mid-session — we want
			// the file's behaviour to be "loaded at launch", not "hot-
			// reloaded"; documented behaviour is easier to reason
			// about than clever).
			sp := fmt.Sprintf(systemPromptTpl, abs, policy.Yolo())
			if section := projMD.Section(); section != "" {
				sp = sp + "\n" + section
			}
			if manifest := skills.Manifest(); manifest != "" {
				sp = sp + "\n" + manifest
			}
			return agent.New(agent.Config{
				Client:       dsClient,
				Provider:     provider,
				Model:        sessionModel,
				SystemPrompt: sp,
				Tools:        reg,
				MaxTurns:     *maxTurns,
			})
		},
		SetModel: func(m string) { sessionModel = m },
		SetYolo: func(y bool) {
			// policy is the single source of truth now — mode flip is
			// observed by every tool's permission.Check call
			// immediately, no registry rebuild needed.
			if y {
				policy.SetMode(permission.ModeYolo)
			} else {
				policy.SetMode(permission.ModeAsk)
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
func runPrint(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo bool, text string, activeSession *session.Session, store *session.Store) error {
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
			tracker.Record(e.Usage)
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
	cost := pricing.Cost(model, tier, c)

	fmt.Fprintf(os.Stderr, "\n--- seek stats ---\n")
	fmt.Fprintf(os.Stderr, "yolo:         %v\n", yolo)
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
//	reasoning_delta  — incremental CoT text from deepseek-reasoner
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
func runJSON(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo bool, text string, activeSession *session.Session, store *session.Store) error {
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
			tracker.Record(e.Usage)
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
	return s[:n] + "…"
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
// SEEK_STYLE=dark|light overrides the detection.
func detectGlamourStyle() string {
	if v := os.Getenv("SEEK_STYLE"); v != "" {
		return v
	}
	if termenv.NewOutput(os.Stdout).HasDarkBackground() {
		return "dark"
	}
	return "light"
}

// buildProvider selects and constructs the LLM provider based on the
// --provider flag and env vars. Returns:
//
//	provider    llm.Provider — non-nil for second-tier providers
//	dsClient    *deepseek.Client — non-nil for DeepSeek (first-class path)
//	provLabel   string — human name for TUI banner ("" = DeepSeek, no banner)
//	modelDefault string — sensible default model for the chosen provider
func buildProvider(provFlag, baseURLFlag, provName string) (
	provider llm.Provider, dsClient *deepseek.Client, provLabel, modelDefault string, err error,
) {
	// Determine effective provider name.
	if provFlag == "" {
		switch {
		case os.Getenv("ANTHROPIC_API_KEY") != "" && os.Getenv("DEEPSEEK_API_KEY") == "":
			provFlag = "anthropic"
		case os.Getenv("OPENAI_API_KEY") != "" && os.Getenv("DEEPSEEK_API_KEY") == "":
			provFlag = "openai"
		case os.Getenv("GEMINI_API_KEY") != "" && os.Getenv("DEEPSEEK_API_KEY") == "":
			provFlag = "gemini"
		default:
			provFlag = "deepseek"
		}
	}

	switch provFlag {
	case "deepseek":
		apiKey := os.Getenv("DEEPSEEK_API_KEY")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("DEEPSEEK_API_KEY is not set")
		}
		return nil, deepseek.New(deepseek.WithAPIKey(apiKey)), "", deepseek.ModelChat, nil

	case "anthropic":
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("ANTHROPIC_API_KEY is not set (needed for --provider=anthropic)")
		}
		return anthropicprov.New(apiKey), nil, "Anthropic", "claude-sonnet-4-20250514", nil

	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("OPENAI_API_KEY is not set (needed for --provider=openai)")
		}
		return openaiprov.New(apiKey), nil, "OpenAI", "gpt-4o", nil

	case "gemini":
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("GEMINI_API_KEY is not set (needed for --provider=gemini)")
		}
		return geminiprov.New(apiKey), nil, "Gemini", "gemini-2.0-flash", nil

	case "compatible":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("DEEPSEEK_API_KEY") // compatible endpoints often share key env
		}
		if baseURLFlag == "" {
			return nil, nil, "", "", fmt.Errorf("--base-url is required for --provider=compatible")
		}
		return compatible.New(apiKey, baseURLFlag, provName), nil, provName, "", nil

	default:
		return nil, nil, "", "", fmt.Errorf("unknown --provider %q; valid: deepseek | anthropic | openai | gemini | compatible", provFlag)
	}
}
