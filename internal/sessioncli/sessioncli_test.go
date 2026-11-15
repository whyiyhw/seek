package sessioncli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// setupTempEnv redirects SEEK_HOME + SEEK_SESSIONS_DIR to a tempdir
// so the CLI operates in isolation (same pattern as checkpointcli).
func setupTempEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_HOME", dir)
	t.Setenv("SEEK_SESSIONS_DIR", filepath.Join(dir, "sessions"))
	return dir
}

// saveFakeTranscript writes a multi-turn session (text + tool call
// + image) into the temp store and returns it.
func saveFakeTranscript(t *testing.T, id string) *session.Session {
	t.Helper()
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 21, 10, 30, 45, 0, time.UTC)
	s := &session.Session{
		SchemaVersion: 3,
		ID:            id,
		CreatedAt:     created,
		UpdatedAt:     created,
		Model:         "deepseek-flash",
		CWD:           "/tmp/proj",
		SystemPrompt:  "You are seek.",
		Turns:         1,
		ToolCalls:     1,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "hello there",
				Images: []deepseek.ImagePart{{Asset: "shot.png"}}},
			{Role: deepseek.RoleAssistant, Content: "checking", ToolCalls: []deepseek.ToolCall{
				{ID: "c1", Type: "function", Function: deepseek.ToolCallFunc{
					Name: "bash", Arguments: `{"command":"ls"}`}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "c1", Content: "file1.go\nfile2.go"},
			{Role: deepseek.RoleAssistant, Content: "two files."},
		},
	}
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func sessionPath(t *testing.T, id string) string {
	t.Helper()
	return filepath.Join(os.Getenv("SEEK_SESSIONS_DIR"), id+".jsonl")
}

// TestExport_Stdout_DoesNotModifySessionFile is the card's second
// acceptance criterion: exporting must leave the source .jsonl
// byte- and mtime-identical.
func TestExport_Stdout_DoesNotModifySessionFile(t *testing.T) {
	setupTempEnv(t)
	s := saveFakeTranscript(t, "20260121-103045-aaa111")

	before, err := os.ReadFile(sessionPath(t, s.ID))
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(sessionPath(t, s.ID))
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := Run([]string{"export", s.ID}, &out, &errOut); err != nil {
		t.Fatalf("export: %v (stderr=%s)", err, errOut.String())
	}

	for _, want := range []string{
		"# seek session " + s.ID,
		"- **Created**: 2026-01-21T10:30:45Z",
		"## User",
		"hello there",
		"*[image: shot.png]*",
		"- **bash** · `command=ls`",
		"> file1.go",
		"two files.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout export missing %q", want)
		}
	}

	after, err := os.ReadFile(sessionPath(t, s.ID))
	if err != nil {
		t.Fatal(err)
	}
	afterStat, err := os.Stat(sessionPath(t, s.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("export modified the session file bytes")
	}
	if !afterStat.ModTime().Equal(beforeStat.ModTime()) {
		t.Errorf("export touched the session file mtime: before=%v after=%v",
			beforeStat.ModTime(), afterStat.ModTime())
	}
}

func TestExport_ToFile(t *testing.T) {
	setupTempEnv(t)
	s := saveFakeTranscript(t, "20260121-103045-bbb222")

	outPath := filepath.Join(t.TempDir(), "sub", "transcript.md")
	var out, errOut bytes.Buffer
	if err := Run([]string{"export", s.ID, "-o", outPath}, &out, &errOut); err != nil {
		t.Fatalf("export -o: %v (stderr=%s)", err, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("-o export must not also print to stdout, got %d bytes", out.Len())
	}
	if !strings.Contains(errOut.String(), "exported session "+s.ID) {
		t.Errorf("expected -o note on stderr, got %q", errOut.String())
	}
	written, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "## Assistant") {
		t.Errorf("exported file missing conversation, got:\n%s", written)
	}
}

func TestExport_DefaultLatestSession(t *testing.T) {
	setupTempEnv(t)
	first := saveFakeTranscript(t, "20260121-103045-ccc333")
	// Second save a beat later so Latest() can order them.
	time.Sleep(10 * time.Millisecond)
	second := saveFakeTranscript(t, "20260121-103059-ddd444")

	var out, errOut bytes.Buffer
	if err := Run([]string{"export"}, &out, &errOut); err != nil {
		t.Fatalf("export (default id): %v", err)
	}
	if !strings.Contains(out.String(), "# seek session "+second.ID) {
		t.Errorf("default export should pick the latest session %s (first was %s), got:\n%s",
			second.ID, first.ID, out.String())
	}
}

func TestExport_UnknownID(t *testing.T) {
	setupTempEnv(t)
	var out, errOut bytes.Buffer
	err := Run([]string{"export", "nope-123"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "nope-123") {
		t.Errorf("expected error naming the missing id, got %v", err)
	}
}

func TestExport_NoSessions(t *testing.T) {
	setupTempEnv(t) // empty store
	var out, errOut bytes.Buffer
	err := Run([]string{"export"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "no sessions") {
		t.Errorf("expected no-sessions error, got %v", err)
	}
}

func TestExport_UnwritableOutPath(t *testing.T) {
	setupTempEnv(t)
	s := saveFakeTranscript(t, "20260121-103045-eee555")

	// A regular FILE sitting where MkdirAll needs a directory —
	// portable permission-free failure path.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := Run([]string{"export", s.ID, "-o", filepath.Join(blocker, "sub", "out.md")}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("expected mkdir error, got %v", err)
	}
}

func TestExport_TooManyPositionalArgs(t *testing.T) {
	setupTempEnv(t)
	var out, errOut bytes.Buffer
	err := Run([]string{"export", "a", "b"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("expected usage error, got %v", err)
	}
}

func TestExport_FlagOrders(t *testing.T) {
	setupTempEnv(t)
	s := saveFakeTranscript(t, "20260121-103045-fff666")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir()) // keep the emitted files out of the package dir
	t.Cleanup(func() { os.Chdir(cwd) })

	cases := []struct {
		argv []string
		file string
	}{
		{[]string{"export", s.ID, "-o", "a.md"}, "a.md"},  // flag after positional (card's natural order)
		{[]string{"export", "-o", "b.md", s.ID}, "b.md"},  // conventional order
		{[]string{"export", s.ID, "-o=c.md"}, "c.md"},     // equals form
		{[]string{"export", "--o", "d.md", s.ID}, "d.md"}, // double-dash form
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		if err := Run(tc.argv, &out, &errOut); err != nil {
			t.Fatalf("%v: %v", tc.argv, err)
		}
		got, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("%v: %v", tc.argv, err)
		}
		if !strings.Contains(string(got), "# seek session "+s.ID) {
			t.Errorf("%v: export file %s missing session header", tc.argv, tc.file)
		}
	}
}

func TestRun_Help(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := Run([]string{"help"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "seek session") {
		t.Errorf("help output missing header: %q", out.String())
	}
}

func TestRun_NoArgsPrintsHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := Run(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "export") {
		t.Errorf("bare `seek session` should print help, got %q", out.String())
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	err := Run([]string{"bogus"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected unknown-subcommand error, got %v", err)
	}
}
