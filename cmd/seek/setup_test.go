package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
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
		"unknown":   "",
	}
	for provider, want := range cases {
		if got := envVarFor(provider); got != want {
			t.Errorf("envVarFor(%q) = %q, want %q", provider, got, want)
		}
	}
}
