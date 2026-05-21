// Command seek is the DeepSeek-first coding agent CLI.
//
// M0 scope: read a prompt from stdin or -p flag, stream a chat completion to
// stdout, print prefix-cache hit ratio when the response finishes. Tool calls,
// interactive TUI, MCP, skills, and the second-class providers (Anthropic /
// OpenAI / Gemini) land in subsequent milestones.
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

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		prompt = flag.String("p", "", "prompt text; if empty, read from stdin")
		model  = flag.String("model", deepseek.ModelChat, "model id (deepseek-chat | deepseek-reasoner)")
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

	client := deepseek.New(deepseek.WithAPIKey(apiKey))

	start := time.Now()
	ch, err := client.ChatStream(ctx, &deepseek.ChatRequest{
		Model: *model,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: text},
		},
	})
	if err != nil {
		return err
	}

	var (
		firstByte time.Duration
		gotFirst  bool
		usage     deepseek.Usage
		finish    string
	)
	for ev := range ch {
		switch ev.Type {
		case deepseek.EventDelta:
			if !gotFirst {
				firstByte = time.Since(start)
				gotFirst = true
			}
			fmt.Print(ev.Delta)
		case deepseek.EventReasoningDelta:
			// Dim the reasoner's CoT so it's visually distinct from the
			// final answer when the user pipes deepseek-reasoner.
			fmt.Fprint(os.Stderr, "\x1b[2m"+ev.Delta+"\x1b[0m")
		case deepseek.EventDone:
			usage = ev.Usage
			finish = ev.FinishReason
		}
	}
	fmt.Println()

	fmt.Fprintf(os.Stderr, "\n--- seek stats ---\n")
	fmt.Fprintf(os.Stderr, "finish:      %s\n", finish)
	fmt.Fprintf(os.Stderr, "ttfb:        %s\n", firstByte.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "elapsed:     %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "prompt tok:  %d (cache hit %d / miss %d, ratio %s)\n",
		usage.PromptTokens, usage.PromptCacheHitTokens, usage.PromptCacheMissTokens,
		deepseek.FormatHitRatio(usage))
	fmt.Fprintf(os.Stderr, "completion:  %d tok\n", usage.CompletionTokens)
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
	// If stdin is a TTY (no piped input), don't block waiting for input —
	// surface a clearer error.
	if stat.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
