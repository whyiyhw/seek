package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/config"
)

// drive runs the named prompt against canned input + collects output.
// Lets us test the parser without mucking with os.Stdin or running a
// real PTY.
func drive(t *testing.T, fn func(*bufio.Scanner, *bytes.Buffer) (string, error), input string) (got string, out string, err error) {
	t.Helper()
	in := strings.NewReader(input)
	scanner := bufio.NewScanner(in)
	var buf bytes.Buffer
	got, err = fn(scanner, &buf)
	return got, buf.String(), err
}

func TestPromptProvider_AcceptsDigit(t *testing.T) {
	got, _, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"1\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "deepseek" {
		t.Errorf("got %q, want deepseek (option 1)", got)
	}
}

func TestPromptProvider_AcceptsName(t *testing.T) {
	// Power users will type the name rather than scroll-and-count.
	got, _, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"anthropic\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "anthropic" {
		t.Errorf("got %q, want anthropic", got)
	}
}

func TestPromptProvider_AcceptsCaseInsensitiveName(t *testing.T) {
	got, _, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"OpenAI\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "openai" {
		t.Errorf("got %q, want openai (case-insensitive)", got)
	}
}

func TestPromptProvider_RetriesOnBadInput(t *testing.T) {
	// First line is gibberish, second is valid. The wizard should
	// re-prompt rather than abort — otherwise typos abort setup.
	got, out, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"xyz\n2\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "anthropic" {
		t.Errorf("after retry got %q, want anthropic (option 2)", got)
	}
	if !strings.Contains(out, "unrecognised") {
		t.Errorf("expected diagnostic on bad input, got: %s", out)
	}
}

func TestPromptProvider_EmptyInputReprompts(t *testing.T) {
	// Pressing Enter at the prompt shouldn't abort — it should re-prompt.
	got, out, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"\n3\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "openai" {
		t.Errorf("got %q, want openai", got)
	}
	if !strings.Contains(out, "enter a number") {
		t.Errorf("expected hint on empty input, got: %s", out)
	}
}

func TestPromptProvider_EOFAborts(t *testing.T) {
	// Closed stdin (Ctrl+D, broken pipe, killed parent) returns an
	// error rather than looping forever.
	_, _, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"") // no input → scanner.Scan() returns false immediately
	if err == nil {
		t.Errorf("expected error on EOF, got nil")
	}
}

func TestPromptAPIKey_StripsWhitespace(t *testing.T) {
	got, _, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptAPIKey(b, s, "deepseek")
		},
		"   sk-abc-123   \n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "sk-abc-123" {
		t.Errorf("got %q, want trimmed key", got)
	}
}

func TestPromptAPIKey_EmptyReprompts(t *testing.T) {
	// Empty line shouldn't be accepted as a key — re-prompt instead.
	got, out, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptAPIKey(b, s, "deepseek")
		},
		"\nsk-real\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "sk-real" {
		t.Errorf("got %q, want sk-real", got)
	}
	if !strings.Contains(out, "empty") {
		t.Errorf("expected diagnostic for empty input: %s", out)
	}
}

func TestEnvVarFor_KnownProviders(t *testing.T) {
	// Sanity-check the table — easy to silently typo "DEEPSEEK_API_KEY".
	cases := map[string]string{
		"deepseek":  "DEEPSEEK_API_KEY",
		"anthropic": "ANTHROPIC_API_KEY",
		"openai":    "OPENAI_API_KEY",
		"gemini":    "GEMINI_API_KEY",
		"glm":       "GLM_API_KEY",
		"opencode":  "OPENCODE_API_KEY",
		"unknown":   "",
	}
	for provider, want := range cases {
		if got := envVarFor(provider); got != want {
			t.Errorf("envVarFor(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestPromptProvider_MenuRendersAllOptions(t *testing.T) {
	// Every providerOption must appear as a numbered row — a row
	// missing from the render is a row the user can't pick.
	_, out, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"1\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for i, p := range providerOptions {
		want := fmt.Sprintf("    %d) %s\n", i+1, p.label)
		if !strings.Contains(out, want) {
			t.Errorf("menu missing row %d (%s); got menu:\n%s", i+1, p.id, out)
		}
	}
	if n := len(providerOptions); n != 6 {
		t.Errorf("providerOptions has %d entries, want 6 (wizard menu row count)", n)
	}
}

func TestPromptProvider_AcceptsDigitForNewLanes(t *testing.T) {
	// opencode sits at slot 5 — the digit channel must reach past the
	// original four rows.
	got, _, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"5\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "opencode" {
		t.Errorf("got %q, want opencode (option 5)", got)
	}
}

func TestPromptProvider_AcceptsNameForNewLanes(t *testing.T) {
	got, _, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"glm\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "glm" {
		t.Errorf("got %q, want glm", got)
	}
}

func TestPromptProvider_HintShowsDynamicRange(t *testing.T) {
	// The retry hints must state the real menu size, derived from
	// len(providerOptions) — a hardcoded range would lie the moment a
	// provider is added. Expected range is computed, not hardcoded, so
	// this test also stays truthful if rows are added later.
	wantRange := fmt.Sprintf("1–%d", len(providerOptions))
	_, out, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptProvider(b, s)
		},
		"nope\n\n1\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	bad := fmt.Sprintf("unrecognised choice %q — enter %s or a provider name", "nope", wantRange)
	if !strings.Contains(out, bad) {
		t.Errorf("unrecognised-input hint lacks dynamic range %q, got: %s", wantRange, out)
	}
	empty := fmt.Sprintf("(enter a number %s, or Ctrl+C to abort)", wantRange)
	if !strings.Contains(out, empty) {
		t.Errorf("empty-input hint lacks dynamic range %q, got: %s", wantRange, out)
	}
}

func TestPromptAPIKey_OmitsFormatLineWhenHintUnknown(t *testing.T) {
	// opencode's key shape is unverified, so its keyHint is empty and
	// the "Format usually starts with" line must be omitted entirely —
	// printing it with an empty prefix would look broken.
	_, out, err := drive(t,
		func(s *bufio.Scanner, b *bytes.Buffer) (string, error) {
			return promptAPIKey(b, s, "opencode")
		},
		"some-key\n")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(out, "Format usually starts with") {
		t.Errorf("expected no format line for empty keyHint, got: %s", out)
	}
	if !strings.Contains(out, "https://console.opencode.ai") {
		t.Errorf("expected docs URL for opencode, got: %s", out)
	}
}

func TestSetKey_RoundTripsMenuProviderIDs(t *testing.T) {
	// The wizard persists via config.SetKey(cfg, p.id, key) — every
	// menu id must land on (and survive a config-file round-trip under)
	// its own provider key, glm/opencode included.
	for _, p := range providerOptions {
		var cfg config.Config
		config.SetKey(&cfg, p.id, "wizard-test-key")
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back config.Config
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := back.Providers[p.id].APIKey; got != "wizard-test-key" {
			t.Errorf("round-trip for %q: got key %q, want wizard-test-key", p.id, got)
		}
	}
}
