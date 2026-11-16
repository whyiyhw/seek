package subagent

// LastAssistant tests (W3.3): the summary-单源 tail read over
// hand-made transcript.jsonl files — happy path, missing file,
// header-only, empty-bodied assistants, torn tail, malformed lines.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/paths"
)

// writeTranscript writes lines into <project sessions>/<parentSid>/
// subagents/<subSid>/transcript.jsonl, creating parents as needed
// (the Manager's begin step 5 normally owns the mkdir).
func writeTranscript(t *testing.T, project, parentSid, subSid string, lines ...string) {
	t.Helper()
	dir, err := paths.SubagentSessionDir(project, parentSid, subSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func TestLastAssistant(t *testing.T) {
	const header = `{"schema_version":2,"id":"h","created_at":"2026-01-01T00:00:00Z"}`
	cases := []struct {
		name  string
		lines []string // nil = no file at all
		want  string
	}{
		{
			name: "last of several assistants wins",
			lines: []string{header,
				`{"role":"user","content":"audit deps"}`,
				`{"role":"assistant","content":"scanning…"}`,
				`{"role":"tool","tool_call_id":"t1","content":"file list"}`,
				`{"role":"assistant","content":"final findings: 3 deps stale"}`,
			},
			want: "final findings: 3 deps stale",
		},
		{
			name:  "missing transcript is empty-not-error (active child)",
			lines: nil,
			want:  "",
		},
		{
			name:  "header only",
			lines: []string{header},
			want:  "",
		},
		{
			name: "empty-bodied assistants skipped",
			lines: []string{header,
				`{"role":"user","content":"p"}`,
				`{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"read"}}]}`,
				`{"role":"assistant","content":"real answer"}`,
				`{"role":"assistant","content":""}`,
			},
			want: "real answer",
		},
		{
			name: "torn tail skipped, complete lines before it read",
			lines: []string{header,
				`{"role":"assistant","content":"done text"}`,
				`{"role":"assistant","content":"partial writ`, // writer mid-append
			},
			want: "done text",
		},
		{
			name: "malformed line skipped",
			lines: []string{header,
				`not json at all`,
				`{"role":"assistant","content":"after corruption"}`,
			},
			want: "after corruption",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHome(t)
			project := t.TempDir()
			if tc.lines != nil {
				writeTranscript(t, project, "parent-1", "sub-1", tc.lines...)
			}
			got, err := LastAssistant(project, "parent-1", "sub-1")
			if err != nil {
				t.Fatalf("LastAssistant: %v", err)
			}
			if got != tc.want {
				t.Errorf("LastAssistant = %q, want %q", got, tc.want)
			}
		})
	}
}
