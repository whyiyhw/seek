// Package memorycli is the `seek memory ...` command dispatcher.
// It mirrors the skillcli pattern (PRD v2 §5.1): shared between two
// consumers — cmd/seek for the CLI and internal/tui for the `/memory`
// slash command.
//
// Each subcommand owns its own flag.FlagSet.
package memorycli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
)

// Run is the public entry point. stdout / stderr are injected so the
// TUI can swap real terminal handles for in-memory buffers.
func Run(args []string, stdout, stderr io.Writer) error {
	return runMemoryCmd(args, stdout, stderr)
}

func runMemoryCmd(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		return cmdList(rest, stdout, stderr)
	case "show", "get":
		return cmdShow(rest, stdout, stderr)
	case "search", "find":
		return cmdSearch(rest, stdout, stderr)
	case "archive", "forget":
		return cmdArchive(rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	default:
		fmt.Fprintf(stderr, "memory: unknown verb %q\n", verb)
		printHelp(stdout)
		return fmt.Errorf("unknown verb %q", verb)
	}
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: seek memory <verb> [args]

Verbs:
  list                List active (non-stale) entries with score
  show    <name>      Show full content of one entry
  search  <query>     Search entries by tagline or tags
  archive <name>      Archive an entry (move to archived.jsonl)
  help                Show this help

`)
}

// loadCurrentProject loads the memory Project for the working directory.
// Returns nil without error when project memory isn't available (e.g.
// ~/.seek is missing) — callers check the nil and produce a clear message.
func loadCurrentProject() (*memory.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func cmdList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "include stale entries")
	if err := fs.Parse(args); err != nil {
		return err
	}

	p, err := loadCurrentProject()
	if err != nil {
		fmt.Fprintln(stderr, "memory:", err)
		return nil
	}

	entries := p.Entries()
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no memory entries")
		return nil
	}

	now := time.Now().UTC()
	halfLife := memory.HalfLifeFromEnv()

	type displayEntry struct {
		e     memory.Entry
		score float64
	}

	var displayed []displayEntry
	for _, e := range entries {
		if e.Stale && !*all {
			continue
		}
		displayed = append(displayed, displayEntry{e: e, score: memory.Score(e, now, halfLife)})
	}

	if len(displayed) == 0 {
		fmt.Fprintln(stdout, "no active memory entries (use --all to include stale)")
		return nil
	}

	// Sort by name for deterministic output.
	sort.Slice(displayed, func(i, j int) bool {
		return displayed[i].e.Name < displayed[j].e.Name
	})

	for _, d := range displayed {
		state := ""
		if d.e.Stale {
			state = " [stale]"
		}
		fmt.Fprintf(stdout, "%-30s  score=%.2f%s\n  %s\n", d.e.Name, d.score, state, d.e.Tagline)
	}
	return nil
}

func cmdShow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("memory show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		fmt.Fprintln(stderr, "usage: seek memory show <name>")
		return nil
	}

	p, err := loadCurrentProject()
	if err != nil {
		fmt.Fprintln(stderr, "memory:", err)
		return nil
	}

	e, ok := p.Get(name)
	if !ok {
		fmt.Fprintf(stderr, "memory: entry %q not found\n", name)
		return nil
	}

	fmt.Fprintf(stdout, "Name:      %s\n", e.Name)
	fmt.Fprintf(stdout, "Tagline:   %s\n", e.Tagline)
	if len(e.Tags) > 0 {
		fmt.Fprintf(stdout, "Tags:      %s\n", strings.Join(e.Tags, ", "))
	}
	fmt.Fprintf(stdout, "Created:   %s\n", e.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(stdout, "Updated:   %s\n", e.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(stdout, "Recalled:  %d times (last: %s)\n", e.RecallCount, e.LastRecalledAt.Format(time.RFC3339))
	if e.Stale {
		fmt.Fprintf(stdout, "Stale:     true (since %s)\n", e.StaleSince.Format(time.RFC3339))
	}
	if e.Pinned {
		fmt.Fprintf(stdout, "Pinned:    true\n")
	}
	if e.AutoSourced {
		fmt.Fprintf(stdout, "Auto:      true\n")
	}
	fmt.Fprintf(stdout, "\n%s\n", e.Content)
	return nil
}

func cmdSearch(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("memory search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		fmt.Fprintln(stderr, "usage: seek memory search <query>")
		return nil
	}

	p, err := loadCurrentProject()
	if err != nil {
		fmt.Fprintln(stderr, "memory:", err)
		return nil
	}

	q := strings.ToLower(query)
	var matched []memory.Entry
	for _, e := range p.Entries() {
		if strings.Contains(strings.ToLower(e.Tagline), q) {
			matched = append(matched, e)
			continue
		}
		for _, tag := range e.Tags {
			if strings.Contains(strings.ToLower(tag), q) {
				matched = append(matched, e)
				break
			}
		}
	}

	if len(matched) == 0 {
		fmt.Fprintf(stdout, "no entries matching %q\n", query)
		return nil
	}

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Name < matched[j].Name
	})
	for _, e := range matched {
		stale := ""
		if e.Stale {
			stale = " [stale]"
		}
		fmt.Fprintf(stdout, "%-30s%s\n  %s\n", e.Name, stale, e.Tagline)
	}
	return nil
}

func cmdArchive(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("memory archive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reason := fs.String("reason", "manual archive", "archive reason")
	if err := fs.Parse(args); err != nil {
		return err
	}

	name := strings.TrimSpace(fs.Arg(0))
	if name == "" {
		fmt.Fprintln(stderr, "usage: seek memory archive [-reason 'why'] <name>")
		return nil
	}

	p, err := loadCurrentProject()
	if err != nil {
		fmt.Fprintln(stderr, "memory:", err)
		return nil
	}

	e, ok := p.Get(name)
	if !ok {
		fmt.Fprintf(stderr, "memory: entry %q not found\n", name)
		return nil
	}

	if err := p.Archive(name, *reason); err != nil {
		fmt.Fprintf(stderr, "memory: archive %q: %v\n", name, err)
		return nil
	}

	fmt.Fprintf(stdout, "archived %q (reason: %s)\n", name, *reason)
	_ = e
	return nil
}

// ListArchived reads archived.jsonl for the current project and prints
// each entry. Used by `seek memory list --archived`.
func ListArchived(w io.Writer, p *memory.Project) error {
	archived, err := p.LoadArchived()
	if err != nil {
		return err
	}
	if len(archived) == 0 {
		fmt.Fprintln(w, "no archived entries")
		return nil
	}
	for _, e := range archived {
		fmt.Fprintf(w, "  %-30s  %s\n", e.Name, e.Tagline)
	}
	return nil
}
