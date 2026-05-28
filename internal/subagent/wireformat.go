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
// the last hyphen) — typically 6 hex chars. Used in wire-format
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
	for _, line := range strings.Split(summary, "\n") {
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
