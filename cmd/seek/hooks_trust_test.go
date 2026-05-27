package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/hooksconfig"
)

func TestStdinTrustPrompt_NoTTY_AlwaysRefuses(t *testing.T) {
	var stderr bytes.Buffer
	p := &stdinTrustPrompt{
		in:  strings.NewReader("y\n"), // even with "y" on the pipe, no-TTY refuses
		out: &stderr,
		tty: false,
	}
	ok := p.AskTrustHooks(hooksconfig.TrustRequest{
		ProjectPath: "/x",
		HookCount:   2,
		Names:       []string{"a", "b"},
	})
	if ok {
		t.Errorf("non-TTY should refuse")
	}
	if !strings.Contains(stderr.String(), "no TTY") {
		t.Errorf("expected explanation in stderr, got %q", stderr.String())
	}
}

func TestStdinTrustPrompt_TTY_AcceptsY(t *testing.T) {
	var stderr bytes.Buffer
	p := &stdinTrustPrompt{
		in:  strings.NewReader("y\n"),
		out: &stderr,
		tty: true,
	}
	ok := p.AskTrustHooks(hooksconfig.TrustRequest{ProjectPath: "/x", HookCount: 1, Names: []string{"a"}})
	if !ok {
		t.Errorf("y should accept")
	}
}

func TestStdinTrustPrompt_TTY_AcceptsYesAnyCase(t *testing.T) {
	var stderr bytes.Buffer
	for _, in := range []string{"y", "Y", "yes\n", "YES\n"} {
		p := &stdinTrustPrompt{
			in:  strings.NewReader(in + "\n"),
			out: &stderr,
			tty: true,
		}
		if !p.AskTrustHooks(hooksconfig.TrustRequest{ProjectPath: "/x", HookCount: 1}) {
			t.Errorf("input %q should accept", in)
		}
	}
}

func TestStdinTrustPrompt_TTY_RefusesOnBlank(t *testing.T) {
	var stderr bytes.Buffer
	p := &stdinTrustPrompt{
		in:  strings.NewReader("\n"),
		out: &stderr,
		tty: true,
	}
	if p.AskTrustHooks(hooksconfig.TrustRequest{ProjectPath: "/x", HookCount: 1}) {
		t.Errorf("blank input should refuse (default no)")
	}
}

func TestStdinTrustPrompt_TTY_RefusesOnN(t *testing.T) {
	var stderr bytes.Buffer
	p := &stdinTrustPrompt{
		in:  strings.NewReader("n\n"),
		out: &stderr,
		tty: true,
	}
	if p.AskTrustHooks(hooksconfig.TrustRequest{ProjectPath: "/x", HookCount: 1}) {
		t.Errorf("n should refuse")
	}
}

func TestStdinTrustPrompt_TTY_RefusesOnEOF(t *testing.T) {
	var stderr bytes.Buffer
	p := &stdinTrustPrompt{
		in:  strings.NewReader(""), // EOF
		out: &stderr,
		tty: true,
	}
	if p.AskTrustHooks(hooksconfig.TrustRequest{ProjectPath: "/x", HookCount: 1}) {
		t.Errorf("EOF should refuse")
	}
}
