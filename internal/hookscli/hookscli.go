// Package hookscli implements the `seek hooks ...` CLI surface and
// the /hooks TUI slash command. Following the skillcli / memorycli
// pattern: stdout/stderr are injected so the TUI can render results
// inline instead of writing directly to the terminal.
//
// Subcommands (PRD §4.1):
//
//	seek hooks list                       — show merged config + avg durations
//	seek hooks check --event e --tool t   — dry-run match without exec
//	seek hooks trust [--reset[=path]]     — manage trust store
//	seek hooks audit [--since d] [--tool t] [--denied]  — query audit log
package hookscli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/internal/paths"
)

// Run dispatches a `seek hooks ...` invocation. The first arg (if any)
// is the verb; remaining args are forwarded to the verb's own flag
// set. Returning an error makes the binary exit non-zero — for
// "expected" error states (no hooks file, no trust) we Fprintln and
// return nil to match the skillcli / memorycli convention where a
// missing file is a UX state, not a programmer error.
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
	case "trust":
		return cmdTrust(rest, stdout, stderr)
	case "audit":
		return cmdAudit(rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	default:
		fmt.Fprintf(stderr, "hooks: unknown verb %q\n", verb)
		printHelp(stdout)
		return fmt.Errorf("unknown verb %q", verb)
	}
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: seek hooks <verb> [args]

Verbs:
  list                            List merged user+project hooks with avg duration.
  check --event E --tool T        Dry-run which hooks fire for (event, tool) without executing.
  trust [--reset[=path]]          Manage project-hooks trust. No args lists trusted projects.
  audit [--since D] [--tool T]    Print recent audit entries. --denied to filter denials only.
  help                            Show this help.

Files:
  ~/.seek/hooks.toml              User-level hooks (no trust prompt).
  <project>/.seek/hooks.toml      Project-level hooks (trust on first visit; sha256 tracks edits).
  ~/.seek/trusted-projects.json   Trust store.
  ~/.seek/hooks-audit.jsonl       Audit log.

See docs/prd/feature-shell-hooks.md for the full spec.
`)
}

// gateForCWD reads + merges the hooks visible from the current working
// directory. No trust prompt is wired (--list and --check should never
// trigger a y/N dialog from the CLI — they're informational). When the
// project file is untrusted, it is excluded from the result and an
// explanatory warning is printed to stderr.
func gateForCWD(stderr io.Writer) (hooksconfig.Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return hooksconfig.Config{}, fmt.Errorf("getwd: %w", err)
	}
	userPath, _ := paths.UserHooksToml()
	projectPath := paths.ProjectHooksToml(cwd)
	trustPath, _ := paths.TrustedProjectsJSON()
	store, terr := hooksconfig.NewTrustStore(trustPath)
	if terr != nil {
		fmt.Fprintln(stderr, "hooks:", terr)
	}
	cfg, warnings := hooksconfig.Gate(userPath, projectPath, cwd, store, nil, hooksconfig.DefaultSyntaxChecker)
	for _, w := range warnings {
		fmt.Fprintln(stderr, w)
	}
	return cfg, nil
}

// ---- list ----

func cmdList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hooks list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	byName := fs.Bool("by-name", false, "sort by name instead of event group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := gateForCWD(stderr)
	if err != nil {
		return err
	}
	rows := hooksconfig.Summarize(cfg)
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no hooks configured")
		return nil
	}
	auditPath, _ := paths.HooksAuditLog()
	avgs := loadAverages(auditPath, 50)
	if *byName {
		hooksconfig.SortByName(rows)
	}

	fmt.Fprintf(stdout, "%-12s  %-8s  %-30s  %-10s  %-10s  %s\n",
		"EVENT", "SOURCE", "NAME", "TOOL", "AVG_MS", "COMMAND")
	for _, r := range rows {
		cmd := r.Command
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		avg := ""
		if v, ok := avgs[r.Name]; ok {
			avg = fmt.Sprintf("%.0f", v)
		}
		skip := ""
		if r.SkipReason != "" {
			skip = " [SKIPPED: " + r.SkipReason + "]"
		}
		fmt.Fprintf(stdout, "%-12s  %-8s  %-30s  %-10s  %-10s  %s%s\n",
			r.Event, r.Source, r.Name, r.Tool, avg, cmd, skip)
	}
	return nil
}

func loadAverages(path string, limit int) map[string]float64 {
	if path == "" {
		return nil
	}
	entries, err := hooksconfig.ReadAuditLog(path)
	if err != nil || len(entries) == 0 {
		return nil
	}
	return hooksconfig.AverageDurationByHook(entries, limit)
}

// ---- check ----

func cmdCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hooks check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	event := fs.String("event", "", "event name (pre_tool|post_tool|pre_prompt|session_start|session_end)")
	tool := fs.String("tool", "", "tool name (required for pre_tool/post_tool)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *event == "" {
		fmt.Fprintln(stderr, "usage: seek hooks check --event <e> [--tool <t>]")
		return nil
	}
	cfg, err := gateForCWD(stderr)
	if err != nil {
		return err
	}
	var pool []hooksconfig.Hook
	switch *event {
	case hooksconfig.EventPreTool:
		pool = cfg.PreTool
	case hooksconfig.EventPostTool:
		pool = cfg.PostTool
	case hooksconfig.EventPrePrompt:
		pool = cfg.PrePrompt
	case hooksconfig.EventSessionStart:
		pool = cfg.SessionStart
	case hooksconfig.EventSessionEnd:
		pool = cfg.SessionEnd
	default:
		fmt.Fprintf(stderr, "hooks: unknown event %q\n", *event)
		return nil
	}
	if len(pool) == 0 {
		fmt.Fprintf(stdout, "no hooks for event %s\n", *event)
		return nil
	}
	matched := 0
	for _, h := range pool {
		switch *event {
		case hooksconfig.EventPreTool, hooksconfig.EventPostTool:
			if *tool == "" || !h.MatchTool(*tool) {
				continue
			}
		}
		matched++
		skip := ""
		if h.SkipReason != "" {
			skip = " [skipped: " + h.SkipReason + "]"
		}
		fmt.Fprintf(stdout, "  %-30s  source=%s  timeout=%dms%s\n",
			h.Name, h.Source, h.EffectiveTimeoutMs(), skip)
	}
	if matched == 0 {
		fmt.Fprintf(stdout, "no hooks match event=%s tool=%s\n", *event, *tool)
	}
	return nil
}

// ---- trust ----

func cmdTrust(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hooks trust", flag.ContinueOnError)
	fs.SetOutput(stderr)
	resetPath := fs.String("reset", "", "remove a project from the trust store; use 'all' to wipe everything; bare flag = current project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	trustPath, _ := paths.TrustedProjectsJSON()
	store, err := hooksconfig.NewTrustStore(trustPath)
	if err != nil {
		fmt.Fprintln(stderr, "hooks:", err)
	}

	// flag.Parse leaves --reset unset as "" — but the user wrote
	// `hooks trust --reset` (no value), which Go's flag pkg treats as
	// "" too. Distinguish with Visit:
	resetVisited := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "reset" {
			resetVisited = true
		}
	})

	if resetVisited {
		target := *resetPath
		switch target {
		case "all":
			if err := store.ResetAll(); err != nil {
				fmt.Fprintln(stderr, "hooks:", err)
				return nil
			}
			fmt.Fprintln(stdout, "trust store cleared")
		case "":
			cwd, _ := os.Getwd()
			if err := store.Reset(cwd); err != nil {
				fmt.Fprintln(stderr, "hooks:", err)
				return nil
			}
			fmt.Fprintf(stdout, "trust cleared for %s\n", cwd)
		default:
			if err := store.Reset(target); err != nil {
				fmt.Fprintln(stderr, "hooks:", err)
				return nil
			}
			fmt.Fprintf(stdout, "trust cleared for %s\n", target)
		}
		return nil
	}

	// Default: list.
	entries := store.List()
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no trusted projects")
		return nil
	}
	fmt.Fprintf(stdout, "%-60s  %-12s  %s\n", "PROJECT", "SHA256", "APPROVED_AT")
	for _, e := range entries {
		sha := e.SHA256
		if len(sha) > 12 {
			sha = sha[:12]
		}
		fmt.Fprintf(stdout, "%-60s  %-12s  %s\n", e.ProjectPath, sha, e.ApprovedAt)
	}
	return nil
}

// ---- audit ----

func cmdAudit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("hooks audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.Duration("since", 0, "only entries newer than this duration (e.g. 24h)")
	tool := fs.String("tool", "", "only entries for this tool name")
	denied := fs.Bool("denied", false, "only entries that were denied")
	limit := fs.Int("limit", 0, "max entries to show (0 = all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	auditPath, _ := paths.HooksAuditLog()
	entries, err := hooksconfig.ReadAuditLog(auditPath)
	if err != nil {
		fmt.Fprintln(stderr, "hooks:", err)
		return nil
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no audit entries")
		return nil
	}

	cutoff := time.Time{}
	if *since > 0 {
		cutoff = time.Now().Add(-*since)
	}

	var filtered []hooksconfig.AuditEntry
	for _, e := range entries {
		if !cutoff.IsZero() {
			if t, err := time.Parse(time.RFC3339, e.TS); err == nil && t.Before(cutoff) {
				continue
			}
		}
		if *tool != "" && e.Tool != *tool {
			continue
		}
		if *denied && !e.Denied {
			continue
		}
		filtered = append(filtered, e)
	}
	if *limit > 0 && len(filtered) > *limit {
		filtered = filtered[len(filtered)-*limit:]
	}
	if len(filtered) == 0 {
		fmt.Fprintln(stdout, "no matching audit entries")
		return nil
	}

	// Sort newest-first for readability.
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].TS > filtered[j].TS })

	fmt.Fprintf(stdout, "%-20s  %-12s  %-20s  %-10s  %-6s  %-8s  %s\n",
		"TS", "EVENT", "HOOK", "TOOL", "EXIT", "DURATION", "DENIED/REASON")
	for _, e := range filtered {
		flag := ""
		if e.Denied {
			flag = "DENIED"
		}
		if e.Reason != "" {
			if flag != "" {
				flag += " · "
			}
			flag += e.Reason
		}
		fmt.Fprintf(stdout, "%-20s  %-12s  %-20s  %-10s  %-6d  %-8d  %s\n",
			e.TS, e.Event, e.Hook, e.Tool, e.ExitCode, e.DurationMs, flag)
	}
	return nil
}

// ResolveProjectHooksPath is a small helper for callers (TUI) that
// want to point a "view file" prompt at the project hooks.toml without
// having to duplicate the .seek/ subdir logic. Keeps the wire format
// in one place.
func ResolveProjectHooksPath(projectAbs string) string {
	if projectAbs == "" {
		return ""
	}
	return filepath.Join(projectAbs, ".seek", "hooks.toml")
}
