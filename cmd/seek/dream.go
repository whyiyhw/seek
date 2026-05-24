package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// dreamRecentSessions is the cap on how many sessions get pulled into
// the dream prompt. Matches PRD §6 "最近 30 次 session". Anything past
// this is unlikely to surface fresh cross-project signal — older
// patterns should already be in M (via /distill).
const dreamRecentSessions = 30

// dreamSessionTail is how many trailing messages per session to ship
// to the thinker. The tail is where session conclusions live ("we
// decided to use X"). Earlier messages are usually exploratory and
// mostly noise for L-extraction.
const dreamSessionTail = 10

// dreamReasonerTimeout caps one dream round-trip. Thinking-mode latency on
// a fully-loaded prompt (30 sessions × 10 messages + N projects worth
// of M entries) is observed around 30-60 s; 3 minutes gives plenty of
// headroom for slow networks without hanging indefinitely.
const dreamReasonerTimeout = 3 * time.Minute

// runDream is the -dream CLI handler. Scans all project memory + recent
// session tails, calls thinking mode, prints candidates to stdout, and
// (when write=true) appends them to ~/.seek/soul.md's Pending section.
//
// Default (write=false) is preview-only by design: the user gets to
// eyeball what would land in L before committing. The PRD doesn't
// require this — but writing to L silently feels wrong for a feature
// whose whole point is "model learns about you" with a v1 emphasis on
// user control.
func runDream(ctx context.Context, client *deepseek.Client, write bool) error {
	// 1. Scan projects.
	projects, err := memory.ListProjects()
	if err != nil {
		return fmt.Errorf("dream: list projects: %w", err)
	}
	if len(projects) == 0 {
		fmt.Fprintln(os.Stderr, "dream: no project memory found in ~/.seek/projects/ — nothing to dream about yet")
		return nil
	}

	// 2. Scan recent sessions (best-effort: session-store failures
	//    degrade dream to projects-only rather than aborting).
	sessionInputs := collectRecentSessions()

	in := memory.DreamInput{
		Projects: make([]memory.DreamProject, 0, len(projects)),
		Sessions: sessionInputs,
	}
	for _, p := range projects {
		entries := p.Entries()
		if len(entries) == 0 {
			continue
		}
		in.Projects = append(in.Projects, memory.DreamProject{
			ID:      p.ID,
			Entries: entries,
		})
	}
	if len(in.Projects) == 0 {
		fmt.Fprintln(os.Stderr, "dream: all projects are empty — distill some entries first via /distill")
		return nil
	}

	fmt.Fprintf(os.Stderr, "dream: scanning %d project(s), %d recent session(s) → calling V4-Flash thinking …\n",
		len(in.Projects), len(in.Sessions))

	// 3. Run the thinking-mode round-trip.
	rctx, cancel := context.WithTimeout(ctx, dreamReasonerTimeout)
	defer cancel()
	dreamer := &memory.Dreamer{Client: client}
	candidates, err := dreamer.Dream(rctx, in)
	if err != nil {
		return fmt.Errorf("dream: %w", err)
	}

	// 4. Print candidates (always — preview is the default).
	if len(candidates) == 0 {
		fmt.Println("dream: no cross-project user traits met the ≥2-source threshold")
		return nil
	}
	fmt.Println()
	fmt.Printf("dream: %d L-pending candidate(s):\n\n", len(candidates))
	fmt.Println(memory.FormatLCandidatesMarkdown(candidates))

	// 5. Optionally write.
	if !write {
		fmt.Println()
		fmt.Println("(preview only — re-run with -dream-write to append to ~/.seek/soul.md)")
		return nil
	}

	soul, err := memory.LoadSoul()
	if err != nil {
		return fmt.Errorf("dream: load soul: %w", err)
	}
	newPending := memory.MergeIntoL(soul.Pending, candidates)
	// M5.10: evaluate existing Pending candidates for promotion / expiry.
	promoted, kept := memory.EvaluatePending(newPending, time.Now())
	if err := soul.ApplyMaintenance(promoted, kept); err != nil {
		return fmt.Errorf("dream: maintenance: %w", err)
	}
	fmt.Printf("\nwrote %d candidate(s) to %s (Pending section)", len(candidates), soul.Path)
	if len(promoted) > 0 {
		fmt.Printf(" + promoted %d to Stable", len(promoted))
	}
	fmt.Println()
	return nil
}

// collectRecentSessions pulls the last dreamRecentSessions session
// summaries from the store, loads each, and trims to the last
// dreamSessionTail messages. Best-effort: any single-session failure
// is skipped silently — the thinker can work with whatever survives.
func collectRecentSessions() []memory.DreamSession {
	store, err := session.NewStore()
	if err != nil {
		return nil
	}
	infos, _, err := store.List()
	if err != nil || len(infos) == 0 {
		return nil
	}
	if len(infos) > dreamRecentSessions {
		infos = infos[:dreamRecentSessions]
	}
	var out []memory.DreamSession
	for _, info := range infos {
		sess, err := store.Load(info.ID)
		if err != nil {
			continue
		}
		msgs := sess.Messages
		if len(msgs) > dreamSessionTail {
			msgs = msgs[len(msgs)-dreamSessionTail:]
		}
		// Drop any reasoning_content — long, low-signal, and
		// thinking mode doesn't need its own prior thoughts to extract
		// user traits.
		msgs = deepseek.StripReasoningContent(msgs)
		out = append(out, memory.DreamSession{ID: sess.ID, Messages: msgs})
	}
	return out
}
