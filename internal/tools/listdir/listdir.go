// Package listdir implements the `list_dir` tool. It closes the gap
// where the LLM previously tried `read` on a directory (errors out) and
// then fell back to `bash ls` (gated by --yolo, refused). With list_dir
// the model has a first-class way to inspect directory contents
// without bash.
package listdir

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/tools"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":     {"type": "string", "description": "Directory path. Absolute or repo-relative."},
    "depth":    {"type": "integer", "description": "Recursion depth. 1 = just the directory. Default 1, max 3.", "minimum": 1, "maximum": 3},
    "show_hidden": {"type": "boolean", "description": "Include dot-prefixed entries. Default false."}
  },
  "required": ["path"],
  "additionalProperties": false
}`)

const description = "List the entries of a directory with type and size, recursively up to the requested depth. One call answers count- and size-questions about a tree (how many files, which is smallest/largest) — no `find`/`wc`/`stat` shell needed. Use this instead of `read` when the path is a directory, or instead of `bash ls` when you don't need to run a shell command."

type Args struct {
	Path       string `json:"path"`
	Depth      int    `json:"depth,omitempty"`
	ShowHidden bool   `json:"show_hidden,omitempty"`
}

type Tool struct{}

// New returns a list_dir tool.
func New() Tool { return Tool{} }

func (Tool) Name() string            { return "list_dir" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }
func (Tool) ReadOnly() bool          { return true }

const (
	defaultDepth = 1
	maxDepth     = 3
	maxEntries   = 500 // cap output to avoid blowing the next prompt
)

func (Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("list_dir", raw, &a, "path", "depth", "show_hidden"); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", tools.MissingField("list_dir", "path", raw, "path", "depth", "show_hidden")
	}
	if a.Depth <= 0 {
		a.Depth = defaultDepth
	}
	if a.Depth > maxDepth {
		a.Depth = maxDepth
	}

	root := filepath.Clean(a.Path)
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("list_dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("list_dir: %s is not a directory (use `read` for files)", root)
	}

	var (
		out       strings.Builder
		truncated bool
		count     int
	)
	header := fmt.Sprintf("%s (depth=%d, hidden=%v)\n", root, a.Depth, a.ShowHidden)
	out.WriteString(header)

	rootDirs, err := walk(root, "", a.Depth, a.ShowHidden, &out, &count, &truncated)
	if err != nil {
		return "", fmt.Errorf("list_dir: %w", err)
	}
	if truncated {
		out.WriteString(fmt.Sprintf("\n… truncated after %d entries — re-call with a narrower path\n", maxEntries))
	}
	// A default-depth listing stops at the top level, so a recursive
	// question ("how many .go files under X", "which is smallest") is
	// NOT answered — and nothing in the output says the rest of the
	// tree is one cheap re-call away, so the model's visible recovery
	// path is `bash find | wc`. Name the dedicated recovery in-band,
	// same idiom as the truncation notice above (eval bash-overreach
	// red-green alternation, 2026-11; description already promises
	// "one call answers count-questions about a tree" — only true when
	// depth covers the tree, so the default-depth output owes the model
	// that pointer).
	if a.Depth == 1 && rootDirs > 0 {
		out.WriteString(fmt.Sprintf("\n… %d subdirectories not expanded at depth=1 — re-call with depth=2 (max 3) to list their entries and sizes\n", rootDirs))
	}
	return out.String(), nil
}

// walk recursively lists entries up to the requested depth. It writes
// indent-prefixed lines so the model can read the structure at a
// glance. We stop early once the global entry cap is hit. Returns the
// number of DIRECTORY entries it listed at this level (feeds the
// default-depth expansion hint in Execute).
func walk(dir, indent string, depthLeft int, showHidden bool, out *strings.Builder, count *int, truncated *bool) (int, error) {
	if depthLeft <= 0 || *truncated {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	// Stable order: dirs first, then files, both alphabetical. Matches
	// what users expect from `ls -la --group-directories-first`.
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di // true (dir) before false (file)
		}
		return entries[i].Name() < entries[j].Name()
	})

	dirs := 0
	for _, e := range entries {
		if !showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if *count >= maxEntries {
			*truncated = true
			return dirs, nil
		}
		*count++

		name := e.Name()
		if e.IsDir() {
			dirs++
			fmt.Fprintf(out, "%s%s/\n", indent, name)
			if _, err := walk(filepath.Join(dir, name), indent+"  ", depthLeft-1, showHidden, out, count, truncated); err != nil {
				// Skip subdirs we can't read (permission errors) — keep
				// listing the rest.
				fmt.Fprintf(out, "%s  (unreadable: %v)\n", indent, err)
				continue
			}
		} else {
			info, err := e.Info()
			if err != nil {
				fmt.Fprintf(out, "%s%s\n", indent, name)
				continue
			}
			fmt.Fprintf(out, "%s%s  %d B\n", indent, name, info.Size())
		}
	}
	return dirs, nil
}
