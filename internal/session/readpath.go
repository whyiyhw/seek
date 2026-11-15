// Lock-free, page-shaped read paths over the session JSONL store —
// the W4.2 asset-plane readers (feature-web-frontend §3.3 caveat).
//
// The serve process sharing these files with the seek subprocess
// writing them is TWO processes: no mutex crosses that line, so these
// readers take no locks at all — plain open + line scan. Safety is
// line-level: the writer's on-disk contract (one JSON object per
// '\n'-terminated line, header first, history never rewritten — see
// the package comment) means a reader can only ever observe (a) whole
// earlier lines, (b) a torn trailing chunk mid-append. (b) is skipped;
// the next read sees the finished line. seek's Save is tmp+rename
// atomic, which is strictly friendlier than that — but the reader
// deliberately does not rely on it, so a future true-append write path
// stays drop-in.

package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// NewStoreAt returns a Store rooted at an explicit directory (created
// if missing). The *At shape follows controlplane.OpenBindingStoreAt:
// embedders and tests inject a concrete root while production resolves
// the env ladder in NewStore.
func NewStoreAt(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("session: NewStoreAt empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("session: mkdir %q: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// path joins the store dir with <id>.jsonl after applying the same id
// rule Load uses — one rule for every reader, and callers above the
// store (e.g. the RPC methods) can pre-validate with it so a traversal
// attempt never becomes a filesystem error.
func (s *Store) path(id string) (string, error) {
	if id == "" {
		return "", errors.New("session: empty id")
	}
	if strings.ContainsAny(id, "/\\.") {
		return "", fmt.Errorf("session: invalid id %q", id)
	}
	return filepath.Join(s.dir, id+".jsonl"), nil
}

// LoadHeader reads only line 1 (the header object) of session id's
// JSONL; Messages stay nil. The cheap metadata read behind asset-plane
// session/get: the whole file may be mid-append by another process,
// but the header line predates every message append, so a torn tail
// never reaches this reader. A legacy v1 body (messages inline in the
// single JSON object) is nil-ed defensively — callers get header
// fields only, same as the JSONL shape.
func (s *Store) LoadHeader(id string) (*Session, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("session: open %s.jsonl: %w", id, err)
	}
	defer f.Close()
	var sess Session
	if err := json.NewDecoder(f).Decode(&sess); err != nil {
		return nil, fmt.Errorf("session: decode header %s: %w", id, err)
	}
	sess.Messages = nil
	return &sess, nil
}

// ReadMessagesPage returns up to limit messages of session id starting
// at message index offset (message 0 is file line 2), as raw JSON —
// bytes verbatim from the file, unknown fields included — plus whether
// at least one more complete message exists past the returned page.
// Together with a cursor of "offset just past the returned page" this
// is a pagination stable under appends: line offsets never shift when
// lines are only added.
//
// Tail semantics (the §3.3 caveat, made concrete): a trailing chunk
// WITHOUT '\n' is an append in progress — skipped unless its bytes
// already form complete JSON (a write torn exactly after the closing
// brace is a whole message; including it now and after the '\n'
// arrives gives the same index, so either choice is stable). A
// COMPLETE line that is not valid JSON is corruption and errors, the
// same posture as Load. Blank lines are skipped (Load's decoder skips
// inter-value whitespace too). v2+ JSONL only: a legacy v1 single-JSON
// session has no message lines and reads as empty (v1 files carry a
// .json extension and never surface through List by id).
func (s *Store) ReadMessagesPage(id string, offset, limit int) ([]json.RawMessage, bool, error) {
	if offset < 0 || limit <= 0 {
		return nil, false, fmt.Errorf("session: ReadMessagesPage offset=%d limit=%d: want offset>=0, limit>0", offset, limit)
	}
	path, err := s.path(id)
	if err != nil {
		return nil, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("session: open %s.jsonl: %w", id, err)
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64<<10)
	var page []json.RawMessage
	more := false
	first := true // line 1 is the header
	idx := -1
	for {
		line, rerr := r.ReadBytes('\n')
		if rerr != nil && rerr != io.EOF {
			return nil, false, fmt.Errorf("session: read %s.jsonl: %w", id, rerr)
		}
		if first {
			first = false
			if rerr == io.EOF {
				break // empty file or torn header: nothing to page
			}
			continue
		}
		trimmed := bytes.TrimSuffix(line, []byte{'\n'})
		if len(trimmed) == 0 {
			if rerr == io.EOF {
				break
			}
			continue // blank line
		}
		if !json.Valid(trimmed) {
			if rerr == io.EOF {
				break // torn append tail: skip; the next call sees it whole
			}
			return nil, false, fmt.Errorf("session: corrupt message line %d in %s.jsonl", idx+3, id)
		}
		idx++
		if idx < offset {
			if rerr == io.EOF {
				break
			}
			continue
		}
		if len(page) == limit {
			more = true
			break // one complete line past the page is all "more" means
		}
		page = append(page, json.RawMessage(trimmed))
		if rerr == io.EOF {
			break
		}
	}
	return page, more, nil
}
