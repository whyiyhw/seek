package skillcli

// `seek skill drafts` — the drafts inbox (feature-skill-distill §6.2).
// Read-mostly by design: the human-review gate means install happens
// through the EXISTING `seek skill install <dir>` command, not through
// anything here. This subcommand only lists (with an install hint per
// row) and removes drafts the user has decided against.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/whyiyhw/seek/internal/skillmgr"
)

// draftsRow is the --json shape of one inbox row.
type draftsRow struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	Recurrence     int    `json:"recurrence"`
	SuggestedScope string `json:"suggested_scope"`
	CreatedAt      string `json:"created_at,omitempty"`
	Dir            string `json:"dir"`
}

func cmdSkillDrafts(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "rm" {
		return cmdSkillDraftsRm(args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("seek skill drafts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill drafts [--json]
       seek skill drafts rm <name>

Lists skill drafts distilled from sessions (~/.seek/skills/.drafts/).
Drafts are invisible to sessions; installing is a deliberate human
action: seek skill install <draft-dir> [--project]

Flags:`)
		fs.PrintDefaults()
	}
	jsonOut := fs.Bool("json", false, "machine-readable output (one JSON array)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store := &skillmgr.DraftStore{}
	drafts, err := store.ListDrafts()
	if err != nil {
		return err
	}

	if *jsonOut {
		rows := make([]draftsRow, 0, len(drafts))
		for _, d := range drafts {
			rows = append(rows, draftsRow{
				Name:           d.Name,
				Status:         d.Status,
				Recurrence:     d.Recurrence,
				SuggestedScope: d.SuggestedScope,
				CreatedAt:      d.CreatedAt,
				Dir:            d.Dir,
			})
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	if len(drafts) == 0 {
		fmt.Fprintln(stdout, "no skill drafts (drafts appear here after /distill extracts a reusable procedure)")
		return nil
	}
	for _, d := range drafts {
		age := ""
		if d.CreatedAt != "" {
			if ts, err := time.Parse(time.RFC3339, d.CreatedAt); err == nil {
				age = fmt.Sprintf("  %dd old", int(time.Since(ts).Hours()/24))
			}
		}
		fmt.Fprintf(stdout, "%s  [%s]  recurrence %d  scope %s%s\n", d.Name, d.Status, d.Recurrence, d.SuggestedScope, age)
		fmt.Fprintf(stdout, "    review & install: seek skill install %s [--project]\n", d.Dir)
	}
	return nil
}

func cmdSkillDraftsRm(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill drafts rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill drafts rm <name>

Removes one draft from ~/.seek/skills/.drafts/. This is the inbox's
"n" — use it for drafts you've decided against, or to clean up after
installing the real copy via seek skill install.`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("drafts rm: exactly one <name> is required")
	}
	store := &skillmgr.DraftStore{}
	dir, err := store.DeleteDraft(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed draft %s (%s)\n", fs.Arg(0), dir)
	return nil
}
