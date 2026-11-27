// acp_askuser.go wires ask_user / propose onto the ACP lane.
//
// The gap it closes: `askPolicy` is born ModeDisabled because only the TUI
// has a picker to render (cmd/seek/main.go:819), and runACP never opted
// in — so on the ACP lane a model asking the human a question got
// ErrDisabled ("rephrase as a plain-text question") even though a human
// was sitting right there in the editor. print/rpc/goal have no human and
// keep that answer; ACP does, and now says so.
//
// TWO channels, picked at initialize from what the client advertised
// (card c_7bqiq5):
//
//   - `elicitation/create` (internal/acp/elicitation.go) when the client
//     declares `clientCapabilities.elicitation.form`. This is the shape the
//     protocol designed for a question: one form, one field per question,
//     enums plus a free-text twin.
//   - `session/request_permission` as a FALLBACK for everyone else. It is
//     the only "ask the human" method a minimal ACP client is guaranteed to
//     implement — Paseo 0.17.4 declares no elicitation capability and has
//     no createElicitation handler at all (verified by reading its
//     app.asar), yet implements requestPermission and even special-cases
//     "chooser" menus (repeated allow kinds) to render them as a question
//     with the toolCall.content as body. Without the fallback, every such
//     client answers ErrDisabled and the model must rephrase in prose.
//
// The fallback is deliberately narrower than the form, because
// request_permission's semantics are pinned to a yes/no gate:
//
//   - ONE popup per question (a batch pops N times, in order).
//   - Options are the question's own rows, all `allow_once` so the menu
//     reads as a choice rather than an approval; a trailing `reject_once`
//     is the Skip row.
//   - `reject_once` = "skip this question" (only this one; the next
//     question still pops). Protocol-level cancel / timeout / client error
//     = "stop asking", the rest of the batch stays cancelled.
//   - MULTI-SELECT is NOT carried: request_permission is single-select, and
//     the per-option Include/Skip fan-out zcode uses would look like a
//     plain [allow, reject] pair to an auto-accepting headless host, which
//     would then silently answer "yes" to every row — a fabricated user
//     answer, worse than none. A multi-select question therefore comes back
//     cancelled with a stderr note, and the model asks in prose.
//   - FREE TEXT has no carrier either; Skip is the honest ending.
//
// Same posture as acp_approval.go on both channels — every non-answer
// ending (timeout, disconnect, cancel, -32601 from a client that declared
// the capability but never implemented it, malformed body) becomes a
// CANCELLED answer, never a hung turn and never a fatal error.
package main

import (
	"context"
	"errors"
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
	// form is the channel negotiated at initialize: true = the client
	// declared `elicitation.form` and gets a real form; false = the
	// request_permission fallback. Written once by the Initialize handler
	// (read loop, before any prompt can start) and read by turn
	// goroutines, so a plain bool is enough — see arm().
	form bool

	mu   sync.Mutex
	turn *acpTurnScope
	seq  int // form counter, keeps field names unique within a turn
}

// arm picks the channel: called from the Initialize handler, which is the
// only place the client's capabilities are known. Both channels keep
// askPolicy ModeAsk — the difference is only HOW the question travels.
func (a *acpAskUser) arm(form bool) {
	a.form = form
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

// askBatch raises the questions on whichever channel initialize selected.
// Length of the returned slice always equals len(b.Questions).
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
	if a.form {
		return a.askViaForm(t, form, b, cancelled)
	}
	return a.askViaPermission(t, b, cancelled)
}

// askViaForm is the elicitation/create path: the whole batch rides as ONE
// form (one property per question), which is what the protocol's form mode
// is shaped for.
func (a *acpAskUser) askViaForm(t *acpTurnScope, form int, b askuser.Batch, cancelled []askuser.Answer) []askuser.Answer {
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

// askViaPermission is the request_permission fallback: one popup per
// question, the question's own rows as options, a trailing Skip. Answers
// are collected per question, so a Skip on question 1 does not abandon
// question 2 — but a protocol-level ending (timeout / cancel / client
// error) stops the batch, because there is no channel left to ask on.
func (a *acpAskUser) askViaPermission(t *acpTurnScope, b askuser.Batch, cancelled []askuser.Answer) []askuser.Answer {
	out := make([]askuser.Answer, len(b.Questions))
	copy(out, cancelled)

	for i, q := range b.Questions {
		if q.MultiSelect {
			// See the package comment: a per-row yes/no fan-out would read
			// as a plain approval gate to an auto-accepting host, which
			// would answer "yes" to every row. Cancelling is the honest
			// answer; the model re-asks in prose.
			fmt.Fprintf(os.Stderr, "acp: ask_user: multi-select question %d not carried over session/request_permission (client has no elicitation.form) — answering cancelled\n", i+1)
			continue
		}
		raw, answered, err := a.askOnePermission(t, i, q)
		if err != nil {
			fmt.Fprintf(os.Stderr, "acp: ask_user unanswered (request_permission): %v\n", err)
			return out // no channel left for the remaining questions
		}
		if !answered {
			continue // Skip: this question stays cancelled, keep asking
		}
		out[i] = askuser.Answer{ChosenIDs: []string{raw}}
	}
	return out
}

// errAskStopped ends the fallback batch: the client closed the prompt
// (cancelled outcome or an unreadable body), so there is no channel left to
// ask the remaining questions on. Distinct from a transport error only for
// the stderr wording.
var errAskStopped = errors.New("acp: question cancelled by client")

// askOnePermission sends one question as a request_permission prompt and
// returns the picked option id. answered=false means the user skipped it
// (our trailing reject_once); err means the batch must stop.
func (a *acpAskUser) askOnePermission(t *acpTurnScope, index int, q askuser.Question) (picked string, answered bool, err error) {
	params := buildAskPermissionParams(t.sessionID, index, q)
	raw, err := a.srv.Call(t.ctx, acp.MethodRequestPermission, params, a.timeout)
	if err != nil {
		return "", false, err
	}
	id, ok := acp.ParsePermissionSelection(raw)
	if !ok {
		return "", false, errAskStopped
	}
	if id == askSkipOptionID {
		return "", false, nil
	}
	if !knownID(q, id) {
		// A closed menu: an id we never offered is a client bug, not a new
		// answer — the same posture as ParsePermissionOutcome's
		// unknown-echo-is-deny.
		return "", false, nil
	}
	return id, true, nil
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

// askSkipOptionID is the trailing "skip this question" row of the
// request_permission fallback's menu. Not a legal askuser option id
// (askuser.Validate reserves "other", and this spelling is ours), so it can
// never collide with a model-provided row.
const askSkipOptionID = "seek:skip"

// buildAskPermissionParams turns ONE question into a
// session/request_permission prompt.
//
// Shape decisions, each load-bearing for a real client:
//
//   - Every option is `allow_once` and the Skip row is `reject_once`. Two or
//     more allow options of the SAME kind is what Paseo's
//     isACPChooserRequest detects, and that detection does two things we
//     want: it disables auto-accept (a headless host must not answer a
//     question for the human) and it renders toolCall.content as the
//     prompt body instead of a one-line approval title.
//   - The question text goes in BOTH the title and a content block: the
//     title is all martty-style overlays draw, the content block is what
//     Paseo/Zed render in detail.
//   - Option Description rides into the option's Name — request_permission
//     options are a single `name` string, there is no secondary text.
//
// index makes the toolCallId unique per question in a batch (the schema
// requires ids to be distinct within a session's prompts).
func buildAskPermissionParams(sessionID string, index int, q askuser.Question) acp.RequestPermissionParams {
	opts := make([]acp.PermissionOption, 0, len(q.Options)+1)
	for _, o := range q.Options {
		name := o.Label
		if name == "" {
			name = o.ID
		}
		if o.Description != "" {
			name += " — " + o.Description
		}
		opts = append(opts, acp.PermissionOption{OptionID: o.ID, Name: name, Kind: acp.OptionAllowOnce})
	}
	opts = append(opts, acp.PermissionOption{
		OptionID: askSkipOptionID,
		Name:     "Skip this question",
		Kind:     acp.OptionRejectOnce,
	})

	title := q.Question
	if q.Header != "" {
		title = q.Header + " — " + q.Question
	}
	var body []string
	for _, o := range q.Options {
		line := o.Label
		if line == "" {
			line = o.ID
		}
		if o.Description != "" {
			line += " — " + o.Description
		}
		body = append(body, line)
	}

	return acp.RequestPermissionParams{
		SessionID: sessionID,
		ToolCall: acp.PermissionToolCall{
			ToolCallID: fmt.Sprintf("ask-%d", index+1),
			// "other" — the schema's ToolKind has no "question" member, and
			// mislabelling it as execute/edit would make clients render a
			// terminal/file affordance for a question.
			Kind:  "other",
			Title: title,
			Content: []acp.ToolCallContentWrap{
				{Type: "content", Content: acp.ContentBlock{Type: "text", Text: strings.Join(body, "\n")}},
			},
		},
		Options: opts,
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
