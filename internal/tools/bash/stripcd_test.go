package bash

import (
	"strings"
	"testing"
)

// TestStripRedundantCD covers the shapes the stripper is allowed to
// touch and — more importantly — the ones it must not. Every "untouched"
// case is a real control-flow shape: rewriting any of them would change
// what the shell does.
func TestStripRedundantCD(t *testing.T) {
	t.Parallel()
	const cwd = "/repo/proj"

	cases := []struct {
		name      string
		command   string
		wantCmd   string
		wantStrip bool
	}{
		// --- stripped: the measured 322/5,015 shape -------------------
		{"exact cwd", "cd /repo/proj && go vet ./...", "go vet ./...", true},
		{"trailing slash on cd target", "cd /repo/proj/ && go vet ./...", "go vet ./...", true},
		{"dotdot inside cwd", "cd /repo/proj/sub/.. && go build ./...", "go build ./...", true},
		{"bare dot", "cd . && go vet ./...", "go vet ./...", true},
		{"quoted path", `cd "/repo/proj" && go vet ./...`, "go vet ./...", true},
		{"extra spaces around &&", "cd /repo/proj   &&   go vet ./...", "go vet ./...", true},
		{"pipeline after the cd", "cd /repo/proj && grep -rn x . | head -3", "grep -rn x . | head -3", true},

		// --- untouched: different directory ---------------------------
		{"different absolute dir", "cd /tmp && go vet ./...", "cd /tmp && go vet ./...", false},
		{"subdirectory", "cd /repo/proj/sub && go vet ./...", "cd /repo/proj/sub && go vet ./...", false},
		{"home-relative", "cd ~/elsewhere && go vet ./...", "cd ~/elsewhere && go vet ./...", false},
		{"sibling-prefix dir", "cd /repo/proj2 && go vet ./...", "cd /repo/proj2 && go vet ./...", false},

		// --- untouched: different control flow -----------------------
		{"semicolon not &&", "cd /repo/proj; go vet ./...", "cd /repo/proj; go vet ./...", false},
		{"or-fallback", "cd /repo/proj || exit 1", "cd /repo/proj || exit 1", false},
		{"nothing after cd", "cd /repo/proj", "cd /repo/proj", false},
		{"trailing && only", "cd /repo/proj &&", "cd /repo/proj &&", false},
		{"cd mid-chain", "go build ./... && cd /repo/proj && go vet ./...",
			"go build ./... && cd /repo/proj && go vet ./...", false},

		// --- untouched: not a cd prefix at all -----------------------
		{"no cd", "go vet ./...", "go vet ./...", false},
		{"cdn-like token", "cdrom /x && go vet ./...", "cdrom /x && go vet ./...", false},
		{"empty", "", "", false},
		{"whitespace", "   ", "   ", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, stripped := stripRedundantCD(c.command, cwd)
			if got != c.wantCmd {
				t.Errorf("command = %q, want %q", got, c.wantCmd)
			}
			if stripped != c.wantStrip {
				t.Errorf("stripped = %v, want %v", stripped, c.wantStrip)
			}
		})
	}
}

// TestStripRedundantCD_NoCwd pins the empty-cwd guard: without a target
// to compare against, every cd must be honoured. Two callers hit this —
// a policy built before the cwd was set, and every test that only
// exercises command parsing.
func TestStripRedundantCD_NoCwd(t *testing.T) {
	t.Parallel()
	const cmd = "cd /anywhere && go vet ./..."
	for _, cwd := range []string{"", " " /* not cleaned, still falsy-ish */} {
		got, stripped := stripRedundantCD(cmd, cwd)
		if stripped {
			t.Errorf("cwd=%q must not strip, got %q", cwd, got)
		}
	}
}

// TestStripRedundantCD_UnterminatedQuote: an unbalanced quote means the
// command is a shape we do not understand, so it runs verbatim rather
// than being guessed at.
func TestStripRedundantCD_UnterminatedQuote(t *testing.T) {
	t.Parallel()
	cmd := `cd "/repo/proj && go vet ./...`
	got, stripped := stripRedundantCD(cmd, "/repo/proj")
	if stripped || got != cmd {
		t.Errorf("unterminated quote must leave the command alone, got %q (stripped=%v)", got, stripped)
	}
	if !strings.Contains(got, "cd ") {
		t.Error("command was mutated")
	}
}
