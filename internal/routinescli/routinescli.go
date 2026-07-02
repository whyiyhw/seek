// Package routinescli implements the `seek cron ...` CLI
// surface (feature-routines.md §4.1). Mirrors checkpointcli /
// worktreecli / hookscli structure: Run(args, stdout, stderr)
// entry + per-verb handlers + printHelp.
//
// Subcommands:
//
//   - create:  register a new cron job
//   - list:    enumerate registered jobs (text table or --json)
//   - delete:  remove a job by name
//   - run:     fire a job immediately, regardless of schedule
//   - tick:    OS scheduler entry — scan + fire all due
//   - help:    usage
//
// Doesn't need API keys / sessions / project state. The
// `seek cron tick` path runs from launchd / systemd / cron /
// Task Scheduler at 1-minute cadence and SHOULD short-circuit
// ahead of the main runtime — done at the cmd/seek dispatch
// layer.
package routinescli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/routines"
)

// WebhookDispatcherFromConfig builds the push-webhook dispatcher from
// ~/.seek/config.json's push_webhooks (v6 柱 M). Returns nil when no
// webhooks are configured (the common case) or when config can't be
// read — push is best-effort and must never block a cron tick, so a
// config-read failure degrades to "no webhooks" with a stderr WARN
// rather than an error. Shared by `cron tick`, `cron run`, and the
// interactive auto-tick in cmd/seek so the wiring can't drift.
func WebhookDispatcherFromConfig() routines.WebhookDispatcher {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cron: read config for push webhooks: %v\n", err)
		return nil
	}
	if len(cfg.PushWebhooks) == 0 {
		return nil
	}
	targets := make([]routines.WebhookTarget, 0, len(cfg.PushWebhooks))
	for _, w := range cfg.PushWebhooks {
		targets = append(targets, routines.WebhookTarget{
			URL:           w.URL,
			Format:        w.Format,
			Events:        w.Events,
			Template:      w.Template,
			AppID:         w.AppID,
			AppSecret:     w.AppSecret,
			ReceiveID:     w.ReceiveID,
			ReceiveIDType: w.ReceiveIDType,
		})
	}
	return routines.NewWebhookDispatcher(targets, nil)
}

// Run is the public entry. args is everything after `seek cron`
// (e.g. `seek cron create --at @daily foo` → args = `["create",
// "--at", "@daily", "foo"]`).
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printHelp(stdout)
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "create":
		return cmdCreate(rest, stdout, stderr)
	case "list", "ls":
		return cmdList(rest, stdout, stderr)
	case "delete", "rm":
		return cmdDelete(rest, stdout, stderr)
	case "run":
		return cmdRun(rest, stdout, stderr)
	case "tick":
		return cmdTick(rest, stdout, stderr)
	case "config":
		return cmdConfig(rest, stdout, stderr)
	case "help", "--help", "-h":
		printHelp(stdout)
		return nil
	}
	return fmt.Errorf("unknown cron subcommand %q (try `seek cron help`)", verb)
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `seek cron — schedule prompts to run unattended

Usage:
  seek cron create [flags] <prompt...>
      Register a new cron job. <prompt> is the user prompt fed to
      `+"`seek -p`"+` at each fire. Flags:
        --name N         job name (default: auto-generated)
        --at SCHEDULE    @every <dur> / @hourly / @daily /
                         @weekly (default: @daily)
        --cwd DIR        project root for the run (default: $HOME)
        --max-runs N     auto-delete after N fires (0 = unlimited)
        --no-yolo        run WITHOUT --yolo (default: yolo on)
        --notify FLAG    always | on_failure | never (default: always)
        --force          overwrite existing job with same name

  seek cron list [--json]
      List registered jobs.

  seek cron delete <name>
      Remove a job.

  seek cron run <name>
      Fire a job NOW, regardless of schedule. Useful for testing
      a new --at expression without waiting.

  seek cron tick
      OS scheduler entry. Scans jobs.jsonl + runs everything due.
      Designed for launchd / systemd-timer / cron / Task Scheduler
      at ~1-minute cadence. Skips silently if another tick holds
      the host-wide tick.lock.

  seek cron config check [--probe]
      Validate the push_webhooks in ~/.seek/config.json (mobile-push
      bridge). Checks each URL's scheme + format offline; --probe sends
      a real test notification to each so you can confirm a channel
      works before relying on it.

  seek cron help
      Show this help.

OS scheduler setup (every minute):
  macOS launchd:    ~/Library/LaunchAgents/com.seek.cron.plist
                    + StartInterval=60 → `+"`launchctl load`"+`
  Linux systemd:    user-level seek-cron.{service,timer} unit
                    OnUnitActiveSec=1min → `+"`systemctl --user enable --now`"+`
  Windows Task:     schtasks /create /tn "seek cron" /tr "seek cron tick"
                    /sc minute

Subprocess env (~/.seek/cron/env):
  OS schedulers hand seek a minimal environment — DEEPSEEK_API_KEY
  and PATH from your interactive shell are NOT inherited. Create
  ~/.seek/cron/env to inject what the scheduler can't see:
    DEEPSEEK_API_KEY=sk-…
    PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
  Format: one KEY=VALUE per line; # comments; balanced quotes
  stripped; no shell expansion. Overlay entries override the
  scheduler-provided env (last-wins). Parse errors fail spawn
  loudly — better than silently running without your API key.
  systemd users can point `+"`EnvironmentFile=`"+` at this same file.`)
}

// cmdCreate parses --name / --at / --cwd / etc, joins the
// remaining positional args into the prompt, validates via
// Schedule + ValidateName + ValidateNotify, then calls
// Store.Create. Returns ErrJobExists wrapped in a clear hint
// when --force is missing.
func cmdCreate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cron create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "job name (auto-generated if omitted)")
	at := fs.String("at", "@daily", "schedule expression (@every <dur> / @hourly / @daily / @weekly)")
	cwd := fs.String("cwd", "", "project root for the cron run (default: $HOME)")
	maxRuns := fs.Int("max-runs", 0, "delete after N fires (0 = unlimited)")
	noYolo := fs.Bool("no-yolo", false, "run subprocess WITHOUT --yolo (default: yolo on)")
	notify := fs.String("notify", "always", "always | on_failure | never")
	force := fs.Bool("force", false, "overwrite existing job with same name")
	autopilotFlag := fs.Bool("autopilot", false, "unattended autopilot job: fires `seek autopilot run <goal>` (parallel worktree fleet, no-remote-guarded) instead of `seek -p`")
	goalFlag := fs.Bool("goal", false, "unattended goal job: fires `seek goal run <condition>` (single agent loops until a cheap model judges <condition> met; yolo-local + no-remote-guarded) instead of `seek -p`")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *autopilotFlag && *goalFlag {
		return fmt.Errorf("cron create: --autopilot and --goal are mutually exclusive")
	}

	// Prompt is the joined positional args. Allow piped stdin
	// only via `--` separator (rare; users mostly use inline).
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		return fmt.Errorf("cron create: <prompt> required (e.g. `seek cron create --at @daily 'summarise PRs'`)")
	}

	sched, err := routines.ParseSchedule(*at)
	if err != nil {
		return fmt.Errorf("cron create: %w", err)
	}
	if err := routines.ValidateNotify(*notify); err != nil {
		return fmt.Errorf("cron create: %w", err)
	}
	if *name == "" {
		*name = autoGenName()
	}
	if err := routines.ValidateName(*name); err != nil {
		return fmt.Errorf("cron create: %w", err)
	}

	cwdResolved := *cwd
	if cwdResolved == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cwdResolved = home
		}
	}

	store, err := routines.OpenStore()
	if err != nil {
		return err
	}
	j := routines.Job{
		Name:        *name,
		Schedule:    sched,
		Prompt:      prompt,
		ProjectRoot: cwdResolved,
		MaxRuns:     *maxRuns,
		Yolo:        !*noYolo,
		Notify:      *notify,
		Autopilot:   *autopilotFlag,
		Goal:        *goalFlag,
	}
	if err := store.Create(j, routines.CreateOptions{Force: *force}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "registered cron job %q (next run: %s)\n", *name, j.Schedule.Raw)
	return nil
}

// cmdList enumerates registered jobs. Default output is a
// fixed-width text table; --json dumps the slice verbatim for
// scripts.
func cmdList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cron list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit jobs as JSON array")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := routines.OpenStore()
	if err != nil {
		return err
	}
	jobs, err := store.List()
	if err != nil {
		return err
	}

	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(jobs)
	}

	if len(jobs) == 0 {
		fmt.Fprintln(stdout, "no cron jobs registered — try `seek cron create --at @daily 'check status'`")
		return nil
	}
	// Columns chosen to fit a typical 100-col terminal:
	//   NAME(20)  SCHEDULE(14)  NEXT_RUN(20)  LAST_STATUS(10)  RUNS(5)  PROMPT(remaining)
	fmt.Fprintf(stdout, "%-20s %-14s %-20s %-10s %5s  %s\n",
		"NAME", "SCHEDULE", "NEXT_RUN", "LAST", "RUNS", "PROMPT")
	for _, j := range jobs {
		next := "-"
		if !j.NextRun.IsZero() {
			next = j.NextRun.UTC().Format("2006-01-02T15:04:05Z")
		}
		fmt.Fprintf(stdout, "%-20s %-14s %-20s %-10s %5d  %s\n",
			truncate(j.Name, 20),
			truncate(j.Schedule.Raw, 14),
			next,
			truncate(j.LastStatus, 10),
			j.RunCount,
			truncate(j.Prompt, 60))
	}
	return nil
}

// cmdDelete removes a job by name. Missing names surface
// ErrJobNotFound from the Store; we wrap it in a user-readable
// hint rather than the raw error.
func cmdDelete(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cron delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("cron delete: exactly one <name> required")
	}
	name := fs.Arg(0)

	store, err := routines.OpenStore()
	if err != nil {
		return err
	}
	if err := store.Delete(name); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "deleted cron job %q\n", name)
	return nil
}

// cmdRun fires a single named job immediately via RunOne. Prints
// the outcome — users typically invoke this to verify a newly-
// registered job behaves as expected before letting OS scheduler
// take over.
func cmdRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cron run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", routines.DefaultRunTimeout, "wall-clock cap for this run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("cron run: exactly one <name> required")
	}
	name := fs.Arg(0)

	store, err := routines.OpenStore()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Fprintf(stdout, "running cron job %q...\n", name)
	if err := routines.RunOne(ctx, store, name, routines.TickOptions{RunTimeout: *timeout, Webhook: WebhookDispatcherFromConfig()}); err != nil {
		return err
	}
	// RunOne returns nil on completed/failed/killed runs; the
	// outcome lives on the Store. Fetch + report.
	got, err := store.Get(name)
	if err != nil {
		// Single-run-only jobs (max_runs=1) auto-delete; missing
		// here just means it ran successfully and removed itself.
		fmt.Fprintf(stdout, "cron job %q ran and self-deleted (max_runs=1 was met)\n", name)
		return nil
	}
	fmt.Fprintf(stdout, "  status: %s\n", got.LastStatus)
	if got.LastError != "" {
		fmt.Fprintf(stdout, "  error:  %s\n", got.LastError)
	}
	if got.LastRunID != "" {
		fmt.Fprintf(stdout, "  run id: %s\n", got.LastRunID)
	}
	return nil
}

// cmdTick is the OS-scheduler entry point. Stays quiet on
// idle ticks (no due jobs / lock contention) so launchd /
// systemd-timer / cron / Task Scheduler logs don't fill up.
// One-line summary when at least one job ran.
func cmdTick(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cron tick", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("verbose", false, "print idle / skipped messages too")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := routines.OpenStore()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := routines.Tick(ctx, store, routines.TickOptions{Webhook: WebhookDispatcherFromConfig()})
	if err != nil {
		return err
	}
	if res.Skipped {
		if *verbose {
			fmt.Fprintln(stdout, "tick skipped (tick.lock held by another process)")
		}
		return nil
	}
	if res.Fired == 0 {
		if *verbose {
			fmt.Fprintf(stdout, "tick: %d job(s) considered, none due\n", res.Considered)
		}
		return nil
	}
	if res.Fired == 1 {
		fmt.Fprintln(stdout, "ran 1 cron job")
	} else {
		fmt.Fprintf(stdout, "ran %d cron jobs\n", res.Fired)
	}
	return nil
}

// autoGenName returns "cron-<timestamp>-<rand>" — same shape
// as session / subagent / wt IDs but with a "cron-" prefix so
// users can spot auto-generated names in `seek cron list`.
func autoGenName() string {
	id := routines.NewRunID(time.Now().UTC())
	return "cron-" + id
}

// truncate shortens s to n chars, adding "…" when cut. Lives
// here rather than reaching into internal/tui's
// truncateForCheckpoint because CLI packages can't depend on
// TUI.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// cmdConfig dispatches `seek cron config <sub>`. Currently only `check`
// (validate push webhooks). Kept as a sub-namespace so future cron-level
// config inspection can land here without new top-level verbs.
func cmdConfig(args []string, stdout, stderr io.Writer) error {
	sub := "check"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "check":
		return cmdConfigCheck(args, stdout, stderr)
	default:
		return fmt.Errorf("unknown `cron config` subcommand %q (try `check`)", sub)
	}
}

// cmdConfigCheck validates ~/.seek/config.json's push_webhooks. By
// default it only checks each URL's scheme + format (offline). With
// --probe it sends a real test notification to each and reports the
// outcome — the recommended way to confirm a channel works BEFORE a
// 3am cron run silently fails to reach it (feature-mobile-push.md §D5).
func cmdConfigCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cron config check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	probe := fs.Bool("probe", false, "send a real test notification to each webhook")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(cfg.PushWebhooks) == 0 {
		fmt.Fprintln(stdout, "no push webhooks configured (push_webhooks in ~/.seek/config.json)")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	failures := 0
	for i, w := range cfg.PushWebhooks {
		format := w.Format
		if format == "" {
			format = "raw"
		}
		// Feishu (IM API) doesn't use a URL — label it by receive_id so
		// the user can tell multiple feishu targets apart; URL-based
		// formats keep the old URL label.
		var label string
		if format == routines.FormatFeishu {
			label = fmt.Sprintf("[%d] feishu app=%s receive=%s", i, w.AppID, w.ReceiveID)
		} else {
			label = fmt.Sprintf("[%d] %s (%s)", i, w.URL, format)
		}
		if err := routines.ValidateWebhookFormat(w.Format); err != nil {
			fmt.Fprintf(stdout, "  ✗ %s — %v\n", label, err)
			failures++
			continue
		}
		// URL gate is skipped for feishu (it goes through the IM API).
		if format != routines.FormatFeishu {
			if err := routines.ValidateWebhookURL(w.URL); err != nil {
				fmt.Fprintf(stdout, "  ✗ %s — %v\n", label, err)
				failures++
				continue
			}
		}
		if *probe {
			target := routines.WebhookTarget{
				URL:           w.URL,
				Format:        w.Format,
				Events:        w.Events,
				Template:      w.Template,
				AppID:         w.AppID,
				AppSecret:     w.AppSecret,
				ReceiveID:     w.ReceiveID,
				ReceiveIDType: w.ReceiveIDType,
			}
			if err := routines.SendTestWebhook(ctx, target, nil); err != nil {
				fmt.Fprintf(stdout, "  ✗ %s — probe failed: %v\n", label, err)
				failures++
				continue
			}
			fmt.Fprintf(stdout, "  ✓ %s — probe delivered\n", label)
			continue
		}
		fmt.Fprintf(stdout, "  ✓ %s\n", label)
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d webhook(s) failed validation", failures, len(cfg.PushWebhooks))
	}
	fmt.Fprintf(stdout, "%d webhook(s) OK\n", len(cfg.PushWebhooks))
	return nil
}
