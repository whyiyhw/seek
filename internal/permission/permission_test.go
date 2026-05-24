package permission

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestYoloAllowsEverything(t *testing.T) {
	p, err := New(t.TempDir(), ModeYolo)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Action{
		{Kind: KindBash, Command: "rm -rf /"},
		{Kind: KindWrite, Path: "/etc/hosts"},
		{Kind: KindEdit, Path: "/no/such/place/file"},
	} {
		if err := p.Check(a); err != nil {
			t.Errorf("yolo denied %v: %v", a, err)
		}
	}
}

func TestBashRequiresYolo(t *testing.T) {
	p, _ := New(t.TempDir(), ModeDeny)
	err := p.Check(Action{Kind: KindBash, Command: "ls"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestWriteInsideCWD(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, ModeDeny)
	for _, path := range []string{
		filepath.Join(root, "a.txt"),
		filepath.Join(root, "deep", "nested", "b.txt"),
		root, // the root itself
	} {
		if err := p.Check(Action{Kind: KindWrite, Path: path}); err != nil {
			t.Errorf("denied %q: %v", path, err)
		}
	}
}

func TestWriteOutsideCWD(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir() // different dir
	p, _ := New(root, ModeDeny)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(other, "x")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestEditAlsoCWDGated(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, ModeDeny)
	err := p.Check(Action{Kind: KindEdit, Path: "/etc/hosts"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestUnknownKind(t *testing.T) {
	p, _ := New(t.TempDir(), ModeDeny)
	err := p.Check(Action{Kind: "voodoo"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

// --- ModeAsk + askFn paths ------------------------------------------
//
// The interactive approval flow is the production path used by every
// TUI session that isn't --yolo. Pre-this-commit it was 0% covered:
// SetAskFn never called by any test, so denial attribution / fallback
// behaviour / safe-action skipping were all untested guesses.

func TestModeAsk_AllowsWhenAskFnReturnsTrue(t *testing.T) {
	p, _ := New(t.TempDir(), ModeAsk)
	var (
		calls int
		seen  Action
	)
	p.SetAskFn(func(a Action) bool {
		calls++
		seen = a
		return true
	})
	if err := p.Check(Action{Kind: KindBash, Command: "ls -la"}); err != nil {
		t.Errorf("expected allow, got %v", err)
	}
	if calls != 1 {
		t.Errorf("askFn called %d times, want 1", calls)
	}
	if seen.Command != "ls -la" {
		t.Errorf("askFn saw command=%q, want ls -la", seen.Command)
	}
}

func TestModeAsk_DeniesWhenAskFnReturnsFalse(t *testing.T) {
	p, _ := New(t.TempDir(), ModeAsk)
	p.SetAskFn(func(_ Action) bool { return false })
	err := p.Check(Action{Kind: KindBash, Command: "rm -rf /"})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
	// The denial must attribute to the user — that's how the LLM
	// knows to ask for clarification rather than retry.
	if !strings.Contains(err.Error(), "user declined") {
		t.Errorf("denial should mention user choice, got %q", err.Error())
	}
}

func TestModeAsk_NoAskFnFallsBackToDeny(t *testing.T) {
	// If the host forgot SetAskFn — the policy must NEVER silently
	// allow. Failing closed is non-negotiable for a permission gate.
	p, _ := New(t.TempDir(), ModeAsk)
	err := p.Check(Action{Kind: KindBash, Command: "ls"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("ask-without-askFn should deny, got %v", err)
	}
	// The error should still surface the --yolo escape hatch.
	if !strings.Contains(err.Error(), "--yolo") {
		t.Errorf("denial message should suggest --yolo: %q", err.Error())
	}
}

func TestModeAsk_SafeActionsBypassAskFn(t *testing.T) {
	// Writes inside CWD are safe; the askFn must NOT be consulted —
	// every safe action that nags the user is a UX regression.
	root := t.TempDir()
	p, _ := New(root, ModeAsk)
	var calls int
	p.SetAskFn(func(_ Action) bool { calls++; return true })

	target := filepath.Join(root, "f.txt")
	if err := p.Check(Action{Kind: KindWrite, Path: target}); err != nil {
		t.Fatalf("safe write denied: %v", err)
	}
	if calls != 0 {
		t.Errorf("askFn consulted %d times for safe write; want 0", calls)
	}
}

// --- SetMode runtime transitions ------------------------------------

func TestSetMode_TransitionFromAskToYoloTakesEffectImmediately(t *testing.T) {
	// /yolo in the TUI uses SetMode for live policy updates. The
	// next Check after the flip must see the new mode without any
	// agent / registry rebuild.
	p, _ := New(t.TempDir(), ModeAsk)
	p.SetAskFn(func(_ Action) bool { return false }) // would deny

	if err := p.Check(Action{Kind: KindBash, Command: "ls"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("pre-flip should deny, got %v", err)
	}

	p.SetMode(ModeYolo)

	if err := p.Check(Action{Kind: KindBash, Command: "ls"}); err != nil {
		t.Errorf("post-flip should allow, got %v", err)
	}
	// Getter sanity — also bumps coverage on Mode / Yolo / CWD,
	// which are trivial but part of the public API.
	if p.Mode() != ModeYolo {
		t.Errorf("Mode() = %v, want ModeYolo", p.Mode())
	}
	if !p.Yolo() {
		t.Errorf("Yolo() = false, want true after flip")
	}
	if p.CWD() == "" {
		t.Errorf("CWD() returned empty string")
	}
}

func TestSetMode_NilPolicySafe(t *testing.T) {
	// Defensive: every public method on *Policy guards against nil
	// receivers. A test that exercises this catches future refactors
	// that accidentally remove the guard (or worse, introduce a
	// dependency on Policy being non-nil).
	var p *Policy
	p.SetMode(ModeYolo)
	p.SetAskFn(func(_ Action) bool { return true })
	if p.Mode() != ModeDeny {
		t.Errorf("nil policy Mode() = %v, want ModeDeny", p.Mode())
	}
	if p.Yolo() {
		t.Errorf("nil policy Yolo() = true, want false")
	}
	// Check on nil policy must DENY, never panic, never allow.
	if err := p.Check(Action{Kind: KindBash}); !errors.Is(err, ErrDenied) {
		t.Errorf("nil policy Check should deny, got %v", err)
	}
}

// --- Concurrent Check -----------------------------------------------

func TestCheck_ConcurrentCallsRaceFree(t *testing.T) {
	// Tool dispatch is sequential today (PRD §3.1); parallel via
	// errgroup is a post-v1.0 item. Pinning the race-freedom contract
	// NOW means the future parallel-dispatch milestone discovers
	// "policy is already safe" instead of "policy needs a mutex" at
	// the same time as it lands the rest of the parallelism work.
	//
	// Mixes reads (Check/Mode/Yolo) and writes (SetMode) to stress
	// the mode field specifically.
	p, _ := New(t.TempDir(), ModeYolo)
	p.SetAskFn(func(_ Action) bool { return true })

	const N = 64
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = p.Check(Action{Kind: KindBash, Command: "ls"})
			_ = p.Mode()
			_ = p.Yolo()
		}()
		go func(flip int) {
			defer wg.Done()
			if flip%2 == 0 {
				p.SetMode(ModeYolo)
			} else {
				p.SetMode(ModeAsk)
			}
		}(i)
	}
	wg.Wait()
}

// --- Symlink resolution (security) ------------------------------------
//
// permission.isWithin now resolves symlinks (filepath.EvalSymlinks) so a
// symlink INSIDE cwd that points OUTSIDE cwd is correctly caught.
func TestIsWithin_SymlinkInsideCWDPointingOutsideIsDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	p, _ := New(root, ModeDeny)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(link, "x")})
	if err == nil {
		t.Error("symlink-in-cwd write allowed — symlink resolution not working")
	}
}

// --- ModePlan tests ---------------------------------------------------

func TestModePlan_AllowsReadInsideCWD(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, ModePlan)
	err := p.Check(Action{Kind: KindRead, Path: filepath.Join(root, "foo.go")})
	if err != nil {
		t.Errorf("plan mode should allow read inside CWD, got %v", err)
	}
}

func TestModePlan_DeniesReadOutsideCWD(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	p, _ := New(root, ModePlan)
	err := p.Check(Action{Kind: KindRead, Path: filepath.Join(other, "secret")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan mode should deny read outside CWD, got %v", err)
	}
}

func TestModePlan_DeniesBash(t *testing.T) {
	p, _ := New(t.TempDir(), ModePlan)
	err := p.Check(Action{Kind: KindBash, Command: "ls"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan mode should deny bash, got %v", err)
	}
}

func TestModePlan_DeniesWriteInsideCWD(t *testing.T) {
	root := t.TempDir()
	p, _ := New(root, ModePlan)
	err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(root, "x.go")})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan mode should deny write even inside CWD, got %v", err)
	}
}

func TestModePlan_DeniesEdit(t *testing.T) {
	p, _ := New(t.TempDir(), ModePlan)
	err := p.Check(Action{Kind: KindEdit, Path: "/some/file"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan mode should deny edit, got %v", err)
	}
}

func TestModePlan_DeniesMemoryRemember(t *testing.T) {
	p, _ := New(t.TempDir(), ModePlan)
	err := p.Check(Action{Kind: KindMemoryRemember, MemoryName: "test"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan mode should deny memory_remember, got %v", err)
	}
}

func TestModePlan_DeniesUnknownKind(t *testing.T) {
	p, _ := New(t.TempDir(), ModePlan)
	err := p.Check(Action{Kind: "voodoo"})
	if !errors.Is(err, ErrDenied) {
		t.Errorf("plan mode should deny unknown kind, got %v", err)
	}
}

func TestPlan_Method(t *testing.T) {
	p, _ := New(t.TempDir(), ModePlan)
	if !p.Plan() {
		t.Error("Plan() should return true in ModePlan")
	}
	if p.Yolo() {
		t.Error("Yolo() should return false in ModePlan")
	}
}
