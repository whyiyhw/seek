// Command seek is the DeepSeek-first coding agent CLI.
//
// M1 wires cmd/seek through pkg/agent with one tool (`read`) and a system
// prompt. The CLI is still single-turn print mode — interactive TUI lands
// in M4. Subsequent milestones add write/edit/bash tools, MCP servers,
// skills, sessions, and the second-tier Anthropic/OpenAI/Gemini providers.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

const systemPrompt = `You are seek, a DeepSeek-powered coding agent.
You have the following tools available:
- read(path, offset?, limit?): read a file from disk with line numbers.

When the user asks about file contents, code, or anything that requires
inspecting the workspace, call the read tool first. Quote relevant lines
in your answer. Keep responses concise.`

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

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	reg := tools.New().Add(read.New())

	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey(apiKey)),
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
				// Dim CoT to stderr so it doesn't pollute stdout when
				// the user pipes output downstream.
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
