package controlplane

// SSE topic dimension for /events (W3.2, card c_3736yl;
// feature-web-frontend §3.2 + §5): a connection opts into a topic
// with ?topic= alongside its conv. The two dimensions coexist without
// touching each other — the conv dimension (W1.3, webfrontend_conv.go)
// keeps its exact channel, buffer and drop-on-full semantics; the
// topic rides a SECOND channel on the same connection (nil-chan-
// blocks-forever lets handleEvents select over both uniformly).
//
// Today's only topic is `subagents` — the task panel's lifecycle
// pushes (started / terminal transitions). The producer is a shared
// tail-follow of the conv's bound-project subagents.jsonl (the W3.1
// reader's file; subagent.Follower folds with the same applyEvent
// core as ListProject, so list and push are one projection). The
// panel protocol is subscribe-FIRST, then GET /subagents for the
// snapshot: pushes patch it, history never replays.
//
// Follow lifecycle (the inspect-rpc §4.2 observer shape): the first
// subscriber of an index path starts ONE follower goroutine, later
// subscribers of the same path share it (refcount), the last
// unsubscribe stops it — a client disconnect can never strand one.

import (
	"encoding/json"
	"time"

	"github.com/whyiyhw/seek/internal/subagent"
)

// topicSubagents names the subagents lifecycle topic — the SSE
// `event:` field AND the envelope's "topic" value. A constant so a
// future topic (the W4 family) extends the set deliberately.
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

// topicFollow is one shared tail-follow: refs counts subscribers of
// the path, stop closes when it reaches zero. Guarded by WebFrontend.mu;
// the followLoop goroutine owns everything else (its Follower).
type topicFollow struct {
	stop chan struct{}
	refs int
}

// subscribeTopic registers one SSE client under BOTH dimensions: conv
// (a channel identical to W1.3 subscribe's — same 16 buffer in
// f.subs, so chat fanout and isolation semantics are byte-identical)
// and topic (the 256 drop-oldest channel in f.topics), and starts or
// joins the index path's shared follow. The follow pins the project
// resolved by the CALLER at this moment — a later /bind rebind does
// not redirect it (resubscribe to follow the new project; GET
// /subagents always resolves fresh).
func (f *WebFrontend) subscribeTopic(conv ConvID, topic, indexPath string) (ch, tch chan string, unsub func()) {
	// Anchor at SUBSCRIBE time, not first tick: an event appended
	// between the two would otherwise be anchored past and missed.
	// Outside mu on purpose — a stat, not map work.
	fo := subagent.NewFollower(indexPath)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[ConvID]map[int]chan string)
	}
	if f.subs[conv] == nil {
		f.subs[conv] = make(map[int]chan string)
	}
	if f.topics == nil {
		f.topics = make(map[string]map[int]chan string)
	}
	if f.topics[topic] == nil {
		f.topics[topic] = make(map[int]chan string)
	}
	if f.follows == nil {
		f.follows = make(map[string]*topicFollow)
	}
	f.next++
	id := f.next // capture NOW: the closure must drop THIS subscriber, not whoever subscribed later (W1.3 pitfall)
	ch = make(chan string, 16)
	tch = make(chan string, topicChanBuf)
	f.subs[conv][id] = ch
	f.topics[topic][id] = tch

	fl := f.follows[indexPath]
	if fl == nil {
		fl = &topicFollow{stop: make(chan struct{})}
		f.follows[indexPath] = fl
		go f.followLoop(fo, fl.stop)
	}
	fl.refs++

	return ch, tch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs[conv], id)
		if len(f.subs[conv]) == 0 {
			delete(f.subs, conv)
		}
		delete(f.topics[topic], id)
		if len(f.topics[topic]) == 0 {
			delete(f.topics, topic)
		}
		if fl := f.follows[indexPath]; fl != nil {
			fl.refs--
			if fl.refs <= 0 {
				delete(f.follows, indexPath)
				close(fl.stop)
			}
		}
	}
}

// followLoop is the shared follower goroutine for one index path. It
// exits when the last subscriber releases the follow (stop closes) —
// nothing else stops it, and it stops nothing else (pushTopic is
// best-effort). Per-tick Poll failures are already tolerated inside
// Follower.Poll (nothing consumed, nothing lost); the loop itself has
// no failure path that outlives stop.
func (f *WebFrontend) followLoop(fo *subagent.Follower, stop <-chan struct{}) {
	tick := time.NewTicker(subagentsTopicPoll)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			for _, p := range fo.Poll() {
				ev, err := json.Marshal(topicEvent{Topic: topicSubagents, Event: p.Event, Sub: p.Sub})
				if err != nil {
					continue // Subagent always marshals; defensive only
				}
				f.pushTopic(topicSubagents, string(ev))
			}
		}
	}
}

// pushTopic fans one event out to every subscriber of topic with
// drop-OLDEST semantics — never blocking the follower. The retry
// after an eviction cannot miss: mu is held and the follower goroutine
// is the only writer, so the freed slot survives until our send.
func (f *WebFrontend) pushTopic(topic, ev string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.topics[topic] {
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
