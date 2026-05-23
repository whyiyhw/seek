package skillcli

// `seek skill create` — scaffold a new skill package. Lives in its
// own file because the templating + non-interactive guard is
// substantial and doesn't share much with the install/uninstall/
// update wrappers.

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/whyiyhw/seek/internal/skillmgr"
)

func cmdSkillCreate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill create <name> --description "<one-line trigger summary>" [flags]

Scaffolds a new skill package alongside the conventional layout:

  <target>/<name>/
  ├── SKILL.md       Entry point (frontmatter + TODO body anchors)
  ├── references/    Empty (with .gitkeep) — drop long-form docs here
  └── README.md      Human-facing intro

Target resolution (priority order):
  --into <dir>       Explicit parent directory
  --project          <cwd>/.seek/skills/
  (neither)          ~/.seek/skills/

PRD §7 #14: target must not exist. There is no --force; use
'seek skill install --force' to overwrite an existing skill.

Flags:`)
		fs.PrintDefaults()
	}
	var (
		description = fs.String("description", "", "single-line trigger condition shown in the system-prompt manifest (required — non-interactive build refuses without it)")
		project     = fs.Bool("project", false, "scaffold into <cwd>/.seek/skills/ (team-shared via git) instead of ~/.seek/skills/")
		into        = fs.String("into", "", "explicit parent directory; wins over --project")
	)
	// Peel <name> off the front so users can write the natural
	// `create my-skill --description "..."` order. Go's flag.Parse
	// stops at the first non-flag arg, so without this we'd require
	// the inverted `create --description "..." my-skill` ordering —
	// the install/uninstall commands tolerate it (positional last)
	// but for create the cargo-new / npm-init order is the muscle
	// memory we want to match.
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fs.Usage()
		return fmt.Errorf("create: <name> must be the first argument (e.g. `seek skill create my-skill --description ...`)")
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("create: unexpected extra positional argument(s): %v", fs.Args())
	}

	// PRD §7 #13 acceptance: refuse without --description. We don't
	// prompt interactively in v2 — the readline plumbing isn't worth
	// the test complexity for a flag the user can easily pass. The
	// help text above makes this requirement clear.
	if *description == "" {
		fs.Usage()
		return fmt.Errorf("create: --description is required (a skill without a description won't load)")
	}

	res, err := skillmgr.Create(skillmgr.CreateOptions{
		Name:        name,
		Description: *description,
		Into:        *into,
		Project:     *project,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "scaffolded %s -> %s\n", res.Name, res.Dir)
	fmt.Fprintln(stdout, "next: edit SKILL.md to fill the TODO sections, then `seek skill install` to test it elsewhere")
	return nil
}
