// Package references implements the `references` tool: semantic
// find-references via a language server (v6 柱 L 瘦身版). It is the one
// thing grep can't do — resolve the actual symbol, follow aliased
// imports, ignore comments/strings — so it's the only LSP operation seek
// exposes (see docs/prd/feature-lsp.md for the ROI rationale).
//
// The tool owns presentation: 1-based→0-based position conversion (LSP's
// headline footgun), locating the symbol's column on the given line,
// output capping, and turning lspclient errors into model-actionable
// "fall back to grep" hints. The lifecycle (lazy-start, session-scoped
// servers) lives in internal/lspclient.
package references

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/lspclient"
	"github.com/whyiyhw/seek/internal/tools"
)

const maxRefs = 50

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "file":      {"type": "string", "description": "Path (relative to the working directory) of a file where the symbol appears."},
    "line":      {"type": "integer", "minimum": 1, "description": "1-based line where the symbol appears (from a prior grep/read)."},
    "symbol":    {"type": "string", "description": "The symbol name on that line; used to find its exact column. Required unless character is given."},
    "character": {"type": "integer", "minimum": 1, "description": "Optional 1-based column of the symbol; used directly instead of locating symbol."}
  },
  "required": ["file", "line"],
  "additionalProperties": false
}`)

const description = "Find every semantic reference to a symbol — who calls or uses it — via the project's language server (gopls / pyright / typescript-language-server). Far more precise than grepping a name: it resolves the actual symbol, follows aliased imports, and ignores comments and strings. Give the file and 1-based line where the symbol appears (from a prior grep/read) plus the symbol name. Needs the language server on PATH; if it's missing, or the project doesn't type-check, fall back to grep."

// Resolver is the slice of lspclient.Manager this tool needs. Narrowing
// it to an interface keeps the tool testable with a fake (no real gopls).
type Resolver interface {
	References(ctx context.Context, absFile, content string, pos lspclient.Position) ([]lspclient.Location, error)
}

type Args struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Symbol    string `json:"symbol,omitempty"`
	Character int    `json:"character,omitempty"`
}

type Tool struct {
	resolver Resolver
}

// New wires the language-server resolver (the session's lspclient.Manager).
// Panics on nil — a registered tool with no resolver is a wiring bug.
func New(r Resolver) Tool {
	if r == nil {
		panic("references: New called with nil Resolver — host did not wire internal/lspclient")
	}
	return Tool{resolver: r}
}

func (Tool) Name() string            { return "references" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

// ReadOnly: references only queries; it never mutates files or shell. It
// can run in plan-analyze and be batched concurrently.
func (Tool) ReadOnly() bool { return true }

var validFields = []string{"file", "line", "symbol", "character"}

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("references", raw, &a, validFields...); err != nil {
		return "", err
	}
	if a.File == "" {
		return "", tools.MissingField("references", "file", raw, validFields...)
	}
	if a.Line < 1 {
		return "", fmt.Errorf("references: line must be >= 1 (1-based)")
	}
	if a.Symbol == "" && a.Character == 0 {
		return "", fmt.Errorf("references: provide symbol (to locate the column) or character")
	}

	clean := filepath.Clean(a.File)
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("references: %w", err)
	}
	content := string(data)

	pos, err := resolvePosition(content, a)
	if err != nil {
		return "", err
	}

	absFile, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("references: %w", err)
	}

	locs, err := t.resolver.References(ctx, absFile, content, pos)
	if err != nil {
		return degradeMessage(err)
	}

	target := a.Symbol
	if target == "" {
		target = fmt.Sprintf("%s:%d:%d", a.File, a.Line, a.Character)
	}
	if len(locs) == 0 {
		return fmt.Sprintf("[references: no references to %s at %s:%d]", target, a.File, a.Line), nil
	}
	return format(target, locs), nil
}

// resolvePosition turns the tool's 1-based input into an LSP 0-based
// position. This is the headline LSP footgun (feature-lsp.md §8): LSP
// counts BOTH line and character from 0.
func resolvePosition(content string, a Args) (lspclient.Position, error) {
	lines := strings.Split(content, "\n")
	if a.Line > len(lines) {
		return lspclient.Position{}, fmt.Errorf("references: line %d is beyond end of file (%d lines)", a.Line, len(lines))
	}
	lineText := lines[a.Line-1]

	if a.Character > 0 {
		return lspclient.Position{Line: a.Line - 1, Character: a.Character - 1}, nil
	}
	// Locate the symbol on the line. Byte index == UTF-16 unit for ASCII
	// identifiers (the common case); non-ASCII before the symbol on the
	// same line could skew it — acceptable for MVP.
	col := strings.Index(lineText, a.Symbol)
	if col < 0 {
		return lspclient.Position{}, fmt.Errorf("references: symbol %q not found on %s:%d", a.Symbol, a.File, a.Line)
	}
	return lspclient.Position{Line: a.Line - 1, Character: col}, nil
}

// degradeMessage turns an lspclient error into a model-actionable result
// (NOT a fatal error) — except a real interrupt, which is propagated.
func degradeMessage(err error) (string, error) {
	var mbe *lspclient.MissingBinaryError
	switch {
	case errors.As(err, &mbe):
		return fmt.Sprintf("[references: %s not found in PATH; install: %s. Fall back to grep for now.]", mbe.Command, mbe.Install), nil
	case errors.Is(err, context.Canceled):
		return "", err // Esc / turn cancelled — propagate the interrupt
	case errors.Is(err, context.DeadlineExceeded):
		return "[references: the language server timed out (it may still be indexing). Try again shortly, or fall back to grep.]", nil
	default:
		// e.g. "no LSP server configured for .rs files", init failures
		return fmt.Sprintf("[references: %v. Fall back to grep.]", err), nil
	}
}

func format(target string, locs []lspclient.Location) string {
	cwd, _ := os.Getwd()
	snip := newSnippetReader()

	total := len(locs)
	shown := locs
	if total > maxRefs {
		shown = locs[:maxRefs]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d reference(s) to %s:\n", total, target)
	for _, loc := range shown {
		path := uriToDisplay(loc.URI, cwd)
		line := loc.Range.Start.Line + 1 // 0-based → 1-based for display
		col := loc.Range.Start.Character + 1
		if s := snip(loc.URI, loc.Range.Start.Line); s != "" {
			fmt.Fprintf(&b, "%s:%d:%d  | %s\n", path, line, col, s)
		} else {
			fmt.Fprintf(&b, "%s:%d:%d\n", path, line, col)
		}
	}
	if total > maxRefs {
		fmt.Fprintf(&b, "… %d more (query a more specific symbol/location to narrow) …\n", total-maxRefs)
	}
	return b.String()
}

func uriToDisplay(uri, cwd string) string {
	abs := strings.TrimPrefix(uri, "file://")
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return abs
}

// newSnippetReader returns a function that yields the trimmed source line
// at a 0-based line number, caching file contents so a hot file isn't
// re-read for each of its references. Tolerant: returns "" on any failure.
func newSnippetReader() func(uri string, line0 int) string {
	cache := map[string][]string{}
	return func(uri string, line0 int) string {
		path := strings.TrimPrefix(uri, "file://")
		lines, ok := cache[path]
		if !ok {
			data, err := os.ReadFile(path)
			if err != nil {
				cache[path] = nil
				return ""
			}
			lines = strings.Split(string(data), "\n")
			cache[path] = lines
		}
		if line0 < 0 || line0 >= len(lines) {
			return ""
		}
		return strings.TrimSpace(lines[line0])
	}
}
