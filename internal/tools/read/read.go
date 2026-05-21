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
	"strings"
)

// schemaBytes is the JSON Schema for the `read` tool's arguments. Frozen
// as a package-level []byte so the wire bytes are byte-identical every
// turn (PRD §4.8.1 — any mutation kills DeepSeek's prefix cache).
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "Absolute or repo-relative path to the file."},
    "offset": {"type": "integer", "description": "1-based line number to start from. Defaults to 1.", "minimum": 1},
    "limit":  {"type": "integer", "description": "Max number of lines to return. Defaults to 2000.", "minimum": 1, "maximum": 5000}
  },
  "required": ["path"],
  "additionalProperties": false
}`)

const description = "Read a file from the local filesystem. Returns the contents with 1-based line numbers prefixed to each line. Use offset/limit for large files; if the file exceeds the limit, the response notes truncation."

// Args is the decoded argument struct for `read`.
type Args struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Tool is the read tool implementation. Construct via New.
type Tool struct{}

func New() Tool { return Tool{} }

func (Tool) Name() string                 { return "read" }
func (Tool) Description() string          { return description }
func (Tool) Schema() json.RawMessage      { return schemaBytes }

const (
	defaultLimit = 2000
	maxLimit     = 5000
)

func (Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("read: bad arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("read: path is required")
	}
	if a.Offset < 0 {
		return "", fmt.Errorf("read: offset must be >= 0")
	}
	if a.Offset == 0 {
		a.Offset = 1
	}
	if a.Limit <= 0 {
		a.Limit = defaultLimit
	}
	if a.Limit > maxLimit {
		a.Limit = maxLimit
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
		return "", fmt.Errorf("read: %s is a directory", clean)
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
		if emitted >= a.Limit {
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
