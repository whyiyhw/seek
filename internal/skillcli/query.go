package skillcli

// Read-side `seek skill` subcommands: list / status / stats. The
// install / uninstall / update commands live in skill.go alongside
// the dispatcher; this file is split off because the formatting +
// aggregation code is heavier and stands on its own logically.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillstats"
)

// skillRow is one row in `seek skill list` / `seek skill stats`
// output. Captures everything the rendering layer needs so the
// stats reader and the loader don't have to be re-queried per row.
type skillRow struct {
	Name        string `json:"name"`
	Source      string `json:"source"` // human label: "user", "project", "builtin"
	Type        string `json:"type"`   // single-file | package | builtin
	Calls       int    `json:"calls"`
	LastUsed    string `json:"last_used,omitempty"` // RFC3339 (empty if never called)
	TopModel    string `json:"top_model,omitempty"`
	TopProvider string `json:"top_provider,omitempty"`
}

// aggregateStats groups []Entry by name. Aggregations are deliberately
// small: count, max(ts), top model, top provider — anything beyond
// that is `status` / v3 territory and would require keeping more data
// per row.
type statsAgg struct {
	Count       int
	LastUsed    string
	TopModel    string
	TopProvider string
	// histograms used by `status` for detail output
	Models    map[string]int
	Providers map[string]int
	// recent holds the last N timestamps (most recent first). Only
	// populated by aggregateForStatus.
	Recent []string
}

// aggregate folds the stream of entries down to per-name aggregates.
// since (when non-zero) drops entries older than now-since.
func aggregate(entries []skillstats.Entry, since time.Duration, now time.Time) map[string]*statsAgg {
	out := map[string]*statsAgg{}
	cutoff := time.Time{}
	if since > 0 {
		cutoff = now.Add(-since)
	}
	for _, e := range entries {
		// Distillation provenance rows (M-SD2) share the file but are
		// not calls — counting them would make a much-distilled draft
		// look like the most-used skill in the tree.
		if e.Provider == skillstats.ProviderDistillAuto {
			continue
		}
		if !cutoff.IsZero() {
			if ts, err := time.Parse(time.RFC3339, e.TS); err == nil {
				if ts.Before(cutoff) {
					continue
				}
			}
			// Failed parse → keep the entry; better noisy than silently dropped.
		}
		a, ok := out[e.Name]
		if !ok {
			a = &statsAgg{Models: map[string]int{}, Providers: map[string]int{}}
			out[e.Name] = a
		}
		a.Count++
		if e.TS > a.LastUsed {
			a.LastUsed = e.TS
		}
		if e.Model != "" {
			a.Models[e.Model]++
		}
		if e.Provider != "" {
			a.Providers[e.Provider]++
		}
	}
	// Resolve top model / provider once at the end.
	for _, a := range out {
		a.TopModel = topKey(a.Models)
		a.TopProvider = topKey(a.Providers)
	}
	return out
}

// topKey returns the key with the highest count from a histogram.
// Ties resolved by lexicographic order so output stays deterministic
// across runs.
func topKey(hist map[string]int) string {
	var best string
	var bestN int
	for k, n := range hist {
		if n > bestN || (n == bestN && k < best) {
			best = k
			bestN = n
		}
	}
	return best
}

// sourceLabel converts a skill.Skill into the short tag shown in
// `list` output ("project", "user", "builtin", ".claude").
func sourceLabel(sk *skill.Skill, userDir string) string {
	if sk.Type == skill.TypeBuiltin {
		return "builtin"
	}
	src := sk.Source
	if strings.HasPrefix(src, userDir) {
		return "user"
	}
	if strings.Contains(src, "/.claude/skills/") {
		return ".claude"
	}
	if strings.Contains(src, "/.seek/skills/") {
		return "project"
	}
	return "other"
}

// loadAll runs the loader against the standard paths so all three
// query commands share one place that knows how to find skills.
func loadAll() (*skill.Set, skill.LoadStats, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, skill.LoadStats{}, "", err
	}
	userDir, _ := paths.UserSkills() // best-effort: empty means home unresolved
	set, stats, err := skill.Load(skill.LoadOptions{
		ProjectDir:    cwd,
		UserSkillsDir: userDir,
	})
	return set, stats, userDir, err
}

func loadStats() ([]skillstats.Entry, error) {
	path, err := paths.UserSkillStats()
	if err != nil {
		return nil, nil // best-effort: home unresolved → empty stats
	}
	return skillstats.Read(path)
}

// ---------- list ----------

func cmdSkillList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill list [--source <which>] [--json]

Flags:`)
		fs.PrintDefaults()
	}
	source := fs.String("source", "", "filter by source: project | user | builtin | .claude")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set, _, userDir, err := loadAll()
	if err != nil {
		return err
	}
	entries, _ := loadStats()
	agg := aggregate(entries, 0, time.Now())

	rows := make([]skillRow, 0, set.Len())
	for _, sk := range set.List() {
		label := sourceLabel(sk, userDir)
		if *source != "" && label != *source {
			continue
		}
		r := skillRow{
			Name:   sk.Name,
			Source: label,
			Type:   sk.Type.String(),
		}
		if a := agg[sk.Name]; a != nil {
			r.Calls = a.Count
			r.LastUsed = a.LastUsed
			r.TopModel = a.TopModel
			r.TopProvider = a.TopProvider
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	if *asJSON {
		return writeJSON(stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no skills loaded")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tTYPE\tCALLS\tLAST_USED")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			r.Name, r.Source, r.Type, r.Calls, r.LastUsed)
	}
	return tw.Flush()
}

// ---------- status ----------

// skillStatusReport is the JSON shape of `seek skill status`.
type skillStatusReport struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Source        string            `json:"source"`
	SourceTier    string            `json:"source_tier"` // user|project|builtin|.claude|other
	Version       string            `json:"version,omitempty"`
	License       string            `json:"license,omitempty"`
	Author        string            `json:"author,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
	Shadowed      []string          `json:"shadowed,omitempty"`
	BodyBytes     int               `json:"body_bytes"`
	Calls         int               `json:"calls"`
	LastUsed      string            `json:"last_used,omitempty"`
	Models        map[string]int    `json:"models,omitempty"`
	Providers     map[string]int    `json:"providers,omitempty"`
	InstallSource map[string]string `json:"install_source,omitempty"`
}

func cmdSkillStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill status <name> [--json]

Flags:`)
		fs.PrintDefaults()
	}
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("status: exactly one <name> is required")
	}
	name := fs.Arg(0)

	set, _, userDir, err := loadAll()
	if err != nil {
		return err
	}
	sk := set.Get(name)
	if sk == nil {
		return fmt.Errorf("status: skill %q not loaded", name)
	}
	report := skillStatusReport{
		Name:         sk.Name,
		Type:         sk.Type.String(),
		Source:       sk.Source,
		SourceTier:   sourceLabel(sk, userDir),
		Version:      sk.Version,
		License:      sk.License,
		Author:       sk.Author,
		AllowedTools: sk.AllowedTools,
		Shadowed:     set.Shadowed(name),
		BodyBytes:    len(sk.Body),
	}
	if sk.InstallSource != nil {
		report.InstallSource = map[string]string{
			"type":         sk.InstallSource.Type,
			"url":          sk.InstallSource.URL,
			"ref":          sk.InstallSource.Ref,
			"subpath":      sk.InstallSource.Subpath,
			"installed_at": sk.InstallSource.InstalledAt,
			"sha256":       sk.InstallSource.ChecksumSHA256,
		}
		// Drop empty fields so the JSON / table stay tight.
		for k, v := range report.InstallSource {
			if v == "" {
				delete(report.InstallSource, k)
			}
		}
	}
	entries, _ := loadStats()
	if a := aggregate(entries, 0, time.Now())[name]; a != nil {
		report.Calls = a.Count
		report.LastUsed = a.LastUsed
		if len(a.Models) > 0 {
			report.Models = a.Models
		}
		if len(a.Providers) > 0 {
			report.Providers = a.Providers
		}
	}

	if *asJSON {
		return writeJSON(stdout, report)
	}
	return renderStatusText(stdout, report)
}

func renderStatusText(w io.Writer, r skillStatusReport) error {
	fmt.Fprintf(w, "%s\n", r.Name)
	fmt.Fprintf(w, "  type          %s\n", r.Type)
	fmt.Fprintf(w, "  source_tier   %s\n", r.SourceTier)
	fmt.Fprintf(w, "  source        %s\n", r.Source)
	if r.Version != "" {
		fmt.Fprintf(w, "  version       %s\n", r.Version)
	}
	if r.License != "" {
		fmt.Fprintf(w, "  license       %s\n", r.License)
	}
	if r.Author != "" {
		fmt.Fprintf(w, "  author        %s\n", r.Author)
	}
	if len(r.AllowedTools) > 0 {
		fmt.Fprintf(w, "  allowed_tools %s (recorded only — not enforced in v2)\n", strings.Join(r.AllowedTools, ", "))
	}
	fmt.Fprintf(w, "  body_bytes    %d\n", r.BodyBytes)
	fmt.Fprintf(w, "  calls         %d\n", r.Calls)
	if r.LastUsed != "" {
		fmt.Fprintf(w, "  last_used     %s\n", r.LastUsed)
	}
	if len(r.Shadowed) > 0 {
		fmt.Fprintf(w, "  shadowed_by_priority:\n")
		for _, p := range r.Shadowed {
			fmt.Fprintf(w, "    - %s\n", p)
		}
	}
	if len(r.InstallSource) > 0 {
		fmt.Fprintf(w, "  install_source:\n")
		// Stable order — sort keys.
		keys := make([]string, 0, len(r.InstallSource))
		for k := range r.InstallSource {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "    %-13s %s\n", k, r.InstallSource[k])
		}
	}
	if len(r.Models) > 0 {
		fmt.Fprintf(w, "  models:\n")
		for _, k := range sortedHistKeys(r.Models) {
			fmt.Fprintf(w, "    %-30s %d\n", k, r.Models[k])
		}
	}
	if len(r.Providers) > 0 {
		fmt.Fprintf(w, "  providers:\n")
		for _, k := range sortedHistKeys(r.Providers) {
			fmt.Fprintf(w, "    %-30s %d\n", k, r.Providers[k])
		}
	}
	return nil
}

// sortedHistKeys returns histogram keys sorted by count descending,
// breaking ties lexicographically so output is reproducible.
func sortedHistKeys(h map[string]int) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if h[keys[i]] != h[keys[j]] {
			return h[keys[i]] > h[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// ---------- stats ----------

// funnelStageOrder fixes the report's row order: the order candidates
// actually flow through (card c_jqwd72). Unknown stages (future layers,
// empty Stage on pre-三期 rows) sort after the known ones, verdicts
// alphabetically within a stage — deterministic output, no map-order
// surprises.
var funnelStageOrder = []string{"L0", "L1", "shape", "evidence", "L2", "L3", "L4"}

func funnelStageRank(stage string) int {
	for i, s := range funnelStageOrder {
		if s == stage {
			return i
		}
	}
	return len(funnelStageOrder)
}

// aggregateFunnel folds provider=distill-auto rows into stage→verdict
// counts — the distillation funnel report (`seek skill stats --distill`,
// feature-skill-distill 三期). Pre-三期 produce rows (no stage) land in
// "" so historical rows still show up instead of vanishing.
func aggregateFunnel(entries []skillstats.Entry, since time.Duration, now time.Time) map[string]map[string]int {
	out := map[string]map[string]int{}
	cutoff := time.Time{}
	if since > 0 {
		cutoff = now.Add(-since)
	}
	for _, e := range entries {
		if e.Provider != skillstats.ProviderDistillAuto {
			continue
		}
		if !cutoff.IsZero() {
			if ts, err := time.Parse(time.RFC3339, e.TS); err == nil && ts.Before(cutoff) {
				continue
			}
		}
		if out[e.Stage] == nil {
			out[e.Stage] = map[string]int{}
		}
		out[e.Stage][e.Verdict]++
	}
	return out
}

func cmdSkillStats(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seek skill stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage: seek skill stats [--top N] [--since DURATION] [--distill] [--idle DURATION] [--json]

Flags:`)
		fs.PrintDefaults()
	}
	top := fs.Int("top", 10, "show this many skills (highest call count first); 0 = no limit")
	since := fs.Duration("since", 30*24*time.Hour, "only count calls newer than this (e.g. 24h, 168h, 720h)")
	distill := fs.Bool("distill", false, "report the skill-distillation funnel (stage × verdict counts) instead of call counts")
	idle := fs.Duration("idle", 0, "list LOADED skills with zero calls in this window, least-recently-used first (e.g. 720h)")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// --idle is its own report with its own window, so the other
	// report flags don't apply to it. fs.Visit walks only explicitly
	// set flags — that's what separates "--idle 720h" from "user
	// silently inherited the since default", which happens to be the
	// same 720h the PRD uses as its idle example.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["idle"] {
		if *distill || set["top"] || set["since"] {
			return fmt.Errorf("stats: --idle cannot be combined with --top, --since or --distill (the window comes from --idle itself)")
		}
		if *idle <= 0 {
			return fmt.Errorf("stats: --idle requires a positive duration (e.g. --idle 720h)")
		}
	}

	entries, err := loadStats()
	if err != nil {
		return err
	}
	if *distill {
		return reportFunnel(entries, *since, *asJSON, stdout)
	}
	if set["idle"] {
		skills, _, userDir, err := loadAll()
		if err != nil {
			return err
		}
		return reportIdle(skills, userDir, entries, *idle, *asJSON, stdout)
	}

	agg := aggregate(entries, *since, time.Now())
	type ranked struct {
		Name string
		*statsAgg
	}
	rows := make([]ranked, 0, len(agg))
	for n, a := range agg {
		rows = append(rows, ranked{Name: n, statsAgg: a})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Name < rows[j].Name
	})
	if *top > 0 && len(rows) > *top {
		rows = rows[:*top]
	}

	if *asJSON {
		// Same shape as `list` for consistency — drop empty Source/Type
		// so the JSON makes clear this came from stats, not loader.
		out := make([]skillRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, skillRow{
				Name:        r.Name,
				Calls:       r.Count,
				LastUsed:    r.LastUsed,
				TopModel:    r.TopModel,
				TopProvider: r.TopProvider,
			})
		}
		return writeJSON(stdout, out)
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no skill calls recorded in the selected window")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCALLS\tLAST_USED\tTOP_MODEL\tTOP_PROVIDER")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n",
			r.Name, r.Count, r.LastUsed, r.TopModel, r.TopProvider)
	}
	return tw.Flush()
}

// reportFunnel renders the distillation funnel report (skill stats
// --distill): one row per (stage, verdict) pair, stages in funnel
// order. The counts answer "which layer eats what" (card c_jqwd72):
// L0 PASS/DROP gate sessions, shape/evidence/L2 REJECT drop
// candidates, L3 ACCEPT/REJECT/ERROR is the semantic filter, L4
// DRAFT/STRENGTHEN is recurrence accumulation producing drafts.
func reportFunnel(entries []skillstats.Entry, since time.Duration, asJSON bool, stdout io.Writer) error {
	funnel := aggregateFunnel(entries, since, time.Now())
	if len(funnel) == 0 {
		fmt.Fprintln(stdout, "no distillation telemetry recorded (rows appear after the auto path runs; $SEEK_AUTO_SKILL_DISTILL=1)")
		return nil
	}

	// Deterministic (stage, verdict) pairs: funnel order, then verdict.
	type pair struct{ stage, verdict string }
	var pairs []pair
	for stage, verdicts := range funnel {
		for verdict := range verdicts {
			pairs = append(pairs, pair{stage, verdict})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		ri, rj := funnelStageRank(pairs[i].stage), funnelStageRank(pairs[j].stage)
		if ri != rj {
			return ri < rj
		}
		if pairs[i].stage != pairs[j].stage {
			return pairs[i].stage < pairs[j].stage
		}
		return pairs[i].verdict < pairs[j].verdict
	})

	if asJSON {
		return writeJSON(stdout, funnel)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STAGE\tVERDICT\tCOUNT")
	for _, p := range pairs {
		fmt.Fprintf(tw, "%s\t%s\t%d\n", p.stage, p.verdict, funnel[p.stage][p.verdict])
	}
	return tw.Flush()
}

// idleRow is one row of `seek skill stats --idle`: a LOADED skill
// with zero counted calls inside the window. LastUsed is the skill's
// true last call (RFC3339, from the unfiltered aggregate) — empty
// means never called at all, a different fact than "not recently",
// and the reason the idle view needs two aggregations.
type idleRow struct {
	Name     string `json:"name"`
	Source   string `json:"source"` // project | user | builtin | .claude | other
	LastUsed string `json:"last_used,omitempty"`
}

// idleRows is the Set-outer-join at the heart of `--idle`
// (feature-memory-skill-bridge §6): every loaded skill the
// window-filtered aggregate has no counted call for — including
// skills with no stats row at all — becomes a row. Stats rows whose
// name isn't in the Set (uninstalled leftovers) are ignored: the join
// is driven from the Set side. Sorted never-used-first, then by
// last_used ascending (RFC3339 strings sort chronologically), ties by
// name so output stays deterministic.
func idleRows(set *skill.Set, userDir string, entries []skillstats.Entry, window time.Duration, now time.Time) []idleRow {
	if set == nil {
		return []idleRow{} // non-nil so --json emits [] instead of null
	}
	inWindow := aggregate(entries, window, now)
	ever := aggregate(entries, 0, now)
	rows := make([]idleRow, 0, set.Len())
	for _, sk := range set.List() {
		if a := inWindow[sk.Name]; a != nil && a.Count > 0 {
			continue
		}
		r := idleRow{Name: sk.Name, Source: sourceLabel(sk, userDir)}
		if a := ever[sk.Name]; a != nil {
			r.LastUsed = a.LastUsed
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].LastUsed != rows[j].LastUsed {
			return rows[i].LastUsed < rows[j].LastUsed
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

// reportIdle renders `seek skill stats --idle`: the observation view
// of the skill lifecycle — which loaded skills have zero calls in the
// window, least-recently-used first. Pure observation, no action:
// usage is not a verdict (§5.2 of the bridge PRD), the uninstall
// decision stays human.
func reportIdle(set *skill.Set, userDir string, entries []skillstats.Entry, window time.Duration, asJSON bool, stdout io.Writer) error {
	rows := idleRows(set, userDir, entries, window, time.Now())
	if asJSON {
		return writeJSON(stdout, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "no idle skills — every loaded skill was called within %s\n", window)
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLAST_USED\tSOURCE")
	for _, r := range rows {
		last := r.LastUsed
		if last == "" {
			last = "never"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, last, r.Source)
	}
	return tw.Flush()
}

// writeJSON emits v as indented JSON to w. Used by every --json
// command so the output formatting stays in one place.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
