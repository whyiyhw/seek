// Package skillcli is the `seek skill ...` command dispatcher
// (PRD v2 §5.1). It's shared between two consumers:
//
//   - cmd/seek for the `seek skill ...` CLI invocation
//   - internal/tui for the `/skill ...` slash command
//
// Both call Run(args, stdout, stderr); the TUI buffers the writers
// so the output renders as scrollback text. Keeping the dispatcher
// in one place is what makes the PRD §5.2 promise ("TUI mirrors
// CLI, same flags + output") cheap to maintain.
//
// Each subcommand owns its own flag.FlagSet (instead of leaning on
// the global flag package) — `seek skill install --force` mustn't
// share flag namespace with the top-level binary's --force etc.,
// and FlagSet's per-call usage strings give cleaner help.
package skillcli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/whyiyhw/seek/internal/skillmgr"
)

// Run is the public entry point. stdout / stderr are injected so the
// TUI can swap real terminal handles for in-memory buffers.
func Run(args []string, stdout, stderr io.Writer) error {
	return runSkillCmd(args, stdout, stderr)
}

// runSkillCmd dispatches `seek skill <verb> [args...]`. Unexported so
// callers go through Run, which keeps the entry-point surface clean.
func runSkillCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printSkillHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "install":
		return cmdSkillInstall(rest, stdout, stderr)
	case "uninstall", "remove", "rm":
		return cmdSkillUninstall(rest, stdout, stderr)
	case "update":
		return cmdSkillUpdate(rest, stdout, stderr)
	case "list", "ls":
		return cmdSkillList(rest, stdout, stderr)
	case "status":
		return cmdSkillStatus(rest, stdout, stderr)
	case "stats":
		return cmdSkillStats(rest, stdout, stderr)
	case "create", "new":
		return cmdSkillCreate(rest, stdout, stderr)
	case "help", "--help", "-h":
		printSkillHelp(stdout)
		return nil
	}
	return fmt.Errorf("unknown skill subcommand %q (try `seek skill help`)", verb)
}

// printSkillHelp is a one-stop overview. Subcommand-specific help
// comes from each FlagSet.Usage when the user runs `seek skill <verb>
// --help`.
func printSkillHelp(w io.Writer) {
	fmt.Fprintln(w, `seek skill — manage installed skill packages

Usage:
  seek skill <command> [flags] [args]

Commands:
  install <source>       Install a skill from a local path, git URL, or HTTPS archive
  uninstall <name>       Remove an installed skill
  update <name>          Re-fetch an installed skill from its recorded source
  update --all           Re-fetch every managed skill
  list                   Show every loaded skill (source, type, call count, last used)
  status <name>          Detailed view of one skill: source path, install record, shadowing, stats
  stats                  Top-N skills by call count (default --top 10 --since 30d)
  create <name>          Scaffold a new skill package (SKILL.md + references/ + README.md)

Run 'seek skill <command> --help' for command-specific flags.`)
}

// ---------- install ----------

func cmdSkillInstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill install <source> [flags]

Sources:
  ./path/to/dir          Local directory containing SKILL.md
  /abs/path              Absolute local path
  https://host/x.tar.gz  HTTPS archive (.tar.gz / .tgz / .tar / .zip)
  https://host/repo      Git URL (use #ref for a specific tag/branch)
  git@host:user/repo     SSH git URL

Flags:`)
		fs.PrintDefaults()
	}
	var (
		name    = fs.String("name", "", "override the skill name (default: SKILL.md frontmatter or directory basename)")
		force   = fs.Bool("force", false, "replace an existing skill of the same name")
		project = fs.Bool("project", false, "install into <cwd>/.seek/skills/ instead of ~/.seek/skills/")
		subpath = fs.String("subpath", "", "for git URLs: subdirectory inside the repo that holds the skill")
		sha256  = fs.String("sha256", "", "for HTTPS archives: expected sha256 of the downloaded archive")
		typFlag = fs.String("type", "", "force source type: local|git|https (default: infer from <source>)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("install: exactly one <source> is required")
	}

	opts := skillmgr.InstallOptions{
		Source:  fs.Arg(0),
		Name:    *name,
		Force:   *force,
		Project: *project,
		Subpath: *subpath,
		SHA256:  *sha256,
	}
	switch strings.ToLower(*typFlag) {
	case "":
		opts.Type = skillmgr.SourceAuto
	case "local":
		opts.Type = skillmgr.SourceLocal
	case "git":
		opts.Type = skillmgr.SourceGit
	case "https":
		opts.Type = skillmgr.SourceHTTPS
	default:
		return fmt.Errorf("--type must be local, git, or https (got %q)", *typFlag)
	}

	res, err := skillmgr.Install(opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s -> %s\n", res.Name, res.Dir)
	return nil
}

// ---------- uninstall ----------

func cmdSkillUninstall(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill uninstall <name>

Flags:`)
		fs.PrintDefaults()
	}
	// --keep-stats is a stub for now — the stats reader/purger
	// lands in M8.4b; today the flag is accepted but a no-op so
	// scripts using it don't break later.
	_ = fs.Bool("keep-stats", true, "keep this skill's rows in .stats.jsonl (default true; --keep-stats=false purges)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("uninstall: exactly one <name> is required")
	}
	res, err := skillmgr.Uninstall(skillmgr.UninstallOptions{Name: fs.Arg(0)})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "uninstalled %s (removed %s)\n", res.Name, res.Path)
	return nil
}

// ---------- update ----------

func cmdSkillUpdate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage:
  seek skill update <name>
  seek skill update --all

Flags:`)
		fs.PrintDefaults()
	}
	all := fs.Bool("all", false, "update every installed skill that has a recorded source")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *all {
		if fs.NArg() > 0 {
			return fmt.Errorf("update: --all conflicts with a positional <name>")
		}
		results, err := skillmgr.UpdateAll(skillmgr.UpdateOptions{})
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Fprintln(stdout, "no managed skills to update")
			return nil
		}
		var failures int
		for _, r := range results {
			if r.Err != nil {
				fmt.Fprintf(stderr, "  %s: %v\n", r.Name, r.Err)
				failures++
				continue
			}
			fmt.Fprintf(stdout, "  %s -> %s\n", r.Name, r.Path)
		}
		if failures > 0 {
			return fmt.Errorf("update --all: %d of %d skills failed", failures, len(results))
		}
		return nil
	}

	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("update: exactly one <name> is required (or use --all)")
	}
	res, err := skillmgr.Update(skillmgr.UpdateOptions{Name: fs.Arg(0)})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "updated %s -> %s\n", res.Name, res.Path)
	return nil
}
