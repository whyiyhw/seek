// Package uitracecli implements `seek ui-trace ...` — the read side of
// the local user-interaction trace (internal/uitrace).
//
// Follows the skillcli / memorycli / hookscli pattern: stdout+stderr
// are injected so the TUI could later render the same report inline,
// and a missing file is a UX state ("no interactions recorded yet"),
// not a programmer error.
//
//	seek ui-trace                  — folded summary of the whole file
//	seek ui-trace --rows           — the raw rows, newest first
//	seek ui-trace --since 7d       — restrict either view to a window
//	seek ui-trace --json           — machine-readable output
//
// Why a CLI at all rather than "just jq the file": the definitions of
// the ratios (intervened / delivered / withdrawn) must live in exactly
// one place, or two reports computed from the same rows will disagree.
// internal/uitrace.Fold is that place; this command is its printer.
package uitracecli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/uitrace"
)

// Run dispatches a `seek ui-trace ...` invocation.
func Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ui-trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.Duration("since", 0, "only rows newer than this duration (e.g. 24h, 168h)")
	rows := fs.Bool("rows", false, "print the raw rows (newest first) instead of the summary")
	limit := fs.Int("limit", 40, "with --rows: max rows to print (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path, err := paths.UITraceLog()
	if err != nil {
		fmt.Fprintln(stderr, "ui-trace:", err)
		return nil
	}
	all, err := uitrace.Read(path)
	if err != nil {
		fmt.Fprintln(stderr, "ui-trace:", err)
		return nil
	}
	events := filterSince(all, *since)

	if *asJSON {
		return printJSON(stdout, path, events, *rows, *limit)
	}
	if len(events) == 0 {
		fmt.Fprintf(stdout, "no interactions recorded yet (%s)\n", path)
		fmt.Fprintln(stdout, "rows appear when you type while a turn is streaming, press Esc mid-turn, or /compact.")
		return nil
	}
	if *rows {
		printRows(stdout, events, *limit)
		return nil
	}
	printSummary(stdout, path, uitrace.Fold(events))
	return nil
}

// filterSince keeps rows at or after now-since. Rows with an unparsable
// timestamp are kept (dropping them silently would undercount) — the
// window is a convenience filter, not an integrity check.
func filterSince(events []uitrace.Event, since time.Duration) []uitrace.Event {
	if since <= 0 {
		return events
	}
	cutoff := time.Now().Add(-since)
	out := make([]uitrace.Event, 0, len(events))
	for _, e := range events {
		t, err := time.Parse(time.RFC3339, e.TS)
		if err != nil || !t.Before(cutoff) {
			out = append(out, e)
		}
	}
	return out
}

func printSummary(w io.Writer, path string, s uitrace.Summary) {
	fmt.Fprintf(w, "ui-trace: %s\n", path)
	fmt.Fprintf(w, "window:   %s .. %s  (%d rows)\n", s.FirstTS, s.LastTS, s.Rows)
	fmt.Fprintln(w)

	// The headline ratio. Interventions that never reached the model are
	// the interesting gap: it is the honest cost of the fallback paths.
	fmt.Fprintf(w, "intervened %d  (inject %d / queue %d / steer %d)\n",
		s.Intervened, s.ByEvent[uitrace.EventInject], s.ByEvent[uitrace.EventQueue], s.ByEvent[uitrace.EventSteer])
	fmt.Fprintf(w, "delivered  %d\n", s.Delivered)
	if s.Intervened > 0 {
		fmt.Fprintf(w, "reached the model: %.0f%% of interventions\n",
			100*float64(s.Delivered)/float64(s.Intervened))
	}
	fmt.Fprintf(w, "withdrawn  %d\n", s.Withdrawn)
	fmt.Fprintf(w, "cancelled  %d\n", s.Canceled)
	fmt.Fprintln(w)

	if n := s.ByEvent[uitrace.EventCompact]; n > 0 {
		// The token total is the pre-compact context sizes summed — the
		// number a "is auto-compact firing too early?" pass looks at.
		// The duration total is the wait it cost; printing the mean
		// alongside keeps "one 60s stall" distinguishable from "ten
		// fast compactions" (a sum alone can't tell them apart). No
		// early return here: the goal/guidance lines below must still print.
		if total := s.DurMSByEvent[uitrace.EventCompact]; total > 0 {
			fmt.Fprintf(w, "compactions %d  (%d prompt tokens rewritten, %dms total / %dms mean)\n",
				n, s.TokensByEvent[uitrace.EventCompact], total, total/int64(n))
		} else {
			fmt.Fprintf(w, "compactions %d  (%d prompt tokens rewritten)\n", n, s.TokensByEvent[uitrace.EventCompact])
		}
	}
	if n := s.ByEvent[uitrace.EventGoal]; n > 0 {
		fmt.Fprintf(w, "goal verdicts %d\n", n)
	}
	if n := s.ByEvent[uitrace.EventGuidance]; n > 0 {
		// Guidance is machine-authored: it shares the intervention
		// denominator with human break-ins, so it is reported
		// separately rather than folded into "intervened".
		fmt.Fprintf(w, "guidance hints %d\n", n)
	}
}

func printRows(w io.Writer, events []uitrace.Event, limit int) {
	start := 0
	if limit > 0 && len(events) > limit {
		start = len(events) - limit
	}
	fmt.Fprintf(w, "%-20s  %-9s  %-5s  %-6s  %-8s  %s\n", "TS", "EVENT", "TURN", "CHARS", "DUR_MS", "DETAIL")
	for i := len(events) - 1; i >= start; i-- {
		e := events[i]
		fmt.Fprintf(w, "%-20s  %-9s  %-5d  %-6d  %-8d  %s\n",
			e.TS, e.Event, e.Turn, e.Chars, e.DurMS, e.Detail)
	}
}

func printJSON(w io.Writer, path string, events []uitrace.Event, rows bool, limit int) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if rows {
		start := 0
		if limit > 0 && len(events) > limit {
			start = len(events) - limit
		}
		// Newest first, same as the table view.
		out := make([]uitrace.Event, 0, len(events)-start)
		for i := len(events) - 1; i >= start; i-- {
			out = append(out, events[i])
		}
		return enc.Encode(struct {
			Path string          `json:"path"`
			Rows []uitrace.Event `json:"rows"`
		}{Path: path, Rows: out})
	}
	return enc.Encode(struct {
		Path string          `json:"path"`
		Sum  uitrace.Summary `json:"summary"`
	}{Path: path, Sum: uitrace.Fold(events)})
}
