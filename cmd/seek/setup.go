package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// runSetupWizard is the first-run interactive bootstrap. Triggered
// when seek starts with no API key in either env or
// ~/.seek/config.json. Writes the chosen provider+key into config,
// then returns so main() can build the provider and continue into
// the normal TUI / print / json path.
//
// Deliberately NOT a Bubble Tea program — this runs BEFORE we know
// whether the terminal is even capable of a full TUI. A plain
// `bufio.Scanner` + Fprint loop works in any environment (piped
// stdin, dumb terminals, IDE consoles) where the regular TUI might
// already be flaky.
//
// On success returns the resolved provider name (e.g. "deepseek") so
// the caller can pass it into buildProvider without re-detecting.
// On cancel (Ctrl+C, Esc, or empty input at the key prompt) returns
// an error — the caller exits cleanly. We don't half-write a config
// in that case.
func runSetupWizard(ctx context.Context, in io.Reader, out io.Writer) (provider string, err error) {
	scanner := bufio.NewScanner(in)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  seek — first-run setup")
	fmt.Fprintln(out, "  ──────────────────────")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "  No API key found in env vars or ~/.seek/config.json.")
	fmt.Fprintln(out, "  This wizard saves one to ~/.seek/config.json (perms 0600).")
	fmt.Fprintln(out, "  Press Ctrl+C at any prompt to abort without writing anything.")
	fmt.Fprintln(out, "")

	// ── Step 1: provider ──
	provider, err = promptProvider(out, scanner)
	if err != nil {
		return "", err
	}

	// ── Step 2: api key ──
	key, err := promptAPIKey(out, scanner, provider)
	if err != nil {
		return "", err
	}

	// ── Step 3: optional live validation ──
	// We only run this for DeepSeek today; the second-tier providers
	// each have their own auth shape (Anthropic anthropic-version
	// header, OpenAI 401-on-list-models, Gemini API-key query param)
	// and a unified "validate" function would be three implementations
	// in a trench coat. Better to defer: if the key is wrong the user
	// will see a clean 401 on first chat call.
	if provider == "deepseek" {
		fmt.Fprint(out, "  Verifying with a 1-token ping... ")
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := pingDeepSeek(vctx, key)
		cancel()
		if err != nil {
			fmt.Fprintln(out, "failed.")
			fmt.Fprintf(out, "  %v\n", err)
			fmt.Fprintln(out, "  The key will still be saved — fix it later by re-running the wizard or")
			fmt.Fprintln(out, "  editing ~/.seek/config.json. Press Enter to continue, Ctrl+C to abort.")
			if !scanner.Scan() {
				return "", fmt.Errorf("setup: aborted at verification")
			}
		} else {
			fmt.Fprintln(out, "ok.")
		}
	}

	// ── Step 4: persist ──
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("setup: load existing config: %w", err)
	}
	config.SetKey(&cfg, provider, key)
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = provider
	}
	if err := config.Save(cfg); err != nil {
		return "", fmt.Errorf("setup: save config: %w", err)
	}

	path, _ := config.Path()
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "  Saved to %s.\n", path)
	fmt.Fprintln(out, "  Tip: set the matching env var to override on a single run, e.g.")
	fmt.Fprintf(out, "       %s=... seek\n", envVarFor(provider))
	fmt.Fprintln(out, "")
	return provider, nil
}

// providerOption is one row in the wizard's provider menu.
type providerOption struct {
	id      string // matches --provider flag values + config keys
	label   string
	keyHint string // example key prefix shown next to the prompt
	docsURL string // where to get a key
}

var providerOptions = []providerOption{
	{"deepseek", "DeepSeek (recommended — full feature set)", "sk-", "https://platform.deepseek.com/api_keys"},
	{"anthropic", "Anthropic Claude", "sk-ant-", "https://console.anthropic.com/account/keys"},
	{"openai", "OpenAI GPT", "sk-", "https://platform.openai.com/api-keys"},
	{"gemini", "Google Gemini", "AIza", "https://aistudio.google.com/app/apikey"},
}

func promptProvider(out io.Writer, scanner *bufio.Scanner) (string, error) {
	fmt.Fprintln(out, "  Step 1/2 — choose a provider:")
	for i, p := range providerOptions {
		fmt.Fprintf(out, "    %d) %s\n", i+1, p.label)
	}
	for {
		fmt.Fprint(out, "  > ")
		if !scanner.Scan() {
			return "", fmt.Errorf("setup: aborted at provider selection")
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Fprintln(out, "  (enter a number 1–4, or Ctrl+C to abort)")
			continue
		}
		// Accept both digit and provider name — power users will
		// type "deepseek" rather than hunt for the number.
		for i, p := range providerOptions {
			if line == fmt.Sprintf("%d", i+1) || strings.EqualFold(line, p.id) {
				fmt.Fprintln(out, "")
				return p.id, nil
			}
		}
		fmt.Fprintf(out, "  unrecognised choice %q — enter 1–4 or a provider name\n", line)
	}
}

func promptAPIKey(out io.Writer, scanner *bufio.Scanner, provider string) (string, error) {
	var opt providerOption
	for _, p := range providerOptions {
		if p.id == provider {
			opt = p
			break
		}
	}
	fmt.Fprintf(out, "  Step 2/2 — paste your %s API key:\n", opt.label)
	fmt.Fprintf(out, "    Get one from %s\n", opt.docsURL)
	fmt.Fprintf(out, "    Format usually starts with %q\n", opt.keyHint)
	for {
		fmt.Fprint(out, "  > ")
		if !scanner.Scan() {
			return "", fmt.Errorf("setup: aborted at key entry")
		}
		key := strings.TrimSpace(scanner.Text())
		if key == "" {
			fmt.Fprintln(out, "  (empty — Ctrl+C to abort, or paste a key)")
			continue
		}
		return key, nil
	}
}

// pingDeepSeek sends a one-token chat completion to verify the key
// works. Treats any 2xx as success; non-2xx and network errors
// surface as the returned error.
func pingDeepSeek(ctx context.Context, apiKey string) error {
	c := deepseek.New(deepseek.WithAPIKey(apiKey))
	_, err := c.Chat(ctx, &deepseek.ChatRequest{
		Model: deepseek.ModelV4Flash,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "ping"},
		},
		MaxTokens: 1,
	})
	return err
}

// envVarFor mirrors internal/config's helper of the same name —
// duplicated here (one line) rather than exported because this
// caller is the only consumer and exporting it would clutter the
// config package's public surface.
func envVarFor(provider string) string {
	switch provider {
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	}
	return ""
}

// shouldTriggerWizard returns true when seek is started with no usable
// auth: no --provider flag, no canonical env var for any known
// provider, no ~/.seek/config.json with a key, and stdin is a TTY
// (otherwise we'd be in a script and the right move is to error out
// loudly, not hang on an interactive prompt).
func shouldTriggerWizard() bool {
	if !stdinIsTTY(os.Stdin) {
		return false
	}
	for _, p := range providerOptions {
		if os.Getenv(envVarFor(p.id)) != "" {
			return false
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	for _, p := range providerOptions {
		if config.KeyFor(cfg, p.id) != "" {
			return false
		}
	}
	return true
}

// stdinIsTTY is a thin wrapper so tests can pass a non-terminal
// reader. In production we check that os.Stdin is character-device-
// like — same trick stdinIsPiped() uses, just the inverse.
func stdinIsTTY(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}
