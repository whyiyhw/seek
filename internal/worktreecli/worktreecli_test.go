package worktreecli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_NoArgsPrintsHelp(t *testing.T) {
	var out bytes.Buffer
	if err := Run(nil, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{"seek worktree", "list", "gc", "--older-than"} {
		if !strings.Contains(out.String(), frag) {
			t.Errorf("help missing %q in:\n%s", frag, out.String())
		}
	}
}

func TestRun_UnknownVerb(t *testing.T) {
	err := Run([]string{"bogus"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error for unknown verb")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the bad verb: %v", err)
	}
	if !strings.Contains(err.Error(), "help") {
		t.Errorf("error should point at help: %v", err)
	}
}

func TestRun_HelpAliases(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		var out bytes.Buffer
		if err := Run([]string{arg}, &out, &bytes.Buffer{}); err != nil {
			t.Errorf("Run(%q) returned error: %v", arg, err)
		}
		if !strings.Contains(out.String(), "seek worktree") {
			t.Errorf("Run(%q) help output empty:\n%s", arg, out.String())
		}
	}
}

// TestGC_BadDurationArg: --older-than expects a Go time.Duration
// literal; garbage should fail at parse, not exec something
// unsafe with a zero duration.
func TestGC_BadDurationArg(t *testing.T) {
	var stderr bytes.Buffer
	err := Run([]string{"gc", "--older-than", "forever"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("expected error for bad duration")
	}
}

// Note: cmdList and cmdGC themselves shell out to git via
// worktree.Manager.{ListFromDisk,PruneDiscarded}; those are
// already covered by internal/worktree/worktree_test.go's
// scripted-git tests. Re-exercising the same code paths here
// would require either a real git binary in tempdir (heavy) or
// reaching into worktree's private GitRunner injection (already
// exposed but redundant). Keep the CLI tests focused on what
// they actually own: arg parsing, dispatch, and help output.
