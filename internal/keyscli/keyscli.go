// Package keyscli implements the `seek keys` family of subcommands —
// list / check / actions. Dispatched ahead of flag.Parse in
// cmd/seek/main.go so flags like --json don't collide with seek's
// top-level binary flags. PRD docs/prd/feature-tui-ergonomics.md §5.1.
package keyscli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/whyiyhw/seek/internal/keymap"
	"github.com/whyiyhw/seek/internal/paths"
)

const helpText = `seek keys — inspect and validate the user-customisable TUI keymap

Usage:
  seek keys <command> [flags]

Commands:
  list                List the active (action → key) keymap including
                      any ~/.seek/keybindings.toml overrides.
  check [<path>]      Validate a keybindings.toml without launching the
                      TUI. Empty path → ~/.seek/keybindings.toml.
                      Exit code 0 on success, 2 on errors.
  actions             List the closed set of rebindable actions and
                      their default keys. Use as the source of truth
                      when authoring keybindings.toml.

Flags (where applicable):
  --json              Emit JSON on stdout instead of human-readable text.

See PRD docs/prd/feature-tui-ergonomics.md for the full spec.
`

// ErrUsage is returned by Run when the user invokes the command in a
// way that the dispatcher should treat as a USAGE error (exit code 2
// per POSIX convention + PRD §5.1) rather than a generic error
// (exit 1). cmd/seek/main.go inspects the returned error to map back
// to the right exit code; everything else is a plain Errorf.
var ErrUsage = errors.New("usage")

// Run dispatches `seek keys <sub> ...`. args is os.Args[2:] (the slice
// AFTER the binary name and "keys"). Returns nil on success, ErrUsage
// (or a wrapping error) for usage/validation issues, and a generic
// error for system failures.
func Run(args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(errOut, helpText)
		return ErrUsage
	}
	switch args[0] {
	case "list":
		return runList(args[1:], out, errOut)
	case "check":
		return runCheck(args[1:], out, errOut)
	case "actions":
		return runActions(args[1:], out, errOut)
	case "help", "-h", "--help":
		fmt.Fprint(out, helpText)
		return nil
	default:
		fmt.Fprintf(errOut, "seek keys: unknown subcommand %q\n\n", args[0])
		fmt.Fprint(errOut, helpText)
		return ErrUsage
	}
}

func runList(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("keys list", flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "emit JSONL on stdout")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	path, err := paths.UserKeybindings()
	if err != nil {
		return fmt.Errorf("keys list: %w", err)
	}
	// Discard warnings here — `seek keys check` is the validation entry
	// point. `list` just shows whatever's currently active, so a slightly
	// broken toml still produces sensible output (defaults).
	km, _ := keymap.Load(path, io.Discard)

	if *asJSON {
		enc := json.NewEncoder(out)
		for _, b := range km.Snapshot() {
			if err := enc.Encode(b); err != nil {
				return fmt.Errorf("keys list: encode: %w", err)
			}
		}
		return nil
	}

	// Text output. Padded for the keymap-list aesthetic of /help.
	fmt.Fprintln(out, "Action                Key             Source")
	fmt.Fprintln(out, "------                ---             ------")
	for _, b := range km.Snapshot() {
		fmt.Fprintf(out, "%-22s%-16s%s\n", b.Action, b.Key, b.Source)
	}
	return nil
}

func runCheck(args []string, out, errOut io.Writer) error {
	var path string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		path = args[0]
	}
	if path == "" {
		p, err := paths.UserKeybindings()
		if err != nil {
			return fmt.Errorf("keys check: %w", err)
		}
		path = p
	}
	errs := keymap.Check(path)
	if len(errs) == 0 {
		fmt.Fprintf(out, "ok: %s\n", path)
		return nil
	}
	for _, e := range errs {
		fmt.Fprintln(errOut, e)
	}
	return ErrUsage
}

func runActions(args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("keys actions", flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "emit JSON array on stdout")
	if err := fs.Parse(args); err != nil {
		return ErrUsage
	}

	infos := keymap.AllActions()
	if *asJSON {
		enc := json.NewEncoder(out)
		if err := enc.Encode(infos); err != nil {
			return fmt.Errorf("keys actions: encode: %w", err)
		}
		return nil
	}

	fmt.Fprintln(out, "Action                Default         Description")
	fmt.Fprintln(out, "------                -------         -----------")
	for _, info := range infos {
		fmt.Fprintf(out, "%-22s%-16s%s\n", info.Action, info.Default, info.Description)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Edit ~/.seek/keybindings.toml with [bindings] section to override.")
	fmt.Fprintln(out, "Example:")
	fmt.Fprintln(out, "  [bindings]")
	fmt.Fprintln(out, `  clear-screen = "ctrl+x"`)
	return nil
}
