package subagent

// Incremental tail-follow of one project's subagents.jsonl (W3.2, the
// web task panel's SSE topic source; feature-web-frontend §3.2). The
// writers are seek child processes in OTHER processes, so a follow is
// stat+read polling at the caller's cadence — there is no in-process
// bus to hook. The Follower folds with the same applyEvent core as
// ListProject (W3.1), so list and push stay one projection; pushes
// carry the folded Subagent row, not the raw event.

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// FollowPush is one panel-visible transition surfaced by Follower.Poll:
// Event is the jsonl event kind ("started" or a terminal kind), Sub is
// the folded row AFTER applying the event — the same shape as
// ListProject rows, so a panel merges by sub_sid with no fold of its
// own (notified / unknown kinds annotate state without pushing).
type FollowPush struct {
	Event string
	Sub   Subagent
}

// Follower incrementally tails one subagents.jsonl. Not safe for
// concurrent use — exactly one caller goroutine per file (the web
// frontend runs one per followed index path).
type Follower struct {
	path   string
	offset int64
	bySid  map[string]*Subagent
}

// NewFollower anchors at indexPath's current END: the first Poll
// surfaces only bytes appended after this call. The panel's initial
// snapshot comes from ListProject (W3.1) — subscribe first, then GET,
// and pushes patch that snapshot; events already in the file are never
// replayed. A missing file is the same posture anchored at 0: its
// later creation is the first followable append, every line in it new.
func NewFollower(indexPath string) *Follower {
	fo := &Follower{path: indexPath, bySid: make(map[string]*Subagent)}
	if st, err := os.Stat(indexPath); err == nil && st.Mode().IsRegular() {
		fo.offset = st.Size()
	}
	return fo
}

// Poll reads the complete lines appended since the last call, folds
// them, and returns the panel-visible pushes in file order. Best-effort
// by design: a missing index, a transient open/stat failure, or a
// malformed line yields no pushes for that tick and consumes nothing —
// the next Poll re-reads from the same offset, so liveness never loses
// data. A trailing partial line (writer mid-append) is left for the
// next Poll. A file that shrank (rewritten — never expected of an
// append-only index) re-anchors at 0 and re-folds from scratch; the
// re-fold re-pushes `started` rows, which clients merge by sub_sid.
func (fo *Follower) Poll() []FollowPush {
	f, err := os.Open(fo.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	if st.Size() < fo.offset {
		fo.offset = 0
		fo.bySid = make(map[string]*Subagent)
	}
	if st.Size() == fo.offset {
		return nil
	}
	r := bufio.NewReader(io.NewSectionReader(f, fo.offset, st.Size()-fo.offset))
	var pushes []FollowPush
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && err != nil {
			break // partial trailing line: the writer's next append completes it
		}
		if len(line) > 0 {
			fo.offset += int64(len(line))
			var e event
			if len(line) > 1 && json.Unmarshal(line[:len(line)-1], &e) == nil {
				if s := applyEvent(fo.bySid, e); s != nil {
					pushes = append(pushes, FollowPush{Event: e.Kind, Sub: *s})
				}
			}
		}
		if err != nil {
			break // clean EOF
		}
	}
	return pushes
}
