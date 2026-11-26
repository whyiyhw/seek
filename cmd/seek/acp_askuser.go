// acp_askuser.go wires ask_user / propose onto the ACP lane.
//
// The gap it closes: `askPolicy` is born ModeDisabled because only the TUI
// has a picker to render (cmd/seek/main.go:819), and runACP never opted
// in — so on the ACP lane a model asking the human a question got
// ErrDisabled ("rephrase as a plain-text question") even though a human
// was sitting right there in the editor. print/rpc/goal have no human and
// keep that answer; ACP does, and now says so.
//
// The channel is `elicitation/create` (internal/acp/elicitation.go). It is
// advertised, not assumed: a client that does not declare
// `clientCapabilities.elicitation.form` keeps the old ErrDisabled
// behaviour verbatim, which is why the existing ACP lane test (whose fake
// client declares nothing) stays green.
//
// Same posture as acp_approval.go — every non-answer ending (timeout,
// disconnect, cancel, -32601 from a client that declared the capability
// but never implemented it, malformed body) becomes a CANCELLED answer,
// never a hung turn and never a fatal error.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/askuser"
)

// acpAskUser is the ACP lane's askuser callback pair. srv/timeout are
// wired once in runACP before Serve starts; turn is swapped per prompt
// (the same lifetime as acpApproval's scope — a question can only be
// asked while a turn is running, because that is the only time a model
// is executing).
type acpAskUser struct {
	srv     *acp.Server
	timeout time.Duration

	mu   sync.Mutex
	turn *acpTurnScope
	seq  int // form counter, keeps field names unique within a turn
}

func newACPAskUser(srv *acp.Server, timeout time.Duration) *acpAskUser {
	return &acpAskUser{srv: srv, timeout: timeout}
}

func (a *acpAskUser) beginTurn(ctx context.Context, sessionID string) {
	a.mu.Lock()
	a.turn = &acpTurnScope{ctx: ctx, sessionID: sessionID}
	a.seq = 0
	a.mu.Unlock()
}

func (a *acpAskUser) endTurn() {
	a.mu.Lock()
	a.turn = nil
	a.mu.Unlock()
}

// ask is the v1 single-question callback. The ask_user tool always goes
// through AskBatch, so this exists for propose (which asks one question)
// and for any future direct caller — it delegates to the batch form so
// there is exactly one form builder.
func (a *acpAskUser) ask(q askuser.Question) askuser.Answer {
	answers := a.askBatch(askuser.Batch{Questions: []askuser.Question{q}})
	if len(answers) == 0 {
		return askuser.Answer{Cancelled: true}
	}
	return answers[0]
}

// askBatch renders the whole batch as ONE form: one property per question,
// which is what ACP's form mode is shaped for (a stack of pickers in the
// TUI becomes a stack of fields in the client). Length of the returned
// slice always equals len(b.Questions).
func (a *acpAskUser) askBatch(b askuser.Batch) []askuser.Answer {
	n := len(b.Questions)
	cancelled := make([]askuser.Answer, n)
	for i := range cancelled {
		cancelled[i] = askuser.Answer{Cancelled: true}
	}
	if n == 0 {
		return cancelled
	}

	a.mu.Lock()
	t := a.turn
	a.seq++
	form := a.seq
	a.mu.Unlock()
	if t == nil || t.ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "acp: ask_user not delivered: no active turn to ask in\n")
		return cancelled
	}

	params := buildElicitationParams(t.sessionID, form, b)
	raw, err := a.srv.Call(t.ctx, acp.MethodElicitationCreate, params, a.timeout)
	if err != nil {
		// Timeout / disconnect / cancellation / -32601 from a client that
		// advertised the capability but never wired a handler: all of them
		// are "no answer". Log the ending so an operator can see WHY the
		// picker never appeared — the model's tool result is the same
		// ErrDisabled-free "cancelled" either way.
		fmt.Fprintf(os.Stderr, "acp: ask_user unanswered: %v\n", err)
		return cancelled
	}
	return answersFromElicitation(b, acp.ParseElicitationResponse(raw))
}

// buildElicitationParams turns a batch of questions into one form schema.
// Field naming: question i owns `q<i>` (the enumerated answer) and
// `q<i>_other` (free text). The free-text twin exists because the TUI
// picker auto-appends an "Other / type your own answer" row — the ACP
// form has no such concept, so it is modelled as a second, optional field.
//
// Nothing is marked required: the free-text field is an ALTERNATIVE to the
// enumerated one, so requiring either would make the other unanswerable.
func buildElicitationParams(sessionID string, form int, b askuser.Batch) acp.CreateElicitationParams {
	props := make(map[string]acp.ElicitationPropertySchema, len(b.Questions)*2)

	var msg strings.Builder
	msg.WriteString("The assistant needs an answer before it can continue.")
	if form > 1 {
		// Only reachable if a turn asks twice; keeps the message honest.
		fmt.Fprintf(&msg, " (question set %d)", form)
	}

	for i, q := range b.Questions {
		key := fmt.Sprintf("q%d", i+1)
		title := q.Question
		if q.Header != "" {
			title = q.Header + " — " + q.Question
		}

		if q.MultiSelect {
			ids := make([]string, 0, len(q.Options))
			for _, o := range q.Options {
				ids = append(ids, o.ID)
			}
			props[key] = acp.ElicitationPropertySchema{
				Type:        "array",
				Title:       title,
				Description: describeOptions(q),
				Items:       &acp.ElicitationItems{Type: "string", Enum: ids},
				MinItems:    1,
				MaxItems:    len(ids),
			}
		} else {
			oneOf := make([]acp.EnumOption, 0, len(q.Options))
			for _, o := range q.Options {
				oneOf = append(oneOf, acp.EnumOption{
					Const:       o.ID,
					Title:       o.Label,
					Description: o.Description,
				})
			}
			props[key] = acp.ElicitationPropertySchema{
				Type:  "string",
				Title: title,
				OneOf: oneOf,
			}
		}

		props[key+"_other"] = acp.ElicitationPropertySchema{
			Type:        "string",
			Title:       title + " (or type your own answer)",
			Description: "Use this instead of picking an option when none of them fit.",
		}
	}

	return acp.CreateElicitationParams{
		SessionID: sessionID,
		Mode:      acp.ElicitationModeForm,
		Message:   msg.String(),
		RequestedSchema: acp.ElicitationSchema{
			Type:       "object",
			Title:      "seek asks",
			Properties: props,
		},
	}
}

// describeOptions renders the option list into the array field's
// description: an `array` property carries enum ids but no per-item
// labels, so without this the human would see raw ids.
func describeOptions(q askuser.Question) string {
	parts := make([]string, 0, len(q.Options))
	for _, o := range q.Options {
		line := o.ID
		if o.Label != "" && o.Label != o.ID {
			line = o.Label + " (" + o.ID + ")"
		}
		if o.Description != "" {
			line += " — " + o.Description
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}

// answersFromElicitation maps the accepted form content back onto one
// Answer per question. A declined / cancelled / unrecognised response
// yields the pre-built cancelled slice. An accepted form with a field left
// empty yields Answer{Cancelled: true} for THAT question only — the
// caller's per-index contract, and the honest reading of "user skipped it".
//
// Free text WINS over the enumerated pick, matching the TUI picker's own
// exclusivity rule ("Other" clears the toggled rows — you wouldn't say
// "ids: A, B + free_text: …"). A well-behaved client sends one or the
// other because the form has two separate fields, but nothing stops it
// filling both, and the reply schema is one-or-the-other by contract.
func answersFromElicitation(b askuser.Batch, r acp.ElicitationResponse) []askuser.Answer {
	out := make([]askuser.Answer, len(b.Questions))
	for i := range out {
		out[i] = askuser.Answer{Cancelled: true}
	}
	if !r.Accepted() {
		return out
	}
	for i, q := range b.Questions {
		key := fmt.Sprintf("q%d", i+1)
		var ans askuser.Answer
		if q.MultiSelect {
			ans.ChosenIDs = filterKnownIDs(q, r.Strings(key))
		} else if id := r.String(key); id != "" {
			if knownID(q, id) {
				ans.ChosenIDs = []string{id}
			}
			// An id outside the offered set is a client bug, not a new
			// option — the same "closed menu" posture as
			// ParsePermissionOutcome's unknown-echo-is-deny.
		}
		if free := strings.TrimSpace(r.String(key + "_other")); free != "" {
			ans = askuser.Answer{FreeText: free} // free text replaces the pick
		}
		if len(ans.ChosenIDs) == 0 && ans.FreeText == "" {
			continue // untouched field: stays Cancelled
		}
		out[i] = ans
	}
	return out
}

func knownID(q askuser.Question, id string) bool {
	for _, o := range q.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

func filterKnownIDs(q askuser.Question, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if knownID(q, id) {
			out = append(out, id)
		}
	}
	return out
}

// acpAskTimeoutEnvVar overrides acpAskTimeoutDefault, in seconds. Separate
// from SEEK_ACP_PERMISSION_TIMEOUT: a permission prompt is a yes/no the
// user expects to be quick, while a question form may sit on screen while
// they read context — but both need a bound, or a client that ignores the
// request while holding stdio open hangs the turn forever.
const acpAskTimeoutEnvVar = "SEEK_ACP_ASK_TIMEOUT"

// acpAskTimeoutDefault matches the permission lane's 10 minutes.
const acpAskTimeoutDefault = 10 * time.Minute

// acpAskTimeoutFromEnv resolves the override. Same injected-getenv
// testability posture as acpPermissionTimeoutFromEnv.
func acpAskTimeoutFromEnv(getenv func(string) string) time.Duration {
	v := strings.TrimSpace(getenv(acpAskTimeoutEnvVar))
	if v == "" {
		return acpAskTimeoutDefault
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return acpAskTimeoutDefault
}
