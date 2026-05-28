// Package worktreecli implements the `seek worktree ...` CLI
// surface (PRD docs/prd/feature-subagent.md §3.8 + §9 v0.6.x
// dot). Mirrors the structure of internal/checkpointcli /
// internal/hookscli / internal/skillcli: a Run(args, stdout,
// stderr) entry point that dispatches verbs to per-subcommand
// handlers.
//
// Subcommands:
//
//   - list:  enumerate every seek-managed worktree on disk via
//            git worktree list --porcelain (sees worktrees from
//            prior sessions too, not just current process).
//   - gc:    prune refs/seek/discarded/<ts> rescue-stash refs
//            past --older-than (default 48h). The same routine
//            that runs at seek startup; CLI exposes it for users
//            who want explicit "I'm cleaning up now".
//   - help / --help / -h: print usage.
//
// The handlers construct a fresh worktree.Manager rooted at
// os.Getwd() — no session id, no project state to load. This
// keeps `seek worktree gc` cheap to invoke from a CI cleanup
// step or a cron job without spinning up the full seek runtime.
package worktreecli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/whyiyhw/seek/internal/worktree"
)

// Run is the public entry. Mirrors checkpointcli.Run.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		return cmdList(rest, stdout, stderr)
	case "gc":
		return cmdGC(rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	}
	return fmt.Errorf("unknown worktree subcommand %q (try `seek worktree help`)", verb)
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `seek worktree — manage seek-owned git worktrees

Usage:
  seek worktree list                        list seek-managed worktrees on disk
  seek worktree gc [--older-than DURATION]  prune rescue-stash refs (default 48h;
                                            --older-than 0 prunes everything)
  seek worktree help                        show this help

These worktrees are created by the `+"`agent`"+` tool with isolation="worktree"
or by direct enter_worktree calls. They live under
~/.seek/projects/<id>/worktrees/<wt-id>/ and branch under
refs/seek/worktrees/<wt-id> (out of git's default refspec so push
won't surface them).`)
}

// cmdList enumerates every seek-managed worktree git is currently
// tracking and prints a one-line summary per entry. NOT scoped to
// the current seek process — sees prior sessions too.
func cmdList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("worktree list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("worktree list: getwd: %w", err)
	}
	mgr, err := worktree.NewManager(cwd)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wts, err := mgr.ListFromDisk(ctx)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	if len(wts) == 0 {
		fmt.Fprintln(stdout, "no seek-managed worktrees in this project")
		return nil
	}
	// Sort by ID (which embeds timestamp) for stable
	// chronological order, newest-first.
	sort.Slice(wts, func(i, j int) bool { return wts[i].ID > wts[j].ID })

	fmt.Fprintf(stdout, "%-22s  %-30s  %s\n", "WT-ID", "BRANCH", "PATH")
	for _, w := range wts {
		fmt.Fprintf(stdout, "%-22s  %-30s  %s\n", w.ID, w.Branch, w.Path)
	}
	return nil
}

// cmdGC prunes refs/seek/discarded/<ts> refs older than the
// supplied threshold. Mirrors what runs at seek startup —
// exposing it as a CLI lets users force-prune from a script or
// shell session without launching the TUI.
func cmdGC(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("worktree gc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	olderThan := fs.Duration("older-than", 48*time.Hour,
		`prune refs older than this (e.g. 48h, 7d=168h, 0 = all)`)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("worktree gc: getwd: %w", err)
	}
	mgr, err := worktree.NewManager(cwd)
	if err != nil {
		return fmt.Errorf("worktree gc: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := mgr.PruneDiscarded(ctx, *olderThan)
	if err != nil {
		return fmt.Errorf("worktree gc: %w", err)
	}
	if n == 0 {
		fmt.Fprintln(stdout, "no discarded refs to prune")
	} else if n == 1 {
		fmt.Fprintln(stdout, "pruned 1 discarded ref")
	} else {
		fmt.Fprintf(stdout, "pruned %d discarded refs\n", n)
	}
	return nil
}
