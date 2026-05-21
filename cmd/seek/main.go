// Command seek is the DeepSeek-first coding agent CLI.
//
// M2 wires the four core tools (read / write / edit / bash) plus the
// DeepSeek-specific fim_complete fast path. Bash and writes outside CWD
// are gated by the permission package; pass --yolo to bypass.
//
// Interactive TUI, sessions, MCP, skills, and the second-tier
// Anthropic/OpenAI/Gemini providers land in subsequent milestones.
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

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/tools/bash"
	"github.com/whyiyhw/seek/internal/tools/edit"
	"github.com/whyiyhw/seek/internal/tools/fimcomplete"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/internal/tools/write"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

const systemPromptTpl = `You are seek, a DeepSeek-powered coding agent.

Available tools:
- read(path, offset?, limit?): read a file with line numbers.
- write(path, content): create or overwrite a file. Refused outside the working directory unless seek was started with --yolo.
- edit(path, old_string, new_string, expected_replacements?): exact substring replacement. old_string must be unique unless expected_replacements is set. new_string="" deletes.
- bash(command, timeout_ms?): run a shell command. Refused unless seek was started with --yolo — in that case ask the user to re-run with --yolo (do not retry blindly).
- fim_complete(path, before_marker, after_marker?, max_tokens?): DeepSeek's fill-in-the-middle endpoint. Cheaper than chat for small gap-fills. Returns text WITHOUT applying — call edit afterwards to apply.

Workflow:
1. Inspect the workspace with read before changing anything.
2. Keep edits minimal and explicit (Claude Code style: tight old_string / new_string).
3. For permission denials, surface the message to the user and stop — do not loop.
4. Working directory: %s. Default --yolo is %v.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		prompt   = flag.String("p", "", "prompt text; if empty, read from stdin")
		model    = flag.String("model", deepseek.ModelChat, "model id (deepseek-chat | deepseek-reasoner)")
		maxTurns = flag.Int("max-turns", 8, "safety bound on agent loop iterations")
		yolo     = flag.Bool("yolo", false, "allow bash + writes outside CWD without prompting")
	)
	flag.Parse()

	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("DEEPSEEK_API_KEY is not set")
	}

	text, err := resolvePrompt(*prompt)
	if err != nil {
		return err
	}
	if text == "" {
		return fmt.Errorf("empty prompt (pass -p or pipe text on stdin)")
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

	reg := tools.New().
		Add(read.New()).
		Add(write.New(policy)).
		Add(edit.New(policy)).
		Add(bash.New(policy)).
		Add(fimcomplete.New(client, *model))

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

	start := time.Now()
	var (
		firstByte time.Duration
		gotFirst  bool
		final     deepseek.Usage
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

		case agent.AgentEnd:
			final = e.Usage
			turns = e.Turns
			toolCalls = e.ToolCalls

		case agent.ErrorEvent:
			fmt.Println()
			return e.Err
		}
	}

	fmt.Println()
	fmt.Fprintf(os.Stderr, "\n--- seek stats ---\n")
	fmt.Fprintf(os.Stderr, "yolo:         %v\n", *yolo)
	fmt.Fprintf(os.Stderr, "turns:        %d\n", turns)
	fmt.Fprintf(os.Stderr, "tool calls:   %d\n", toolCalls)
	fmt.Fprintf(os.Stderr, "ttfb:         %s\n", firstByte.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "elapsed:      %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "prompt tok:   %d (cache hit %d / miss %d, ratio %s)\n",
		final.PromptTokens, final.PromptCacheHitTokens, final.PromptCacheMissTokens,
		deepseek.FormatHitRatio(final))
	fmt.Fprintf(os.Stderr, "completion:   %d tok\n", final.CompletionTokens)
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
