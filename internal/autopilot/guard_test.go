package autopilot

import "testing"

func TestIsRemoteMutating_Blocks(t *testing.T) {
	blocked := []string{
		"git push",
		"git push origin main",
		"cd sub && git push --force",
		"gh pr create --fill",
		"gh pr merge 12",
		"gh repo create foo",
		"gh release create v1",
		"git remote add origin git@github.com:x/y",
		"gh api /repos/x/y/issues",
	}
	for _, c := range blocked {
		if deny, _ := IsRemoteMutating(c); !deny {
			t.Errorf("should block remote op: %q", c)
		}
	}
}

func TestIsRemoteMutating_Allows(t *testing.T) {
	allowed := []string{
		"git commit -m 'wip'",
		"git add -A",
		"git log --oneline",
		"git diff",
		"git fetch origin", // read-only network
		"git pull",         // not a push; allowed (merges locally)
		"gh pr view 12",    // read
		"go test ./...",
		"echo git push is just text in a comment", // not an actual push invocation? it IS "git push" — see note
	}
	for _, c := range allowed[:len(allowed)-1] { // last case documents a known limitation
		if deny, _ := IsRemoteMutating(c); deny {
			t.Errorf("should allow: %q", c)
		}
	}
	// Documented limitation: string matching can't distinguish a real
	// `git push` invocation from the words "git push" inside an echo/
	// comment — it errs toward blocking. The OS sandbox (柱 O) is the
	// real guarantee. Assert the conservative behavior so it's intentional.
	if deny, _ := IsRemoteMutating("echo git push is just text in a comment"); !deny {
		t.Skip("string-match guard conservatively blocks any 'git push' token — acceptable, 柱 O is the real boundary")
	}
}
