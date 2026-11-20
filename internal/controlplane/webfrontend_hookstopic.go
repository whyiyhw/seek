package controlplane

// SSE `hooks` topic for /events (W5 c_xz2bey; closes inspect-rpc
// §6 #6/#7 — hooks/audit/tail's follow=true used to answer -32602 with
// "the SSE hooks topic is future work"). Same topic dimension and
// hub mechanics as `subagents` (webfrontend_topic.go): the connection
// rides a second drop-oldest channel, the FOLLOW is the fanout unit,
// and len(subs) is the refcount.
//
// Two deliberate divergences from the subagents topic, both forced by
// the data source:
//
//   - GLOBAL follow, no conv binding. The audit JSONL
//     (~/.seek/hooks-audit.jsonl) is machine-global — there is no
//     per-project index to resolve, so ?topic=hooks works on an
//     UNBOUND conv (the subagents topic 400s there; its binding is
//     what picks the file). Exactly one follow exists per serve
//     process, keyed by the audit path; every subscriber wants the
//     same bytes, so the per-follow fanout's cross-project-leak
//     property is moot but its refcount property still applies.
//
//   - Pushes are the audit line VERBATIM — no {"topic":...} envelope.
//     An AuditEntry row is itself the shipped wire format (v3 §3.6:
//     third-party tools tail this very file); the SSE event name
//     (`hooks`, from the topic constant) is the topic marker, and the
//     frame data doubles as a hooks/audit/tail row byte-for-byte. The
//     page merges SSE rows and /rpc tail rows with one shape.
//
// Panel protocol unchanged from W3.2: subscribe FIRST (anchor), then
// fetch the one-shot snapshot (hooks/audit/tail); pushes patch it.
// Reconnect resync is the client's job — it re-fetches the tail on
// ES reopen and merges (rows carry no unique id beyond their own
// bytes, so the merge dedupes by whole-row equality).

import (
	"time"

	"github.com/whyiyhw/seek/internal/hooksconfig"
)

// topicHooks names the hooks audit topic — the SSE `event:` field. A
// constant alongside topicSubagents so the topic set extends
// deliberately (handleEvents validates against both).
const topicHooks = "hooks"

// hooksTopicPoll is the audit-log tail-follow poll interval, the same
// stat+read cadence rationale as subagentsTopicPoll. Hook executions
// are human-cadence events; 1s is invisible latency at ~zero CPU. A
// var so tests tighten it.
var hooksTopicPoll = time.Second

// subscribeHooksTopic registers one SSE client under BOTH dimensions
// (conv channel identical to W1.3 subscribe's; topic channel on the
// GLOBAL audit follow) and starts or joins that follow. Mirrors
// subscribeTopic with the audit source swapped in.
func (f *WebFrontend) subscribeHooksTopic(conv ConvID, auditPath string) (ch chan sseFrame, tch chan string, unsub func()) {
	// Anchor at SUBSCRIBE time, not first tick — an entry appended
	// between the two would otherwise be anchored past and missed.
	fo := hooksconfig.NewAuditFollower(auditPath)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[ConvID]map[int]chan sseFrame)
	}
	if f.subs[conv] == nil {
		f.subs[conv] = make(map[int]chan sseFrame)
	}
	if f.follows == nil {
		f.follows = make(map[string]*topicFollow)
	}
	f.next++
	id := f.next // capture NOW: the closure must drop THIS subscriber (W1.3 pitfall)
	ch = make(chan sseFrame, 16)
	tch = make(chan string, topicChanBuf)
	f.subs[conv][id] = ch

	fl := f.follows[auditPath]
	if fl == nil {
		fl = &topicFollow{stop: make(chan struct{}), subs: make(map[int]chan string)}
		f.follows[auditPath] = fl
		go f.hooksFollowLoop(fo, fl)
	}
	fl.subs[id] = tch

	return ch, tch, func() { f.dropTopicSub(conv, id, auditPath) }
}

// hooksFollowLoop is the audit log's shared follower goroutine — the
// same lifecycle contract as followLoop: exits when the last
// subscriber releases the follow, stops nothing else, pushFollow is
// best-effort. Each poll's lines go out verbatim (see file header).
func (f *WebFrontend) hooksFollowLoop(fo *hooksconfig.AuditFollower, fl *topicFollow) {
	tick := time.NewTicker(hooksTopicPoll)
	defer tick.Stop()
	for {
		select {
		case <-fl.stop:
			return
		case <-tick.C:
			for _, line := range fo.Poll() {
				f.pushFollow(fl, line)
			}
		}
	}
}
