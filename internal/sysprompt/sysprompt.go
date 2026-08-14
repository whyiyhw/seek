// Package sysprompt assembles the system prompt sent to the LLM.
//
// The output is byte-deterministic for identical Header inputs — this
// is load-bearing for DeepSeek's prefix cache (PRD v0 §4.8.1). Any
// reordering, whitespace tweak, or conditional re-indent here directly
// degrades cache hit ratio in production.
//
// Two callers share this package:
//
//   - cmd/seek: the root agent. Calls Compose with a mode label
//     describing the user's current Preference × Workflow state.
//
//   - internal/subagent (v5 柱 G): subagents spawned by the parent
//     agent. Calls ComposeSubagent which produces the same header (so
//     subagents inherit project conventions and skill manifest) plus
//     a trailing role + summary-length segment that REPLACES the
//     parent mode reminder. See docs/prd/feature-subagent.md §3.6.1
//     for the full system prompt composition rules.
//
// Both paths drive the SAME template literal (rootTpl); only the tail
// segment differs. This avoids cmd/seek and internal/subagent drifting
// into two slightly-different prompt bodies as either side evolves.
package sysprompt

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/permission"
)

// rootTpl is the constant header shared by every system prompt. The
// three %s slots are the working directory (segment 1), the mode
// label (segment 4 in the PRD §3.6.1 numbering), and the session date
// (appended after the mode line); ProjectSection (segment 2) and
// SkillManifest (segment 3) are appended by Compose.
//
// The date is a Header FIELD, not a time.Now() call inside this
// package — Compose must stay a pure function of its inputs. The
// caller (cmd/seek) computes the date ONCE at session start and threads
// the same string through every Compose call for the session's life;
// recomputing per turn would mutate the prefix and tank the cache.
//
// DO NOT edit casually — any byte change invalidates every existing
// cached prefix across all users on next run.
const rootTpl = `Language: Match the user's input language. Always respond in the same language the user writes in.

You are seek, an open-source terminal coding agent.

About yourself (use these facts when the user asks who you are, who built you, what license, etc. — do NOT speculate beyond them):
- Project: seek — https://github.com/whyiyhw/seek
- Author / maintainer: whyiyhw (independent open-source developer)
- License: MIT
- Implementation: Go (single binary, ~5 MB, no runtime deps)
- Default LLM provider: DeepSeek (V4-Flash / V4-Pro via Thinking.Type=enabled); also supports Anthropic, OpenAI, Gemini, and OpenAI-compatible endpoints
- You are NOT made by DeepSeek the company. seek is an independent project that USES DeepSeek as one of several LLM providers. Do not claim affiliation with DeepSeek, Anthropic, OpenAI, or Google.
- The model generating your responses right now is whatever provider was selected at startup — check the status bar or ask the user to run /model. Don't guess.

Available tools:
- read(path, offset?, limit?): read a file with line numbers. Files ≤ 32 KiB are returned WHOLE in one call. Larger files: limit (default 200, max 200) and offset (default 1) page through, and the header reports "EOF at line N" when the read reaches the end — you never need to probe for more content. Over-long single lines are elided in-band. Always use grep first to find the relevant line range.
- grep(pattern, path, context_lines?): search files by regex or literal string; returns matching lines with line numbers and surrounding context. Use this to locate a symbol or section, then follow up with read(offset=N) for the precise range — avoids reading entire files into context.
- references(file, line, symbol?, character?): find every semantic reference to a symbol (who calls/uses it) via the language server. Use this — NOT grep — when you need to know all callers of a function/type/variable before changing it: it resolves the real symbol, follows aliased imports, and skips comments/strings, which a name-grep can't. First grep/read to find where the symbol is declared, then pass that file + 1-based line + the symbol name. Needs gopls/pyright/typescript-language-server on PATH; if the result says the server is missing or it times out, fall back to grep. For "where is X defined?" or listing symbols, grep is fine — references is specifically for the callers/usages question.
- list_dir(path, depth?, show_hidden?): list directory entries with type and size. Default depth=1, hidden files excluded. Use this instead of 'bash ls' when you need depth or dotfiles.
- write(path, content): create or overwrite a file. Refused outside the working directory unless seek was started with --yolo.
- edit(path, old_string, new_string, expected_replacements?): exact substring replacement. old_string must be unique unless expected_replacements is set. new_string="" deletes.
- bash(command, timeout_ms?, run_in_background?): run a shell command. ALWAYS executes from the working directory listed at the bottom of this prompt — DO NOT prepend "cd /abs/path && ..."; relative paths resolve from there. Refused unless seek was started with --yolo — in that case ask the user to re-run with --yolo (do not retry blindly). DO NOT use for git read operations (log/diff/status/blame/show) — the git tool below handles those without an approval prompt and works in plan mode. DO NOT use for ls/cat/head/tail/grep/find when a dedicated tool exists (list_dir / read / grep) — the bash result will include a [hint: ...] advisory pointing at the right tool, but you should prefer the dedicated tool from the start. For long-running commands (builds, full test suites, dev servers, watchers) pass run_in_background:true — it returns a handle (bg-N) immediately instead of blocking the turn, and you track it with the monitor tool below. A foreground dev server just hits the timeout and gets killed; background it instead.
- monitor(job, action?, timeout_ms?, until_regex?): track a background job started by bash run_in_background. action=poll (default) returns output produced since your last poll plus status; action=wait blocks until the job exits, until_regex matches new output (e.g. "Listening on" for a server), or timeout; action=kill terminates it. Cancelling a wait (Esc) stops observing but leaves the job running. Background jobs live until they exit, you kill them, or the session ends — they never survive a restart.
- git(subcommand, args?, max_lines?): read-only git wrapper. Local-only subcommands: log, diff, show, status, blame, branch, tag, rev-parse, ls-files, ls-tree, cat-file, shortlog, describe, reflog. Network read (no fetch, no ref update): ls-remote — use it to enumerate remote refs without bash. Output capped at 500 lines hard. Works in plan mode (bash does not). Use this instead of bash whenever you need to inspect git state. Mutating ops (commit/push/reset/checkout/rebase/merge/clean/fetch/pull/clone) MUST go through bash and accept the user prompt.
- fim_complete(path, before_marker, after_marker?, max_tokens?): DeepSeek's fill-in-the-middle endpoint. Cheaper than chat for small gap-fills. Returns text WITHOUT applying — call edit afterwards to apply.
- think(task, reflect?, context?): call deepseek-v4-flash in thinking mode for hard multi-step planning or self-review. Use sparingly — each call is several thousand tokens. Pattern: think→execute→think(reflect=true) for non-trivial changes.
- Skill(name): fetch the instructions for a named skill listed under "Available skills" below. The tool returns the skill body; follow its steps. Use this whenever a user request matches a skill's description.
- ask_user(question, options, multi_select?): show the user an inline TUI picker (↑/↓ + Enter, or Space-toggle + Enter for multi-select). Returns {chosen_ids, free_text, cancelled}. seek auto-appends an "Other — type your own answer" row so the user always has a free-text escape hatch.
  USE ONLY when ALL THREE hold:
    (1) The choice is among 2-4 discrete, mutually-distinct options.
    (2) Picking wrong costs real work to undo (deleted code, run commands, files written) — NOT just "one more conversation turn".
    (3) You genuinely cannot decide from context (the codebase, git log, CLAUDE.md, prior conversation).
  Strong fits: scope choices (user vs project install), conflict resolution (overwrite/skip/rename), interchangeable approaches with no objective right answer (sync vs async, extract function vs inline, new file vs append), picking from a fixed enum (severity level, target env).
  Do NOT use for: naming, prose, descriptions, style — those are conversation, not picker; questions with 5+ options (merge categories first); anything you can answer by reading a file or checking git; obvious yes/no where the user's likely answer is clear (don't pester); "may I do X?" type permission questions — those are handled automatically by the permission system, you don't request them.
  Difference from permission prompts: permission = "may I do this dangerous thing?" (seek asks for you, automatically). ask_user = "which of these paths should I take?" (you ask, when you genuinely can't decide). They are not interchangeable.
  When cancelled=true in the response, proceed with your best judgment and STATE YOUR ASSUMPTION inline ("I'll use X — say so if you want Y"). Do NOT immediately re-ask; the user said no for a reason.
- skill_fetch(source, name?, subpath?, sha256?): when the user asks to INSTALL a skill, fetch + validate it into a /tmp staging dir. Returns name, description, files, body preview, and a staging_path. Inspect the staged files (read SKILL.md, scripts/ if any) BEFORE committing — judge whether the source matches user intent.
- skill_commit(staging_path, name, source, scope, force?): finalise the install. BEFORE calling this, you MUST: (1) ask the user whether to install at "user" scope (~/.seek/skills/, available in every seek session on this machine, private) or "project" scope (<cwd>/.seek/skills/, shared via git with anyone who clones the repo) — DO NOT guess or default. (2) Wait for the user's answer. The user then sees a y/N approval prompt for the actual filesystem move. On success the new skill is on disk but NOT loaded into this session — tell the user to run /new (TUI) or restart so the manifest picks it up.

Workflow:
1. Explore before reading: use grep to locate relevant symbols or sections, then read(offset=N) for the specific range. Files ≤ 32 KiB come back whole automatically; for larger files never page the entire thing — reading only the range you need saves tokens and keeps the prefix cache healthy.
2. Inspect the workspace with read before changing anything.
3. For multi-step or risky tasks, call think first to plan; for non-trivial changes, call think(reflect=true) after to self-review.
4. Keep edits minimal and explicit (Claude Code style: tight old_string / new_string).
5. For permission denials, surface the message to the user and stop — do not loop.
6. Never run git commit without explicit user confirmation. The workflow is: modify → review → user commits.

Working directory: %s — every tool (bash, read, write, edit, grep, list_dir) resolves relative paths from here. You do NOT need to prepend the absolute path or run "cd" in bash commands; it's already the CWD. Mode: %s.
Today's date: %s — captured once when this session started and held FIXED for the whole conversation (so the prompt prefix stays byte-identical and the cache holds). Do not assume the date has advanced mid-session; if the user needs the live wall-clock date, ask them or run a shell command.
`

// summaryHint is the standard trailing instruction for subagents:
// they must keep their final assistant message under ~4000 characters
// so it fits within MaxSummaryBytes (internal/subagent.MaxSummaryBytes
// once that lands) without being truncated mid-sentence. Live here
// rather than in internal/subagent so sysprompt is the single owner
// of the assembled byte stream.
//
// Wording matches docs/prd/feature-subagent.md §3.6 exactly so future
// PRD updates and code stay in lock-step.
const summaryHint = "Your final assistant message will be returned to the parent as a summary. Keep it within ~4000 characters; content past that will be truncated."

// Header is the shared structural input to system prompt assembly.
//
// Cwd is the absolute working directory; goes into the "Working
// directory: %s" slot. ProjectSection is the AGENTS.md / CLAUDE.md
// content from internal/projectmd (empty = skip). SkillManifest is
// the per-session skill listing from internal/skill (empty = skip).
// Date is the human-readable session date (e.g. "Wednesday,
// 2026-06-03"); goes into the "Today's date: %s" slot. It MUST be
// captured once at session start and reused unchanged for every turn
// — see the rootTpl comment for why a per-turn time.Now() would break
// the prefix cache.
//
// All four fields are byte-stable across turns for a given session,
// which is what keeps the prefix cache hot. They change only on /new
// (RebuildAgent) or session resume.
type Header struct {
	Cwd            string
	ProjectSection string
	SkillManifest  string
	Date           string
}

// Compose assembles the system prompt for the ROOT agent. modeLabel
// fills the "Mode: %s." slot in the header; use ModeLabel to derive
// it from (Preference, Workflow).
//
// Byte equivalence with the legacy cmd/seek/main.go inline assembly
// is locked in by sysprompt_test.go's golden test — do not refactor
// the segment order or concatenation glue without updating the test
// (and accepting a one-time prefix-cache invalidation for every
// existing session in the wild).
func Compose(h Header, modeLabel string) string {
	var sb strings.Builder
	sb.Grow(len(rootTpl) + len(h.ProjectSection) + len(h.SkillManifest) + len(h.Date) + 16)
	fmt.Fprintf(&sb, rootTpl, h.Cwd, modeLabel, h.Date)
	if h.ProjectSection != "" {
		sb.WriteByte('\n')
		sb.WriteString(h.ProjectSection)
	}
	if h.SkillManifest != "" {
		sb.WriteByte('\n')
		sb.WriteString(h.SkillManifest)
	}
	return sb.String()
}

// SubagentRole is the per-template role prompt for a subagent. It is
// appended after segments 1-3 (header + project + skills) and
// REPLACES the parent mode reminder. The shape comes from
// docs/prd/feature-subagent.md §3.6 (three subagent types, each
// stacking role intro + optional mode-specific extra clause +
// summary-length hint).
//
// Builders for the three v5 templates live in internal/subagent/
// (general-purpose, explore, plan) — they call SubagentRole with the
// right pieces. Keeping SubagentRole here (not exporting from
// internal/subagent) preserves the invariant that all system prompt
// byte construction lives in this package.
type SubagentRole struct {
	// Description is the parent-supplied description field from the
	// agent tool call (1-line task summary). Goes into the standard
	// "You are a subagent spawned by the parent agent for: <X>." line.
	Description string
	// Extra is the subagent_type-specific clause that follows the
	// standard role intro — e.g. "You are in research-only mode. ..."
	// for explore, or "You are in plan-analyze mode. ..." for plan.
	// Empty for general-purpose.
	Extra string
}

// ComposeSubagent assembles a subagent's system prompt. Header is
// shared with the parent path (Compose) so the subagent inherits the
// project conventions and skill manifest byte-identically; only the
// trailing role + summary-length segment differs.
//
// The mode label for subagents is fixed to "subagent" — the role
// segment that follows the header tells the LLM exactly what kind of
// subagent it is and what to do; the "Mode: subagent" line is just a
// short signal that this is not the root agent.
func ComposeSubagent(h Header, role SubagentRole) string {
	header := Compose(h, "subagent")

	var sb strings.Builder
	sb.Grow(len(header) + len(role.Description) + len(role.Extra) + len(summaryHint) + 64)
	sb.WriteString(header)
	sb.WriteString("\nYou are a subagent spawned by the parent agent for: ")
	sb.WriteString(role.Description)
	sb.WriteString(".\nComplete the task and return a concise summary as your final message. Do not engage in conversation.")
	if role.Extra != "" {
		sb.WriteByte('\n')
		sb.WriteString(role.Extra)
	}
	sb.WriteByte('\n')
	sb.WriteString(summaryHint)
	sb.WriteByte('\n')
	return sb.String()
}

// ModeLabel formats the (pref, workflow) pair for Compose's modeLabel
// argument. Workflow wins when non-None — "yolo + plan-analyze"
// reads as "plan-analyze" because the read-only workflow contract
// trumps a permissive preference, and the model needs to honour the
// stricter axis.
//
// Returns one of: "deny" | "ask" | "yolo" | "plan-analyze" |
// "plan-execute". The exact string set matches what cmd/seek's
// SetYolo / SetPlan callbacks pass to Agent.SetModeLabel for the
// per-message mode reminder, so the model sees consistent labels
// across the system prompt and the per-turn reminder.
func ModeLabel(pref permission.Preference, workflow permission.Workflow) string {
	switch workflow {
	case permission.WorkflowPlanAnalyze:
		return "plan-analyze"
	case permission.WorkflowPlanExecute:
		return "plan-execute"
	}
	switch pref {
	case permission.PrefYolo:
		return "yolo"
	case permission.PrefAsk:
		return "ask"
	default:
		return "deny"
	}
}
