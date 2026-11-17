// Package agentscli implements the `seek agents ...` CLI surface
// (feature-orchestration.md §5, M-O2.3). Following the skillcli /
// hookscli pattern: stdout/stderr are injected so callers (tests, a
// future TUI mirror) can buffer output.
//
// Verbs:
//
//	seek agents list               — the type registry as session
//	                                 startup sees it (same Load wiring
//	                                 as cmd/seek: project > user >
//	                                 config > builtin)
//	seek agents check              — validate every definition file;
//	                                 non-zero exit on any load error
//	seek agents create <name>      — scaffold a definition (the write
//	                                 path — internal/agentsmgr, the
//	                                 only legal writer)
//	seek agents remove <name>      — delete a user-scope definition
//
// list/check are read-only and never touch the filesystem beyond
// reading; create/remove go exclusively through agentsmgr.
package agentscli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/whyiyhw/seek/internal/agentsmgr"
	"github.com/whyiyhw/seek/internal/agenttypes"
	"github.com/whyiyhw/seek/internal/config"
)

// Run dispatches a `seek agents ...` invocation. The first arg (if
// any) is the verb; the rest go to the verb's own flag set.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "list", "ls":
		return cmdList(rest, stdout, stderr)
	case "check":
		return cmdCheck(rest, stdout, stderr)
	case "create", "new":
		return cmdCreate(rest, stdout, stderr)
	case "remove", "rm":
		return cmdRemove(rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	default:
		fmt.Fprintf(stderr, "agents: unknown verb %q\n", verb)
		printHelp(stderr)
		return fmt.Errorf("unknown verb %q", verb)
	}
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: seek agents <verb> [args]

Verbs:
  list                        List the agent-type registry as startup loads it
                              (project > user > config > builtin, with shadowing).
  check                       Validate every agent definition file and the config
                              agents section; non-zero exit on any error.
  create <name> --description D   Scaffold a new agent type (user scope; --project
                              for <cwd>/.seek/agents/, the checked-in scope).
  remove <name>               Delete a user-scope agent type (project types: git).

Files:
  <project>/.seek/agents/<name>.md   Project-level, checked-in (wins on collision).
  ~/.seek/agents/<name>.md           User-level.

Definition files are plain Markdown (D11 frontmatter: name, description,
tools, provider, model, effort, workflow, max_turns + role-prompt body).
check covers load-time validation; tool-name and provider-lane legality
are spawn-time checks and are not reported here.

See docs/prd/feature-orchestration.md §5 for the full spec.
`)
}

// loadForCWD mirrors cmd/seek's startup wiring so `agents list` and
// `agents check` see EXACTLY what the next session would load: the
// cwd's project scope, the user scope, and the config `agents` routing
// overrides. A config that fails to load contributes no overrides —
// same posture as startup (agentCfg, _ := config.Load()).
func loadForCWD() (*agenttypes.Set, agenttypes.LoadStats, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, agenttypes.LoadStats{}, fmt.Errorf("getwd: %w", err)
	}
	var configAgents map[string]agenttypes.RouteOverride
	if cfg, err := config.Load(); err == nil && len(cfg.Agents) > 0 {
		configAgents = make(map[string]agenttypes.RouteOverride, len(cfg.Agents))
		for name, ac := range cfg.Agents {
			configAgents[name] = agenttypes.RouteOverride{
				Provider: ac.Provider,
				Model:    ac.Model,
				Effort:   ac.Effort,
				MaxTurns: ac.MaxTurns,
			}
		}
	}
	return agenttypes.Load(agenttypes.LoadOptions{
		ProjectDir:   cwd,
		ConfigAgents: configAgents,
	})
}

// ---------- list ----------

func cmdList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek agents list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: seek agents list")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("list: unexpected argument %q", fs.Arg(0))
	}

	set, stats, err := loadForCWD()
	if err != nil {
		return err
	}
	defs := set.List()
	fmt.Fprintf(stdout, "%d agent type(s) defined (project > user > config > builtin on name collision)\n", len(defs))
	fmt.Fprintf(stdout, "%-18s %-8s %-22s %s\n", "TYPE", "ORIGIN", "LANE", "DESCRIPTION")
	for _, d := range defs {
		fmt.Fprintf(stdout, "%-18s %-8s %-22s %s\n",
			truncate(d.Name, 18), truncate(d.Origin.String(), 8),
			truncate(laneLabel(d), 22), truncate(d.Description, 60))
	}
	// Shadowed names get a provenance footer (same hint as /agents
	// types) so an override is visible rather than a mystery.
	for _, d := range defs {
		if sh := set.Shadowed(d.Name); len(sh) > 0 {
			fmt.Fprintf(stdout, "%s overrides: %s\n", d.Name, strings.Join(sh, ", "))
		}
	}
	for _, e := range stats.Errors {
		// agenttypes errors already carry the "agents:" prefix —
		// don't double it (cmd/seek's startup printer predates that
		// prefix; we don't).
		fmt.Fprintln(stderr, e)
	}
	return nil
}

// laneLabel renders the routing knobs column. Local twin of the
// TUI's cmdAgentTypes lane string — view layers stay separate, same
// trade-off as shortSubSid in internal/tui.
func laneLabel(d *agenttypes.Definition) string {
	lane := "inherit"
	switch {
	case d.Provider != "" && d.Model != "":
		lane = d.Provider + "/" + d.Model
	case d.Provider != "":
		lane = d.Provider
	case d.Model != "":
		lane = d.Model
	}
	if d.Effort != "" {
		lane += " effort=" + d.Effort
	}
	if d.MaxTurns > 0 {
		lane += fmt.Sprintf(" max_turns=%d", d.MaxTurns)
	}
	return lane
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ---------- check ----------

func cmdCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek agents check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: seek agents check")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("check: unexpected argument %q", fs.Arg(0))
	}

	_, stats, err := loadForCWD()
	if err != nil {
		return err
	}
	var slots []string
	fileScoped := false // any project / user / config contribution?
	for slot, n := range stats.BySource {
		if n > 0 {
			slots = append(slots, fmt.Sprintf("%s: %d", slot, n))
			if slot != "builtin" {
				fileScoped = true
			}
		}
	}
	fmt.Fprintf(stdout, "%s\n", strings.Join(slots, ", "))
	if !fileScoped {
		// BySource always counts the three built-ins; "empty" here
		// means nothing user-authored will load.
		fmt.Fprintln(stdout, "no agent definition files found — only the three built-ins will load")
		return nil
	}
	if len(stats.Errors) == 0 {
		fmt.Fprintln(stdout, "ok — all agent definitions valid")
		return nil
	}
	for _, e := range stats.Errors {
		fmt.Fprintln(stdout, "error:", e)
	}
	return fmt.Errorf("check: %d agent definition error(s)", len(stats.Errors))
}

// ---------- create ----------

func cmdCreate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek agents create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek agents create <name> --description D [flags]

Writes <name>.md into the user scope (~/.seek/agents/) or, with
--project, <cwd>/.seek/agents/ (checked in, team-shared).

Flags:`)
		fs.PrintDefaults()
	}
	var (
		description = fs.String("description", "", "one-line summary shown when picking a type (required)")
		body        = fs.String("body", "", "role prompt (the markdown body); empty scaffolds a TODO prompt")
		tools       = fs.String("tools", "", "comma-separated tool whitelist; empty = inherit the parent registry")
		provider    = fs.String("provider", "", "lane override: deepseek | glm")
		model       = fs.String("model", "", "model id within the lane")
		effort      = fs.String("effort", "", "low | medium | high")
		workflow    = fs.String("workflow", "", "plan-analyze (tighten-only; the only legal value)")
		maxTurns    = fs.Int("max-turns", 0, "child turn cap; 0 = inherit")
		project     = fs.Bool("project", false, "write <cwd>/.seek/agents/ instead of ~/.seek/agents/")
	)
	// Peel <name> off the front so users can write the natural
	// `create my-type --description "..."` order — Go's flag.Parse
	// stops at the first non-flag arg. Same peeling as skillcli's
	// cmdSkillCreate (cargo-new / npm-init muscle memory).
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		fs.Usage()
		return fmt.Errorf("create: <name> must be the first argument (e.g. `seek agents create my-type --description ...`)")
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("create: unexpected extra positional argument(s): %v", fs.Args())
	}
	opts := agentsmgr.CreateOptions{
		Name:        name,
		Description: *description,
		Body:        *body,
		Provider:    *provider,
		Model:       *model,
		Effort:      *effort,
		Workflow:    *workflow,
		MaxTurns:    *maxTurns,
		Project:     *project,
	}
	if *tools != "" {
		for _, t := range strings.Split(*tools, ",") {
			if t = strings.TrimSpace(t); t != "" {
				opts.Tools = append(opts.Tools, t)
			}
		}
	}
	res, err := agentsmgr.Create(opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created %s -> %s\n", res.Name, res.Path)
	fmt.Fprintln(stdout, "takes effect on the next seek launch (definitions load once at session start)")
	return nil
}

// ---------- remove ----------

func cmdRemove(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek agents remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek agents remove <name>

Deletes ~/.seek/agents/<name>.md (user scope only — project-level
definitions are checked in; remove those via git). Built-in types
cannot be removed.

Flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("remove: exactly one <name> is required")
	}
	// Wire the live registry so built-in names are refused with a
	// clear message instead of a confusing "not found".
	set, _, err := loadForCWD()
	if err != nil {
		return err
	}
	res, err := agentsmgr.Remove(agentsmgr.RemoveOptions{Name: fs.Arg(0), Builtins: set})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s (%s)\n", res.Name, res.Path)
	return nil
}
