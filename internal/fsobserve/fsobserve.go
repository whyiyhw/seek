// Package fsobserve tracks which files the model has actually looked at
// during a session, so a whole-file overwrite can be refused when it
// would be writing blind.
//
// # The hole this closes
//
// seek's `edit` tool is already safe by construction: it requires an
// exact `old_string` match with an expected occurrence count, so an edit
// built on a stale or imagined view of the file simply fails to match.
// The failure mode is a clear error, not a silent clobber.
//
// `write` has no such property. It is `os.WriteFile` — full replacement,
// no matching, no comparison. A model can overwrite a file it has never
// read, or one that changed on disk after it read it, and the tool will
// happily do it. The checkpoint snapshotter makes that recoverable, but
// recovery requires a human to NOTICE, which is exactly what does not
// happen during an unattended run.
//
// So the rule "read before you overwrite" — which lives in AGENTS.md and
// in the tool descriptions as advice — becomes a mechanism here.
//
// # Why stat, not a content hash
//
// The freshness token is (size, mtime), not a digest. `read` serves
// windowed views of large files; hashing would force a full read of
// every file the model peeks at, so a windowed peek into a 100 MB log
// would cost 100 MB of I/O to produce a token. Stat is O(1) regardless
// of file size.
//
// The tradeoff is deliberate and biased toward safety: `touch` with no
// content change reads as stale (a false "re-read please", costing one
// call), while a same-size write within the mtime granularity could read
// as fresh (a false negative). Git's stat cache makes the same bet for
// the same reason. If a false negative ever bites in practice, the fix
// is to add a digest for small files only — not to hash everything.
package fsobserve

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Status is the verdict for a proposed whole-file overwrite.
type Status int

const (
	// StatusOK means the write may proceed: either the file does not
	// exist yet (creation, nothing to clobber) or the model has seen its
	// current contents.
	StatusOK Status = iota
	// StatusUnseen means the file exists but nothing in this session has
	// read it. Overwriting would discard content the model never saw.
	StatusUnseen
	// StatusStale means the model read the file, but it changed on disk
	// afterwards — a concurrent editor, a build step, a git operation.
	// The model's content is based on a view that no longer holds.
	StatusStale
)

// Token is the cheap freshness fingerprint for a path: file identity
// plus size and mtime.
//
// The identity half (dev, ino) is what size+mtime alone cannot give.
// A coding agent's working tree is full of operations that swap the file
// behind a path rather than modifying it in place — `git checkout`,
// `git stash`, and essentially every formatter and code generator, which
// write a temp file and rename it over the target. Those change the
// inode; they do not reliably change the size, and on a filesystem with
// coarse mtime granularity they may not change mtime within the same
// tick either. dsh carries the same pair for the same reason
// (packages/fs/fs-local/src/fsio.ts:74-75 hashes
// `dev:ino:size:mtimeNs:ctimeNs`).
//
// Off Unix the identity half is always (0, 0) — see ident_other.go — so
// the token degrades to size+mtime rather than failing.
type Token struct {
	size    int64
	modTime time.Time
	dev     uint64
	ino     uint64
}

// tokenOf fingerprints an already-stat'd file.
func tokenOf(fi os.FileInfo) Token {
	dev, ino := fileIdent(fi)
	return Token{size: fi.Size(), modTime: fi.ModTime(), dev: dev, ino: ino}
}

// equal reports whether two fingerprints describe the same file in the
// same state. time.Time must be compared with Equal, not ==, because two
// Times can denote the same instant with different monotonic/location
// state.
func (t Token) equal(o Token) bool {
	return t.size == o.size &&
		t.dev == o.dev &&
		t.ino == o.ino &&
		t.modTime.Equal(o.modTime)
}

// Decision is the plan for one proposed whole-file write. It mirrors
// dsh's FsWriteIntent (createIfAbsent / replaceIfVersion): the caller
// does not receive a yes/no, it receives WHICH guarded operation to
// perform, so the guarantee can be enforced by the write syscall itself
// instead of by a check that has already gone stale by the time the
// write runs.
type Decision struct {
	// Status is the verdict. Anything other than StatusOK means refuse.
	Status Status
	// Guarded reports whether the caller must perform one of the two
	// guarded writes below. When false the caller does a plain
	// unconditional write — that is the case for a nil Store (the guard
	// is opt-in and must not change behaviour when unconfigured) and for
	// a target that is not a regular file (the write's own error is
	// clearer than anything this package could say about it).
	Guarded bool
	// Exists reports whether the target was present at plan time. When
	// false the caller must create EXCLUSIVELY (O_EXCL): that turns
	// "it was absent a moment ago" into "the kernel guarantees we
	// created it", closing the window between plan and write.
	Exists bool
	// Token is the observed fingerprint, meaningful only when
	// Status == StatusOK && Exists. The caller re-checks it against the
	// file it actually opened before replacing the contents.
	Token Token
}

// Matches reports whether fi is still the file this decision planned
// for. Callers should stat the OPEN FILE DESCRIPTOR rather than the
// path, so the file being verified is provably the file being written.
func (d Decision) Matches(fi os.FileInfo) bool {
	return d.Token.equal(tokenOf(fi))
}

// Store records per-path observations for one session. The zero value is
// not usable; call New. Safe for concurrent use — read-only tools
// dispatch as a parallel batch, so several reads can land at once.
type Store struct {
	mu   sync.Mutex
	seen map[string]Token
}

// New returns an empty Store. One per session: carrying observations
// across sessions would claim the model has seen a file that was only
// read in a previous conversation, which is precisely the blind-write
// case this package exists to catch.
func New() *Store { return &Store{seen: map[string]Token{}} }

// Observe records that the current on-disk state of path has been seen.
//
// Called by `read` after a successful read, and by `write` / `edit`
// after they change a file — the tool that just produced the content
// obviously knows it. Without the write/edit call, a legitimate
// read → edit → write sequence would trip the stale check on the
// model's OWN edit.
//
// A path that cannot be stat'd is silently ignored: nothing to record,
// and failing here would turn an unreadable-but-harmless path into a
// tool error far from its cause.
func (s *Store) Observe(path string) {
	if s == nil {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.seen[path] = tokenOf(fi)
	s.mu.Unlock()
}

// Forget drops any observation for path. Used when a file is deleted so
// a later recreation is not mistaken for a seen file.
func (s *Store) Forget(path string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.seen, path)
	s.mu.Unlock()
}

// Plan decides how a whole-file write to path must be performed.
//
// A nil Store plans an unguarded create-or-replace: the guard is opt-in,
// and a tool constructed without one must behave exactly as it did
// before this package existed.
//
// Plan's answer is advisory about TIMING and authoritative about
// PERMISSION. Status is the decision; Exists and Token tell the caller
// which guarded syscall re-establishes that decision atomically at write
// time. A caller that merely trusts Status and then calls os.WriteFile
// has a race; see the write tool for the intended shape.
func (s *Store) Plan(path string) Decision {
	if s == nil {
		// Guard not configured: behave exactly as an unguarded write.
		// Returning Guarded:false rather than "absent" matters — an
		// O_EXCL create here would refuse every legitimate overwrite in
		// a tool that never opted in.
		return Decision{Status: StatusOK}
	}
	fi, err := os.Stat(path)
	if err != nil {
		// Does not exist (or is unreadable) — there is nothing to
		// clobber, so creation is allowed, but exclusively: the file may
		// appear before we write.
		//
		// A dangling symlink also lands here (Stat follows links), and
		// O_CREAT|O_EXCL refuses to create through a symlink at all. The
		// caller reports that as "exists, unread", which is a slightly
		// off explanation for a rare case but never an unsafe one.
		return Decision{Status: StatusOK, Guarded: true}
	}
	if !fi.Mode().IsRegular() {
		// Directory, device, socket… Not our problem: the write's own
		// error names the actual issue better than a guard message
		// about reading the file first.
		return Decision{Status: StatusOK}
	}

	s.mu.Lock()
	prev, ok := s.seen[path]
	s.mu.Unlock()

	cur := tokenOf(fi)
	switch {
	case !ok:
		return Decision{Status: StatusUnseen, Guarded: true, Exists: true}
	case !prev.equal(cur):
		return Decision{Status: StatusStale, Guarded: true, Exists: true}
	default:
		return Decision{Status: StatusOK, Guarded: true, Exists: true, Token: prev}
	}
}

// Check is Plan reduced to its verdict, for callers that only need to
// know whether a write is permitted.
func (s *Store) Check(path string) Status { return s.Plan(path).Status }

// Explain renders the model-facing refusal for a non-OK status.
//
// The text names the exact recovery step. A refusal the model cannot act
// on is worse than no refusal: it burns a turn and then gets retried
// verbatim. Returns "" for StatusOK.
func Explain(st Status, path string) string {
	switch st {
	case StatusUnseen:
		return fmt.Sprintf("write refused: %s already exists and has not been read in this session. "+
			"`write` replaces the WHOLE file, so writing now would discard content you have never seen. "+
			"Call `read` on it first (then write, or use `edit` if you only need to change part of it).", path)
	case StatusStale:
		return fmt.Sprintf("write refused: %s changed on disk after you read it — another process, "+
			"a build step, or a git operation touched it. Overwriting now would silently discard "+
			"those changes. Call `read` on it again to see the current contents, then decide.", path)
	default:
		return ""
	}
}
