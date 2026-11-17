package subagent

import (
	"fmt"
	"strings"
)

// MaxSummaryBytes caps the byte length of the subagent's final
// summary body in the parent tool result. The cap matches the
// "~4000 characters" hint the subagent template puts in the child
// system prompt (sysprompt.go summaryHint), so a well-behaved
// subagent never sees its summary truncated; the cap exists to bound
// pathologies (the model ignored the hint, the model hallucinated a
// dump of an entire file).
//
// 4096 is at the byte cap order of magnitude as grep's hard cap
// (internal/tools/grep), keeping seek's tool-output size budget
// consistent. Truncation appends "\n…(truncated)".
const MaxSummaryBytes = 4096

// truncationSuffix is appended when summary exceeds MaxSummaryBytes.
// Lives as a const so tests can assert on it without copy-pasting
// the literal.
const truncationSuffix = "\n…(truncated)"

// shortSid returns the random suffix of a sub_sid (the part after
// the last hyphen) — typically 16 hex chars. Used in wire-format
// footers as a stable, human-readable identifier without leaking
// the timestamp prefix that's mostly noise to the reader.
func shortSid(subSid string) string {
	if i := strings.LastIndex(subSid, "-"); i >= 0 && i < len(subSid)-1 {
		return subSid[i+1:]
	}
	return subSid
}

// headline derives the one-line headline for the "[agent: completed]"
// wire format from the subagent's final summary. Takes the first
// non-empty line, trimmed to at most 120 chars (matching the
// description field's effective limit). Returns "" if the summary
// has no usable first line — the caller still emits the prefix
// without a headline so the wire format stays parseable.
func headline(summary string) string {
	for line := range strings.SplitSeq(summary, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 120 {
			return trimmed[:120]
		}
		return trimmed
	}
	return ""
}

// truncateSummary enforces MaxSummaryBytes. Note: cuts at byte
// boundary which can split a UTF-8 codepoint — we step back to the
// previous valid boundary to avoid emitting a broken rune. The
// truncation suffix tells both the LLM (next turn) and the human
// reader that content was lost.
func truncateSummary(summary string) string {
	if len(summary) <= MaxSummaryBytes {
		return summary
	}
	limit := MaxSummaryBytes
	// Step back to the previous UTF-8 codepoint boundary so we
	// never emit a half-encoded rune. RFC 3629: continuation bytes
	// have the high bits 10xxxxxx.
	for limit > 0 && (summary[limit]&0xC0) == 0x80 {
		limit--
	}
	return summary[:limit] + truncationSuffix
}

// FormatCompleted builds the parent tool-result string for a
// successful subagent run, per docs/prd/feature-subagent.md §3.2.
//
// Layout:
//
//	[agent: completed] <headline>
//	<blank>
//	<truncated summary body>
//	<blank>
//	— sub-sid: <short> · turns: <n> · tokens: <p+c>
//
// The "[agent: completed]" prefix is wire-format contract (the
// parent transcript-side parser pattern from plan-mode reconstruct).
// Everything after the headline line is display — formatting may
// change without breaking consumers that match on the prefix only.
func FormatCompleted(subSid string, summary string, turns int, tokens Tokens) string {
	var sb strings.Builder
	sb.WriteString("[agent: completed] ")
	sb.WriteString(headline(summary))
	sb.WriteString("\n\n")
	sb.WriteString(truncateSummary(summary))
	sb.WriteString("\n\n— sub-sid: ")
	sb.WriteString(shortSid(subSid))
	fmt.Fprintf(&sb, " · turns: %d · tokens: %d", turns, tokens.Prompt+tokens.Completion)
	return sb.String()
}

// FormatContinued builds the parent tool-result string for a
// SUCCESSFUL continuation run (Manager.Continue). Identical layout
// to FormatCompleted plus one footer token — the resumed-from
// lineage — inserted after the sub-sid:
//
//	[agent: completed] <headline>
//	<blank>
//	<truncated summary body>
//	<blank>
//	— sub-sid: <new> · resumed-from: <old> · turns: <n> · tokens: <p+c>
//
// The "[agent: completed]" prefix is deliberately the SAME token as
// a fresh run's (a continuation that completed IS a completed run;
// parsers matching on the prefix keep working). The lineage sits
// after the closing footer tokens per the wire-format discipline
// (additions go after closed tokens, never inside them).
func FormatContinued(subSid, fromSid, summary string, turns int, tokens Tokens) string {
	var sb strings.Builder
	sb.WriteString("[agent: completed] ")
	sb.WriteString(headline(summary))
	sb.WriteString("\n\n")
	sb.WriteString(truncateSummary(summary))
	sb.WriteString("\n\n— sub-sid: ")
	sb.WriteString(shortSid(subSid))
	sb.WriteString(" · resumed-from: ")
	sb.WriteString(shortSid(fromSid))
	fmt.Fprintf(&sb, " · turns: %d · tokens: %d", turns, tokens.Prompt+tokens.Completion)
	return sb.String()
}

// FormatFailed builds the parent tool-result string for a failed /
// canceled / killed subagent run. The reason is part of the wire-
// format prefix per PRD §3.2 (enclosed in the bracket, so parsers
// see `[agent: failed reason=<X>]` as a single matchable token).
//
// hint is a short human-readable explanation appended after the
// closing bracket; can be empty.
func FormatFailed(subSid string, reason string, hint string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[agent: failed reason=%s]", reason)
	if hint != "" {
		sb.WriteByte(' ')
		sb.WriteString(hint)
	}
	sb.WriteString("\n\n— sub-sid: ")
	sb.WriteString(shortSid(subSid))
	return sb.String()
}

// ----- background wire formats (feature-orchestration PRD §4.1 D1/D2) -----
//
// Prefix contract for run_in_background spawns: the immediate tool
// result is `[agent: background started sub-sid=<full>]` and the
// terminal notice (delivered as a <context> Prepend on a later turn)
// is `[agent: background done sub-sid=<full>]` or
// `[agent: background failed reason=<X>]`. Background failure reuses
// the `reason=` closed enum of the sync path (canceled / timeout /
// spawn_error / killed). Additions go AFTER the closing bracket —
// never inside it (wire-format discipline, same as `[plan: approved]`).

// FormatBackgroundStarted builds the immediate tool-result string the
// `agent` tool returns for run_in_background=true. The parent turn
// gets this back right away and keeps working; the final summary
// arrives separately as a <context> notification on a later turn.
func FormatBackgroundStarted(subSid string, description string) string {
	var sb strings.Builder
	sb.WriteString("[agent: background started sub-sid=")
	sb.WriteString(subSid)
	sb.WriteString("] ")
	sb.WriteString(description)
	sb.WriteString("\n\n— runner active; final summary will arrive as a <context> note on a later turn (or on resume). Track via /agents.")
	return sb.String()
}

// FormatBackgroundDone builds the terminal notice body for a
// background subagent that completed successfully. Same layout as
// FormatCompleted but under the `[agent: background done ...]` prefix
// so transcript-side parsers can tell "I already have this result"
// (background notice) from "this was my blocking tool result"
// (completed).
func FormatBackgroundDone(subSid string, summary string, turns int, tokens Tokens) string {
	var sb strings.Builder
	sb.WriteString("[agent: background done sub-sid=")
	sb.WriteString(subSid)
	sb.WriteString("] ")
	sb.WriteString(headline(summary))
	sb.WriteString("\n\n")
	sb.WriteString(truncateSummary(summary))
	sb.WriteString("\n\n— sub-sid: ")
	sb.WriteString(shortSid(subSid))
	fmt.Fprintf(&sb, " · turns: %d · tokens: %d", turns, tokens.Prompt+tokens.Completion)
	return sb.String()
}

// FormatBackgroundContinued is FormatBackgroundDone's continuation
// counterpart: same layout plus the resumed-from lineage token in
// the footer, mirroring FormatContinued's addition to
// FormatCompleted. The "[agent: background done sub-sid=<full>]"
// prefix is IDENTICAL to a fresh background run's so prefix parsers
// can't tell (and don't need to tell) a continuation from a spawn.
func FormatBackgroundContinued(subSid, fromSid, summary string, turns int, tokens Tokens) string {
	var sb strings.Builder
	sb.WriteString("[agent: background done sub-sid=")
	sb.WriteString(subSid)
	sb.WriteString("] ")
	sb.WriteString(headline(summary))
	sb.WriteString("\n\n")
	sb.WriteString(truncateSummary(summary))
	sb.WriteString("\n\n— sub-sid: ")
	sb.WriteString(shortSid(subSid))
	sb.WriteString(" · resumed-from: ")
	sb.WriteString(shortSid(fromSid))
	fmt.Fprintf(&sb, " · turns: %d · tokens: %d", turns, tokens.Prompt+tokens.Completion)
	return sb.String()
}

// FormatBackgroundFailed is FormatFailed's background counterpart —
// same closed reason enum, background prefix so parsers (and the
// model reading its own transcript on resume) can distinguish the
// async delivery channel from the sync tool result.
func FormatBackgroundFailed(subSid string, reason string, hint string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[agent: background failed reason=%s]", reason)
	if hint != "" {
		sb.WriteByte(' ')
		sb.WriteString(hint)
	}
	sb.WriteString("\n\n— sub-sid: ")
	sb.WriteString(shortSid(subSid))
	return sb.String()
}

// ----- resume-backfill wire formats (feature-orchestration PRD §4.1 D3) -----
//
// These build the Turn-1 notices the resume path rehydrates from disk
// (fold subagents.jsonl + read the child transcript tail). Two shapes:
//
//   - FormatBackgroundResumed: a completed run whose summary is
//     recoverable from the child transcript. Same
//     `[agent: background done sub-sid=<full>]` prefix token as the
//     live D2 notice (a parser can't tell and doesn't need to tell a
//     resumed delivery from a live one); the footer omits `turns:`
//     because the turn count is deliberately not persisted in the
//     index (D5 — Cost 不入索引, and turns with it) and fabricating a
//     0 would be a lie in a wire format.
//   - FormatBackgroundLost: the one-line status note for terminal
//     background runs with NO trusted summary — orphaned (owning
//     process died before terminal state), or a completed run whose
//     transcript is gone. New `lost` prefix token in the background
//     family; the reason enum inside `[agent: background failed
//     reason=...]` stays closed (D3: orphaned/failed 回灌一行状态
//     注记, not a summary).

// FormatBackgroundResumed builds the D3 resume notice for a completed
// background run: live-D2 layout (headline + body) under the done
// prefix, with the disk-recoverable footer (sub-sid, lineage, tokens)
// and a resume-delivery marker so the model understands why a result
// it never saw a handle-completion for arrives now.
func FormatBackgroundResumed(subSid, fromSid, summary string, tokens Tokens) string {
	var sb strings.Builder
	sb.WriteString("[agent: background done sub-sid=")
	sb.WriteString(subSid)
	sb.WriteString("] ")
	sb.WriteString(headline(summary))
	sb.WriteString("\n\n")
	sb.WriteString(truncateSummary(summary))
	sb.WriteString("\n\n— sub-sid: ")
	sb.WriteString(shortSid(subSid))
	if fromSid != "" {
		sb.WriteString(" · resumed-from: ")
		sb.WriteString(shortSid(fromSid))
	}
	fmt.Fprintf(&sb, " · tokens: %d — delivered on session resume", tokens.Prompt+tokens.Completion)
	return sb.String()
}

// FormatBackgroundLost builds the D3 one-line status note for a
// terminal background run whose summary cannot be trusted or read:
// orphaned runs (the owning seek process died before writing a
// terminal event — no summary was ever produced) and completed runs
// whose transcript.jsonl is missing (记录在、transcript 失联). PRD D3:
// these get a status annotation, never a fabricated summary.
func FormatBackgroundLost(subSid string, status Status, description string) string {
	var sb strings.Builder
	sb.WriteString("[agent: background lost sub-sid=")
	sb.WriteString(subSid)
	sb.WriteString("] status=")
	sb.WriteString(string(status))
	if description != "" {
		sb.WriteString(" — ")
		sb.WriteString(description)
	}
	sb.WriteString("\n\n— no summary available (")
	switch status {
	case StatusOrphaned:
		sb.WriteString("owning process died before this run finished")
	default:
		sb.WriteString("transcript missing")
	}
	sb.WriteString("); check /agents or re-spawn the task if still needed.")
	return sb.String()
}
