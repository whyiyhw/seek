package main

// Real-binary acceptance for c_skczdu (the autopilot lane's permission
// posture + the two arg shapes), on a fake OpenAI-compatible backend:
// no keys, no network, temp HOME/SEEK_HOME, a throwaway git repo.
//
// Why the REAL binary and not just the unit table: the bug this card
// fixes lived in the WIRING between flag.Parse, initialPref, and the
// subagent manager. autopilot_lane_test.go pins the pure functions
// (autopilotInvocation, initialPreference, Policy.Spawn); this file
// pins that main actually reaches them — a table passing while
// `autopilotMode := autopilotLane` were unwired would still ship the
// original bug.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// Role markers, taken from the prompts that actually ship
	// (internal/autopilot/decompose.go / internal/verifier/prompt.go).
	decomposeRoleMarker = "planner for an autonomous"
	judgeRoleMarker     = "independent verifier"
)

// autopilotBackend scripts the three roles autopilot asks of one
// chat surface: decompose, worker, judge. A worker with an empty
// command answers "DONE" straight away; otherwise its FIRST turn (no
// tool result in the conversation yet) issues one bash call, and the
// turn after the tool result ends the task.
type autopilotBackend struct {
	srv           *httptest.Server
	workerCommand string

	mu     sync.Mutex
	bodies [][]byte
}

func newAutopilotBackend(t *testing.T, workerCommand string) *autopilotBackend {
	t.Helper()
	b := &autopilotBackend{workerCommand: workerCommand}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	t.Cleanup(b.srv.Close)
	return b
}

// reqEnvelope is the slice of the request body the script needs.
type reqEnvelope struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func (b *autopilotBackend) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.bodies = append(b.bodies, body)
	b.mu.Unlock()

	var req reqEnvelope
	_ = json.Unmarshal(body, &req)
	var sys strings.Builder
	sawToolResult := false
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			sys.WriteString(m.Content)
		case "tool":
			sawToolResult = true
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	switch {
	case strings.Contains(sys.String(), decomposeRoleMarker):
		writeSSE(w, contentChunk(`[{"title":"smoke","prompt":"prove the lane","done_when":["worker replied"]}]`))
	case strings.Contains(sys.String(), judgeRoleMarker):
		writeSSE(w, contentChunk(`{"assertions":[{"index":1,"verdict":"pass","evidence":["T1"],"note":"fake"}],"overall_note":"fake"}`))
	case !sawToolResult && b.workerCommand != "":
		args, _ := json.Marshal(map[string]string{"command": b.workerCommand})
		tc, _ := json.Marshal(map[string]any{
			"index": 0, "id": "c1", "type": "function",
			"function": map[string]string{"name": "bash", "arguments": string(args)},
		})
		writeSSERaw(w,
			`{"choices":[{"delta":{"tool_calls":[`+string(tc)+`]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`)
	default:
		writeSSE(w, contentChunk("DONE"))
	}
}

func contentChunk(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"delta":         map[string]any{"content": content},
			"finish_reason": nil,
		}},
	})
	return string(b)
}

func writeSSE(w io.Writer, chunks ...string) {
	writeSSERaw(w, append(chunks,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`)...)
}

func writeSSERaw(w io.Writer, chunks ...string) {
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	io.WriteString(w, "data: [DONE]\n\n")
}

func (b *autopilotBackend) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.bodies)
}

// toolResults returns every role="tool" message content the backend saw
// — i.e. what the workers' tools actually answered. That is the only
// place a denied bash surfaces on this lane (the report prints
// outcomes, not tool transcripts).
func (b *autopilotBackend) toolResults() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, body := range b.bodies {
		var req reqEnvelope
		if err := json.Unmarshal(body, &req); err != nil {
			continue
		}
		for _, m := range req.Messages {
			if m.Role == "tool" {
				out = append(out, m.Content)
			}
		}
	}
	return out
}

// runAutopilotE2E runs the built binary on the autopilot lane inside
// workDir under a SCRATCH env (not os.Environ: a DEEPSEEK_API_KEY lying
// in the test shell must not sneak into an unattended-lane test).
func runAutopilotE2E(t *testing.T, backendURL, home, workDir string, extra ...string) (stdout, stderr string, code int) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH: the autopilot lane needs real worktrees")
	}
	bin := buildSeekBin(t)
	args := []string{
		"--provider", "compatible",
		"--base-url", backendURL + "/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
		"--no-cron-tick",
		"--no-project-md",
	}
	args = append(args, extra...)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"SEEK_HOME=" + home,
		"OPENAI_API_KEY=e2e-key",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
		"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@seek.test",
		"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@seek.test",
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code = 0
	var exitErr *exec.ExitError
	if err != nil {
		var ok bool
		if exitErr, ok = err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("run autopilot lane: %v\nstderr:\n%s", err, errBuf.String())
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// autopilotGitRepo creates the one-commit repo the lane needs (worktrees
// are created from HEAD).
func autopilotGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@seek.test",
			"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@seek.test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "e2e")
	run("config", "user.email", "e2e@seek.test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return dir
}

// Acceptance ① at the binary level: the lane elevates pref by ITSELF —
// no `--yolo` on the command line — so a scheduled/cron invocation gets
// working workers, and the worker's bash really executes inside its
// worktree (the marker file is the proof; a PrefDeny gate would answer
// `bash is gated` and leave no file).
func TestAutopilotLaneE2E_WorkerBashRunsWithoutYoloFlag(t *testing.T) {
	home := t.TempDir()
	repo := autopilotGitRepo(t)
	b := newAutopilotBackend(t, "echo AUTOPILOT_LANE_BASH_OK > marker.txt")

	stdout, stderr, code := runAutopilotE2E(t, b.srv.URL, home, repo, "autopilot", "run", "prove the lane")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `autopilot "prove the lane"`) {
		t.Fatalf("report line missing from stdout:\n%s", stdout)
	}
	for _, tr := range b.toolResults() {
		if strings.Contains(tr, "bash is gated") {
			t.Fatalf("worker bash was gated on the autopilot lane — pref did not elevate:\n%s", tr)
		}
	}
	matches, err := filepath.Glob(filepath.Join(home, "projects", "*", "worktrees", "*", "marker.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("no marker.txt under any autopilot worktree — the worker's bash did not run\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "AUTOPILOT_LANE_BASH_OK") {
		t.Fatalf("marker content = %q", body)
	}
}

// Acceptance ② at the binary level: elevating the pref must NOT loosen
// the no-remote guard. The worker asks for the one operation the lane
// may never perform; the tool result the model sees must carry the
// autopilot-lane refusal.
func TestAutopilotLaneE2E_NoRemoteGuardStillRefuses(t *testing.T) {
	home := t.TempDir()
	repo := autopilotGitRepo(t)
	// Assembled here rather than typed as one literal so the string
	// scan a reader does is the same one the guard does — no
	// obfuscation, just a readable split.
	remoteOp := "git " + "push" + " origin main"
	b := newAutopilotBackend(t, remoteOp)

	_, stderr, code := runAutopilotE2E(t, b.srv.URL, home, repo, "autopilot", "run", "prove the guard")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a refused tool call is a tool result, not a crash)\nstderr:\n%s", code, stderr)
	}
	var sawRefusal bool
	for _, tr := range b.toolResults() {
		if strings.Contains(tr, "no-remote guard on the autopilot lane") {
			sawRefusal = true
		}
	}
	if !sawRefusal {
		t.Fatalf("worker never saw the no-remote refusal — guard not mounted on the elevated lane\ntool results: %v", b.toolResults())
	}
}

// Acceptance ③ at the binary level: both shapes the card measured.
// The supported one runs the lane; the unsupported one fails loudly
// BEFORE any provider work — never folding the flag into the goal.
func TestAutopilotFlagShapesE2E(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantCode    int
		wantStderr  string // substring; "" = don't care
		wantStdout  string // substring
		wantNoReqs  bool   // the backend must see ZERO requests
		wantLaneRun bool   // a real lane run (needs a repo)
	}{
		{
			name:        "flag before the subcommand works",
			args:        []string{"--yolo", "autopilot", "run", "flag shape"},
			wantCode:    0,
			wantStdout:  `autopilot "flag shape"`,
			wantLaneRun: true,
		},
		{
			name:       "flag after the subcommand dies with guidance",
			args:       []string{"autopilot", "run", "flag shape", "--yolo"},
			wantCode:   1,
			wantStderr: "put it BEFORE the subcommand",
			wantNoReqs: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			b := newAutopilotBackend(t, "")
			repo := home // any dir works: the malformed shape dies first
			if c.wantLaneRun {
				repo = autopilotGitRepo(t)
			}
			stdout, stderr, code := runAutopilotE2E(t, b.srv.URL, home, repo, c.args...)
			if code != c.wantCode {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, c.wantCode, stdout, stderr)
			}
			if c.wantStderr != "" && !strings.Contains(stderr, c.wantStderr) {
				t.Fatalf("stderr misses %q:\n%s", c.wantStderr, stderr)
			}
			if c.wantStdout != "" && !strings.Contains(stdout, c.wantStdout) {
				t.Fatalf("stdout misses %q:\n%s", c.wantStdout, stdout)
			}
			if strings.Contains(stdout+stderr, "--yolo") && c.wantLaneRun {
				t.Fatalf("the flag leaked into the run instead of being parsed:\n%s", stdout+stderr)
			}
			if n := b.requestCount(); c.wantNoReqs && n != 0 {
				t.Fatalf("backend saw %d requests, want 0 (the malformed shape must die before a provider call)", n)
			}
			if c.wantLaneRun && b.requestCount() == 0 {
				t.Fatal("backend saw no requests — the lane never reached the provider")
			}
		})
	}
}

// The cron billing gate is keyed off the RESOLVED lane (flag.Args()),
// not argv[1] — otherwise `seek --yolo autopilot run` (now a valid
// shape) would bypass the v8 §9.1 metered-key gate that c_kdblc4
// installed. Refusal must land before any request.
func TestAutopilotCronGate_E2EFiresOnFlagLedShape(t *testing.T) {
	home := t.TempDir()
	b := newAutopilotBackend(t, "")
	bin := buildSeekBin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"--yolo",
		"--provider", "compatible",
		"--base-url", b.srv.URL+"/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
		"--no-cron-tick",
		"--no-project-md",
		"autopilot", "run", "gate probe",
	)
	cmd.Dir = home
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"SEEK_HOME=" + home,
		"OPENAI_API_KEY=e2e-key",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
		"SEEK_AUTOPILOT_CRON=1",
		"SEEK_AUTOPILOT_PROVIDER=deepseek",
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		t.Fatalf("cron-fired run on a metered lane must be refused\nstdout:\n%s", outBuf.String())
	}
	if !strings.Contains(errBuf.String(), "metered") || !strings.Contains(errBuf.String(), "SEEK_AUTOPILOT_ALLOW_METERED") {
		t.Fatalf("refusal text missing from stderr:\n%s", errBuf.String())
	}
	if n := b.requestCount(); n != 0 {
		t.Fatalf("backend saw %d requests — the gate must refuse before any provider call", n)
	}
}
