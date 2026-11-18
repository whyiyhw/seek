package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Tests for the ExpandFiles submit hook (feature-deepseek-app-api F3):
// document parts ride the prompt through the optional filePrompter
// sibling interface, and a fake agent WITHOUT that interface keeps
// working (fallback branch — files just don't ride, which only ever
// happens to test fakes in production).

// docAgent is a fakeAgent that also implements filePrompter.
type docAgent struct {
	*fakeAgent
	gotFiles []([]deepseek.FilePart)
	gotText  []string
}

func (a *docAgent) PromptFiles(ctx context.Context, text string, images []deepseek.ImagePart, files []deepseek.FilePart) <-chan agent.Event {
	a.mu.Lock()
	a.gotFiles = append(a.gotFiles, files)
	a.gotText = append(a.gotText, text)
	a.mu.Unlock()
	return a.events
}

func TestSubmit_ExpandFiles_RidesPrompt(t *testing.T) {
	t.Parallel()

	fa := &docAgent{fakeAgent: newFakeAgent()}
	m := testModel().WithAgent(fa).WithCustomState(func(m *Model) {
		m.opts.Ctx = context.Background()
	}).Build()
	m.opts.ExpandFiles = func(ctx context.Context, model, text string) (string, []deepseek.FilePart) {
		return text + "\n\n[file: report.pdf — uploaded · text · 12 pages · file-api-x]",
			[]deepseek.FilePart{{Asset: "ab12cd34ef56.pdf", FileID: "file-api-x"}}
	}

	_, cmd := m.submit("read @report.pdf")
	if cmd == nil {
		t.Fatal("submit must return a non-nil cmd")
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.gotFiles) != 1 {
		t.Fatalf("PromptFiles calls = %d, want 1", len(fa.gotFiles))
	}
	if len(fa.gotFiles[0]) != 1 || fa.gotFiles[0][0].FileID != "file-api-x" {
		t.Fatalf("files = %+v", fa.gotFiles[0])
	}
	if !strings.Contains(fa.gotText[0], "[file: report.pdf — uploaded") {
		t.Fatalf("routed text missing marker: %q", fa.gotText[0])
	}
	if len(fa.PromptCalls) != 0 {
		t.Fatalf("plain Prompt must not fire when files ride: %v", fa.PromptCalls)
	}
}

// TestSubmit_ExpandFiles_FakeWithoutPromptFiles: the optional-interface
// fallback — an AgentClient without PromptFiles (every test fake predating
// F3) still receives the routed text via plain Prompt. No panic, no drop
// of the submit itself.
func TestSubmit_ExpandFiles_FakeWithoutPromptFiles(t *testing.T) {
	t.Parallel()

	fa := newFakeAgent()
	m := testModel().WithAgent(fa).WithCustomState(func(m *Model) {
		m.opts.Ctx = context.Background()
	}).Build()
	m.opts.ExpandFiles = func(ctx context.Context, model, text string) (string, []deepseek.FilePart) {
		return text + "\n\n[file: report.pdf — uploaded · text · 12 pages · file-api-x]",
			[]deepseek.FilePart{{Asset: "ab12cd34ef56.pdf", FileID: "file-api-x"}}
	}

	_, cmd := m.submit("read @report.pdf") // must not panic
	if cmd == nil {
		t.Fatal("submit must return a non-nil cmd")
	}
	if got := fa.PromptCalls; len(got) != 1 || !strings.Contains(got[0], "[file: report.pdf — uploaded") {
		t.Fatalf("fallback Prompt = %v", got)
	}
}

// TestSubmit_ExpandFiles_NilHookIsIdentity: no hook → plain Prompt, the
// pre-F3 path unchanged.
func TestSubmit_ExpandFiles_NilHookIsIdentity(t *testing.T) {
	t.Parallel()

	fa := newFakeAgent()
	m := testModel().WithAgent(fa).WithCustomState(func(m *Model) {
		m.opts.Ctx = context.Background()
	}).Build()

	_, cmd := m.submit("plain text")
	if cmd == nil {
		t.Fatal("submit must return a non-nil cmd")
	}
	if got := fa.PromptCalls; len(got) != 1 || got[0] != "plain text" {
		t.Fatalf("Prompt = %v", got)
	}
}
