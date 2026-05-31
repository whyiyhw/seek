// Package monitor implements the `monitor` tool: track a background
// shell job started by `bash run_in_background` (v6 柱 K). One tool, three
// actions — poll (new output + status), wait (block until exit / regex /
// timeout), kill (terminate the process group).
//
// Design notes (PRD §4):
//   - D1: a single tool with an action enum, not three separate tools, to
//     keep the schema/attention budget small.
//   - It deliberately does NOT implement tools.ReadOnlyTool. kill mutates
//     process state, and — more importantly — wait BLOCKS; batching a
//     blocking wait concurrently with read-only tools would stall the
//     batch. Non-ReadOnly means the agent runs it on its own.
//   - No permission.Policy: monitor only ever touches jobs that were
//     already launched through the gated bash tool. poll/wait are reads;
//     kill reduces side effects. There is no NEW dangerous action to gate.
package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/bgjob"
	"github.com/whyiyhw/seek/internal/tools"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "job":         {"type": "string", "description": "Background job handle from bash run_in_background, e.g. bg-1."},
    "action":      {"type": "string", "enum": ["poll", "wait", "kill"], "description": "poll (default): output produced since your last poll plus current status. wait: block until the job exits, until_regex matches new output, or timeout. kill: terminate the job's process group."},
    "timeout_ms":  {"type": "integer", "minimum": 100, "maximum": 600000, "description": "wait only: max time to block. Default 120000 (2m), max 600000 (10m)."},
    "until_regex": {"type": "string", "description": "wait only: return as soon as this Go regexp matches the job's output (e.g. \"Listening on\" for a dev server)."}
  },
  "required": ["job"],
  "additionalProperties": false
}`)

const description = "Track a background shell job started by `bash run_in_background`. action=poll (default) returns output produced since your last poll plus status; action=wait blocks until the job exits, until_regex matches, or timeout; action=kill terminates it. Use poll to check progress, wait to block on completion or a readiness line, kill to stop a job you no longer need."

type Args struct {
	Job        string `json:"job"`
	Action     string `json:"action,omitempty"`
	TimeoutMS  int    `json:"timeout_ms,omitempty"`
	UntilRegex string `json:"until_regex,omitempty"`
}

type Tool struct {
	mgr *bgjob.Manager
}

// New wires the session's background-job manager. Panics on nil — a
// registered monitor tool with no manager is a wiring bug (mirrors
// wakeup.New).
func New(mgr *bgjob.Manager) Tool {
	if mgr == nil {
		panic("monitor: New called with nil Manager — host did not wire internal/bgjob")
	}
	return Tool{mgr: mgr}
}

func (Tool) Name() string            { return "monitor" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

var validFields = []string{"job", "action", "timeout_ms", "until_regex"}

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("monitor", raw, &a, validFields...); err != nil {
		return "", err
	}
	if a.Job == "" {
		return "", tools.MissingField("monitor", "job", raw, validFields...)
	}

	action := a.Action
	if action == "" {
		action = "poll"
	}

	switch action {
	case "poll":
		pr, err := t.mgr.Poll(a.Job)
		if err != nil {
			return "", err
		}
		return formatPoll(a.Job, pr), nil

	case "wait":
		wr, err := t.mgr.Wait(ctx, a.Job, a.UntilRegex, time.Duration(a.TimeoutMS)*time.Millisecond)
		if err != nil {
			// Unknown job / bad until_regex, or the turn ctx was cancelled
			// (Esc). Propagate verbatim — for a ctx cancel this signals the
			// agent the turn was interrupted; the job keeps running (Esc
			// stops observing, not the job — PRD §4 D5/D6).
			return "", err
		}
		return formatWait(a.Job, wr), nil

	case "kill":
		if err := t.mgr.Kill(a.Job); err != nil {
			return "", err
		}
		// Kill is a no-op if the job already finished; report the real state.
		pr, err := t.mgr.Poll(a.Job)
		if err != nil {
			return "", err
		}
		return statusHeader(a.Job, pr.Status, pr.ExitCode, pr.Elapsed, ""), nil

	default:
		return "", fmt.Errorf("monitor: unknown action %q (valid: poll, wait, kill)", action)
	}
}

// statusHeader renders the wire-format status line, e.g.
// "[bg-1: running, elapsed=12s]" or "[bg-1: exited code=0, elapsed=1.3s]".
func statusHeader(id string, st bgjob.Status, code int, elapsed time.Duration, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s: %s", id, st)
	if st == bgjob.StatusExited {
		fmt.Fprintf(&b, " code=%d", code)
	}
	fmt.Fprintf(&b, ", elapsed=%s", elapsed.Round(time.Millisecond))
	if extra != "" {
		b.WriteString(", ")
		b.WriteString(extra)
	}
	b.WriteByte(']')
	return b.String()
}

func windowBody(window []byte, dropped int64) string {
	var b strings.Builder
	if dropped > 0 {
		fmt.Fprintf(&b, "... %d earlier bytes dropped (output buffer overflow) ...\n", dropped)
	}
	if len(window) == 0 {
		if dropped > 0 {
			return b.String() // already noted the drop
		}
		return "(no new output)"
	}
	b.Write(window)
	return b.String()
}

func formatPoll(id string, pr bgjob.PollResult) string {
	return statusHeader(id, pr.Status, pr.ExitCode, pr.Elapsed, "") + "\n" + windowBody(pr.Window, pr.Dropped)
}

func formatWait(id string, wr bgjob.WaitResult) string {
	var extra string
	switch wr.Reason {
	case bgjob.ReasonTimeout:
		extra = "wait timed out"
	case bgjob.ReasonMatched:
		extra = "until_regex matched"
	}
	return statusHeader(id, wr.Status, wr.ExitCode, wr.Elapsed, extra) + "\n" + windowBody(wr.Window, wr.Dropped)
}
