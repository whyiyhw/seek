package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// memory_isolated_e2e_test.go — card c_4rp2py's wire-level acceptance.
//
//   ① default startup (no --memory / no --isolated) is byte-identical
//      across startups AND identical to an explicit `--memory=rw`;
//   ② --memory=ro puts L+M in the prompt, exposes memory_recall only, and
//      leaves the memory files byte-identical; --memory=off loads nothing;
//   ③ --isolated keeps skill.Load / mcptool.LoadServers out of the run —
//      proved by the MCP stub's pid file never appearing on disk (the
//      c_xujwvq E2E precedent) and by the absence of the skill/MCP/
//      project-md/memory prompt segments.
//
// The fixtures are hermetic: SEEK_HOME == HOME == workDir, so the user
// skill tree, mcp.json, soul.md and the memory project all live inside
// t.TempDir().

// runSeekPlain is runSeekE2E WITHOUT the fixed --no-save /
// --no-project-md pair: both flags are part of what these tests vary.
func runSeekPlain(t *testing.T, backendURL, workDir string, extra ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildSeekBin(t)
	args := []string{
		"-p", "hi",
		"--provider", "compatible",
		"--base-url", backendURL + "/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
		"--no-cron-tick",
	}
	args = append(args, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"SEEK_HOME="+workDir,
		"HOME="+workDir,
		"OPENAI_API_KEY=e2e-key",
		// Same seam as startACPChild: the startup catalogue gate keys off
		// the OpenCode CREDENTIAL (c_nfeloo), so an ambient
		// OPENCODE_API_KEY in the developer's shell must not make these
		// children fetch the catalogue from opencode.ai.
		"OPENCODE_API_KEY=",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
		"SEEK_AUTO_DREAM=0",
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

// bodyMessages decodes the captured request body into its messages.
func bodyMessages(t *testing.T, body []byte) []struct {
	Role    string `json:"role"`
	Content string `json:"content"`
} {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request body: %v\n%s", err, body)
	}
	return req.Messages
}

// systemPrompt returns the system message text.
func systemPrompt(t *testing.T, body []byte) string {
	t.Helper()
	for _, m := range bodyMessages(t, body) {
		if m.Role == "system" {
			return m.Content
		}
	}
	t.Fatal("request carries no system message")
	return ""
}

// memoryContextSources lists the <context source="memory.*"> blocks the
// harness injected (the memory hook's whole wire footprint).
func memoryContextSources(t *testing.T, body []byte) []string {
	t.Helper()
	var out []string
	for _, m := range bodyMessages(t, body) {
		if m.Role != "user" {
			continue
		}
		if !strings.HasPrefix(m.Content, "<context source=") {
			continue
		}
		if strings.Contains(m.Content, "<context source=\"memory.") {
			first := m.Content[strings.Index(m.Content, "\"")+1:]
			if i := strings.Index(first, "\""); i > 0 {
				out = append(out, first[:i])
			}
		}
	}
	return out
}

func memoryToolsInBody(t *testing.T, body []byte) []string {
	t.Helper()
	var out []string
	for _, n := range toolNamesFromBody(t, body) {
		if strings.HasPrefix(n, "memory_") {
			out = append(out, n)
		}
	}
	return out
}

// seedMemoryFixture writes ~/.seek/soul.md (L) + a user skill + a stub
// MCP roster + AGENTS.md under workDir, so every machine-local prompt
// source has something to contribute. Returns the MCP stub's pid file.
func seedMemoryFixture(t *testing.T, workDir string) (pidFile string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, "soul.md"),
		[]byte("---\nschema_version: 1\nupdated_at: 2026-11-01T00:00:00Z\n---\n\n# User profile\n\n## Stable\n\n- prefer explicit error handling\n\n## Pending\n"),
		0o644); err != nil {
		t.Fatalf("write soul.md: %v", err)
	}
	skillDir := filepath.Join(workDir, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: a fixture skill\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"),
		[]byte("# Fixture instructions\n\nuse the fixture.\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	pidFile = filepath.Join(workDir, "mcp-child.pid")
	writeMCPConfig(t, workDir, stubServerRoster(pidFile, nil))
	return pidFile
}

// seedProjectMemory creates the project's M directory by running seek
// once (rw), then appends one entry to memory.jsonl. Returning the
// created directory keeps the project-id hashing out of the test (the
// child resolves cwd itself, which is why the id is discovered not
// computed).
func seedProjectMemory(t *testing.T, backendURL, workDir string) string {
	t.Helper()
	b := newE2EBackend(t)
	if _, stderr, code := runSeekPlain(t, b.srv.URL, workDir, "--no-save"); code != 0 {
		t.Fatalf("seed run exit = %d\nstderr:\n%s", code, stderr)
	}
	dirs, err := filepath.Glob(filepath.Join(workDir, "projects", "*"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("seed run did not create exactly one project dir: %v %v", dirs, err)
	}
	entry := `{"schema_version":1,"name":"fixture-decision","tagline":"JSONL for storage","content":"append-only","created_at":"2026-11-20T00:00:00Z","updated_at":"2026-11-20T00:00:00Z","last_recalled_at":"2026-11-20T00:00:00Z","recall_count":0}` + "\n"
	if err := os.WriteFile(filepath.Join(dirs[0], "memory.jsonl"), []byte(entry), 0o644); err != nil {
		t.Fatalf("write memory.jsonl: %v", err)
	}
	return dirs[0]
}

// Acceptance ①: with no memory/isolation flags the request is byte-
// identical across startups, and an explicit `--memory=rw` (the default
// spelled out) produces the same bytes. Also the historical surface is
// intact: all four memory tools plus the L/M prompt blocks.
func TestMemoryFlagE2E_DefaultIsByteIdenticalAndRW(t *testing.T) {
	workDir := t.TempDir()
	seedMemoryFixture(t, workDir)
	seedProjectMemory(t, "", workDir) // no backend needed beyond the seed run

	// The seed run above consumed its own backend; use fresh ones.
	b1 := newE2EBackend(t)
	if _, stderr, code := runSeekPlain(t, b1.srv.URL, workDir, "--no-save"); code != 0 {
		t.Fatalf("startup 1 exit = %d\nstderr:\n%s", code, stderr)
	}
	b2 := newE2EBackend(t)
	if _, stderr, code := runSeekPlain(t, b2.srv.URL, workDir, "--no-save", "--memory=rw"); code != 0 {
		t.Fatalf("startup 2 exit = %d\nstderr:\n%s", code, stderr)
	}
	body1, body2 := b1.firstBody(t), b2.firstBody(t)
	if !bytes.Equal(body1, body2) {
		t.Errorf("absent vs explicit --memory=rw differ:\n%s\n%s", body1, body2)
	}

	wantTools := []string{"memory_amend", "memory_archive", "memory_recall", "memory_remember"}
	if got := memoryToolsInBody(t, body1); !reflect.DeepEqual(got, wantTools) {
		t.Errorf("default memory tools = %v, want %v", got, wantTools)
	}
	wantSources := []string{"memory.soul", "memory.index"}
	if got := memoryContextSources(t, body1); !reflect.DeepEqual(got, wantSources) {
		t.Errorf("default memory prompt blocks = %v, want %v", got, wantSources)
	}
}

// Acceptance ② (the three-way table): off injects nothing and exposes no
// memory tool; ro injects L+M and exposes ONLY memory_recall; rw is the
// baseline. ro must also leave memory.jsonl untouched (no recall touch,
// no GC flip, no observe-stats file).
func TestMemoryFlagE2E_ModeTable(t *testing.T) {
	workDir := t.TempDir()
	seedMemoryFixture(t, workDir)
	projectDir := seedProjectMemory(t, "", workDir)
	memFile := filepath.Join(projectDir, "memory.jsonl")
	before, err := os.ReadFile(memFile)
	if err != nil {
		t.Fatalf("read memory.jsonl: %v", err)
	}

	cases := []struct {
		name      string
		modeFlag  string
		wantTools []string
		wantSoul  bool
		wantIndex bool
	}{
		{"off loads nothing", "--memory=off", nil, false, false},
		{"ro is read-only memory", "--memory=ro", []string{"memory_recall"}, true, true},
		{"rw is the baseline", "", []string{"memory_amend", "memory_archive", "memory_recall", "memory_remember"}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newE2EBackend(t)
			args := []string{"--no-save"}
			if c.modeFlag != "" {
				args = append(args, c.modeFlag)
			}
			stdout, stderr, code := runSeekPlain(t, b.srv.URL, workDir, args...)
			if code != 0 {
				t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			body := b.firstBody(t)
			if got := memoryToolsInBody(t, body); !reflect.DeepEqual(got, c.wantTools) {
				t.Errorf("memory tools = %v, want %v", got, c.wantTools)
			}
			sources := memoryContextSources(t, body)
			hasSoul, hasIndex := false, false
			for _, s := range sources {
				switch s {
				case "memory.soul":
					hasSoul = true
				case "memory.index":
					hasIndex = true
				}
			}
			if hasSoul != c.wantSoul {
				t.Errorf("soul block present = %v, want %v (sources %v)", hasSoul, c.wantSoul, sources)
			}
			if hasIndex != c.wantIndex {
				t.Errorf("index block present = %v, want %v (sources %v)", hasIndex, c.wantIndex, sources)
			}

			// ro: the on-disk memory must be untouched — a recall-count
			// touch (TouchInjections on the injected index) would rewrite
			// the JSONL even though nobody called memory_recall.
			if c.modeFlag == "--memory=ro" {
				after, rerr := os.ReadFile(memFile)
				if rerr != nil {
					t.Fatalf("re-read memory.jsonl: %v", rerr)
				}
				if !bytes.Equal(before, after) {
					t.Errorf("--memory=ro rewrote memory.jsonl:\nbefore: %s\nafter:  %s", before, after)
				}
				if _, serr := os.Stat(filepath.Join(projectDir, "observe-stats.json")); serr == nil {
					t.Errorf("--memory=ro wrote observe-stats.json")
				}
			}
		})
	}
}

// --no-save no longer disables memory (judge-engine 缺口②, semantic
// narrowed to the transcript): the L/M blocks still reach the prompt.
func TestNoSaveNoLongerDisablesMemory(t *testing.T) {
	workDir := t.TempDir()
	seedMemoryFixture(t, workDir)
	seedProjectMemory(t, "", workDir)

	b := newE2EBackend(t)
	stdout, stderr, code := runSeekPlain(t, b.srv.URL, workDir, "--no-save")
	if code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if got := memoryContextSources(t, b.firstBody(t)); len(got) == 0 {
		t.Errorf("--no-save dropped the memory prompt blocks (sources %v)", got)
	}
	// …while the transcript really is not persisted: --no-save's own job.
	if entries, err := os.ReadDir(filepath.Join(workDir, "sessions")); err == nil && len(entries) > 0 {
		t.Errorf("--no-save persisted %d session file(s)", len(entries))
	}
}

// Acceptance ③: --isolated keeps every machine-local prompt source out
// AND never forks the configured MCP server (pid file is the proof on
// disk, c_xujwvq precedent) nor reads the skill tree.
func TestIsolatedE2E_NoSkillsNoMCPNoProjectMDNoMemory(t *testing.T) {
	workDir := t.TempDir()
	pidFile := seedMemoryFixture(t, workDir)
	seedProjectMemory(t, "", workDir)

	// Control: the same fixture WITHOUT --isolated does fork the stub and
	// does surface all four sections. Without this arm the isolated
	// assertions could pass on a broken fixture.
	t.Run("control run sees everything", func(t *testing.T) {
		os.Remove(pidFile)
		b := newE2EBackend(t)
		stdout, stderr, code := runSeekPlain(t, b.srv.URL, workDir, "--no-save")
		if code != 0 {
			t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		sp := systemPrompt(t, b.firstBody(t))
		for _, want := range []string{"# Available skills", "fixture skill", "# Project instructions", "MCP tools"} {
			if !strings.Contains(sp, want) {
				t.Errorf("control prompt misses %q", want)
			}
		}
		waitForPid(t, pidFile)
	})

	t.Run("isolated run sees none of it", func(t *testing.T) {
		os.Remove(pidFile)
		b := newE2EBackend(t)
		stdout, stderr, code := runSeekPlain(t, b.srv.URL, workDir, "--no-save", "--isolated")
		if code != 0 {
			t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		sp := systemPrompt(t, b.firstBody(t))
		for _, banned := range []string{"# Available skills", "fixture skill", "# Project instructions", "fixture instructions"} {
			if strings.Contains(sp, banned) {
				t.Errorf("--isolated prompt still carries %q", banned)
			}
		}
		if got := memoryContextSources(t, b.firstBody(t)); len(got) != 0 {
			t.Errorf("--isolated prompt carries memory blocks: %v", got)
		}
		if got := memoryToolsInBody(t, b.firstBody(t)); len(got) != 0 {
			t.Errorf("--isolated registered memory tools: %v", got)
		}
		names := toolNamesFromBody(t, b.firstBody(t))
		for _, n := range names {
			if n == "Skill" {
				t.Errorf("--isolated registered the Skill tool: %v", names)
			}
			if strings.HasPrefix(n, "butler__") {
				t.Errorf("--isolated registered an MCP tool: %v", names)
			}
		}
		// Startup must be silent about both loaders.
		if strings.Contains(stderr, "mcp:") {
			t.Errorf("--isolated stderr mentions MCP:\n%s", stderr)
		}
		if strings.Contains(stderr, "Loaded ") && strings.Contains(stderr, "skills") {
			t.Errorf("--isolated stderr reports a skill load:\n%s", stderr)
		}

		// The fork is the cost: give any stray child a moment to write its
		// pid, then assert it never did.
		time.Sleep(200 * time.Millisecond)
		if _, err := os.Stat(pidFile); err == nil {
			t.Fatal("--isolated forked the configured MCP server")
		}
	})
}

// --isolated + a memory-loading mode is a usage error (exit 2, before any
// prompt), because the switch's promise is "no machine-local state".
func TestIsolatedE2E_ConflictsWithMemoryRo(t *testing.T) {
	workDir := t.TempDir()
	b := newE2EBackend(t)
	_, stderr, code := runSeekPlain(t, b.srv.URL, workDir, "--isolated", "--memory=ro")
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "--isolated") || !strings.Contains(stderr, "--memory") {
		t.Errorf("stderr should name both flags:\n%s", stderr)
	}
	if n := b.requestCount(); n != 0 {
		t.Errorf("backend saw %d requests, want 0", n)
	}
}

// --isolated also short-circuits the -tools precheck with an EMPTY MCP
// config, so an MCP-looking name dies at flag time instead of deferring
// to a load that will never happen.
func TestIsolatedE2E_MCPToolNameDiesWithoutFork(t *testing.T) {
	workDir := t.TempDir()
	pidFile := seedMemoryFixture(t, workDir)

	b := newE2EBackend(t)
	_, stderr, code := runSeekPlain(t, b.srv.URL, workDir, "--isolated", "-tools", "butler__echo")
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "unknown tool(s): butler__echo") {
		t.Errorf("stderr misses the offender:\n%s", stderr)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("the MCP server spawned despite --isolated")
	}
	if n := b.requestCount(); n != 0 {
		t.Errorf("backend saw %d requests, want 0", n)
	}
}

// A bad --memory value is a flag-usage error (exit 2) with no provider
// handshake behind it.
func TestMemoryFlagE2E_UnknownModeExits2(t *testing.T) {
	workDir := t.TempDir()
	b := newE2EBackend(t)
	_, stderr, code := runSeekPlain(t, b.srv.URL, workDir, "--memory=sometimes")
	if code != 2 {
		t.Fatalf("exit = %d, want 2\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "off|ro|rw") {
		t.Errorf("stderr should name the accepted values:\n%s", stderr)
	}
	if n := b.requestCount(); n != 0 {
		t.Errorf("backend saw %d requests, want 0", n)
	}
}

// Help is the discovery surface: both switches are published contracts.
func TestMemoryIsolatedHelp_DocumentsSwitches(t *testing.T) {
	bin := buildSeekBin(t)
	cmd := exec.Command(bin, "-h")
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("seek -h: %v\n%s", err, errBuf.String())
	}
	text := out.String() + errBuf.String()
	for _, want := range []string{
		"-memory off|ro|rw",
		"off|ro|rw",
		"-isolated",
		"no skills, no MCP servers",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("help is missing %q", want)
		}
	}
}
