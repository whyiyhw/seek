package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/whyiyhw/seek/internal/hooksconfig"
)

// stdinTrustPrompt is the pre-TUI trust prompt for project hooks.toml.
// It runs BEFORE bubbletea starts, so we can drive it directly from
// stdin without coordinating with the TUI's askuser channel — at this
// point stdin still belongs to us.
//
// Why not the askuser policy: askuser is wired to bubbletea (its
// SetAskFn pushes onto a channel the TUI drains), and the TUI hasn't
// started yet. Plumbing a "second" pre-TUI askuser flow would be a
// lot of wiring for one trust dialog. A simple stdin prompt is also
// closer to the cli verb's stdin path.
//
// When stdin is not a TTY (piped input, -p, redirected from a file),
// AskTrustHooks returns false WITHOUT reading anything — the user
// gets a warning explaining that project hooks are disabled and they
// can approve via `seek hooks` interactively. PRD §3.5 says the
// trust prompt is the only thing standing between a freshly-cloned
// repo and `bash -c` — failing closed (refuse) is the correct default
// for non-interactive launches.
type stdinTrustPrompt struct {
	in  io.Reader
	out io.Writer
	// tty indicates whether we have a real terminal on stdin. When
	// false, the prompt refuses without consuming bytes.
	tty bool
}

func newStdinTrustPrompt() *stdinTrustPrompt {
	return &stdinTrustPrompt{
		in:  os.Stdin,
		out: os.Stderr,
		tty: isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd()),
	}
}

// AskTrustHooks implements hooksconfig.TrustPrompt. Prints a small
// summary of the request and reads y / n from stdin. Anything other
// than a leading y (case-insensitive) is treated as deny.
func (p *stdinTrustPrompt) AskTrustHooks(req hooksconfig.TrustRequest) bool {
	if !p.tty {
		fmt.Fprintf(p.out,
			"hooks: project at %s defines %d shell hook(s); skipping (no TTY for trust prompt).\n  Run `seek hooks list` interactively to approve, or `seek hooks trust --reset` to clear.\n",
			req.ProjectPath, req.HookCount)
		return false
	}
	fmt.Fprintln(p.out)
	fmt.Fprintf(p.out, "seek: project %s defines %d shell hook(s):\n", req.ProjectPath, req.HookCount)
	for i, name := range req.Names {
		event := ""
		if i < len(req.Events) {
			event = " (" + req.Events[i] + ")"
		}
		fmt.Fprintf(p.out, "  - %s%s\n", name, event)
	}
	fmt.Fprintf(p.out, "  file:    %s\n", req.Path)
	fmt.Fprintf(p.out, "  sha256:  %s\n", req.SHA256)
	fmt.Fprint(p.out, "\nThese hooks will run shell commands on tool-use events. Trust them? [y/N]: ")
	reader := bufio.NewReader(p.in)
	line, err := reader.ReadString('\n')
	if err != nil {
		// EOF without input → treat as no.
		fmt.Fprintln(p.out)
		return false
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	c := strings.ToLower(line)[0]
	return c == 'y'
}
