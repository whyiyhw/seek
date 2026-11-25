package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/tools"
)

// ---------------------------------------------------------------------------
// parseToolsFlag — flag-value hygiene (trim / empty / dedup).
// ---------------------------------------------------------------------------

func TestParseToolsFlag(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",", nil},
		{" , , ", nil},
		{"read", []string{"read"}},
		{"read,edit,write", []string{"read", "edit", "write"}},
		{" read , edit , write ", []string{"read", "edit", "write"}},
		{"read,read,read", []string{"read"}},
		{"read,,edit", []string{"read", "edit"}},
	}
	for _, tc := range tests {
		if got := parseToolsFlag(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseToolsFlag(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// applyToolsWhitelist — in-process behaviour (sentinel, filtering,
// session recording, resume warning).
// ---------------------------------------------------------------------------

func newWhitelistTestRegistry() *tools.Registry {
	reg := tools.New()
	for _, n := range []string{"read", "edit", "write", "bash", "grep", "monitor", "git"} {
		reg.Add(stubParentTool{name: n})
	}
	return reg
}

func TestApplyToolsWhitelist_FiltersAndRecords(t *testing.T) {
	reg := newWhitelistTestRegistry()
	active := session.New("m", "/tmp", "sp", false, false)

	if err := applyToolsWhitelist("read,edit,write,bash,grep", reg, nil, active); err != nil {
		t.Fatalf("applyToolsWhitelist: %v", err)
	}
	want := []string{"bash", "edit", "grep", "read", "write"}
	if got := reg.Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("reg.Names() = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(active.Tools, want) {
		t.Errorf("active.Tools = %v, want %v", active.Tools, want)
	}
}

func TestApplyToolsWhitelist_EmptyKeepsFullRegistry(t *testing.T) {
	reg := newWhitelistTestRegistry()
	before := reg.Names()
	active := session.New("m", "/tmp", "sp", false, false)

	for _, v := range []string{"", ",", " "} {
		if err := applyToolsWhitelist(v, reg, nil, active); err != nil {
			t.Fatalf("applyToolsWhitelist(%q): %v", v, err)
		}
	}
	if got := reg.Names(); !reflect.DeepEqual(got, before) {
		t.Errorf("empty whitelist changed the registry: %v, want %v", got, before)
	}
	if !reflect.DeepEqual(active.Tools, before) {
		t.Errorf("active.Tools = %v, want the full set %v", active.Tools, before)
	}
}

func TestApplyToolsWhitelist_UnknownNameErrToolsUsage(t *testing.T) {
	reg := newWhitelistTestRegistry()
	before := reg.Names()

	err := applyToolsWhitelist("read,no-such-tool", reg, nil, nil)
	if !errors.Is(err, errToolsUsage) {
		t.Fatalf("err = %v, want errToolsUsage", err)
	}
	// Registry untouched on rejection — the whole value is invalid, not
	// just the unknown entry.
	if got := reg.Names(); !reflect.DeepEqual(got, before) {
		t.Errorf("registry mutated on rejection: %v, want %v", got, before)
	}
}

// The `none` sentinel empties the registry outright — the zero-tool
// surface a program consumer (judge/pipe) needs. Wire() must then carry
// nothing, so no `tools` field can reach the model.
func TestApplyToolsWhitelist_NoneEmptiesRegistry(t *testing.T) {
	for _, v := range []string{"none", " none ", "none,"} {
		t.Run(v, func(t *testing.T) {
			reg := newWhitelistTestRegistry()
			active := session.New("m", "/tmp", "sp", false, false)

			if err := applyToolsWhitelist(v, reg, nil, active); err != nil {
				t.Fatalf("applyToolsWhitelist(%q): %v", v, err)
			}
			if got := reg.Names(); len(got) != 0 {
				t.Errorf("reg.Names() = %v, want empty after the zero-tool sentinel", got)
			}
			if reg.Lookup("read") != nil {
				t.Error("Lookup(read) survived the zero-tool sentinel")
			}
			if wire := reg.Wire(); len(wire) != 0 {
				t.Errorf("Wire() = %v, want no tools", wire)
			}
			if len(active.Tools) != 0 {
				t.Errorf("active.Tools = %v, want empty (saved sessions must record the empty set)", active.Tools)
			}
		})
	}
}

// The sentinel is not a tool name: mixing it with names is a usage
// error (exit 2), and the registry stays untouched so nothing half-applies.
func TestApplyToolsWhitelist_NoneMixedWithNamesErrToolsUsage(t *testing.T) {
	for _, v := range []string{"none,read", "read,none", "none,read,edit"} {
		t.Run(v, func(t *testing.T) {
			reg := newWhitelistTestRegistry()
			before := reg.Names()

			var err error
			stderr := captureStderr(t, func() {
				err = applyToolsWhitelist(v, reg, nil, nil)
			})
			if !errors.Is(err, errToolsUsage) {
				t.Fatalf("applyToolsWhitelist(%q) err = %v, want errToolsUsage", v, err)
			}
			if !strings.Contains(stderr, "sentinel") {
				t.Errorf("stderr = %q, want it to explain that none is a sentinel", stderr)
			}
			if got := reg.Names(); !reflect.DeepEqual(got, before) {
				t.Errorf("registry mutated on rejection: %v, want %v", got, before)
			}
		})
	}
}

func TestApplyToolsWhitelist_NoSaveSessionNilIsFine(t *testing.T) {
	reg := newWhitelistTestRegistry()
	if err := applyToolsWhitelist("read", reg, nil, nil); err != nil {
		t.Fatalf("applyToolsWhitelist with nil session: %v", err)
	}
	if got := reg.Names(); len(got) != 1 || got[0] != "read" {
		t.Errorf("reg.Names() = %v, want [read]", got)
	}
}

// captureStderr swaps os.Stderr for a pipe, runs f, and returns what
// was written. Single-threaded test use only — nothing else may print
// while f runs.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	f()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("drain pipe: %v", err)
	}
	return buf.String()
}

func TestApplyToolsWhitelist_ResumeWarnsOnChangedSet(t *testing.T) {
	reg := newWhitelistTestRegistry()
	resumed := session.New("m", "/tmp", "sp", false, false)
	resumed.Tools = []string{"git", "grep", "read"}

	stderr := captureStderr(t, func() {
		if err := applyToolsWhitelist("read,edit", reg, resumed, resumed); err != nil {
			t.Errorf("applyToolsWhitelist: %v", err)
		}
	})
	if !strings.Contains(stderr, "工具集变了，缓存会失效一次") {
		t.Errorf("stderr = %q, want the tool-set-changed cache warning", stderr)
	}
	// The live set replaces the recorded one so the NEXT resume compares
	// against what actually ran.
	want := []string{"edit", "read"}
	if !reflect.DeepEqual(resumed.Tools, want) {
		t.Errorf("resumed.Tools = %v, want %v", resumed.Tools, want)
	}
}

func TestApplyToolsWhitelist_ResumeSameSetNoWarning(t *testing.T) {
	reg := newWhitelistTestRegistry()
	resumed := session.New("m", "/tmp", "sp", false, false)
	resumed.Tools = []string{"read"}

	stderr := captureStderr(t, func() {
		if err := applyToolsWhitelist("read", reg, resumed, resumed); err != nil {
			t.Errorf("applyToolsWhitelist: %v", err)
		}
	})
	if strings.Contains(stderr, "工具集") {
		t.Errorf("stderr = %q, want no warning for identical tool set", stderr)
	}
}

// Pre-feature sessions carry no Tools record — resume must not guess
// (no warning), but still records the current set going forward.
func TestApplyToolsWhitelist_ResumeLegacySessionNoRecord(t *testing.T) {
	reg := newWhitelistTestRegistry()
	resumed := session.New("m", "/tmp", "sp", false, false)
	resumed.Tools = nil

	stderr := captureStderr(t, func() {
		if err := applyToolsWhitelist("read", reg, resumed, resumed); err != nil {
			t.Errorf("applyToolsWhitelist: %v", err)
		}
	})
	if strings.Contains(stderr, "工具集") {
		t.Errorf("stderr = %q, want no warning when the session has no recorded set", stderr)
	}
	if !reflect.DeepEqual(resumed.Tools, []string{"read"}) {
		t.Errorf("resumed.Tools = %v, want [read]", resumed.Tools)
	}
}

// ---------------------------------------------------------------------------
// E2E — the compiled binary against an httptest fake backend. These are
// the v8·T1 acceptance lanes: the tools ARRAY on the wire, byte-stable
// across startups, exit code 2 on unknown names, empty==absent.
// ---------------------------------------------------------------------------

var (
	seekBinOnce sync.Once
	seekBinDir  string
	seekBinPath string
	seekBinErr  error
)

// TestMain owns the shared E2E binary's lifecycle: the artifact must
// outlive any single test's t.TempDir() (the first consumer would
// otherwise clean it up from under the next test), so it lands in its
// own temp dir removed when the whole test binary exits.
func TestMain(m *testing.M) {
	// The ACP MCP passthrough tests re-enter this binary as a fake MCP
	// server over stdio (cmd/seek/acp_mcp_test.go: stubMCPMain). Mirrors
	// internal/controlplane's `-p` stub dispatch: intercept before
	// m.Run() so the real flag parser never sees the stub argument.
	if len(os.Args) > 1 && os.Args[1] == "-acp-mcp-stub" {
		stubMCPMain()
		os.Exit(0)
	}
	code := m.Run()
	if seekBinDir != "" {
		_ = os.RemoveAll(seekBinDir)
	}
	os.Exit(code)
}

// buildSeekBin compiles the package under test once per test binary
// run. Latter tests reuse the artifact.
func buildSeekBin(t *testing.T) string {
	t.Helper()
	seekBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "seek-e2e-bin-")
		if err != nil {
			seekBinErr = fmt.Errorf("temp dir: %v", err)
			return
		}
		seekBinDir = dir
		out := filepath.Join(dir, "seek-e2e")
		if runtime.GOOS == "windows" {
			// CreateProcess only executes files carrying a known
			// extension (.exe); an extensionless path fails as
			// "executable file not found in %PATH%" even though go
			// build wrote the file (2026-11-18 nightly).
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = "." // package main lives here
		if buf, err := cmd.CombinedOutput(); err != nil {
			seekBinErr = fmt.Errorf("go build: %v\n%s", err, buf)
			return
		}
		seekBinPath = out
	})
	if seekBinErr != nil {
		t.Fatalf("build seek: %v", seekBinErr)
	}
	return seekBinPath
}

// e2eBackend is an httptest stand-in for an OpenAI-compatible chat
// backend. It records every raw request body (bytes, not just parsed
// fields — byte-identity across startups is the assertion) and replies
// with a canned single-turn SSE stream.
type e2eBackend struct {
	srv *httptest.Server
	mu  sync.Mutex
	raw [][]byte
}

const e2eSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"ok.\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n" +
	"data: [DONE]\n\n"

func newE2EBackend(t *testing.T) *e2eBackend {
	t.Helper()
	b := &e2eBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.raw = append(b.raw, body)
		b.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, e2eSSE)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// firstBody returns the first captured request body (the initial
// prompt — the one whose prefix every later request extends).
func (b *e2eBackend) firstBody(t *testing.T) []byte {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.raw) == 0 {
		t.Fatal("backend captured no requests")
	}
	out := make([]byte, len(b.raw[0]))
	copy(out, b.raw[0])
	return out
}

// requestCount reports how many requests the backend has served.
func (b *e2eBackend) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.raw)
}

func toolNamesFromBody(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request body: %v\n%s", err, body)
	}
	names := make([]string, 0, len(req.Tools))
	for _, tl := range req.Tools {
		names = append(names, tl.Function.Name)
	}
	// Wire() sorts by name already; sort again so the assertion is
	// order-independent by construction.
	slices.Sort(names)
	return names
}

// runSeekE2E runs the compiled binary in print mode against the fake
// backend under a hermetic env (SEEK_HOME + HOME + cwd all inside the
// same temp dir → no MCP config, no skills, no project-md, no git).
// extra args ride after the fixed ones.
func runSeekE2E(t *testing.T, backendURL, workDir string, extra ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildSeekBin(t)
	args := []string{
		"-p", "hi",
		"--provider", "compatible",
		"--base-url", backendURL + "/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
		"--no-save",
		"--no-cron-tick",
		"--no-project-md",
	}
	args = append(args, extra...)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"SEEK_HOME="+workDir,
		"HOME="+workDir,
		"OPENAI_API_KEY=e2e-key",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run seek: %v\nstderr:\n%s", err, errBuf.String())
	}
	return outBuf.String(), errBuf.String(), code
}

// Acceptance ①: `-tools read,edit,write,bash,grep` → the tools array
// sent to the backend is EXACTLY those five, and two separate
// startups produce byte-identical first requests (the whitelist is
// frozen at startup, so the prefix — and the cache key — is stable).
func TestToolsFlagE2E_WhitelistFiveToolsDeterministic(t *testing.T) {
	workDir := t.TempDir()
	const flagVal = "read,edit,write,bash,grep"

	b1 := newE2EBackend(t)
	if _, _, code := runSeekE2E(t, b1.srv.URL, workDir, "-tools", flagVal); code != 0 {
		t.Fatalf("first startup exit = %d, want 0", code)
	}
	body1 := b1.firstBody(t)

	b2 := newE2EBackend(t)
	if _, _, code := runSeekE2E(t, b2.srv.URL, workDir, "-tools", flagVal); code != 0 {
		t.Fatalf("second startup exit = %d, want 0", code)
	}
	body2 := b2.firstBody(t)

	want := []string{"bash", "edit", "grep", "read", "write"}
	if got := toolNamesFromBody(t, body1); !reflect.DeepEqual(got, want) {
		t.Errorf("startup 1 tools = %v, want exactly %v", got, want)
	}
	if got := toolNamesFromBody(t, body2); !reflect.DeepEqual(got, want) {
		t.Errorf("startup 2 tools = %v, want exactly %v", got, want)
	}
	if !bytes.Equal(body1, body2) {
		t.Errorf("two startups differ:\n%s\n%s", body1, body2)
	}
}

// Acceptance ②: unknown tool name → exit code 2, stderr names the
// offender and prints the available list.
func TestToolsFlagE2E_UnknownNameExits2(t *testing.T) {
	workDir := t.TempDir()
	b := newE2EBackend(t)

	_, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "read,bogus_tool")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "bogus_tool") {
		t.Errorf("stderr misses the unknown name:\n%s", stderr)
	}
	if !strings.Contains(stderr, "available tools:") {
		t.Errorf("stderr misses the available list:\n%s", stderr)
	}
	if n := b.requestCount(); n != 0 {
		t.Errorf("backend saw %d requests, want 0 (no prompt before the flag dies)", n)
	}
}

// Acceptance ③ (guard): an explicitly EMPTY -tools value must equal
// not passing the flag at all — request bytes identical, and both are
// the unfiltered default registry for this env.
func TestToolsFlagE2E_EmptyEqualsAbsent(t *testing.T) {
	workDir := t.TempDir()

	bNoFlag := newE2EBackend(t)
	if _, _, code := runSeekE2E(t, bNoFlag.srv.URL, workDir); code != 0 {
		t.Fatalf("no-flag run exit = %d, want 0", code)
	}
	bodyNoFlag := bNoFlag.firstBody(t)

	bEmpty := newE2EBackend(t)
	if _, _, code := runSeekE2E(t, bEmpty.srv.URL, workDir, "-tools", ""); code != 0 {
		t.Fatalf("empty-flag run exit = %d, want 0", code)
	}
	bodyEmpty := bEmpty.firstBody(t)

	if !bytes.Equal(bodyNoFlag, bodyEmpty) {
		t.Errorf("absent vs empty -tools differ:\n%s\n%s", bodyNoFlag, bodyEmpty)
	}
	// Sanity: the default lane really is unfiltered — more than the
	// bare five (references, list_dir, monitor, git, Skill, … all ride
	// along), proving the empty value didn't silently whitelist.
	if got := toolNamesFromBody(t, bodyNoFlag); len(got) <= 5 {
		t.Errorf("default registry has %d tools (≤5) — expected the full set, got %v", len(got), got)
	}
}

// Acceptance ① of the zero-tool sentinel: `-tools none` empties the
// registry, so the model request body carries NO tools field at all —
// not an empty array, absent. Asserted on raw bytes (the same shape the
// prefix cache and the second-tier adapters see).
func TestToolsFlagE2E_NoneSendsNoToolsField(t *testing.T) {
	workDir := t.TempDir()

	b := newE2EBackend(t)
	stdout, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "none")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	body := b.firstBody(t)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal request body: %v\n%s", err, body)
	}
	if _, present := raw["tools"]; present {
		t.Errorf("request body still carries a tools field:\n%s", body)
	}
	if got := toolNamesFromBody(t, body); len(got) != 0 {
		t.Errorf("tools = %v, want none", got)
	}
	// Belt and braces on the bytes: no `"tools"` key anywhere in the body.
	if bytes.Contains(body, []byte(`"tools":`)) {
		t.Errorf("raw body mentions a tools key:\n%s", body)
	}
}

// Acceptance ③: the sentinel does not eat lists — `-tools none,read`
// exits 2 before any prompt reaches the backend, and says why.
func TestToolsFlagE2E_NoneMixedExits2(t *testing.T) {
	workDir := t.TempDir()
	b := newE2EBackend(t)

	_, stderr, code := runSeekE2E(t, b.srv.URL, workDir, "-tools", "none,read")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "sentinel") {
		t.Errorf("stderr misses the sentinel explanation:\n%s", stderr)
	}
	if n := b.requestCount(); n != 0 {
		t.Errorf("backend saw %d requests, want 0 (no prompt before the flag dies)", n)
	}
}

// Resume lane: a session saved under one whitelist, resumed under
// another, runs fine (exit 0) and warns exactly once on stderr.
func TestToolsFlagE2E_ResumeChangedSetWarns(t *testing.T) {
	workDir := t.TempDir()

	b1 := newE2EBackend(t)
	if _, _, code := runSeekE2E(t, b1.srv.URL, workDir, "-tools", "read,edit"); code != 0 {
		t.Fatalf("seed run exit = %d, want 0", code)
	}
	// Seed was --no-save; re-run WITHOUT it so a session lands in the
	// store, still under the read,edit whitelist.
	b2 := newE2EBackend(t)
	stdout, stderr, code := runSeekSaveE2E(t, b2.srv.URL, workDir, "-tools", "read,edit")
	if code != 0 {
		t.Fatalf("save run exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	b3 := newE2EBackend(t)
	stdout, stderr, code = runSeekSaveE2E(t, b3.srv.URL, workDir, "--continue", "-tools", "read,bash")
	if code != 0 {
		t.Fatalf("resume run exit = %d, want 0 (照常运行)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if n := strings.Count(stderr, "工具集变了，缓存会失效一次"); n != 1 {
		t.Errorf("resume stderr has %d warning(s), want exactly 1:\n%s", n, stderr)
	}
}

// runSeekSaveE2E is runSeekE2E WITHOUT --no-save: the run persists a
// session under SEEK_HOME so a follow-up --continue can pick it up.
func runSeekSaveE2E(t *testing.T, backendURL, workDir string, extra ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildSeekBin(t)
	args := []string{
		"-p", "hi",
		"--provider", "compatible",
		"--base-url", backendURL + "/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
		"--no-cron-tick",
		"--no-project-md",
	}
	args = append(args, extra...)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"SEEK_HOME="+workDir,
		"HOME="+workDir,
		"OPENAI_API_KEY=e2e-key",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
	)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run seek (save): %v\nstderr:\n%s", err, errBuf.String())
	}
	return outBuf.String(), errBuf.String(), code
}

// Help is the discovery surface for the sentinel: a published contract
// with no mention in `-h` is a contract nobody finds. Asserted against
// the built binary so the text that ships is the text checked.
func TestToolsFlagHelp_DocumentsNoneSentinel(t *testing.T) {
	bin := buildSeekBin(t)
	cmd := exec.Command(bin, "-h")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	// flag.ExitOnError sends -h's text to stderr and the usage printer
	// may take either stream; accept both.
	if err := cmd.Run(); err != nil {
		t.Fatalf("seek -h: %v\n%s", err, errBuf.String())
	}
	text := out.String() + errBuf.String()
	for _, want := range []string{
		`"none" = zero tools`,
		"Empty or absent = full default registry",
		"must be the only value",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help is missing %q:\n%s", want, text)
		}
	}
}
