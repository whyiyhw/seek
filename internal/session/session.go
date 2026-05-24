// Package session persists seek conversations to disk so they can be
// resumed across runs, branched, and summarised. One session = one
// JSONL file in ~/.seek/sessions/ (or $SEEK_SESSIONS_DIR if overridden;
// see NewStore for the full precedence ladder).
//
// File format (JSONL, one JSON object per line):
//
//	line 1:    session header — all metadata fields, no messages key
//	line 2..N: one deepseek.Message per line, in order
//
// Benefits over a single JSON blob:
//   - Append-friendly: new messages can be streamed without rewriting
//     the header (future optimisation).
//   - Partial-crash safety: a truncated write loses at most one message
//     line; earlier lines are always valid.
//   - grep/tail/jq friendly: standard ML dataset format.
//
// Trade-offs:
//   - Save still rewrites the whole file atomically (temp + rename).
//     True append optimisation is a follow-up; correctness first.
//   - No locking. seek is single-user / single-process.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// CurrentSchemaVersion is incremented whenever the on-disk layout
// changes in a backward-incompatible way. JSONL format = version 2.
const CurrentSchemaVersion = 2

// Session is the in-memory (and on-disk) representation of one
// conversation. All time fields are UTC; ID is sortable by creation
// time so a directory listing is naturally ordered.
type Session struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Model         string    `json:"model"`
	Yolo          bool      `json:"yolo"`
	Plan          bool      `json:"plan,omitempty"`
	CWD           string    `json:"cwd"`
	SystemPrompt  string    `json:"system_prompt,omitempty"`
	// Messages is omitted from the JSONL header line and written as
	// individual lines 2..N instead. omitempty ensures the key is
	// absent from the header even when the slice is non-nil.
	Messages  []deepseek.Message `json:"messages,omitempty"`
	Turns     int                `json:"turns"`
	ToolCalls int                `json:"tool_calls"`
	Usage     deepseek.Usage     `json:"usage"`
	// ParentID is set for sessions created by /branch or /compact —
	// points at the session this one was forked from.
	ParentID string `json:"parent_id,omitempty"`
	// Effort overrides the reasoning_effort sent to DeepSeek. Empty
	// (the on-disk default) means "do not override" — the agent falls
	// back to ShouldEnableThinking() on the active model. Values: "" |
	// "high" | "max". Persists per-session so a one-off escalation
	// doesn't silently leak to future sessions; omitempty keeps the
	// JSONL header tidy for the common no-override case.
	Effort string `json:"effort,omitempty"`
	// Lang records the response language preference for this session.
	// "" or "auto" = detect from system locale; "en" = English;
	// "zh" = Chinese. Stored per-session so a one-off switch doesn't
	// leak to future sessions; omitempty keeps the JSONL header tidy
	// for the common auto-detect case.
	Lang string `json:"lang,omitempty"`
}

// New constructs a fresh Session with a timestamp-based ID.
func New(model, cwd, systemPrompt string, yolo, plan bool) *Session {
	now := time.Now().UTC()
	return &Session{
		SchemaVersion: CurrentSchemaVersion,
		ID:            generateID(now),
		CreatedAt:     now,
		UpdatedAt:     now,
		Model:         model,
		Yolo:          yolo,
		Plan:          plan,
		CWD:           cwd,
		SystemPrompt:  systemPrompt,
	}
}

// generateID returns a sortable ID: "20260121-103045-a1b2c3"
// (timestamp + 6 random hex chars). Lexical order == creation order.
// On entropy exhaustion it falls back to a nanosecond-granularity suffix
// so sessions never collide from a zero-valued random buffer.
func generateID(t time.Time) string {
	var rnd [3]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		// Fallback: nanosecond-precision hex suffix from the timestamp.
		// The fractional seconds make IDs unique even at high concurrency.
		return fmt.Sprintf("%s-%s",
			t.Format("20060102-150405"),
			fmt.Sprintf("%06x", t.Nanosecond()/1000))
	}
	return fmt.Sprintf("%s-%s",
		t.Format("20060102-150405"),
		hex.EncodeToString(rnd[:]))
}

// Touch updates UpdatedAt to now.
func (s *Session) Touch() { s.UpdatedAt = time.Now().UTC() }

// Repair fixes two replay-blocking shapes in the message history and
// returns the number of trailing messages dropped (0 == no trimming
// done — empty-content backfills are silent because they don't change
// history length).
//
// Failure modes covered:
//
//  1. Trailing orphan tool_calls: if seek was interrupted while the
//     model was streaming a tool_call, the assistant message was
//     persisted with tool_calls but no matching tool result messages.
//     Every subsequent API call then fails with "An assistant message
//     with 'tool_calls' must be followed by tool messages responding
//     to each 'tool_call_id'", leaving the user with a session they
//     can't continue without manual surgery.
//  2. Empty tool-role Content: an earlier seek build let tools that
//     "succeed silently" (memory_observe) persist tool messages with
//     Content="". `deepseek.Message.Content` has `omitempty`, so the
//     `content` field disappears from the wire body and DeepSeek
//     rejects the next call with `messages[N]: missing field
//     'content'`. We backfill those in place; the live agent path now
//     prevents new ones via buildToolResultMsg in pkg/agent.
func (s *Session) Repair() int {
	backfillEmptyToolContent(s.Messages)
	repaired, dropped := repairMessages(s.Messages)
	s.Messages = repaired
	return dropped
}

// emptyToolContentPlaceholder mirrors pkg/agent.emptyToolContentPlaceholder.
// Duplicated rather than imported because internal/session must not
// depend on pkg/agent (the dependency goes the other way). The two
// constants are tested for byte-equality in session_test.go via a
// reference string check so a future drift is caught.
const emptyToolContentPlaceholder = "(no output)"

// backfillEmptyToolContent mutates msgs in place: tool-role messages
// with empty Content get a neutral placeholder so the wire body
// produced by the next API call has a `content` field present.
func backfillEmptyToolContent(msgs []deepseek.Message) {
	for i := range msgs {
		if msgs[i].Role == deepseek.RoleTool && msgs[i].Content == "" {
			msgs[i].Content = emptyToolContentPlaceholder
		}
	}
}

func repairMessages(msgs []deepseek.Message) (_ []deepseek.Message, dropped int) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != deepseek.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		needed := make(map[string]bool, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			needed[tc.ID] = true
		}
		for j := i + 1; j < len(msgs); j++ {
			if msgs[j].Role == deepseek.RoleTool {
				delete(needed, msgs[j].ToolCallID)
			}
		}
		if len(needed) == 0 {
			return msgs, 0
		}
		return msgs[:i], len(msgs) - i
	}
	return msgs, 0
}

// Fork returns a new Session branching off s: fresh ID, ParentID
// pointing at s, independent copy of the message slice, reset
// counters/usage. Model / Yolo / Plan / CWD / SystemPrompt are inherited.
//
// The parent is untouched in memory; callers that want it on disk at
// the fork point should Save it before calling Fork.
func (s *Session) Fork() *Session {
	now := time.Now().UTC()
	msgs := make([]deepseek.Message, len(s.Messages))
	for i, m := range s.Messages {
		if len(m.ToolCalls) > 0 {
			m.ToolCalls = append([]deepseek.ToolCall(nil), m.ToolCalls...)
		}
		msgs[i] = m
	}
	return &Session{
		SchemaVersion: CurrentSchemaVersion,
		ID:            generateID(now),
		CreatedAt:     now,
		UpdatedAt:     now,
		Model:         s.Model,
		Yolo:          s.Yolo,
		Plan:          s.Plan,
		CWD:           s.CWD,
		SystemPrompt:  s.SystemPrompt,
		Messages:      msgs,
		ParentID:      s.ID,
		Lang:          s.Lang,
	}
}

// ---------- Store ----------

// Store reads and writes Sessions to a directory.
type Store struct{ dir string }

// NewStore returns a Store rooted at the seek sessions directory.
// Path resolution (precedence high→low):
//
//  1. $SEEK_SESSIONS_DIR — fine-grain override for the sessions dir
//     alone, useful when a user wants per-project session stores
//     while keeping mcp.json / skills under the shared root.
//  2. $SEEK_HOME/sessions — explicit root override (see paths.Home).
//  3. ~/.seek/sessions — the default.
//
// The directory is created if missing.
func NewStore() (*Store, error) {
	dir := os.Getenv("SEEK_SESSIONS_DIR")
	if dir == "" {
		resolved, err := paths.Sessions()
		if err != nil {
			return nil, err
		}
		dir = resolved
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: mkdir %q: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the resolved storage directory.
func (s *Store) Dir() string { return s.dir }

// Save writes the session as JSONL atomically (write tmp, rename).
//
//	line 1:    session header (all fields except messages)
//	line 2..N: one deepseek.Message per line
func (s *Store) Save(sess *Session) error {
	if sess == nil {
		return errors.New("session: Save nil")
	}
	if sess.ID == "" {
		return errors.New("session: Save with empty ID")
	}
	sess.Touch()

	final := filepath.Join(s.dir, sess.ID+".jsonl")
	tmp := final + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("session: open tmp %s: %w", sess.ID, err)
	}
	enc := json.NewEncoder(f)

	// Line 1: header — Session with Messages nil so omitempty drops it.
	header := *sess
	header.Messages = nil
	if err := enc.Encode(&header); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("session: encode header %s: %w", sess.ID, err)
	}

	// Lines 2..N: messages.
	for i := range sess.Messages {
		if err := enc.Encode(&sess.Messages[i]); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("session: encode message %d of %s: %w", i, sess.ID, err)
		}
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("session: sync tmp %s: %w", sess.ID, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("session: close tmp %s: %w", sess.ID, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("session: rename %s: %w", sess.ID, err)
	}
	return nil
}

// Load reads a session by ID.
func (s *Store) Load(id string) (*Session, error) {
	if id == "" {
		return nil, errors.New("session: Load empty id")
	}
	if strings.ContainsAny(id, "/\\.") {
		return nil, fmt.Errorf("session: invalid id %q", id)
	}

	path := filepath.Join(s.dir, id+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session: open %s.jsonl: %w", id, err)
	}
	defer f.Close()
	return decodeJSONL(f, id)
}

// decodeJSONL decodes a JSONL session file: line 1 → header,
// remaining lines → messages appended to header.Messages.
func decodeJSONL(r io.Reader, id string) (*Session, error) {
	dec := json.NewDecoder(r)
	var sess Session
	if err := dec.Decode(&sess); err != nil {
		return nil, fmt.Errorf("session: decode header %s: %w", id, err)
	}
	for dec.More() {
		var msg deepseek.Message
		if err := dec.Decode(&msg); err != nil {
			return nil, fmt.Errorf("session: decode message in %s: %w", id, err)
		}
		sess.Messages = append(sess.Messages, msg)
	}
	return &sess, nil
}

// Latest returns the session with the most-recent UpdatedAt, or nil
// when the store is empty. Used by --continue.
func (s *Store) Latest() (*Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	ids := collectIDs(entries)
	type result struct {
		id string
		at time.Time
	}
	results := make([]result, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			meta, err := s.loadMeta(id)
			if err == nil {
				results[i] = result{id: id, at: meta.UpdatedAt}
			}
		}(i, id)
	}
	wg.Wait()
	var bestID string
	var bestAt time.Time
	for _, r := range results {
		if r.id != "" && r.at.After(bestAt) {
			bestAt = r.at
			bestID = r.id
		}
	}
	if bestID == "" {
		return nil, nil
	}
	return s.Load(bestID)
}

// SessionInfo is the cheap metadata view returned by List.
type SessionInfo struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
	Model     string
	Turns     int
	ToolCalls int
	ParentID  string
}

// List returns metadata for every session in the store, newest first.
// The second return value collects per-file load errors; the third is
// a fatal error (e.g. ReadDir failed).
func (s *Store) List() ([]SessionInfo, []error, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, err
	}
	ids := collectIDs(entries)
	type result struct {
		info SessionInfo
		err  error
	}
	results := make([]result, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			meta, err := s.loadMeta(id)
			results[i] = result{info: meta, err: err}
		}(i, id)
	}
	wg.Wait()
	var out []SessionInfo
	var errs []error
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		out = append(out, r.info)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, errs, nil
}

// collectIDs returns the unique session IDs present in the directory.
func collectIDs(entries []os.DirEntry) []string {
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	return ids
}

// loadMeta reads only the session header (JSONL line 1) without
// allocating the full message history.
func (s *Store) loadMeta(id string) (SessionInfo, error) {
	if strings.ContainsAny(id, "/\\.") {
		return SessionInfo{}, fmt.Errorf("session: invalid id %q", id)
	}

	path := filepath.Join(s.dir, id+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("session: open %s.jsonl: %w", id, err)
	}
	defer f.Close()
	var sess Session
	if decErr := json.NewDecoder(f).Decode(&sess); decErr != nil {
		return SessionInfo{}, fmt.Errorf("session: decode header %s: %w", id, decErr)
	}
	return SessionInfo{
		ID:        sess.ID,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		Model:     sess.Model,
		Turns:     sess.Turns,
		ToolCalls: sess.ToolCalls,
		ParentID:  sess.ParentID,
	}, nil
}
