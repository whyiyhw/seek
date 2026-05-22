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
// Legacy .json files (schema_version ≤ 1) are transparently loaded and
// migrated on next Save.
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
	SchemaVersion int                `json:"schema_version"`
	ID            string             `json:"id"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
	Model         string             `json:"model"`
	Yolo          bool               `json:"yolo"`
	CWD           string             `json:"cwd"`
	SystemPrompt  string             `json:"system_prompt,omitempty"`
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
}

// New constructs a fresh Session with a timestamp-based ID.
func New(model, cwd, systemPrompt string, yolo bool) *Session {
	now := time.Now().UTC()
	return &Session{
		SchemaVersion: CurrentSchemaVersion,
		ID:            generateID(now),
		CreatedAt:     now,
		UpdatedAt:     now,
		Model:         model,
		Yolo:          yolo,
		CWD:           cwd,
		SystemPrompt:  systemPrompt,
	}
}

// generateID returns a sortable ID: "20260121-103045-a1b2c3"
// (timestamp + 6 random hex chars). Lexical order == creation order.
func generateID(t time.Time) string {
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	return fmt.Sprintf("%s-%s",
		t.Format("20060102-150405"),
		hex.EncodeToString(rnd[:]))
}

// Touch updates UpdatedAt to now.
func (s *Session) Touch() { s.UpdatedAt = time.Now().UTC() }

// Repair trims trailing orphan tool_calls shapes from the message
// history. Returns the number of messages dropped (0 == no repair
// needed).
//
// This exists because of a real-world failure mode: if seek was
// interrupted while the model was streaming a tool_call, the assistant
// message was persisted with tool_calls but no matching tool result
// messages. Every subsequent API call then fails with "An assistant
// message with 'tool_calls' must be followed by tool messages
// responding to each 'tool_call_id'", leaving the user with a session
// they can't continue without manual surgery.
func (s *Session) Repair() int {
	repaired, dropped := repairMessages(s.Messages)
	s.Messages = repaired
	return dropped
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
// counters/usage. Model / Yolo / CWD / SystemPrompt are inherited.
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
		CWD:           s.CWD,
		SystemPrompt:  s.SystemPrompt,
		Messages:      msgs,
		ParentID:      s.ID,
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

// Load reads a session by ID. Tries the new .jsonl format first; falls
// back to the legacy .json format for sessions written by older builds.
func (s *Store) Load(id string) (*Session, error) {
	if id == "" {
		return nil, errors.New("session: Load empty id")
	}
	if strings.ContainsAny(id, "/\\.") {
		return nil, fmt.Errorf("session: invalid id %q", id)
	}

	path := filepath.Join(s.dir, id+".jsonl")
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		return decodeJSONL(f, id)
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("session: open %s.jsonl: %w", id, err)
	}

	// Legacy .json fallback.
	legacyPath := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return nil, fmt.Errorf("session: read %s: %w", id, err)
	}
	var out Session
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("session: decode %s: %w", id, err)
	}
	return &out, nil
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
	var bestID string
	var bestAt time.Time
	for _, id := range collectIDs(entries) {
		meta, err := s.loadMeta(id)
		if err != nil {
			continue
		}
		if meta.UpdatedAt.After(bestAt) {
			bestAt = meta.UpdatedAt
			bestID = id
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
	var out []SessionInfo
	var errs []error
	for _, id := range collectIDs(entries) {
		meta, err := s.loadMeta(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, errs, nil
}

// collectIDs returns the unique session IDs present in the directory.
// .jsonl files take precedence over .json: if both exist for the same
// ID only one entry is returned (using the .jsonl version).
func collectIDs(entries []os.DirEntry) []string {
	seen := make(map[string]bool)
	var ids []string
	// First pass: .jsonl (canonical format).
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	// Second pass: legacy .json files not already covered by .jsonl.
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// loadMeta reads only the session header (line 1 for JSONL; incremental
// parse for legacy JSON) without allocating the full message history.
func (s *Store) loadMeta(id string) (SessionInfo, error) {
	if strings.ContainsAny(id, "/\\.") {
		return SessionInfo{}, fmt.Errorf("session: invalid id %q", id)
	}

	// Try JSONL: only decode line 1 — messages not loaded.
	path := filepath.Join(s.dir, id+".jsonl")
	f, err := os.Open(path)
	if err == nil {
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
	if !os.IsNotExist(err) {
		return SessionInfo{}, fmt.Errorf("session: open %s.jsonl: %w", id, err)
	}

	// Legacy .json fallback: incremental parse skips messages array.
	return s.loadMetaLegacyJSON(id)
}

// loadMetaLegacyJSON is the incremental-parser path for old .json
// sessions. It skips the messages array without allocating it.
func (s *Store) loadMetaLegacyJSON(id string) (SessionInfo, error) {
	f, err := os.Open(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return SessionInfo{}, fmt.Errorf("session: open %s.json: %w", id, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return SessionInfo{}, fmt.Errorf("session: invalid json in %s", id)
	}
	var info SessionInfo
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return info, fmt.Errorf("session: key in %s: %w", id, err)
		}
		switch key.(string) {
		case "id":
			_ = dec.Decode(&info.ID)
		case "created_at":
			_ = dec.Decode(&info.CreatedAt)
		case "updated_at":
			_ = dec.Decode(&info.UpdatedAt)
		case "model":
			_ = dec.Decode(&info.Model)
		case "turns":
			_ = dec.Decode(&info.Turns)
		case "tool_calls":
			_ = dec.Decode(&info.ToolCalls)
		case "parent_id":
			_ = dec.Decode(&info.ParentID)
		default:
			if err := skipJSONValue(dec); err != nil {
				return info, err
			}
		}
	}
	return info, nil
}

// skipJSONValue advances the decoder past the next JSON value without
// allocating Go objects. Handles nested objects and arrays.
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar already consumed
	}
	closing := json.Delim('}')
	if d == '[' {
		closing = ']'
	}
	for dec.More() {
		if err := skipJSONValue(dec); err != nil {
			return err
		}
	}
	endTok, err := dec.Token()
	if err != nil {
		return err
	}
	if endTok.(json.Delim) != closing {
		return fmt.Errorf("session: mismatched delimiters")
	}
	return nil
}
