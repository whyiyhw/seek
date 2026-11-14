package skillcli

// `seek skill drafts` — the drafts inbox review flow (feature-skill-
// distill §6.2, card c_jvn3ky). One command surface, four verbs:
//
//	drafts                 list (name / source session / produced-at /
//	                       funnel verdict)
//	drafts show <name>     full SKILL.md + diff vs the nearest
//	                       installed skill (same trigram metric L2 uses)
//	drafts approve <name>  install through the EXISTING skillmgr.Install,
//	                       then delete the draft (a reviewed draft must
//	                       not re-enter the inbox)
//	drafts reject <name>   delete the draft (rm stays as an alias)
//
// The human-review gate is unchanged in kind: before `approve` runs,
// drafts live under ~/.seek/skills/.drafts/ where the loader never
// looks (dot-dir skip is the load-bearing wall, pinned by tests in
// skillmgr/draft_test.go and re-asserted end-to-end by the full-flow
// test in drafts_test.go). approve is the only verb here that moves a
// draft across that wall, and it does so by delegating to the same
// skillmgr.Install `seek skill install` uses — scope selection follows
// the existing convention: the sidecar's suggested_scope is the
// default, --project / --user override it.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/diff"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skilldistill"
	"github.com/whyiyhw/seek/internal/skillmgr"
)

// draftsRow is the --json shape of one inbox row.
type draftsRow struct {
	Name           string                      `json:"name"`
	Status         string                      `json:"status"`
	Recurrence     int                         `json:"recurrence"`
	SuggestedScope string                      `json:"suggested_scope"`
	CreatedAt      string                      `json:"created_at,omitempty"`
	Sources        []skillmgr.DraftSource      `json:"sources,omitempty"`
	FilterTrace    []skillmgr.FilterTraceEntry `json:"filter_trace,omitempty"`
	Dir            string                      `json:"dir"`
}

const draftsUsage = `Lists skill drafts distilled from sessions (~/.seek/skills/.drafts/).
Drafts are invisible to sessions until approved; approval installs via
the existing skillmgr install chain.

  seek skill drafts                  list the inbox
  seek skill drafts show <name>      full text + diff vs nearest skill
  seek skill drafts approve <name>   install (--project/--user picks scope)
  seek skill drafts reject <name>    delete the draft (rm is an alias)`

func cmdSkillDrafts(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "show":
			return cmdSkillDraftsShow(args[1:], stdout, stderr)
		case "approve":
			return cmdSkillDraftsApprove(args[1:], stdout, stderr)
		case "reject", "rm":
			return cmdSkillDraftsReject(args[1:], stdout, stderr)
		}
	}

	fs := flag.NewFlagSet("seek skill drafts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: seek skill drafts [--json]\n\n"+draftsUsage+"\n\nFlags:")
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
				Sources:        d.Sources,
				FilterTrace:    d.FilterTrace,
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
		fmt.Fprintf(stdout, "%s  [%s]  recurrence %d  scope %s  created %s  from %s  funnel %s\n",
			d.Name, d.Status, d.Recurrence, d.SuggestedScope,
			draftCreated(d.CreatedAt), draftSourceSessions(d.Sources), draftFunnel(d.FilterTrace))
		fmt.Fprintf(stdout, "    show: seek skill drafts show %s · approve: seek skill drafts approve %s · reject: seek skill drafts reject %s\n",
			d.Name, d.Name, d.Name)
	}
	return nil
}

// draftCreated renders the produced-at column: date-only from the
// RFC3339 sidecar value, "—" when absent (sidecar-less draft).
func draftCreated(at string) string {
	if ts, err := time.Parse(time.RFC3339, at); err == nil {
		return ts.Format("2006-01-02")
	}
	return "—"
}

// draftSourceSessions renders the source-session column: the first
// session id plus "+N" when several sessions contributed, "—" when
// the sidecar carries none.
func draftSourceSessions(sources []skillmgr.DraftSource) string {
	if len(sources) == 0 || sources[0].SessionID == "" {
		return "—"
	}
	out := "session " + sources[0].SessionID
	if n := len(sources) - 1; n > 0 {
		out += fmt.Sprintf(" (+%d)", n)
	}
	return out
}

// draftFunnel renders the funnel-score column: each recorded stage's
// verdict ("L2:GREY L3:ACCEPT"), "—" when the sidecar carries no trace.
func draftFunnel(trace []skillmgr.FilterTraceEntry) string {
	if len(trace) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(trace))
	for _, e := range trace {
		parts = append(parts, e.Stage+":"+e.Verdict)
	}
	return strings.Join(parts, " ")
}

// ---------- show ----------

func cmdSkillDraftsShow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill drafts show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill drafts show <name>

Prints the draft's provenance (source session, funnel verdicts), the
full SKILL.md, and a diff against the nearest installed skill — the
same trigram-similarity metric the L2 dedup layer uses.`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("drafts show: exactly one <name> is required")
	}

	store := &skillmgr.DraftStore{}
	detail, err := store.ReadDraft(fs.Arg(0))
	if err != nil {
		return err
	}
	d := detail.Info

	fmt.Fprintf(stdout, "%s  [%s]  recurrence %d  suggested scope %s\n", d.Name, d.Status, d.Recurrence, d.SuggestedScope)
	fmt.Fprintf(stdout, "  from %s  created %s  funnel %s\n",
		draftSourceSessions(d.Sources), draftCreated(d.CreatedAt), draftFunnel(d.FilterTrace))
	fmt.Fprintln(stdout, "\n--- SKILL.md "+strings.Repeat("-", 60))
	for line := range strings.SplitSeq(strings.TrimRight(string(detail.MD), "\n"), "\n") {
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintln(stdout, "--- end SKILL.md "+strings.Repeat("-", 55))

	// Nearest installed skill: parse the draft, load what a session
	// would see, and rank by the L2 metric. Builtins count too — they
	// are skills the draft would be duplicating.
	parsed, err := skill.Parse(detail.MD, "draft")
	if err != nil {
		// ReadDraft skims the dir; a draft whose SKILL.md went bad
		// since landing still deserves the listing + error, not silence.
		return fmt.Errorf("drafts show: draft SKILL.md no longer parses: %w", err)
	}
	set, _, err := skill.Load(skill.LoadOptions{})
	if err != nil {
		return fmt.Errorf("drafts show: load installed skills: %w", err)
	}
	nearest, sim := skilldistill.NearestSkill(parsed.Description, parsed.Body, set)
	// Below the funnel's grey threshold a diff is noise, not signal —
	// the panel exists to surface near-duplicates worth eyeballing.
	if nearest == "" || sim < skilldistill.SimilarityGrey {
		if nearest == "" {
			fmt.Fprintln(stdout, "\nnearest installed skill: none (no meaningful overlap)")
		} else {
			fmt.Fprintf(stdout, "\nnearest installed skill: none (best %s at %.2f, below the %.2f grey-zone threshold)\n",
				nearest, sim, skilldistill.SimilarityGrey)
		}
		return nil
	}
	sk := set.Get(nearest)
	fmt.Fprintf(stdout, "\nnearest installed skill: %s (similarity %.2f)\n", nearest, sim)
	old := sk.Description
	if sk.Body != "" {
		old += "\n\n" + sk.Body
	}
	fmt.Fprintln(stdout, diff.Unified(old, parsed.Description+"\n\n"+parsed.Body, nearest+" vs draft "+d.Name))
	return nil
}

// ---------- approve ----------

func cmdSkillDraftsApprove(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill drafts approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill drafts approve <name> [--project|--user] [--force]

Installs the draft through the existing skillmgr install chain (same
path as `+"`seek skill install <dir>`"+`), then removes it from the
drafts inbox. Scope defaults to the sidecar's suggested_scope;
--project / --user override it. Restart the session afterwards for
the manifest to pick the skill up.`)
		fs.PrintDefaults()
	}
	project := fs.Bool("project", false, "install into <cwd>/.seek/skills/ (overrides suggested scope)")
	user := fs.Bool("user", false, "install into ~/.seek/skills/ (overrides suggested scope)")
	force := fs.Bool("force", false, "replace an installed skill of the same name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("drafts approve: exactly one <name> is required")
	}
	if *project && *user {
		return fmt.Errorf("drafts approve: --project and --user are mutually exclusive")
	}
	name := fs.Arg(0)

	store := &skillmgr.DraftStore{}
	detail, err := store.ReadDraft(name)
	if err != nil {
		return err
	}

	// Scope: sidecar suggestion is the default (PRD §5.4 — the draft
	// never decides, it suggests), flags are the human's explicit say.
	scope := detail.Info.SuggestedScope
	scopeOrigin := "sidecar suggestion"
	if scope != "user" {
		scope = "project" // guards against garbage in a hand-edited sidecar
	}
	if *project {
		scope, scopeOrigin = "project", "--project"
	}
	if *user {
		scope, scopeOrigin = "user", "--user"
	}

	res, err := skillmgr.Install(skillmgr.InstallOptions{
		Source:  detail.Info.Dir,
		Type:    skillmgr.SourceLocal,
		Force:   *force,
		Project: scope == "project",
	})
	if err != nil {
		// The draft MUST survive a failed install — deleting it here
		// would turn a name collision into data loss.
		return fmt.Errorf("drafts approve: %w (draft kept in the inbox)", err)
	}
	if _, err := store.DeleteDraft(name); err != nil {
		return fmt.Errorf("drafts approve: installed to %s but deleting the draft failed: %w", res.Dir, err)
	}
	fmt.Fprintf(stdout, "approved %s: installed -> %s (scope: %s, %s)\n", res.Name, res.Dir, scope, scopeOrigin)
	fmt.Fprintln(stdout, "restart the session for the manifest to pick it up")
	return nil
}

// ---------- reject / rm ----------

func cmdSkillDraftsReject(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill drafts reject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill drafts reject <name>

Removes one draft from ~/.seek/skills/.drafts/ — the inbox's "n".
`+"`rm`"+` is an alias. Use it for drafts you've decided against, or to
clean up after approving (approve already removes the draft itself).`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("drafts reject: exactly one <name> is required")
	}
	store := &skillmgr.DraftStore{}
	dir, err := store.DeleteDraft(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "rejected draft %s (deleted %s)\n", fs.Arg(0), dir)
	return nil
}
