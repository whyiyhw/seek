// Package checkpointcli implements the `seek checkpoint ...`,
// `seek undo`, and `seek redo` CLI surfaces (PRD docs/prd/
// feature-checkpoint.md §4.1). Shared between cmd/seek (direct
// invocation) and internal/tui (slash-command dispatch) by the
// same pattern as skillcli / memorycli.
//
// Subcommands operate on a single session's checkpoint state.
// Resolution order for the session id:
//
//  1. --session <id> explicit flag
//  2. -continue / most-recently-updated session (the same default
//     `seek` itself uses)
//
// We deliberately do NOT touch the live session that may be open
// in another seek process. The user is on their own there: if they
// run `seek checkpoint restore` while a TUI is mid-prompt, the TUI
// will see the working tree change but no internal state coupling
// breaks (the TUI consults disk on next read).
package checkpointcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/checkpoint"
	"github.com/whyiyhw/seek/internal/session"
)

// Run is the public entry. Mirrors skillcli.Run.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		return cmdList(rest, stdout, stderr)
	case "restore":
		return cmdRestore(rest, stdout, stderr)
	case "prune":
		return cmdPrune(rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	}
	return fmt.Errorf("unknown checkpoint subcommand %q (try `seek checkpoint help`)", verb)
}

// RunUndo handles `seek undo`. Lives here (rather than a sibling
// undocli package) because it shares 90% of its plumbing —
// resolveSession, manager construction, error formatting.
func RunUndo(args []string, stdout, stderr io.Writer) error {
	return cmdUndo(args, stdout, stderr)
}

// RunRedo handles `seek redo`. Mirror of RunUndo.
func RunRedo(args []string, stdout, stderr io.Writer) error {
	return cmdRedo(args, stdout, stderr)
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `seek checkpoint — inspect / restore / prune the safety-net layer

Usage:
  seek checkpoint <command> [flags] [args]

Commands:
  list                   List git checkpoints for a session (default: most recent)
  restore <turn>         Restore the working tree to the named checkpoint
  prune --before <date>  Delete checkpoint refs older than a date (RFC3339 or YYYY-MM-DD)

Shared flags:
  --session <id>         Operate on a specific session (default: most-recently-updated)
  --json                 (list only) emit JSONL on stdout

See also:
  seek undo / seek redo  File-level undo/redo (per-write granularity)`)
}

// ----- shared helpers -----

// resolveSession applies the --session flag's resolution policy and
// returns (loaded session, manager). The manager has nil Sink — CLI
// callers see warnings as plain stderr writes done by the caller.
func resolveSession(sessionFlag string, stderr io.Writer) (*session.Session, *checkpoint.Manager, error) {
	store, err := session.NewStore()
	if err != nil {
		return nil, nil, err
	}
	var s *session.Session
	if sessionFlag != "" {
		s, err = store.Load(sessionFlag)
	} else {
		s, err = store.Latest()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("resolve session: %w", err)
	}
	if s == nil {
		return nil, nil, fmt.Errorf("no session to operate on — pass --session <id> or run `seek -list`")
	}
	abs := s.CWD
	if abs == "" {
		abs, _ = os.Getwd()
	}
	abs, _ = filepath.Abs(abs)

	m := checkpoint.New(checkpoint.Config{
		SessionID:  s.ID,
		ProjectAbs: abs,
		CWD:        abs,
		Sink:       stderrSink{w: stderr},
		KeepOnExit: true, // CLI never cleans up — that's the agent's job
	})
	return s, m, nil
}

type stderrSink struct{ w io.Writer }

func (s stderrSink) Warn(m string) { fmt.Fprintln(s.w, m) }

// parseBefore accepts RFC3339 or YYYY-MM-DD (UTC midnight).
func parseBefore(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("--before is required (RFC3339 or YYYY-MM-DD)")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("could not parse --before=%q (try YYYY-MM-DD or RFC3339)", s)
}

// ----- list -----

func cmdList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("checkpoint list", flag.ContinueOnError)
	sess := fs.String("session", "", "session id (default: most-recently-updated)")
	asJSON := fs.Bool("json", false, "emit JSONL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	s, m, err := resolveSession(*sess, stderr)
	if err != nil {
		return err
	}
	list, err := m.ListGitCheckpoints()
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		for _, c := range list {
			if err := enc.Encode(c); err != nil {
				return err
			}
		}
		return nil
	}
	if len(list) == 0 {
		fmt.Fprintf(stdout, "no git checkpoints for session %s\n", s.ID)
		return nil
	}
	// Sort by turn ascending for stable display.
	sort.Slice(list, func(i, j int) bool { return list[i].Turn < list[j].Turn })
	fmt.Fprintf(stdout, "session %s\n", s.ID)
	fmt.Fprintf(stdout, "%-5s %-20s %-12s %s\n", "TURN", "TIMESTAMP", "BRANCH", "LABEL")
	for _, c := range list {
		fmt.Fprintf(stdout, "%-5d %-20s %-12s %s\n",
			c.Turn,
			c.TS.Local().Format("2006-01-02 15:04:05"),
			truncate(c.Branch, 12),
			truncate(c.Label, 64))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// ----- restore -----

func cmdRestore(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("checkpoint restore", flag.ContinueOnError)
	sess := fs.String("session", "", "session id")
	dryRun := fs.Bool("dry-run", false, "list affected files without modifying the working tree")
	force := fs.Bool("force", false, "overwrite a dirty working tree")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: seek checkpoint restore <turn|last>")
	}
	turn, err := parseTurnArg(rest[0])
	if err != nil {
		return err
	}
	_, m, err := resolveSession(*sess, stderr)
	if err != nil {
		return err
	}
	res, err := m.RestoreGit(context.Background(), checkpoint.RestoreOptions{
		Turn:   turn,
		DryRun: *dryRun,
		Force:  *force,
	})
	if err != nil {
		return err
	}
	if res.DryRun {
		fmt.Fprintf(stdout, "would restore turn %d (commit %s); %d affected file(s):\n",
			res.Checkpoint.Turn, res.Checkpoint.Commit, len(res.AffectedFiles))
		for _, f := range res.AffectedFiles {
			fmt.Fprintln(stdout, "  ", f)
		}
		return nil
	}
	fmt.Fprintf(stdout, "restored turn %d: %d file(s) reset; HEAD unchanged at %s\n",
		res.Checkpoint.Turn, len(res.AffectedFiles), res.Checkpoint.HeadBefore)
	return nil
}

// parseTurnArg accepts "last" / "latest" → 0 (sentinel for
// RestoreGit) or a positive integer.
func parseTurnArg(s string) (int, error) {
	switch s {
	case "last", "latest", "-1":
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid turn %q: pass a positive integer or `last`", s)
	}
	return n, nil
}

// ----- prune -----

func cmdPrune(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("checkpoint prune", flag.ContinueOnError)
	sess := fs.String("session", "", "session id")
	before := fs.String("before", "", "delete checkpoints older than this date")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t, err := parseBefore(*before)
	if err != nil {
		return err
	}
	_, m, err := resolveSession(*sess, stderr)
	if err != nil {
		return err
	}
	n, err := m.PruneGit(context.Background(), t)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "pruned %d checkpoint(s) before %s\n", n, t.Format(time.RFC3339))
	return nil
}

// ----- undo -----

func cmdUndo(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	sess := fs.String("session", "", "session id")
	count := fs.Int("n", 1, "number of undo steps")
	force := fs.Bool("force", false, "skip external-modification check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	path := ""
	if len(rest) > 0 {
		path = rest[0]
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	_, m, err := resolveSession(*sess, stderr)
	if err != nil {
		return err
	}
	results, err := m.Undo(checkpoint.UndoOptions{Path: path, Count: *count, Force: *force})
	if len(results) > 0 {
		for _, r := range results {
			fmt.Fprintf(stdout, "undone: %s\n", r.Restored)
		}
	}
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintln(stdout, "(nothing to undo)")
	}
	return nil
}

// ----- redo -----

func cmdRedo(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("redo", flag.ContinueOnError)
	sess := fs.String("session", "", "session id")
	count := fs.Int("n", 1, "number of redo steps")
	force := fs.Bool("force", false, "skip external-modification check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	path := ""
	if len(rest) > 0 {
		path = rest[0]
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	_, m, err := resolveSession(*sess, stderr)
	if err != nil {
		return err
	}
	results, err := m.Redo(checkpoint.RedoOptions{Path: path, Count: *count, Force: *force})
	if len(results) > 0 {
		for _, r := range results {
			fmt.Fprintf(stdout, "redone: %s\n", r.Restored)
		}
	}
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintln(stdout, "(nothing to redo)")
	}
	return nil
}

// describeAt allows future help renderers to peek at a single
// checkpoint by turn — currently exported only for tests.
func describeAt(list []checkpoint.GitCheckpoint, turn int) string {
	for _, c := range list {
		if c.Turn == turn {
			return strings.Join([]string{
				fmt.Sprintf("turn %d", c.Turn),
				c.TS.Local().Format(time.RFC3339),
				c.Label,
			}, " · ")
		}
	}
	return ""
}
