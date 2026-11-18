// Authorized shell stdout injection: the pre_prompt_inject face
// (feature-orchestration §6 D17 / feature-hooks-injection §3.4 + §4.5,
// milestone M-O3.2).
//
// The pillar-B red line ("shell hook stdout never enters the prompt")
// is REFINED, not overturned, here: UNAUTHORIZED stdout still never
// enters the prompt (the pre_prompt observer has no plumbing for it,
// and no other event touches PrePromptOut). pre_prompt_inject is the
// single opt-in exception, and its output goes through the exact same
// single seam as every other injection — PrePromptHook.Prepend,
// wrapped in a user-role <context> block at the turn frontier
// (channel D: zero marginal cache cost, §2.3; commit-once into
// history, never rewritten).
//
// Authorization semantics (§4.5 精确化 2): what the user signs up for
// by configuring the event is a CONTENT-trust decision — "let this
// external command's output reach the model's view" — same family as
// webfetch's URL gate. It is NOT a cache-risk decision: channel D is
// byte-safe by construction. The deny paths below all block the
// INJECTION (stdout dropped), never the turn — consistent with every
// other non-pre_tool event (degrade-not-block, pillar B §3.4).

package hooks

import (
	"context"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// InjectRunner adapts ShellRunner's pre_prompt_inject dispatch onto the
// PrePromptHook interface. It exists as its own type (instead of a
// method set on ShellRunner) so registration order can place it AFTER
// the in-process PrePrompt hooks — see shell_runner.go's header note.
type InjectRunner struct {
	r *ShellRunner
}

// InjectHook returns the injection face of the runner. The wiring layer
// registers the result only when HasInjectHooks is true.
func (r *ShellRunner) InjectHook() *InjectRunner { return &InjectRunner{r: r} }

// HasInjectHooks reports whether at least one pre_prompt_inject hook is
// configured and not statically skipped. Used by main.go to decide
// whether the face is worth a Registry slot.
func (r *ShellRunner) HasInjectHooks() bool {
	if r == nil {
		return false
	}
	for _, h := range r.cfg.PrePromptInject {
		if h.SkipReason == "" {
			return true
		}
	}
	return false
}

// OnPrePrompt runs every pre_prompt_inject hook in declaration order
// (project hooks before user hooks — Merge order) and turns each
// successful stdout into one user-role Prepend message. It never
// returns an error and never touches UserText (D17 explicitly vetoes
// shell rewriting the user's message): the worst a broken hook can do
// is contribute nothing.
func (ir *InjectRunner) OnPrePrompt(ctx context.Context, in PrePromptIn) (PrePromptOut, error) {
	return ir.r.runInject(ctx), nil
}

// runInject is the dispatch loop shared by the InjectRunner face.
//
// Per-hook outcomes:
//   - exit 0 + stdout      → <context type="hook" name="..."> Prepend
//     message, hard-truncated at max_bytes with a " [truncated]" wire
//     marker when over budget (same marker as SEEK_TOOL_RESULT);
//   - exit 0 + empty stdout → no message (no empty context blocks in
//     history); audit row records injected_bytes=0;
//   - non-zero exit / timeout / exec failure → stdout DROPPED, hook
//     skipped, turn proceeds (audit row carries the failure reason);
//   - SkipReason (static bash -n failure) → never exec'd, audit row
//     says skipped.
//
// Audit (§4.5 留痕): every dispatch writes a row whose injected_bytes
// is the byte count that actually entered the prompt AFTER truncation
// (marker included), with Reason noting the truncation.
func (r *ShellRunner) runInject(ctx context.Context) PrePromptOut {
	var prepend []deepseek.Message
	for _, h := range r.cfg.PrePromptInject {
		if h.SkipReason != "" {
			// Same skip semantics as the OnPreToolUse dispatch site.
			r.writeAudit(hooksconfig.AuditEntry{
				Event:    hooksconfig.EventPrePromptInject,
				Hook:     h.Name,
				ExitCode: -1,
				Reason:   "skipped: " + h.SkipReason,
			})
			continue
		}
		env := r.baseEnv(hooksconfig.EventPrePromptInject)
		stdout, entry, timedOut := r.execHook(ctx, h, "", "", env)
		if timedOut || entry.ExitCode != 0 {
			// Deny path for the INJECTION: stdout is discarded, the
			// turn is not blocked, the next hook still runs.
			r.writeAudit(entry)
			continue
		}
		// defaultExec right-trims newlines; trim again so the wrap
		// format holds under any executor seam (tests, future
		// transports) — no blank line before </context>.
		content := strings.TrimRight(stdout, "\n")
		if max := h.EffectiveMaxBytes(); len(content) > max {
			content = content[:max] + truncationMarker
			entry.InjectedBytes = len(content)
			entry.Reason = fmt.Sprintf("truncated at max_bytes=%d", max)
		} else {
			entry.InjectedBytes = len(content)
		}
		r.writeAudit(entry)
		if content == "" {
			continue
		}
		prepend = append(prepend, deepseek.Message{
			Role: deepseek.RoleUser,
			Content: fmt.Sprintf("<context type=\"hook\" name=%q>\n%s\n</context>",
				h.Name, content),
		})
	}
	return PrePromptOut{Prepend: prepend}
}
