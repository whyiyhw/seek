package controlplane

// M2 router: the plane's per-message brain. One Inbound flows through
// the §4 lifecycle IN THIS ORDER (the order is the PRD contract):
//
//	去重(EventID LRU) → DirectAt 过滤 → 校验 → 指令 / ConvID→binding 路由
//
// Commands (/bind /projects /switch /new /cancel) are plane-parsed and
// frontend-agnostic (D-C2); ordinary prompts look up the ConvID's
// binding, exec seek in the bound directory via the M3 Executor
// (executor.go), write the fresh session id back into the binding
// (D-C3) and push the reply out through the frontend the conversation
// came from (Send-by-prefix, D-C1). An unbound conversation's first
// ordinary message is answered with a "bind first" hint, not an error.
//
// /cancel is router-owned, deliberately OUTSIDE the executor: a cancel
// must never queue behind the run it is meant to kill. The router
// tracks each conversation's live run contexts; /cancel fires them all
// (the in-flight one AND any still queued behind the per-conv gate —
// "stop this conversation's work" is the honest UX).

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// defaultDedupCapacity bounds the EventID LRU. Feishu redelivers within
// seconds and mints fresh ids per event; a few hundred slots would do.
// 1024 leaves generous headroom for a chatty multi-group day.
const defaultDedupCapacity = 1024

// RouterConfig tunes a Router. Zero fields fill from defaults; the
// serve CLI maps plane config onto this struct (projects candidates,
// global yolo posture).
type RouterConfig struct {
	// Projects are the candidate directories the /projects command
	// renders (config `serve.projects`, used as given). Binding to any
	// other existing directory stays legal — candidates are a
	// convenience, not an allowlist.
	Projects []string
	// Yolo is the global serve.yolo switch: exec requests run with
	// --yolo when this OR the binding's own flag is set (D-C5).
	Yolo bool
	// DedupCapacity bounds the EventID seen-set; <=0 → default.
	DedupCapacity int
}

// Router is the plane's inbound handler: hand it to Serve as `handle`
// and every dispatched Inbound goes through the lifecycle above. Safe
// for concurrent use (Serve dispatches each message in its own
// goroutine; the store, dedup set and active-run map are all guarded).
type Router struct {
	fronts map[string]Frontend // by Name() — Send-by-ConvID-prefix lookup
	store  *BindingStore
	exec   *Executor
	cfg    RouterConfig
	dedup  *eventDedup

	mu     sync.Mutex
	active map[ConvID][]*runCancel // live/queued run cancels, oldest first
}

// runCancel boxes one run's cancel func so the registry can compare
// entries by pointer identity (funcs themselves are not comparable).
type runCancel struct{ cancel context.CancelFunc }

// NewRouter builds a Router over the given frontends (Send targets,
// keyed by Name), binding store and executor. Build one per serve
// process, together with its Serve loop.
func NewRouter(fronts []Frontend, store *BindingStore, exec *Executor, cfg RouterConfig) *Router {
	byName := make(map[string]Frontend, len(fronts))
	for _, f := range fronts {
		if f == nil {
			continue // Serve already rejects nils; defensive here
		}
		byName[f.Name()] = f
	}
	if cfg.DedupCapacity <= 0 {
		cfg.DedupCapacity = defaultDedupCapacity
	}
	return &Router{
		fronts: byName,
		store:  store,
		exec:   exec,
		cfg:    cfg,
		dedup:  newEventDedup(cfg.DedupCapacity),
		active: make(map[ConvID][]*runCancel),
	}
}

// Handle is the Serve handler: one Inbound in, zero or one Send out.
// It blocks until the reply is delivered (for prompts: until the seek
// run finishes) — Serve runs it on the handler goroutine, and the
// executor's per-conv gate, not Handle, owns serialisation.
func (r *Router) Handle(ctx context.Context, m Inbound) {
	// 1. Dedup (§4): at-least-once channels redeliver; the first
	// sighting processes, repeats die here. Empty EventID (a frontend
	// without native ids — e.g. a webhook adapter) is NEVER deduped:
	// collapsing those would eat distinct messages.
	if m.EventID != "" && !r.dedup.firstSighting(m.EventID) {
		return
	}

	// 2. DirectAt filter (D-F3 normalised): group chatter must not
	// trigger anything — not prompts, not commands. Silent drop; a
	// bot answering every un-@-mentioned message is spam by design.
	if !m.DirectAt {
		return
	}

	// 3. Validate: a malformed event (bad ConvID, empty text) is WARNed
	// and skipped (§6 malformed bar) — the plane never crashes on it,
	// and never replies into an address it cannot trust.
	if m.Conv.FrontendName() == "" {
		fmt.Fprintf(os.Stderr, "controlplane: router: drop malformed inbound (conv %q, %d chars)\n", m.Conv, len(m.Text))
		return
	}
	if strings.TrimSpace(m.Text) == "" {
		fmt.Fprintf(os.Stderr, "controlplane: router: drop empty message from %s (event %q)\n", m.Conv, m.EventID)
		return
	}

	// 4. Route: leading-word "/" → command; anything else → prompt.
	if cmd, rest, isCmd := cutCommand(m.Text); isCmd {
		r.handleCommand(ctx, m, cmd, rest)
		return
	}
	r.handlePrompt(ctx, m)
}

// cutCommand splits "/bind /some/path" into ("bind", "/some/path",
// true). A leading word starting with "/" claims the command
// namespace; unknown names still route as commands so the user gets
// the command list back instead of a baffling seek run.
func cutCommand(text string) (cmd, rest string, ok bool) {
	word, rest, _ := strings.Cut(text, " ")
	if !strings.HasPrefix(word, "/") || len(word) < 2 {
		return "", "", false
	}
	return strings.TrimPrefix(word, "/"), strings.TrimSpace(rest), true
}

// ---- commands (D-C2) ----

func (r *Router) handleCommand(ctx context.Context, m Inbound, cmd, rest string) {
	switch cmd {
	case "bind":
		r.cmdBind(ctx, m, rest)
	case "switch":
		r.cmdSwitch(ctx, m, rest)
	case "new":
		r.cmdNew(ctx, m)
	case "projects":
		r.cmdProjects(ctx, m)
	case "cancel":
		r.cmdCancel(ctx, m)
	default:
		r.reply(ctx, m.Conv, fmt.Sprintf("未知指令 /%s。可用：/bind <项目路径> · /projects · /switch <项目路径> · /new · /cancel", cmd))
	}
}

// resolveProjectPath turns a user-typed path into an absolute one:
// leading ~ expands against the home dir (a phone user cannot type
// /Users/<name>/... comfortably), relatives resolve against the serve
// process CWD, and the result must be an existing directory — /bind
// validating here is what keeps the store free of dead paths.
func resolveProjectPath(raw string) (string, error) {
	p := raw
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("解析 ~ (home): %w", err)
		}
		if p == "~" {
			p = home
		} else {
			p = filepath.Join(home, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("解析路径: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("目录不存在：%s", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("不是目录：%s", abs)
	}
	return abs, nil
}

// existingBinding returns the current binding (ok=true) so /bind and
// /switch can preserve the hand-editable Yolo flag across rebinds —
// changing the project must not silently reset a deliberate trust
// posture (bindings.json yolo:true is the only Yolo source in v1).
func (r *Router) existingBinding(conv ConvID) (Binding, bool) {
	b, err := r.store.Get(conv)
	if err != nil {
		return Binding{}, false
	}
	return b, true
}

// putBinding persists b (stamping UpdatedAt) or replies with the
// failure. Returns false when the write failed.
func (r *Router) putBinding(ctx context.Context, m Inbound, b Binding) bool {
	if err := r.store.Put(b); err != nil {
		r.reply(ctx, m.Conv, fmt.Sprintf("写入绑定失败：%v", err))
		return false
	}
	return true
}

// cmdBind: /bind <path> — bind (or rebind) the conversation to a
// project directory, always with a FRESH session (D-C2 "session 留空").
// Rebinding the very same path is legal and also resets the session:
// /bind is "start over here", not "no-op if unchanged".
func (r *Router) cmdBind(ctx context.Context, m Inbound, rawPath string) {
	if rawPath == "" {
		r.reply(ctx, m.Conv, "用法：/bind <项目路径>（目录必须存在；~ 开头会展开）")
		return
	}
	abs, err := resolveProjectPath(rawPath)
	if err != nil {
		r.reply(ctx, m.Conv, fmt.Sprintf("绑定失败：%v", err))
		return
	}
	b := Binding{ConvID: m.Conv, ProjectPath: abs}
	if old, ok := r.existingBinding(m.Conv); ok {
		b.Yolo = old.Yolo
	}
	if !r.putBinding(ctx, m, b) {
		return
	}
	r.reply(ctx, m.Conv, fmt.Sprintf("已绑定项目 %s（会话已重置）。直接发消息开始。", abs))
}

// cmdSwitch: /switch <path> — same effect as /bind but framed as a
// CHANGE: it requires an existing binding. On an unbound conversation
// it points at /bind instead of silently creating one.
func (r *Router) cmdSwitch(ctx context.Context, m Inbound, rawPath string) {
	if rawPath == "" {
		r.reply(ctx, m.Conv, "用法：/switch <项目路径>")
		return
	}
	if _, ok := r.existingBinding(m.Conv); !ok {
		r.reply(ctx, m.Conv, unboundHint)
		return
	}
	abs, err := resolveProjectPath(rawPath)
	if err != nil {
		r.reply(ctx, m.Conv, fmt.Sprintf("切换失败：%v", err))
		return
	}
	b := Binding{ConvID: m.Conv, ProjectPath: abs}
	if old, ok := r.existingBinding(m.Conv); ok {
		b.Yolo = old.Yolo
	}
	if !r.putBinding(ctx, m, b) {
		return
	}
	r.reply(ctx, m.Conv, fmt.Sprintf("已切换项目：%s（会话已重置）。", abs))
}

// unboundHint is the first-prompt answer for ordinary messages from an
// unbound conversation (D-C2). One wording, reused by /switch and
// /new so every "you need a binding first" reply reads the same.
const unboundHint = "该会话还没绑定项目。先发 /bind <项目路径>（目录必须存在），或 /projects 看候选。"

// cmdNew: /new — keep the project, drop the session. The next message
// starts a fresh seek conversation in the same directory.
func (r *Router) cmdNew(ctx context.Context, m Inbound) {
	old, ok := r.existingBinding(m.Conv)
	if !ok {
		r.reply(ctx, m.Conv, unboundHint)
		return
	}
	old.SessionID = ""
	if !r.putBinding(ctx, m, old) {
		return
	}
	r.reply(ctx, m.Conv, fmt.Sprintf("已开新会话（项目不变：%s）。", old.ProjectPath))
}

// cmdProjects: /projects — render the configured candidates, marking
// the one this conversation is bound to. Candidates are read-only
// convenience (config `serve.projects`); binding elsewhere stays legal,
// and a binding outside the list is still shown.
func (r *Router) cmdProjects(ctx context.Context, m Inbound) {
	var sb strings.Builder
	sb.WriteString("候选项目：\n")
	if len(r.cfg.Projects) == 0 {
		sb.WriteString("（config 未配置 serve.projects —— /bind 可绑定任意存在的目录）\n")
	}
	bound, isBound := r.existingBinding(m.Conv)
	for _, p := range r.cfg.Projects {
		mark := ""
		if isBound {
			if abs, err := filepath.Abs(p); err == nil && abs == bound.ProjectPath {
				mark = "  ← 当前"
			}
		}
		sb.WriteString("• " + p + mark + "\n")
	}
	if isBound {
		sb.WriteString("当前绑定：" + bound.ProjectPath)
		if bound.SessionID != "" {
			sb.WriteString("（会话 " + bound.SessionID + "）")
		}
	} else {
		sb.WriteString("当前未绑定，/bind <路径> 绑定。")
	}
	r.reply(ctx, m.Conv, sb.String())
}

// cmdCancel: /cancel — fire the cancellation of every live/queued run
// of THIS conversation. The run's own Handle observes the dead ctx and
// sends the terminal "已中断" reply (below); this ack confirms the
// request, it does not wait for the kill to land.
func (r *Router) cmdCancel(ctx context.Context, m Inbound) {
	r.mu.Lock()
	cancels := r.active[m.Conv]
	delete(r.active, m.Conv)
	r.mu.Unlock()

	if len(cancels) == 0 {
		r.reply(ctx, m.Conv, "当前没有正在运行的任务。")
		return
	}
	for _, rc := range cancels {
		rc.cancel()
	}
	noun := "当前 run"
	if len(cancels) > 1 {
		noun = fmt.Sprintf("%d 个排队/运行中的任务", len(cancels))
	}
	r.reply(ctx, m.Conv, "已中断 "+noun+"。")
}

// ---- prompts: binding lookup → exec → session writeback → reply ----

func (r *Router) handlePrompt(ctx context.Context, m Inbound) {
	b, err := r.store.Get(m.Conv)
	if err != nil {
		if errors.Is(err, ErrBindingNotFound) {
			r.reply(ctx, m.Conv, unboundHint)
			return
		}
		r.reply(ctx, m.Conv, fmt.Sprintf("读取绑定失败：%v", err))
		return
	}

	// The run ctx is the one /cancel fires. Derived from the handler
	// ctx so shutdown-drain cancellation still kills runs (Serve's
	// "handlers MUST respect their ctx" contract).
	runCtx, cancel := context.WithCancel(ctx)
	rc := &runCancel{cancel: cancel}
	r.trackRun(m.Conv, rc)
	defer r.untrackRun(m.Conv, rc)

	res, err := r.exec.Run(runCtx, ExecRequest{
		Conv:      string(m.Conv),
		Dir:       b.ProjectPath,
		SessionID: b.SessionID,
		Message:   m.Text,
		Yolo:      r.cfg.Yolo || b.Yolo,
	})
	if err != nil {
		// Distinguish the two dead-ctx shapes: OUR /cancel fired (run
		// ctx dead, parent alive) vs shutdown drain (parent dead too).
		// Timeouts have neither dead and read as plain failures.
		if runCtx.Err() != nil && ctx.Err() == nil {
			r.reply(ctx, m.Conv, "run 已中断。")
		} else {
			r.reply(ctx, m.Conv, "run 失败："+err.Error())
		}
		return
	}

	// Session writeback (D-C3): store the id the run reported. An
	// empty id (agent_end without one — accepted edge in §6 verified ①)
	// keeps the previously stored id: nothing better is known.
	if res.SessionID != "" && res.SessionID != b.SessionID {
		b.SessionID = res.SessionID
		if err := r.store.Put(b); err != nil {
			// Writeback failure must not eat the answer: reply anyway,
			// keep the old session on the binding (worse case: the next
			// message resumes an older context or self-heals a bad id).
			fmt.Fprintf(os.Stderr, "controlplane: router: writeback %s failed: %v\n", m.Conv, err)
		}
	}

	text := res.Reply
	if strings.TrimSpace(text) == "" {
		text = "（本次运行没有文本输出）"
	}
	r.reply(ctx, m.Conv, text)
}

// trackRun / untrackRun maintain the per-conv cancel registry.
// Multiple entries per conv are normal: a message queued behind a
// running sibling registers BEFORE the executor grants it the gate —
// /cancel must reach both. untrack matches by pointer so a finished
// run never unregisters a successor's cancel.
func (r *Router) trackRun(conv ConvID, rc *runCancel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active[conv] = append(r.active[conv], rc)
}

func (r *Router) untrackRun(conv ConvID, rc *runCancel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.active[conv]
	for i, c := range cur {
		if c == rc {
			r.active[conv] = append(cur[:i], cur[i+1:]...)
			return
		}
	}
}

// reply delivers one Reply through the frontend the conversation came
// from (Send-by-prefix, D-C1). Best-effort: a failed Send is logged
// and dropped — the Frontend port's own posture — never propagated.
func (r *Router) reply(ctx context.Context, conv ConvID, text string) {
	fe, ok := r.fronts[conv.FrontendName()]
	if !ok {
		fmt.Fprintf(os.Stderr, "controlplane: router: no frontend %q for %s — reply dropped\n", conv.FrontendName(), conv)
		return
	}
	if err := fe.Send(ctx, conv, Reply{Text: text}); err != nil {
		fmt.Fprintf(os.Stderr, "controlplane: router: send to %s failed: %v\n", conv, err)
	}
}

// ---- EventID dedup (bounded LRU) ----

// eventDedup is a mutex-guarded bounded set of recently seen event ids
// with true LRU eviction (repeat sightings refresh recency, matching
// the PRD's "LRU 去重" wording). At-least-once channels redeliver
// within seconds; capacity only bounds memory, and eviction beyond it
// costs at worst one duplicate execution of a long-gone event.
type eventDedup struct {
	mu   sync.Mutex
	cap  int
	ll   *list.List               // front = most recent
	elem map[string]*list.Element // id → ll element
}

func newEventDedup(capacity int) *eventDedup {
	return &eventDedup{
		cap:  capacity,
		ll:   list.New(),
		elem: make(map[string]*list.Element, capacity),
	}
}

// firstSighting reports whether id is new, recording it either way.
func (d *eventDedup) firstSighting(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if el, ok := d.elem[id]; ok {
		d.ll.MoveToFront(el) // LRU touch
		return false
	}
	d.elem[id] = d.ll.PushFront(id)
	if d.ll.Len() > d.cap {
		oldest := d.ll.Back()
		if oldest != nil {
			d.ll.Remove(oldest)
			delete(d.elem, oldest.Value.(string))
		}
	}
	return true
}
