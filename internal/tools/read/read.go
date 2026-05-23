// Package read implements the `read` tool: load a file from disk and
// return its contents with line numbers. First and simplest of the four
// core tools (read / write / edit / bash).
package read

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/tools"
)

// schemaBytes is the JSON Schema for the `read` tool's arguments. Frozen
// as a package-level []byte so the wire bytes are byte-identical every
// turn (PRD §4.8.1 — any mutation kills DeepSeek's prefix cache).
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "Absolute or repo-relative path to the file."},
    "offset": {"type": "integer", "description": "1-based line number to start from. Defaults to 1. Use with successive calls to page through a file.", "minimum": 1}
  },
  "required": ["path"],
  "additionalProperties": false
}`)

const description = "Read up to 50 lines from a file (with 1-based line numbers). There is no limit parameter — every read returns at most 50 lines. Use grep to locate the exact range first, then read(offset=N) to retrieve it. OR list a directory's immediate entries when the path is a directory. For deeper recursion or to show hidden entries, use list_dir explicitly."

// Args is the decoded argument struct for `read`.
type Args struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
}

// Tool is the read tool implementation. Construct via New.
type Tool struct{}

func New() Tool { return Tool{} }

func (Tool) Name() string                 { return "read" }
func (Tool) Description() string          { return description }
func (Tool) Schema() json.RawMessage      { return schemaBytes }
func (Tool) ReadOnly() bool               { return true }

const defaultLimit = 50

func (Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("read", raw, &a, "path", "offset"); err != nil {
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

	clean := filepath.Clean(a.Path)
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

	var (
		out       strings.Builder
		lineNo    = 0
		emitted   = 0
		truncated bool
	)

	sc := bufio.NewScanner(f)
	// Allow individual lines up to 1 MiB.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		lineNo++
		if lineNo < a.Offset {
			continue
		}
		if emitted >= defaultLimit {
			truncated = true
			break
		}
		fmt.Fprintf(&out, "%6d\t%s\n", lineNo, sc.Text())
		emitted++
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read: scan: %w", err)
	}

	header := fmt.Sprintf("%s (%d bytes", clean, info.Size())
	if a.Offset > 1 {
		header += fmt.Sprintf(", from line %d", a.Offset)
	}
	header += fmt.Sprintf(", %d lines emitted", emitted)
	if truncated {
		header += fmt.Sprintf(", TRUNCATED — continue with offset=%d", lineNo)
	}
	header += ")\n"
	return header + out.String(), nil
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
