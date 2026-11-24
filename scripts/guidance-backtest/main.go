package main

// replay — offline backtest of internal/guidance against real session
// transcripts. Not part of the shipped binary (this file lives under
// scripts/), because it exists to answer ONE question before the rules
// earn a permanent place: would they have fired on real work, and would
// the firings have been tolerable?
//
// The question is not rhetorical. The bash success-path advisory fired
// 3,871 times and changed nothing (docs/pitfalls/eval/success-path-hints-
// do-not-teach). Firing COUNT alone cannot distinguish "useful nudge"
// from "expensive noise", but it can rule out the worst case before a
// rule ever reaches a live session — and it tells us whether a threshold
// is calibrated for real tool cadence or only for the test fixtures.
//
// Usage:
//
//	go run ./scripts/guidance-backtest [-dir ~/.seek/sessions] [-v]
//
// Fidelity note — what this DOES and DOES NOT model:
//
//   - It replays the exact TurnBrief the loop builds: one brief per
//     assistant turn WITH tool calls, tools in dispatch order, TextChars
//     = len(assistant content).
//   - It consults Guidance only at the in-loop boundary (turn > 0), the
//     only place the live loop calls it (the settle point is excluded
//     there, and here).
//   - It cannot know about human injections (QueueUserMessage supersedes
//     guidance at the same boundary). Transcripts on disk do not record
//     which user messages were injections. So these firing counts are an
//     UPPER BOUND.
//   - It cannot know about Esc or MaxTurns exhaustion; a rule's lifetime
//     cap (2 per session) is the only ceiling applied.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/guidance"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ruleStat aggregates one rule across the whole replay.
type ruleStat struct {
	sessions int   // sessions where it fired at least once
	firings  int   // total firings
	gaps     []int // turns between consecutive firings of this rule, same session
}

func main() {
	dir := flag.String("dir", "", "sessions dir (default: ~/.seek/sessions)")
	verbose := flag.Bool("v", false, "print one line per firing")
	maxPrint := flag.Int("max-print", 40, "with -v: cap printed firings (0 = all)")
	flag.Parse()

	if *dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		*dir = filepath.Join(home, ".seek", "sessions")
	}

	agg := map[string]*ruleStat{
		guidance.RuleRetryLoop:  {},
		guidance.RuleFailureRun: {},
	}

	type totals struct{ disk, replayed, skipped, parseErrs int }
	var t totals
	var turnCounts []int
	printed := 0

	files, err := os.ReadDir(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", *dir, err)
		os.Exit(1)
	}

	for _, e := range files {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		t.disk++
		msgs, err := loadMessages(filepath.Join(*dir, e.Name()))
		if err != nil {
			// Unreadable (permissions) or truncated transcripts are
			// skipped, not fatal: a backtest over a real store must
			// survive its own messy history.
			if os.IsPermission(err) {
				t.skipped++
			} else {
				t.parseErrs++
			}
			continue
		}
		if len(msgs) == 0 {
			t.skipped++
			continue
		}
		t.replayed++
		turnCounts = append(turnCounts, countToolTurns(msgs))

		g := guidance.New()
		turn := 0
		lastFire := map[string]int{}
		firedHere := map[string]bool{}
		for i := 0; i < len(msgs); i++ {
			m := msgs[i]
			if m.Role != deepseek.RoleAssistant || len(m.ToolCalls) == 0 {
				continue
			}
			tools := make([]agent.ToolBrief, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				res := ""
				for j := i + 1; j < len(msgs); j++ {
					if msgs[j].Role == deepseek.RoleTool && msgs[j].ToolCallID == tc.ID {
						res = msgs[j].Content
						break
					}
				}
				tools = append(tools, agent.ToolBrief{
					Name:   tc.Function.Name,
					Args:   tc.Function.Arguments,
					Result: res,
				})
			}
			turn++
			// turn > 0: the live loop exempts the first turn, and this
			// replay must not credit a rule with a firing the loop would
			// never make.
			if turn == 1 {
				continue
			}
			brief := agent.TurnBrief{Index: turn - 1, Tools: tools, TextChars: len(m.Content)}
			text := g.Advise(brief)
			if text == "" {
				continue
			}
			rule := g.LastAdviceRule()
			st := agg[rule]
			if st == nil {
				continue
			}
			st.firings++
			firedHere[rule] = true
			if prev, ok := lastFire[rule]; ok {
				st.gaps = append(st.gaps, brief.Index-prev)
			}
			lastFire[rule] = brief.Index
			if *verbose && (*maxPrint == 0 || printed < *maxPrint) {
				fmt.Printf("%-40s turn %-3d %-12s %s\n",
					trimID(e.Name()), brief.Index, rule, firstLine(text, 90))
				printed++
			}
		}
		for rule := range firedHere {
			agg[rule].sessions++
		}
	}

	report(t, turnCounts, agg)
}

// loadMessages reads a session JSONL: line 1 is the header, the rest are
// messages. Malformed message lines are skipped (an interrupted write
// leaves a torn tail; the rest of the file is still useful).
func loadMessages(path string) ([]deepseek.Message, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	var out []deepseek.Message
	for i, ln := range lines {
		if i == 0 || strings.TrimSpace(ln) == "" {
			continue
		}
		var m deepseek.Message
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func countToolTurns(msgs []deepseek.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == deepseek.RoleAssistant && len(m.ToolCalls) > 0 {
			n++
		}
	}
	return n
}

func trimID(name string) string { return strings.TrimSuffix(name, ".jsonl") }

func firstLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func report(t struct{ disk, replayed, skipped, parseErrs int }, turnCounts []int, agg map[string]*ruleStat) {
	sort.Ints(turnCounts)
	fmt.Printf("sessions on disk   : %d\n", t.disk)
	fmt.Printf("replayed           : %d  (skipped %d unreadable/empty, %d parse errors)\n",
		t.replayed, t.skipped, t.parseErrs)
	if len(turnCounts) > 0 {
		fmt.Printf("tool turns/session : median %d, p90 %d, max %d\n",
			turnCounts[len(turnCounts)/2],
			turnCounts[len(turnCounts)*9/10],
			turnCounts[len(turnCounts)-1])
	}
	fmt.Println()

	names := []string{guidance.RuleRetryLoop, guidance.RuleFailureRun}
	fmt.Printf("%-14s  %8s  %8s  %9s  %s\n", "RULE", "SESSIONS", "FIRINGS", "PER-SESSION", "MEDIAN GAP")
	for _, n := range names {
		a := agg[n]
		per := 0.0
		if a.sessions > 0 {
			per = float64(a.firings) / float64(a.sessions)
		}
		gap := "—"
		if len(a.gaps) > 0 {
			g := append([]int(nil), a.gaps...)
			sort.Ints(g)
			gap = fmt.Sprintf("%d turns", g[len(g)/2])
		}
		fmt.Printf("%-14s  %8d  %8d  %11.2f  %s\n", n, a.sessions, a.firings, per, gap)
	}
	fmt.Println()
	fmt.Println("UPPER BOUND: transcripts do not record human injections, which")
	fmt.Println("supersede guidance at the same boundary — real firings are ≤ these.")
}
