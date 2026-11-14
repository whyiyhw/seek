// Package sessioncli implements the `seek session ...` CLI
// surface. Dispatched ahead of flag.Parse in cmd/seek by the same
// pattern as skillcli / memorycli / checkpointcli: session queries
// need neither API keys nor the agent runtime.
//
// One verb today: `export` — render a persisted session's JSONL
// transcript as human-readable markdown. The rendering itself
// lives in internal/sessionexport (pure, reusable); this package
// only resolves which session, and where the bytes go. The serve
// web panel can later call sessionexport directly for a download
// button, bypassing this CLI layer entirely.
package sessioncli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/sessionexport"
)

// Run is the public entry. Mirrors skillcli.Run.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "export":
		return cmdExport(rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	}
	return fmt.Errorf("unknown session subcommand %q (try `seek session help`)", verb)
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `seek session — session-level utilities

Usage:
  seek session export [id] [-o <file>]   Render a session transcript as markdown

Commands:
  export   Transcript JSONL → human-readable markdown: role sections, tool
           calls folded to summary lines, session timestamps. <id> defaults
           to the most-recently-updated session (see seek -list); prints to
           stdout, or writes a file with -o. The source session file is
           never modified.

Flags:
  -o <file>   Write the markdown to this file instead of stdout`)
}

// cmdExport resolves the target session (positional id, else the
// most-recently-updated), renders it read-only, and emits the
// markdown to stdout or the -o file. Loading goes through
// Store.Load / Store.Latest — both are read-only, so the source
// .jsonl is untouched (tested).
func cmdExport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("session export", flag.ContinueOnError)
	out := fs.String("o", "", "write markdown to this file instead of stdout")
	fs.SetOutput(stderr)
	if err := fs.Parse(flagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: seek session export [id] [-o <file>]")
	}
	id := ""
	if fs.NArg() == 1 {
		id = fs.Arg(0)
	}

	store, err := session.NewStore()
	if err != nil {
		return fmt.Errorf("session store: %w", err)
	}
	var s *session.Session
	if id != "" {
		if s, err = store.Load(id); err != nil {
			return fmt.Errorf("export: %w (see `seek -list`)", err)
		}
	} else {
		if s, err = store.Latest(); err != nil {
			return err
		}
		if s == nil {
			return fmt.Errorf("no sessions in %s — pass an id (see `seek -list`)", store.Dir())
		}
	}

	md := sessionexport.Render(s)
	if *out == "" {
		if _, err := io.WriteString(stdout, md); err != nil {
			return fmt.Errorf("export: write stdout: %w", err)
		}
		return nil
	}
	if dir := filepath.Dir(*out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("export: mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(*out, []byte(md), 0o644); err != nil {
		return fmt.Errorf("export: write %s: %w", *out, err)
	}
	fmt.Fprintf(stderr, "exported session %s → %s (%d bytes)\n", s.ID, *out, len(md))
	return nil
}

// flagsFirst reorders args so flag tokens (with their values)
// precede positional arguments, then returns the recombined slice.
// flag.Parse stops at the first positional, which would otherwise
// reject the natural order `seek session export <id> -o <file>`;
// after the reorder both that and `-o <file> <id>` parse. Unknown
// flags pass through so flag.Parse itself reports them.
func flagsFirst(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-o" || a == "--o":
			flags = append(flags, a)
			if i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			} // bare trailing -o: let flag.Parse report the missing value
		case strings.HasPrefix(a, "-o=") || strings.HasPrefix(a, "--o="):
			flags = append(flags, a)
		case len(a) > 1 && a[0] == '-':
			flags = append(flags, a)
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}
