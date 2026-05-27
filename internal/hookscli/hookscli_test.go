package hookscli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/paths"
)

// withSEEKHome points SEEK_HOME at a tempdir so the CLI's paths
// helpers resolve away from the real ~/.seek/.
func withSEEKHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, had := os.LookupEnv("SEEK_HOME")
	os.Setenv("SEEK_HOME", dir)
	t.Cleanup(func() {
		if had {
			os.Setenv("SEEK_HOME", prev)
		} else {
			os.Unsetenv("SEEK_HOME")
		}
	})
	return dir
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("help output missing usage; got %q", stdout.String())
	}
}

func TestRun_UnknownVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run([]string{"banana"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for unknown verb")
	}
	if !strings.Contains(stderr.String(), "unknown verb") {
		t.Errorf("expected 'unknown verb' on stderr; got %q", stderr.String())
	}
}

func TestRun_ListNoHooks(t *testing.T) {
	withSEEKHome(t)
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"list"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "no hooks") {
		t.Errorf("expected 'no hooks'; got %q", stdout.String())
	}
}

func TestRun_ListUserHooks(t *testing.T) {
	withSEEKHome(t)
	userPath, _ := paths.UserHooksToml()
	writeFile(t, userPath, `[[pre_tool]]
name = "u-audit"
command = "true"
match = { tool = "*" }
`)
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"list"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "u-audit") {
		t.Errorf("missing hook name in list output:\n%s", out)
	}
	if !strings.Contains(out, "user") {
		t.Errorf("missing source column 'user':\n%s", out)
	}
}

func TestRun_CheckEventTool(t *testing.T) {
	withSEEKHome(t)
	userPath, _ := paths.UserHooksToml()
	writeFile(t, userPath, `[[pre_tool]]
name = "edit-lint"
command = "true"
match = { tool = "edit" }

[[pre_tool]]
name = "all-audit"
command = "true"
match = { tool = "*" }
`)
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"check", "--event", "pre_tool", "--tool", "bash"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "all-audit") {
		t.Errorf("all-audit should match tool=bash:\n%s", out)
	}
	if strings.Contains(out, "edit-lint") {
		t.Errorf("edit-only hook should not match bash:\n%s", out)
	}
}

func TestRun_CheckDoesNotExecute(t *testing.T) {
	// PRD acceptance #7: --event/--tool dry-run must NOT execute the
	// hook command. We point command at a script that touches a file;
	// after `check` the file must NOT exist.
	withSEEKHome(t)
	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, "fired")
	userPath, _ := paths.UserHooksToml()
	writeFile(t, userPath, `[[pre_tool]]
name = "would-fire"
command = "touch `+sentinel+`"
match = { tool = "*" }
`)
	var stdout, stderr bytes.Buffer
	_ = Run([]string{"check", "--event", "pre_tool", "--tool", "bash"}, &stdout, &stderr)
	if _, err := os.Stat(sentinel); err == nil {
		t.Errorf("check executed the hook (sentinel created)")
	}
}

func TestRun_TrustList_EmptyByDefault(t *testing.T) {
	withSEEKHome(t)
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"trust"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "no trusted projects") {
		t.Errorf("empty store output: %q", stdout.String())
	}
}

func TestRun_TrustResetAll(t *testing.T) {
	withSEEKHome(t)
	// Pre-seed: write the trust file manually.
	trustPath, _ := paths.TrustedProjectsJSON()
	writeFile(t, trustPath, `{"entries":[{"project_path":"/x","sha256":"abc","approved_at":"2026"}]}`)

	var stdout, stderr bytes.Buffer
	if err := Run([]string{"trust", "--reset", "all"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "cleared") {
		t.Errorf("reset output: %q", stdout.String())
	}

	// Now list — should be empty.
	stdout.Reset()
	_ = Run([]string{"trust"}, &stdout, &stderr)
	if !strings.Contains(stdout.String(), "no trusted projects") {
		t.Errorf("after reset, expected empty list: %q", stdout.String())
	}
}

func TestRun_AuditEmpty(t *testing.T) {
	withSEEKHome(t)
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"audit"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "no audit entries") {
		t.Errorf("expected 'no audit entries': %q", stdout.String())
	}
}

func TestRun_AuditFilterDenied(t *testing.T) {
	withSEEKHome(t)
	auditPath, _ := paths.HooksAuditLog()
	writeFile(t, auditPath, `{"ts":"2026-05-27T00:00:00Z","event":"pre_tool","hook":"a","tool":"bash","exit_code":1,"denied":true}
{"ts":"2026-05-27T00:00:01Z","event":"pre_tool","hook":"b","tool":"bash","exit_code":0,"denied":false}
`)
	var stdout, stderr bytes.Buffer
	if err := Run([]string{"audit", "--denied"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "DENIED") {
		t.Errorf("expected DENIED entry: %s", out)
	}
	if strings.Contains(out, " b ") {
		t.Errorf("non-denied entry leaked: %s", out)
	}
}
