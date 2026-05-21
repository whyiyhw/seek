// Command seek is the DeepSeek-first coding agent CLI.
//
// M3 wires in cache.Tracker (session-level prefix-cache stats), pricing
// (off-peak tier awareness + per-call cost), and the Think tool that
// bridges the chat loop into deepseek-reasoner. Interactive TUI lands in
// M4; full reasoner-then-chat skill arrives with skill loading in M5.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/tools/bash"
	"github.com/whyiyhw/seek/internal/tools/edit"
	"github.com/whyiyhw/seek/internal/tools/fimcomplete"
	"github.com/whyiyhw/seek/internal/tools/listdir"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/internal/tools/think"
	"github.com/whyiyhw/seek/internal/tools/write"
	"github.com/whyiyhw/seek/internal/tui"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"

	"github.com/muesli/termenv"
)

const systemPromptTpl = `You are seek, a DeepSeek-powered coding agent.

Available tools:
- read(path, offset?, limit?): read a file with line numbers. Files only — use list_dir for directories.
- list_dir(path, depth?, show_hidden?): list directory entries with type and size. Default depth=1, hidden files excluded. Prefer over 'bash ls'.
- write(path, content): create or overwrite a file. Refused outside the working directory unless seek was started with --yolo.
- edit(path, old_string, new_string, expected_replacements?): exact substring replacement. old_string must be unique unless expected_replacements is set. new_string="" deletes.
- bash(command, timeout_ms?): run a shell command. Refused unless seek was started with --yolo — in that case ask the user to re-run with --yolo (do not retry blindly).
- fim_complete(path, before_marker, after_marker?, max_tokens?): DeepSeek's fill-in-the-middle endpoint. Cheaper than chat for small gap-fills. Returns text WITHOUT applying — call edit afterwards to apply.
- think(task, reflect?, context?): call deepseek-reasoner for hard multi-step planning or self-review. Use sparingly — each call is several thousand tokens. Pattern: think→execute→think(reflect=true) for non-trivial changes.

Workflow:
1. Inspect the workspace with read before changing anything.
2. For multi-step or risky tasks, call think first to plan; for non-trivial changes, call think(reflect=true) after to self-review.
3. Keep edits minimal and explicit (Claude Code style: tight old_string / new_string).
4. For permission denials, surface the message to the user and stop — do not loop.

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
		prompt   = flag.String("p", "", "prompt text; if non-empty (or stdin is piped) seek runs in print mode and exits")
		model    = flag.String("model", deepseek.ModelChat, "model id (deepseek-chat | deepseek-reasoner)")
		maxTurns = flag.Int("max-turns", 8, "safety bound on agent loop iterations")
		yolo     = flag.Bool("yolo", false, "allow bash + writes outside CWD without prompting")
	)
	flag.Parse()

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("DEEPSEEK_API_KEY is not set")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	policy, err := permission.New(cwd, *yolo)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	client := deepseek.New(deepseek.WithAPIKey(apiKey))
	tracker := cache.New()

	reg := tools.New().
		Add(read.New()).
		Add(listdir.New()).
		Add(write.New(policy)).
		Add(edit.New(policy)).
		Add(bash.New(policy)).
		Add(fimcomplete.New(client, *model)).
		Add(think.New(client))

	abs, _ := filepath.Abs(cwd)
	systemPrompt := fmt.Sprintf(systemPromptTpl, abs, *yolo)

	ag, err := agent.New(agent.Config{
		Client:       client,
		Model:        *model,
		SystemPrompt: systemPrompt,
		Tools:        reg,
		MaxTurns:     *maxTurns,
	})
	if err != nil {
		return err
	}

	// Route: explicit -p flag OR piped stdin → print mode; otherwise TUI.
	if *prompt != "" || stdinIsPiped() {
		text, err := resolvePrompt(*prompt)
		if err != nil {
			return err
		}
		if text == "" {
			return fmt.Errorf("empty prompt (pass -p or pipe text on stdin)")
		}
		return runPrint(ctx, ag, tracker, *model, *yolo, text)
	}

	// Mutable session state — wrapped in closures so TUI commands like
	// /reset, /model, /yolo can update the host without owning these
	// fields directly.
	sessionModel := *model
	sessionYolo := *yolo
	sessionPolicy := policy
	sessionReg := reg

	return tui.Run(tui.Options{
		Agent:        ag,
		Tracker:      tracker,
		Model:        sessionModel,
		Yolo:         sessionYolo,
		CWD:          abs,
		Ctx:          ctx,
		GlamourStyle: detectGlamourStyle(),

		RebuildAgent: func() (*agent.Agent, error) {
			return agent.New(agent.Config{
				Client:       client,
				Model:        sessionModel,
				SystemPrompt: fmt.Sprintf(systemPromptTpl, abs, sessionYolo),
				Tools:        sessionReg,
				MaxTurns:     *maxTurns,
			})
		},
		SetModel: func(m string) { sessionModel = m },
		SetYolo: func(y bool) {
			sessionYolo = y
			if p, err := permission.New(cwd, y); err == nil {
				sessionPolicy = p
				// Rebuild the tool registry so write/edit/bash see the
				// new policy. (Reg's frozen Wire() output is preserved
				// for cache-prefix stability; we swap in-place here.)
				_ = sessionPolicy // referenced — keeps lint happy
				sessionReg = tools.New().
					Add(read.New()).
					Add(listdir.New()).
					Add(write.New(sessionPolicy)).
					Add(edit.New(sessionPolicy)).
					Add(bash.New(sessionPolicy)).
					Add(fimcomplete.New(client, sessionModel)).
					Add(think.New(client))
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
func runPrint(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo bool, text string) error {
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

		case agent.AgentEnd:
			turns = e.Turns
			toolCalls = e.ToolCalls

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
