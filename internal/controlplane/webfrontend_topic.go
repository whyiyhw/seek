package controlplane

// SSE topic dimension for /events (W3.2, card c_3736yl;
// feature-web-frontend §3.2 + §5): a connection opts into a topic
// with ?topic= alongside its conv. The two dimensions coexist without
// touching each other — the conv dimension (W1.3, webfrontend_conv.go)
// keeps its exact channel, buffer and drop-on-full semantics; the
// topic rides a SECOND channel on the same connection (nil-chan-
// blocks-forever lets handleEvents select over both uniformly).
//
// Today's topics are `subagents` (the task panel's lifecycle pushes —
// started / terminal transitions; the producer is a shared tail-follow
// of the conv's bound-project subagents.jsonl, the W3.1 reader's file,
// where subagent.Follower folds with the same applyEvent core as
// ListProject, so list and push are one projection) and `hooks` (the
// audit tail — W5 c_xz2bey, webfrontend_hookstopic.go, same mechanics
// with a global follow and verbatim lines). The panel protocol is
// subscribe-FIRST, then the one-shot snapshot for the list: pushes
// patch it, history never replays.
//
// The fanout unit is the FOLLOW, not the topic name: subscriber
// channels live on the per-index-path topicFollow, and followLoop
// pushes only to its own follow's subscribers. A topic-name-keyed
// hub would fan project A's rows to every subagents subscriber
// including ones bound to project B — the exact cross-project leak
// the W3.2 rework round fixed; per-follow fanout makes that leak
// structurally impossible, not key-discipline-enforced.
//
// Follow lifecycle (the inspect-rpc §4.2 observer shape): the first
// subscriber of an index path starts ONE follower goroutine, later
// subscribers of the same path share it (len(subs) IS the refcount),
// the last unsubscribe stops it — a client disconnect can never
// strand one.

import (
	"encoding/json"
	"time"

	"github.com/whyiyhw/seek/internal/subagent"
)

// topicSubagents names the subagents lifecycle topic — the SSE
// `event:` field AND the envelope's "topic" value. A constant so a
// future topic extends the set deliberately (hooks did, W5 c_xz2bey).
const topicSubagents = "subagents"

// topicChanBuf is the per-client topic buffer: 256 queued events with
// drop-OLDEST eviction (the inspect-rpc §4.2 backpressure the web PRD
// §5 adopts as "hub 满丢老"): a full buffer loses its oldest event.
// The payloads are folded rows — the client merges by sub_sid, so
// losing an old row to keep the newest is strictly better than
// dropping the incoming one; a lagged client resyncs via GET
// /subagents.
const topicChanBuf = 256

// subagentsTopicPoll is the tail-follow poll interval. The writers of
// subagents.jsonl are seek child processes in other processes — no
// in-process signal exists and stdlib has no watcher, so the follow is
// stat+read polling. Lifecycle events are sparse; 1s is invisible
// latency for a task panel at ~zero CPU. A var so tests tighten it.
var subagentsTopicPoll = time.Second

// topicEvent is the SSE envelope of one topic push. No "conv" field:
// topic events address the followed PROJECT, which several convs may
// be bound to — a conv stamp would be arbitrary, not authoritative.
type topicEvent struct {
	Topic string            `json:"topic"`
	Event string            `json:"event"`
	Sub   subagent.Subagent `json:"sub"`
}

// topicFollow is one shared tail-follow AND its subscriber set: subs
// holds every topic client of this index path's channel (the fanout
// set), and its length IS the refcount — stop closes when the last
// subscriber leaves. subs guarded by WebFrontend.mu; the followLoop
// goroutine owns everything else (its Follower).
type topicFollow struct {
	stop chan struct{}
	subs map[int]chan string
}

// subscribeTopic registers one SSE client under BOTH dimensions: conv
// (a channel identical to W1.3 subscribe's — same 16 buffer in
// f.subs, so chat fanout and isolation semantics are byte-identical)
// and topic (the 256 drop-oldest channel on the index path's follow),
// and starts or joins that path's shared follow. No topic-name
// registry: the caller has already validated the topic (only
// "subagents" exists) and resolved the index path — the follow IS the
// topic subscription. It pins the project resolved by the CALLER at
// this moment — a later /bind rebind does not redirect it
// (resubscribe to follow the new project; GET /subagents always
// resolves fresh).
func (f *WebFrontend) subscribeTopic(conv ConvID, indexPath string) (ch chan sseFrame, tch chan string, unsub func()) {
	// Anchor at SUBSCRIBE time, not first tick: an event appended
	// between the two would otherwise be anchored past and missed.
	// Outside mu on purpose — a stat, not map work.
	fo := subagent.NewFollower(indexPath)
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
	id := f.next // capture NOW: the closure must drop THIS subscriber, not whoever subscribed later (W1.3 pitfall)
	ch = make(chan sseFrame, 16)
	tch = make(chan string, topicChanBuf)
	f.subs[conv][id] = ch

	fl := f.follows[indexPath]
	if fl == nil {
		fl = &topicFollow{stop: make(chan struct{}), subs: make(map[int]chan string)}
		f.follows[indexPath] = fl
		go f.followLoop(fo, fl)
	}
	fl.subs[id] = tch

	return ch, tch, func() { f.dropTopicSub(conv, id, indexPath) }
}

// dropTopicSub is the unsub body shared by every topic subscribe
// (subagents and hooks): drop the conv channel, drop the follow
// membership, and stop the follow with its last subscriber — len(subs)
// IS the refcount, so a client disconnect can never strand one.
func (f *WebFrontend) dropTopicSub(conv ConvID, id int, path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subs[conv], id)
	if len(f.subs[conv]) == 0 {
		delete(f.subs, conv)
	}
	if fl := f.follows[path]; fl != nil {
		delete(fl.subs, id)
		if len(fl.subs) == 0 {
			delete(f.follows, path)
			close(fl.stop)
		}
	}
}

// followLoop is the shared follower goroutine for one index path. It
// exits when the last subscriber releases the follow (stop closes) —
// nothing else stops it, and it stops nothing else (pushFollow is
// best-effort). Per-tick Poll failures are already tolerated inside
// Follower.Poll (nothing consumed, nothing lost); the loop itself has
// no failure path that outlives stop.
func (f *WebFrontend) followLoop(fo *subagent.Follower, fl *topicFollow) {
	tick := time.NewTicker(subagentsTopicPoll)
	defer tick.Stop()
	for {
		select {
		case <-fl.stop:
			return
		case <-tick.C:
			for _, p := range fo.Poll() {
				ev, err := json.Marshal(topicEvent{Topic: topicSubagents, Event: p.Event, Sub: p.Sub})
				if err != nil {
					continue // Subagent always marshals; defensive only
				}
				f.pushFollow(fl, string(ev))
			}
		}
	}
}

// pushFollow fans one event out to THIS follow's subscribers with
// drop-OLDEST semantics — never blocking the follower, never reaching
// another follow's subscribers (another project's clients). The retry
// after an eviction cannot miss: mu is held and the follower goroutine
// is the only writer, so the freed slot survives until our send.
func (f *WebFrontend) pushFollow(fl *topicFollow, ev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range fl.subs {
		select {
		case ch <- ev:
		default:
			// Full: evict the OLDEST queued event, then retry.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}
