package autopilot

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools/bash"
)

// The lane composition cmd/seek mounts on every unattended lane (goal /
// autopilot / SEEK_ACP_YOLO ACP, docs/prd/feature-paseo-provider.md
// §3.3): a YOLO policy (no human to ask) + the no-remote guard. Yolo
// opens LOCAL edit/shell only — push/PR-class remote mutations stay
// blocked, double-insuring the drive cards' own "never push" rule.
func TestGuardedYoloBash_BlocksRemoteMutating(t *testing.T) {
	p, err := permission.New(t.TempDir(), permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	tool := bash.New(p).WithDeny(IsRemoteMutating)

	for _, c := range []string{
		"git push",
		"git push origin main",
		"cd sub && git push --force",
		"gh pr create --fill",
	} {
		raw, _ := json.Marshal(bash.Args{Command: c})
		out, err := tool.Execute(context.Background(), raw)
		if err != nil {
			t.Fatalf("%q: deny must be a tool result, not a fatal error: %v", c, err)
		}
		// Guard fires before exec — the refusal must reach the model
		// as a blocked result carrying the reason (nothing runs).
		if !strings.Contains(out, "blocked") {
			t.Errorf("%q should be blocked by the guard, got: %q", c, out)
		}
	}
}

func TestGuardedYoloBash_AllowsLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX exec smoke; pattern-level allows (go test / git commit / ls) run on all platforms in guard_test.go")
	}
	p, err := permission.New(t.TempDir(), permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	tool := bash.New(p).WithDeny(IsRemoteMutating)

	// One representative local command executes end-to-end (proves
	// non-matching commands pass through the guard to exec). The full
	// allow list is asserted at pattern level in guard_test.go —
	// actually running `go test` here would recurse, `git commit`
	// would need repo scaffolding for no extra signal.
	raw, _ := json.Marshal(bash.Args{Command: "ls"})
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "blocked") || !strings.Contains(out, "exit=0") {
		t.Errorf("`ls` should run normally under the guard, got: %q", out)
	}
}
