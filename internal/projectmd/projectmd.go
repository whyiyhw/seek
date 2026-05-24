// Package projectmd discovers and loads the per-project instructions
// file that seek auto-appends to the system prompt at startup.
//
// Convention: `AGENTS.md` — the neutral, tool-agnostic filename that
// Cursor, Aider, and others have converged on. We deliberately don't
// also load CLAUDE.md or .cursorrules — picking one canonical source
// avoids drift questions ("which one wins when they disagree?") and
// keeps the startup story easy to explain. Users who want their
// existing CLAUDE.md to be picked up can rename or symlink it.
//
// Discovery: walk up from cwd, first hit wins, bounded to maxAscend
// levels so we don't scan to filesystem root on a deeply-nested dir.
package projectmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// Filename is the conventional file we look for. Exported so tests
// and the /skills-equivalent introspection commands can reference it
// without hardcoding the string.
const Filename = "AGENTS.md"

// maxAscend caps how far up the directory tree we walk. Five levels
// is enough to find a project root from any reasonable subdirectory
// (src/foo/bar/baz still reaches the root) without scanning all the
// way to "/" on machines where seek is launched from a deep path.
const maxAscend = 5

// maxBytes is the hard ceiling on how much we'll inject into the
// system prompt. A multi-MB file would obliterate the context window
// and probably indicates the user pointed us at the wrong file
// (e.g. a CHANGELOG). 64 KB is well above the realistic ceiling for
// human-written project instructions and well below what a 128K-1M
// model can stomach.
const maxBytes = 64 * 1024

// Result is what Load returns. Path is empty when no file was found
// (not an error — most projects won't have one).
type Result struct {
	Content  string
	Path     string // absolute path of the file we read; empty if none
	Bytes    int    // raw byte count, before any truncation marker
	Truncate bool   // true if we cut Content off at maxBytes
}

// Load walks up from startDir looking for AGENTS.md. It returns a
// Result and any read error that wasn't "not found". A missing file
// is the common case — Result{Path: ""} with nil error.
func Load(startDir string) (Result, error) {
	if startDir == "" {
		// Fall back to cwd if the caller didn't pass one. Useful for
		// tests; production always passes the resolved working dir.
		var err error
		startDir, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return Result{}, err
	}

	for i := 0; i < maxAscend; i++ {
		candidate := filepath.Join(dir, Filename)
		data, err := os.ReadFile(candidate)
		if err == nil {
			return shape(candidate, data), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			// Real read error (permission, IO) — surface it rather
			// than silently skipping. A misconfigured AGENTS.md that
			// can't be read is probably a thing the user wants to
			// know about.
			return Result{}, fmt.Errorf("projectmd: %s: %w", candidate, err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Hit filesystem root before we found anything.
			break
		}
		dir = parent
	}
	return Result{}, nil
}

func shape(path string, data []byte) Result {
	r := Result{
		Path:  path,
		Bytes: len(data),
	}
	if len(data) > maxBytes {
		b := data[:maxBytes]
		// Drop trailing bytes to avoid splitting a multi-byte UTF-8
		// character. The loop caps at 3 iterations (max continuation
		// bytes for a 4-byte rune) and bails if the input contains
		// invalid UTF-8 beyond a simple truncated rune.
		for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
			b = b[:len(b)-1]
		}
		r.Content = string(b) +
			fmt.Sprintf("\n\n…[truncated %d bytes; AGENTS.md is too large — keep it under %d KB]\n",
				len(data)-len(b), maxBytes/1024)
		r.Truncate = true
	} else {
		r.Content = string(data)
	}
	return r
}

// Section formats Content as a system-prompt-ready block, with the
// source path labelled so the model knows where the instructions
// came from. Returns "" when r.Path is empty so callers can simply
// concatenate without a nil check.
func (r Result) Section() string {
	if r.Path == "" {
		return ""
	}
	return fmt.Sprintf("# Project instructions (from %s)\n\n%s\n", r.Path, r.Content)
}
