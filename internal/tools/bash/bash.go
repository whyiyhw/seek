// Package bash implements the `bash` tool: run a shell command via
// /bin/sh -c (or %COMSPEC on Windows) with a timeout. Gated by the
// permission Policy — without --yolo every bash call is refused with
// instructions for the model to surface the request to the user.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/whyiyhw/seek/internal/permission"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "command":    {"type": "string", "description": "Shell command. Runs under /bin/sh -c (POSIX) or cmd.exe /C (Windows)."},
    "timeout_ms": {"type": "integer", "description": "Timeout in milliseconds. Default 120000 (2m), max 600000 (10m).", "minimum": 100, "maximum": 600000}
  },
  "required": ["command"],
  "additionalProperties": false
}`)

const description = "Execute a shell command. By default seek refuses bash; the user must opt in by re-running with --yolo. When allowed, combined stdout/stderr is returned (truncated past 32 KiB). Use timeout_ms to bound long-running commands."

const (
	defaultTimeoutMS = 120_000
	maxTimeoutMS     = 600_000
	maxOutputBytes   = 32 * 1024
)

type Args struct {
	Command   string `json:"command"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type Tool struct {
	policy *permission.Policy
}

func New(p *permission.Policy) Tool { return Tool{policy: p} }

func (Tool) Name() string            { return "bash" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bash: bad arguments: %w", err)
	}
	if a.Command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	if err := t.policy.Check(permission.Action{Kind: permission.KindBash, Command: a.Command}); err != nil {
		return "", err
	}

	timeout := a.TimeoutMS
	if timeout <= 0 {
		timeout = defaultTimeoutMS
	}
	if timeout > maxTimeoutMS {
		timeout = maxTimeoutMS
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(cctx, "cmd.exe", "/C", a.Command)
	} else {
		cmd = exec.CommandContext(cctx, "/bin/sh", "-c", a.Command)
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	output := buf.Bytes()
	truncated := false
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes]
		truncated = true
	}

	exitCode := 0
	timedOut := false
	if err != nil {
		// context.DeadlineExceeded surfaces as ctx.Err() rather than the
		// cmd.Run() error on some platforms; check both.
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			timedOut = true
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else if !timedOut {
			// Non-exit error (e.g. shell not found). Surface verbatim so
			// the model can adjust.
			return "", fmt.Errorf("bash: %w (output: %s)", err, string(output))
		}
	}

	header := fmt.Sprintf("$ %s\n(exit=%d, elapsed=%s", a.Command, exitCode, dur.Round(time.Millisecond))
	if timedOut {
		header += ", TIMED OUT"
	}
	if truncated {
		header += fmt.Sprintf(", output truncated to %d bytes", maxOutputBytes)
	}
	header += ")\n"
	return header + string(output), nil
}
