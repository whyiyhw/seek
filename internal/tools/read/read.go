// Package read implements the `read` tool: load a file from disk and
// return its contents with line numbers. First and simplest of the four
// core tools (read / write / edit / bash).
package read

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/fsobserve"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools"
)

// schemaBytes is the JSON Schema for the `read` tool's arguments. Frozen
// as a package-level []byte so the wire bytes are byte-identical every
// turn (PRD §4.8.1 — any mutation kills DeepSeek's prefix cache). The
// limit maximum mirrors defaultMaxLimit; a configured limit lower than
// the schema maximum is enforced at runtime with a clear error.
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "Absolute or repo-relative path to the file."},
    "offset": {"type": "integer", "description": "1-based line number to start from. Defaults to 1. Use with successive calls to page through a file.", "minimum": 1},
    "limit":  {"type": "integer", "minimum": 1, "maximum": 200, "default": 200, "description": "Maximum lines to return (capped at 200). Defaults to 200. Small files (<= 32 KiB) are always returned whole regardless of limit."}
  },
  "required": ["path"],
  "additionalProperties": false
}`)

const description = "Read lines from a file (with 1-based line numbers). Small files (<= 32 KiB) are returned WHOLE in one call. Larger files: accepts optional limit (default 200, max 200) and offset (default 1); the header reports the total ('EOF at line N') when the read reached the end, so you never need a probing read. Over-long single lines are elided in-band. Use grep to locate the exact range first, then read(offset=N, limit=N) to retrieve it. OR list a directory's immediate entries when the path is a directory. For deeper recursion or to show hidden entries, use list_dir explicitly."

// Args is the decoded argument struct for `read`.
type Args struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Defaults (configurable per-tool via WithLimits).
const (
	// defaultMaxLimit caps one read call's emitted lines.
	defaultMaxLimit = 200
	// defaultWholeReadBytes: regular files at or below this size are
	// emitted whole in one call regardless of limit/offset semantics
	// (offset still applies). 32 KiB is the measured sweet spot from the
	// read-tool A/B eval (docs/test-plan-read-tool.md §7.0/§7.2 — 16 KiB
	// left case C cost 0.3% short of the H1 gate; 64 KiB cost more than
	// 32 KiB because the model wrote longer answers).
	defaultWholeReadBytes = 32 * 1024
	// maxLineBytes caps an individual emitted line; longer lines are
	// elided in-band with a marker instead of failing the read.
	maxLineBytes = 1024
	// maxResultBytes caps the whole result; the middle is elided.
	maxResultBytes = 64 * 1024
	// headKeep/tailKeep bound the elided middle (clampOutput pattern).
	headKeep = 8 * 1024
	tailKeep = 24 * 1024
)

// Tool is the read tool implementation. Construct via New.
type Tool struct {
	policy         *permission.Policy
	observer       *fsobserve.Store
	maxLimit       int
	wholeReadBytes int
}

// New returns a read tool gated by the given permission policy.
func New(p *permission.Policy) Tool {
	return Tool{policy: p, maxLimit: defaultMaxLimit, wholeReadBytes: defaultWholeReadBytes}
}

// WithLimits overrides the per-call line cap and the whole-read size
// threshold (config.Read.max_limit / whole_read_bytes). Zero values
// keep the defaults.
func (t Tool) WithLimits(maxLimit, wholeReadBytes int) Tool {
	if maxLimit > 0 {
		t.maxLimit = maxLimit
	}
	if wholeReadBytes > 0 {
		t.wholeReadBytes = wholeReadBytes
	}
	return t
}

// WithObserver records a successful FULL, UN-ELIDED read in s, which is
// what lets the `write` tool refuse a blind whole-file overwrite later.
// A partial read (offset > 1, or a limit that truncated the file) does
// NOT vouch for the whole file, and neither does a whole-file read whose
// output was elided (an over-long line, or the result-level middle cut)
// — that records an elided note so the refusal names the real recovery.
// Optional: a nil observer leaves read's behaviour unchanged.
func (t Tool) WithObserver(s *fsobserve.Store) Tool {
	t.observer = s
	return t
}

func (Tool) Name() string            { return "read" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }
func (Tool) ReadOnly() bool          { return true }

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("read", raw, &a, "path", "offset", "limit"); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", tools.MissingField("read", "path", raw, "path", "offset")
	}
	if a.Offset < 0 {
		return "", fmt.Errorf("read: offset must be >= 0")
	}
	if a.Offset == 0 {
		a.Offset = 1
	}
	// Default limit to maxLimit if omitted (omitempty drops 0 from JSON,
	// so Go zero-value is 0). Reject values above the configured maximum
	// — the schema's maximum should catch this, but if the model ignores
	// it, error clearly.
	if a.Limit == 0 {
		a.Limit = t.maxLimit
	} else if a.Limit > t.maxLimit {
		return "", fmt.Errorf("read: limit=%d exceeds maximum (%d). Valid: 1-%d, or omit the parameter for the default.", a.Limit, t.maxLimit, t.maxLimit)
	}

	clean := t.policy.Resolve(a.Path)

	if err := t.policy.Check(permission.Action{
		Kind: permission.KindRead,
		Path: a.Path,
	}); err != nil {
		return "", err
	}

	f, err := os.Open(clean)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read: stat: %w", err)
	}
	if info.IsDir() {
		// Match Claude Code's "Read does what I mean": rather than
		// erroring and forcing the model to retry with list_dir, do
		// the obvious thing and return a shallow listing. list_dir is
		// still the right answer when the caller needs recursion or
		// hidden files, but the default behaviour is the one that
		// avoids an extra LLM round-trip.
		f.Close()
		return listDirShallow(clean)
	}

	// I2: small regular files are emitted whole in one call — the model
	// should never need to page a file that fits in a single response,
	// and a whole read is what qualifies as "seen" for the write guard.
	whole := info.Mode().IsRegular() && info.Size() <= int64(t.wholeReadBytes)
	limit := a.Limit
	if whole {
		limit = 1 << 30
	}

	var (
		out           strings.Builder
		lineNo        = 0
		emitted       = 0
		truncated     bool
		reachedEOF    bool
		anyLineElided bool
	)
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, elided, lerr := readLine(r, maxLineBytes)
		if lerr == io.EOF {
			reachedEOF = true
			break
		}
		if lerr != nil {
			return "", fmt.Errorf("read: %w", lerr)
		}
		lineNo++
		if lineNo < a.Offset {
			continue
		}
		if emitted >= limit {
			truncated = true
			break
		}
		emitted++
		fmt.Fprintf(&out, "%6d\t%s", lineNo, line)
		if elided > 0 {
			anyLineElided = true
			fmt.Fprintf(&out, " … [%d bytes of this line elided]", elided)
		}
		out.WriteByte('\n')
	}

	header := fmt.Sprintf("%s (%d bytes", clean, info.Size())
	if a.Offset > 1 {
		header += fmt.Sprintf(", from line %d", a.Offset)
	}
	header += fmt.Sprintf(", %d lines emitted", emitted)
	// I1: the model must be able to tell "file ends here" from "more
	// pages exist" without a probing read.
	if reachedEOF {
		header += fmt.Sprintf(", EOF at line %d", lineNo)
	}
	if truncated {
		header += fmt.Sprintf(", TRUNCATED — continue with offset=%d", lineNo)
	}
	header += ")\n"

	outStr := header + out.String()
	// I4: bound the whole result (middle elision, tail intact).
	resultElided := len(outStr) > maxResultBytes
	if resultElided {
		outStr = outStr[:headKeep] +
			fmt.Sprintf("\n… [read output elided: %d bytes removed — the tail below is intact]\n", len(outStr)-headKeep-tailKeep) +
			outStr[len(outStr)-tailKeep:]
	}

	// I3: only a read that showed the model every byte vouches for the
	// file. A windowed or truncated read shows a fragment — and so does
	// one whose output was elided, in-band (an over-long line) or at the
	// result level (the 64 KiB middle cut): reachedEOF says the scan
	// covered the file, elision says the model did not see it. Such a
	// read records an elided note instead, so the write guard refuses
	// with recovery advice that names the real way out rather than the
	// "read it again" step that can never succeed for this file shape.
	if reachedEOF && a.Offset <= 1 && !truncated {
		if anyLineElided || resultElided {
			t.observer.NoteElided(clean)
		} else {
			t.observer.Observe(clean)
		}
	}
	return outStr, nil
}

// readLine reads the next line from r, newline stripped. Returns the
// kept bytes, the number of bytes elided (0 when the line fits), and
// io.EOF when no line remains. Over-long lines are truncated in-band
// (head kept, remainder drained and counted) instead of failing the
// whole read — bufio.Scanner's token-too-long would otherwise turn a
// minified bundle into a hard error.
func readLine(r *bufio.Reader, maxKeep int) (line []byte, elided int, err error) {
	kept := make([]byte, 0, 256)
	consumed := 0
	for {
		frag, e := r.ReadSlice('\n')
		consumed += len(frag)
		if len(kept)+len(frag) > maxKeep+1 { // +1 lets the \n fit exactly
			// Line exceeds the keep cap: keep what fits, drain the rest
			// so the next readLine starts at the next line.
			if take := maxKeep - len(kept); take > 0 {
				kept = append(kept, frag[:take]...)
			}
			for e == bufio.ErrBufferFull {
				frag, e = r.ReadSlice('\n')
				consumed += len(frag)
			}
			if e == io.EOF && len(kept) == 0 {
				return nil, 0, io.EOF
			}
			return trimNL(kept), consumed - len(kept), normEOF(e)
		}
		kept = append(kept, frag...)
		if e != bufio.ErrBufferFull {
			if e == io.EOF && len(kept) == 0 {
				return nil, 0, io.EOF
			}
			return trimNL(kept), 0, normEOF(e)
		}
	}
}

// trimNL strips one trailing newline plus the CR that preceded it
// (Scanner.Text semantics — bufio's ScanLines drops the \r of a CRLF
// pair). Without the CR half, every line of a CRLF working tree would
// carry an invisible \r into the model's context — tokeniser noise the
// model cannot see to reproduce, and a behaviour change from the
// Scanner era this file used to keep.
func trimNL(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

// normEOF turns a clean end-of-file into nil so callers only see io.EOF
// for "no line at all".
func normEOF(err error) error {
	if err == io.EOF {
		return nil
	}
	return err
}

// listDirShallow is the directory fallback for Read. Same shape as
// list_dir at depth=1: skips dotfiles (use list_dir with show_hidden
// if you want them), dirs-before-files alphabetical order, file sizes
// in bytes. Output ends with a one-line nudge so the model knows that
// list_dir is the right tool for deeper exploration.
func listDirShallow(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di // directories first
		}
		return entries[i].Name() < entries[j].Name()
	})

	var (
		sb      strings.Builder
		visible int
	)
	fmt.Fprintf(&sb, "%s (directory)\n", dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		visible++
		if e.IsDir() {
			fmt.Fprintf(&sb, "%s/\n", e.Name())
		} else {
			size := int64(0)
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
			fmt.Fprintf(&sb, "%s  %d B\n", e.Name(), size)
		}
	}
	fmt.Fprintf(&sb, "\n%d entries shown (hidden files excluded; call list_dir with show_hidden=true or depth>1 for more)\n", visible)
	return sb.String(), nil
}
