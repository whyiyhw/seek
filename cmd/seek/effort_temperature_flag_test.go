package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// effort_temperature_flag_test.go covers card c_trrsn7 (judge precondition
// ①: cost 3.2×). It pins four things the flag exists for:
//
//  1. parseEffortFlag / parseTemperatureFlag's accepted surface.
//  2. The wire consequence on a print-mode run — `--effort off`/`low` drop
//     the derived completion cap to 16K (this lane's real money lever,
//     since reasoning_effort itself is DeepSeek-only), and `--temperature 0`
//     emits the key while its absence emits nothing.
//  3. Regression: with no flag the body is byte-identical to the
//     pre-flag shape, and an explicit `--effort max` (the historical
//     default) doesn't perturb it either.
//  4. Resume precedence: a saved session's effort is inherited ONLY when
//     the flag was not passed — the same rule --model/--yolo/--plan follow.

func TestParseEffortFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},     // absent flag: no override, and the caller
		{"off", "", false},  // distinguishes the two via flag.Visit
		{"none", "", false}, // /effort alias
		{"OFF", "", false},  // case-insensitive
		{" high ", "high", false},
		{"max", "max", false},
		// low is the rung the judge lane actually wants (cheapest
		// REASONING level — measured 3.2× cheaper than max on output).
		// The CLI takes it even though the TUI picker stays off|high|max.
		{"low", "low", false},
		{"LOW", "low", false},
		{"medium", "", true}, // compat alias for high; not spelled here
		{"turbo", "", true},
	}
	for _, c := range cases {
		got, err := parseEffortFlag(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseEffortFlag(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("parseEffortFlag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseTemperatureFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    *float64
		wantErr bool
	}{
		{"", nil, false},    // absent: no key on the wire
		{"   ", nil, false}, // whitespace-only is absent too
		{"0", ptrFloat(0), false},
		{"0.0", ptrFloat(0), false},
		{"1", ptrFloat(1), false},
		{"2", ptrFloat(2), false},
		{"0.7", ptrFloat(0.7), false},
		{"-1", nil, true},
		{"3", nil, true},
		{"abc", nil, true},
		{"NaN", nil, true},
		{"Inf", nil, true},
	}
	for _, c := range cases {
		got, err := parseTemperatureFlag(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseTemperatureFlag(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		switch {
		case c.want == nil && got != nil:
			t.Errorf("parseTemperatureFlag(%q) = %v, want nil", c.in, *got)
		case c.want != nil && got == nil:
			t.Errorf("parseTemperatureFlag(%q) = nil, want %v", c.in, *c.want)
		case c.want != nil && *got != *c.want:
			t.Errorf("parseTemperatureFlag(%q) = %v, want %v", c.in, *got, *c.want)
		}
	}
}

func ptrFloat(f float64) *float64 { return &f }

// bodyKey decodes the first captured request body and reports the raw
// presence + value of a top-level key. Raw presence (not the decoded
// value) is what the prefix-cache contract turns on: `"temperature":null`
// would decode to nil and silently pass a value-only assertion.
func bodyKey(t *testing.T, body []byte, key string) (present bool, raw json.RawMessage) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode request body: %v\n%s", err, body)
	}
	v, ok := m[key]
	return ok, v
}

// TestEffortTemperatureFlagE2E_DefaultBodyUnchanged is acceptance ②:
// with neither flag the request bytes must be exactly what a pre-flag
// build sent. Two shapes pin it:
//
//   - the default body carries NO "temperature" key at all (a
//     present-but-null key would still move the prefix-cache bytes);
//   - explicitly passing the historical default (`--effort max`) is
//     byte-identical to passing nothing, and so is an explicitly empty
//     `--effort ""` — the flag being present must not perturb the body.
func TestEffortTemperatureFlagE2E_DefaultBodyUnchanged(t *testing.T) {
	workDir := t.TempDir()

	run := func(extra ...string) []byte {
		b := newE2EBackend(t)
		stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, extra...)
		if code != 0 {
			t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		return b.firstBody(t)
	}

	base := run()
	if strings.Contains(string(base), `"temperature"`) {
		t.Errorf("default body must not carry a temperature key:\n%s", base)
	}
	if got := run("--effort", "max"); !bytes.Equal(got, base) {
		t.Errorf("--effort max (the historical default) changed the body:\n%s\n---\n%s", base, got)
	}
	if got := run("--effort", ""); !bytes.Equal(got, base) {
		t.Errorf("--effort \"\" (absent) changed the body:\n%s\n---\n%s", base, got)
	}
}

// TestEffortFlagE2E_OffDropsDerivedCap is the money assertion: the default
// session effort is "max" (64K derived cap), so a print-mode batch pays
// the deepest budget on every call. `--effort off` must land on the
// historical 16K — and an explicit --max-tokens must still win, because
// that is the pre-existing contract (docs/guide-budget.md §1.1).
func TestEffortFlagE2E_OffDropsDerivedCap(t *testing.T) {
	workDir := t.TempDir()

	for _, c := range []struct {
		name string
		args []string
		want int
	}{
		{"default (max) → 64k", nil, 65536},
		{"--effort off → 16k", []string{"--effort", "off"}, 16384},
		{"--effort low → 16k", []string{"--effort", "low"}, 16384},
		{"--effort high → 16k", []string{"--effort", "high"}, 16384},
		{"--effort max → 64k", []string{"--effort", "max"}, 65536},
		{"--effort off + explicit --max-tokens wins", []string{"--effort", "off", "--max-tokens", "9000"}, 9000},
		// An explicitly EMPTY value is "absent", not "off" — the same
		// contract `-tools ""` keeps. Scripts interpolating a maybe-unset
		// variable must not silently drop the default to 16K.
		{"--effort \"\" (empty) = absent → 64k", []string{"--effort", ""}, 65536},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newE2EBackend(t)
			stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, c.args...)
			if code != 0 {
				t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			body := b.firstBody(t)
			_, raw := bodyKey(t, body, "max_tokens")
			var got int
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("max_tokens: %v\n%s", err, body)
			}
			if got != c.want {
				t.Errorf("max_tokens = %d, want %d\n%s", got, c.want, body)
			}
		})
	}
}

// TestTemperatureFlagE2E_WirePresence pins the knob end-to-end on the
// OpenAI-compatible lane (the one every print-mode batch uses today).
func TestTemperatureFlagE2E_WirePresence(t *testing.T) {
	workDir := t.TempDir()
	for _, c := range []struct {
		name    string
		args    []string
		wantKey bool
		wantVal float64
	}{
		{"absent → no key", nil, false, 0},
		{"--temperature 0 → key with 0", []string{"--temperature", "0"}, true, 0},
		{"--temperature 0.7 → key with 0.7", []string{"--temperature", "0.7"}, true, 0.7},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newE2EBackend(t)
			stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, c.args...)
			if code != 0 {
				t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			body := b.firstBody(t)
			present, raw := bodyKey(t, body, "temperature")
			if present != c.wantKey {
				t.Fatalf("temperature key present = %v, want %v\n%s", present, c.wantKey, body)
			}
			if c.wantKey {
				var got float64
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("temperature: %v\n%s", err, body)
				}
				if got != c.wantVal {
					t.Errorf("temperature = %v, want %v", got, c.wantVal)
				}
			}
		})
	}
}

// TestEffortFlagE2E_ResumeInheritsUnlessFlagged covers the precedence rule
// at the only place it can break: a saved session's effort must be
// restored on --continue, but an explicitly passed --effort must win.
func TestEffortFlagE2E_ResumeInheritsUnlessFlagged(t *testing.T) {
	workDir := t.TempDir()

	// Seed a session whose recorded effort is "high" (16K derived cap).
	b1 := newE2EBackend(t)
	if _, stderr, code := runSeekSaveE2E(t, b1.srv.URL, workDir, "--effort", "high"); code != 0 {
		t.Fatalf("seed exit = %d, want 0\n%s", code, stderr)
	}
	if cap := maxTokensOf(t, b1.firstBody(t)); cap != 16384 {
		t.Fatalf("seed max_tokens = %d, want 16384", cap)
	}

	// --continue with NO flag: inherit "high" → 16K.
	b2 := newE2EBackend(t)
	if _, stderr, code := runSeekSaveE2E(t, b2.srv.URL, workDir, "--continue"); code != 0 {
		t.Fatalf("inherit exit = %d, want 0\n%s", code, stderr)
	}
	if cap := maxTokensOf(t, b2.firstBody(t)); cap != 16384 {
		t.Errorf("inherited max_tokens = %d, want 16384 (saved effort high)", cap)
	}

	// --continue WITH --effort max: the flag wins → 64K.
	b3 := newE2EBackend(t)
	if _, stderr, code := runSeekSaveE2E(t, b3.srv.URL, workDir, "--continue", "--effort", "max"); code != 0 {
		t.Fatalf("override exit = %d, want 0\n%s", code, stderr)
	}
	if cap := maxTokensOf(t, b3.firstBody(t)); cap != 65536 {
		t.Errorf("override max_tokens = %d, want 65536 (explicit --effort max must beat the saved session)", cap)
	}
}

func maxTokensOf(t *testing.T, body []byte) int {
	t.Helper()
	_, raw := bodyKey(t, body, "max_tokens")
	var got int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("max_tokens: %v\n%s", err, body)
	}
	return got
}

// TestEffortTemperatureFlagE2E_BadValueExits2: a bad VALUE is a usage
// error (exit 2, complaint on stderr naming the offender), and it must
// die before any request reaches the backend — otherwise a typo costs
// a handshake and, on a metered lane, real money.
func TestEffortTemperatureFlagE2E_BadValueExits2(t *testing.T) {
	workDir := t.TempDir()
	for _, c := range []struct {
		name     string
		args     []string
		wantText string
	}{
		{"bad effort", []string{"--effort", "turbo"}, "turbo"},
		{"bad temperature (text)", []string{"--temperature", "warm"}, "warm"},
		{"temperature out of range", []string{"--temperature", "3"}, "3"},
		{"temperature negative", []string{"--temperature", "-1"}, "-1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newE2EBackend(t)
			_, stderr, code := runSeekE2E(t, b.srv.URL, workDir, c.args...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2\nstderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, c.wantText) {
				t.Errorf("stderr misses %q:\n%s", c.wantText, stderr)
			}
			if n := b.requestCount(); n != 0 {
				t.Errorf("backend saw %d requests, want 0 (validation must precede any call)", n)
			}
		})
	}
}
