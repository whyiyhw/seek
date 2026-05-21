package fimcomplete

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func newFIMServer(t *testing.T, wantPrompt, wantSuffix, returnText string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body deepseek.FIMRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if wantPrompt != "" && body.Prompt != wantPrompt {
			t.Errorf("prompt = %q, want %q", body.Prompt, wantPrompt)
		}
		if wantSuffix != "" && body.Suffix != wantSuffix {
			t.Errorf("suffix = %q, want %q", body.Suffix, wantSuffix)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","choices":[{"index":0,"text":"`+returnText+`","finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}))
}

func TestFIMComplete_HappyPath(t *testing.T) {
	dir := t.TempDir()
	src := "func add(a, b int) int {\n  // here\n}\n"
	path := filepath.Join(dir, "f.go")
	os.WriteFile(path, []byte(src), 0o644)

	srv := newFIMServer(t,
		"func add(a, b int) int {\n  // here",
		"\n}\n",
		"\\n  return a + b",
	)
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	tool := New(c, "")

	args, _ := json.Marshal(Args{Path: path, BeforeMarker: "// here", AfterMarker: "\n}"})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "return a + b") {
		t.Errorf("missing completion: %s", out)
	}
	if !strings.Contains(out, "FIM completion") {
		t.Errorf("missing header: %s", out)
	}
}

func TestFIMComplete_BeforeMarkerNotUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("foo foo"), 0o644)

	tool := New(deepseek.New(deepseek.WithAPIKey("t")), "")
	args, _ := json.Marshal(Args{Path: path, BeforeMarker: "foo"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Errorf("err = %v", err)
	}
}

func TestFIMComplete_NoAfterMarker_EOFSuffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("prefix HERE"), 0o644)

	srv := newFIMServer(t, "prefix HERE", "", "tail")
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	args, _ := json.Marshal(Args{Path: path, BeforeMarker: "HERE"})
	out, err := New(c, "").Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "tail") {
		t.Errorf("missing tail: %s", out)
	}
}

func TestFIMComplete_AfterMarkerMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("only here"), 0o644)

	tool := New(deepseek.New(deepseek.WithAPIKey("t")), "")
	args, _ := json.Marshal(Args{Path: path, BeforeMarker: "only", AfterMarker: "zzz"})
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "after_marker not found") {
		t.Errorf("err = %v", err)
	}
}

func TestFIMComplete_MissingArgs(t *testing.T) {
	tool := New(deepseek.New(deepseek.WithAPIKey("t")), "")
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("err = %v", err)
	}
}
