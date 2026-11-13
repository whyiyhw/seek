package acp

import (
	"errors"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

// stubEnv returns a getenv that answers from a fixed map (unset key ⇒ "").
func stubEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestYoloFromEnv(t *testing.T) {
	cases := []struct {
		name string
		val  string // value of SEEK_ACP_YOLO; "" also covers "unset"
		want bool
	}{
		{"unset", "", false},
		{"empty", "", false},
		{"one", "1", true},
		{"one padded", " 1 ", true}, // trimmed
		{"true", "true", true},
		{"TRUE", "TRUE", true}, // case-insensitive
		{"yes", "yes", true},
		{"on", "on", true},
		{"zero", "0", false},
		{"false", "false", false},
		{"off", "off", false},
		{"garbage", "maybe", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := YoloFromEnv(stubEnv(map[string]string{YoloEnvVar: c.val})); got != c.want {
				t.Errorf("YoloFromEnv(SEEK_ACP_YOLO=%q) = %v, want %v", c.val, got, c.want)
			}
		})
	}
	// nil getenv (defensive branch): off, never a silent yolo.
	if YoloFromEnv(nil) {
		t.Error("YoloFromEnv(nil) = true, want false")
	}
	// Env var must be consulted by its exported name — the contract
	// cmd/seek and provider configs (paseo `env` block) spell it with.
	if YoloEnvVar != "SEEK_ACP_YOLO" {
		t.Errorf("YoloEnvVar = %q, want %q", YoloEnvVar, "SEEK_ACP_YOLO")
	}
}

// TestACPHeadlessPrefBashGate covers the two runtime paths the `seek acp`
// lane takes through the permission gate (D-2, docs/prd/
// feature-paseo-provider.md §3.3): env unset → the lane initialises at
// PrefDeny (bash denied — read-only whitelisted inspectors still pass),
// SEEK_ACP_YOLO truthy → PrefYolo (bash allowed, same posture as goal /
// autopilot "no human to ask"). Mirrors cmd/seek's wiring exactly:
// resolve the pref from env, THEN permission.New — so the gate verdict
// and the sysprompt Mode label can't disagree. Asserts Check's verdict
// only; nothing is ever executed.
func TestACPHeadlessPrefBashGate(t *testing.T) {
	const dangerous = "git commit -am 'card work'" // what drive cards need and Deny blocks
	for _, tc := range []struct {
		name      string
		env       map[string]string
		wantDenie bool // wantErr := permission.ErrDenied
	}{
		{"default deny (env unset)", nil, true},
		{"SEEK_ACP_YOLO=1 → yolo", map[string]string{YoloEnvVar: "1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Same resolution order as cmd/seek's initialPref block.
			pref := permission.PrefDeny
			if YoloFromEnv(stubEnv(tc.env)) {
				pref = permission.PrefYolo
			}
			policy, err := permission.New(t.TempDir(), pref)
			if err != nil {
				t.Fatalf("permission.New: %v", err)
			}

			err = policy.Check(permission.Action{Kind: permission.KindBash, Command: dangerous})
			if tc.wantDenie {
				if !errors.Is(err, permission.ErrDenied) {
					t.Fatalf("Check(bash %q) under %v = %v, want ErrDenied", dangerous, pref, err)
				}
				// Deny still lets whitelisted read-only bash through
				// (ReadOnly is set only by the bash tool's readonly.go;
				// step 0 of Check honours it regardless of pref) — the
				// headless lane's floor, not total paralysis.
				if err := policy.Check(permission.Action{Kind: permission.KindBash, ReadOnly: true, Command: "go vet ./..."}); err != nil {
					t.Fatalf("Check(read-only bash) under Deny = %v, want nil", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Check(bash %q) under %v = %v, want nil", dangerous, pref, err)
			}
		})
	}
}
