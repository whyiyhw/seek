// Package session persists seek conversations to disk so they can be
// resumed across runs, branched, and summarised. One session = one
// JSON file in ~/.config/seek/sessions/ (or $SEEK_SESSIONS_DIR if
// overridden).
//
// Trade-offs:
//
//   - JSON-per-session, not JSONL across all sessions. Atomic write
//     (temp + rename) is simple; sessions are small (~MB at most).
//   - Save on every TurnEnd. Cheap enough not to bother batching.
//   - No locking. seek is single-user / single-process; concurrent
//     writes to the same session ID would be a bug, not a use case.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Session is the on-disk representation of one conversation. All time
// fields are UTC; SessionID is sortable by creation time so a simple
// directory listing is already ordered.
type Session struct {
	ID           string             `json:"id"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	Model        string             `json:"model"`
	Yolo         bool               `json:"yolo"`
	CWD          string             `json:"cwd"`
	SystemPrompt string             `json:"system_prompt,omitempty"`
	Messages     []deepseek.Message `json:"messages"`
	Turns        int                `json:"turns"`
	ToolCalls    int                `json:"tool_calls"`
	Usage        deepseek.Usage     `json:"usage"`
	// ParentID is set for sessions created by /branch — points at the
	// session this one was forked from.
	ParentID string `json:"parent_id,omitempty"`
}

// New constructs a fresh Session with a timestamp-based ID.
func New(model, cwd, systemPrompt string, yolo bool) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:           generateID(now),
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        model,
		Yolo:         yolo,
		CWD:          cwd,
		SystemPrompt: systemPrompt,
	}
}

// generateID returns a sortable ID like "20260121-103045-a1b2c3":
// timestamp + 6 hex chars. Lexical order = creation order.
func generateID(t time.Time) string {
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	return fmt.Sprintf("%s-%s",
		t.Format("20060102-150405"),
		hex.EncodeToString(rnd[:]))
}

// Touch updates UpdatedAt to now.
func (s *Session) Touch() { s.UpdatedAt = time.Now().UTC() }

// Fork returns a new Session that branches off s: fresh ID, ParentID
// pointing at s, an independent copy of the message slice, and reset
// counters/usage. Model / Yolo / CWD / SystemPrompt are inherited so
// the branch picks up where the parent left off.
//
// The parent is left untouched in memory; callers that want it on disk
// at the fork point should Save it before forking.
func (s *Session) Fork() *Session {
	now := time.Now().UTC()
	msgs := make([]deepseek.Message, len(s.Messages))
	copy(msgs, s.Messages)
	return &Session{
		ID:           generateID(now),
		CreatedAt:    now,
		UpdatedAt:    now,
		Model:        s.Model,
		Yolo:         s.Yolo,
		CWD:          s.CWD,
		SystemPrompt: s.SystemPrompt,
		Messages:     msgs,
		ParentID:     s.ID,
	}
}

// Store reads and writes Sessions to a directory.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at the seek sessions directory.
// $SEEK_SESSIONS_DIR overrides the default $XDG_CONFIG_HOME/seek/sessions
// (falling back to ~/.config/seek/sessions/). The directory is created
// if missing.
func NewStore() (*Store, error) {
	dir := os.Getenv("SEEK_SESSIONS_DIR")
	if dir == "" {
		base, err := userConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(base, "seek", "sessions")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: mkdir %q: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the resolved storage directory (useful for error
// messages and CLI display).
func (s *Store) Dir() string { return s.dir }

func userConfigDir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// Save writes the session atomically: write to <id>.json.tmp, then
// rename. On POSIX, rename is atomic — a concurrent reader either
// sees the old version or the new one, never a partial file.
func (s *Store) Save(sess *Session) error {
	if sess == nil {
		return errors.New("session: Save nil")
	}
	if sess.ID == "" {
		return errors.New("session: Save with empty ID")
	}
	sess.Touch()

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("session: encode %s: %w", sess.ID, err)
	}

	final := filepath.Join(s.dir, sess.ID+".json")
	tmp := final + ".tmp"

	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("session: write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("session: rename: %w", err)
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
	path := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("session: read %s: %w", id, err)
	}
	var out Session
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("session: decode %s: %w", id, err)
	}
	return &out, nil
}

// Latest returns the session with the most-recent UpdatedAt, or nil
// with a nil error when the store is empty. Used by --continue.
func (s *Store) Latest() (*Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var newest *Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		sess, err := s.Load(id)
		if err != nil {
			// Skip unreadable files rather than fail outright — a
			// botched session shouldn't lock the user out of every
			// other one.
			continue
		}
		if newest == nil || sess.UpdatedAt.After(newest.UpdatedAt) {
			newest = sess
		}
	}
	return newest, nil
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
// Loading all files is fine for a personal tool — even 1000 sessions
// is a single millisecond of work.
func (s *Store) List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		sess, err := s.Load(id)
		if err != nil {
			continue
		}
		out = append(out, SessionInfo{
			ID:        sess.ID,
			CreatedAt: sess.CreatedAt,
			UpdatedAt: sess.UpdatedAt,
			Model:     sess.Model,
			Turns:     sess.Turns,
			ToolCalls: sess.ToolCalls,
			ParentID:  sess.ParentID,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}
