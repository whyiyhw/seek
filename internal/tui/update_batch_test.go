package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/askuser"
)

// armBatch attaches a multi-question batch to a fresh Model — the
// v2 counterpart of armQuestion. Returns the model + the reply
// channel the test reads to inspect the eventual []Answer.
func armBatch(t *testing.T, qs []askuser.Question) (*Model, <-chan []askuser.Answer) {
	t.Helper()
	m := emptyModel()
	reply := make(chan []askuser.Answer, 1)
	m.pendingBatch = &askuser.BatchRequest{
		Batch: askuser.Batch{Questions: qs},
		Reply: reply,
	}
	m.pendingBatchIdx = 0
	m.pendingBatchAnswers = make([]askuser.Answer, 0, len(qs))
	m.pendingQuestionSelected = map[int]bool{}
	m.pendingQuestionCursor = 0
	return m, reply
}

func twoQuestionBatch() []askuser.Question {
	return []askuser.Question{
		{
			Question: "Pick framework",
			Header:   "Framework",
			Options: []askuser.Option{
				{ID: "react", Label: "React"},
				{ID: "vue", Label: "Vue"},
			},
		},
		{
			Question: "Pick styling",
			Header:   "Styling",
			Options: []askuser.Option{
				{ID: "tw", Label: "Tailwind"},
				{ID: "css", Label: "CSS Modules"},
			},
		},
	}
}

// TestHandleBatchKey_AdvancesThroughQuestions is the happy-path
// pin: pick Q1 → state advances to Q2 → pick Q2 → reply fires
// with both answers in order. The shared per-question state
// (cursor/selected) is reset between questions.
func TestHandleBatchKey_AdvancesThroughQuestions(t *testing.T) {
	m, reply := armBatch(t, twoQuestionBatch())

	// Q1: cursor on 'react' (default), Enter accepts.
	updated, _ := m.handleBatchKey(keyEnter())
	m = ptrModel(updated)

	if m.pendingBatch == nil {
		t.Fatal("batch should still be active after Q1 — Q2 pending")
	}
	if m.pendingBatchIdx != 1 {
		t.Errorf("pendingBatchIdx = %d, want 1 after answering Q1", m.pendingBatchIdx)
	}
	if len(m.pendingBatchAnswers) != 1 {
		t.Fatalf("pendingBatchAnswers len = %d, want 1", len(m.pendingBatchAnswers))
	}
	if m.pendingBatchAnswers[0].ChosenIDs[0] != "react" {
		t.Errorf("Q1 answer = %v, want react", m.pendingBatchAnswers[0].ChosenIDs)
	}
	if m.pendingQuestionCursor != 0 {
		t.Errorf("cursor should reset to 0 between questions; got %d", m.pendingQuestionCursor)
	}

	// Q2: cursor down to 'css', Enter accepts → batch completes.
	updated, _ = m.handleBatchKey(keyDown())
	m = ptrModel(updated)
	updated, _ = m.handleBatchKey(keyEnter())
	m = ptrModel(updated)

	if m.pendingBatch != nil {
		t.Errorf("pendingBatch should clear after final question answered")
	}
	select {
	case ans := <-reply:
		if len(ans) != 2 {
			t.Fatalf("reply answers len = %d, want 2", len(ans))
		}
		if ans[0].ChosenIDs[0] != "react" {
			t.Errorf("ans[0] = %v, want react", ans[0].ChosenIDs)
		}
		if ans[1].ChosenIDs[0] != "css" {
			t.Errorf("ans[1] = %v, want css", ans[1].ChosenIDs)
		}
	default:
		t.Fatal("batch reply never fired")
	}
}

// TestHandleKey_BatchSingleSelect_NonFirstOption is the regression pin
// for the "only the first option works" bug. In a batch picker,
// choosing any option that requires navigating off row 0 must register.
//
// Crucially it drives keys through the REAL router (handleKey), NOT
// handleBatchKey directly — the bug lived in the router's
// pendingBatch-vs-pendingQuestion precedence plus a transient
// pendingQuestion that leaked between keys. The older tests call
// handleBatchKey directly, which rebuilds the faux request every call
// and never exercises the route, so they stayed green while the real
// app dropped the answer. Pressing ↓ then Enter must land on 'vue', not
// vanish into an orphaned channel.
func TestHandleKey_BatchSingleSelect_NonFirstOption(t *testing.T) {
	m, reply := armBatch(t, twoQuestionBatch())

	// Q1: navigate down to 'vue' (index 1), then Enter.
	updated, _ := m.handleKey(keyDown())
	m = ptrModel(updated)
	updated, _ = m.handleKey(keyEnter())
	m = ptrModel(updated)

	if m.pendingBatchIdx != 1 {
		t.Fatalf("after ↓+Enter on Q1: idx = %d, want 1 (answer was dropped — the bug)", m.pendingBatchIdx)
	}
	if len(m.pendingBatchAnswers) != 1 || m.pendingBatchAnswers[0].ChosenIDs[0] != "vue" {
		t.Fatalf("Q1 answer = %+v, want [vue]", m.pendingBatchAnswers)
	}

	// Q2: navigate down to 'css' (index 1), then Enter → batch completes.
	updated, _ = m.handleKey(keyDown())
	m = ptrModel(updated)
	updated, _ = m.handleKey(keyEnter())
	m = ptrModel(updated)

	if m.pendingBatch != nil {
		t.Error("batch should complete after both questions answered")
	}
	select {
	case ans := <-reply:
		if len(ans) != 2 || ans[0].ChosenIDs[0] != "vue" || ans[1].ChosenIDs[0] != "css" {
			t.Fatalf("reply = %+v, want [vue css]", ans)
		}
	default:
		t.Fatal("batch reply never fired")
	}
}

// TestHandleBatchKey_EscMidBatch_PreservesPriorAnswers covers the
// load-bearing PRD §3.3 cancel contract: Esc on Q2 keeps Q1's
// real answer and pads Q2..N as Cancelled, then fires the
// reply (doesn't keep prompting for already-given-up
// questions).
func TestHandleBatchKey_EscMidBatch_PreservesPriorAnswers(t *testing.T) {
	qs := []askuser.Question{
		twoQuestionBatch()[0],
		twoQuestionBatch()[1],
		{
			Question: "Pick auth",
			Header:   "Auth",
			Options: []askuser.Option{
				{ID: "jwt", Label: "JWT"},
				{ID: "session", Label: "Session"},
			},
		},
	}
	m, reply := armBatch(t, qs)

	// Q1: accept default (react).
	updated, _ := m.handleBatchKey(keyEnter())
	m = ptrModel(updated)
	if m.pendingBatchIdx != 1 {
		t.Fatalf("after Q1: idx = %d, want 1", m.pendingBatchIdx)
	}

	// Q2: press Esc — should preserve Q1 and pad Q2, Q3 as Cancelled.
	updated, _ = m.handleBatchKey(keyEsc())
	m = ptrModel(updated)

	if m.pendingBatch != nil {
		t.Error("Esc mid-batch should complete the batch immediately")
	}
	select {
	case ans := <-reply:
		if len(ans) != 3 {
			t.Fatalf("answers len = %d, want 3", len(ans))
		}
		if ans[0].Cancelled || ans[0].ChosenIDs[0] != "react" {
			t.Errorf("Q1 answer should be preserved; got %+v", ans[0])
		}
		if !ans[1].Cancelled {
			t.Errorf("Q2 (where user pressed Esc) must be Cancelled; got %+v", ans[1])
		}
		if !ans[2].Cancelled {
			t.Errorf("Q3 (never reached) must be Cancelled; got %+v", ans[2])
		}
	default:
		t.Fatal("reply never fired after mid-batch Esc")
	}
}

// TestHandleBatchKey_SingleQuestionBatchDegrades verifies that a
// 1-element batch behaves identically to a v1 single picker —
// Enter on the cursor accepts and the reply fires with one
// answer.
func TestHandleBatchKey_SingleQuestionBatchDegrades(t *testing.T) {
	qs := []askuser.Question{twoQuestionBatch()[0]}
	m, reply := armBatch(t, qs)

	updated, _ := m.handleBatchKey(keyEnter())
	m = ptrModel(updated)

	if m.pendingBatch != nil {
		t.Error("single-question batch should complete on first Enter")
	}
	select {
	case ans := <-reply:
		if len(ans) != 1 {
			t.Fatalf("answers len = %d, want 1", len(ans))
		}
		if ans[0].ChosenIDs[0] != "react" {
			t.Errorf("answer = %v, want react", ans[0].ChosenIDs)
		}
	default:
		t.Fatal("single-question batch reply never fired")
	}
}

// TestRenderBatchStack_ProgressHeader pins the "N of M" progress
// header that distinguishes multi-question batches from a single
// picker. Single-question batches must NOT show the header
// (UX-degrades to v1 look exactly).
func TestRenderBatchStack_ProgressHeader(t *testing.T) {
	// Single question — no header.
	m, _ := armBatch(t, []askuser.Question{twoQuestionBatch()[0]})
	if strings.Contains(m.renderBatchStack(), "of 1") || strings.Contains(m.renderBatchStack(), "questions") {
		t.Errorf("single-question batch should NOT show 'N of M' header; got:\n%s", m.renderBatchStack())
	}

	// Two questions — header present.
	m2, _ := armBatch(t, twoQuestionBatch())
	out := m2.renderBatchStack()
	if !strings.Contains(out, "2 question") {
		t.Errorf("multi-question batch should show '2 questions' header; got:\n%s", out)
	}
	if !strings.Contains(out, "1 of 2") {
		t.Errorf("multi-question batch should show '1 of 2' progress; got:\n%s", out)
	}
}

// TestRenderBatchStack_HeaderFieldUsedForTopic verifies the v2
// `header` field flows into the per-question chip label. Without
// it, the renderer falls back to "Question N" which is the right
// behavior for questions without headers but should be overridden
// when one is provided.
func TestRenderBatchStack_HeaderFieldUsedForTopic(t *testing.T) {
	m, _ := armBatch(t, twoQuestionBatch())
	out := m.renderBatchStack()
	// Both headers appear (active question + pending question).
	if !strings.Contains(out, "Framework") {
		t.Errorf("Q1 Header 'Framework' should appear in render; got:\n%s", out)
	}
	if !strings.Contains(out, "Styling") {
		t.Errorf("Q2 Header 'Styling' (pending) should appear in render; got:\n%s", out)
	}
}

// TestRenderBatchStack_AnsweredSummary verifies the dim summary
// line for already-answered questions shows the chosen option
// label (not raw ID), which is what users read to remember
// what they picked.
func TestRenderBatchStack_AnsweredSummary(t *testing.T) {
	m, _ := armBatch(t, twoQuestionBatch())
	// Simulate Q1 answered with "react".
	m.pendingBatchAnswers = []askuser.Answer{{ChosenIDs: []string{"react"}}}
	m.pendingBatchIdx = 1
	out := m.renderBatchStack()
	if !strings.Contains(out, "Framework: React") {
		t.Errorf("answered summary should read 'Framework: React' (label, not ID); got:\n%s", out)
	}
}

// TestSummariseAnswer_CoversAllShapes — direct unit test on the
// helper. Documents how each Answer shape renders in the stack.
func TestSummariseAnswer_CoversAllShapes(t *testing.T) {
	q := askuser.Question{Options: []askuser.Option{
		{ID: "a", Label: "Apple"},
		{ID: "b", Label: "Banana"},
	}}
	cases := []struct {
		name string
		ans  askuser.Answer
		want string
	}{
		{"single-select", askuser.Answer{ChosenIDs: []string{"a"}}, "Apple"},
		{"multi-select", askuser.Answer{ChosenIDs: []string{"a", "b"}}, "Apple, Banana"},
		{"free-text short", askuser.Answer{FreeText: "rye bread"}, `"rye bread"`},
		{"free-text long", askuser.Answer{FreeText: strings.Repeat("x", 50)}, `"` + strings.Repeat("x", 37) + `..."`},
		{"cancelled", askuser.Answer{Cancelled: true}, "(cancelled)"},
		{"empty", askuser.Answer{}, "(empty)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := summariseAnswer(q, c.ans)
			if got != c.want {
				t.Errorf("summariseAnswer = %q, want %q", got, c.want)
			}
		})
	}
}

// ptrModel is the v2 counterpart of the existing test helper
// pattern — unwrap the tea.Model interface returned by handlers.
func ptrModel(updated tea.Model) *Model {
	m := updated.(Model)
	return &m
}

// --- preview rendering tests --------------------------------------

// TestTruncatePreview_LineLimit pins the vertical bound — at most
// previewMaxLines (12) lines rendered, with "[truncated]" appended.
func TestTruncatePreview_LineLimit(t *testing.T) {
	// 15 lines — over the 12 limit.
	preview := strings.Repeat("line\n", 14) + "line"
	got := truncatePreview(preview, previewMaxLines, previewMaxCols)
	got = stripAnsi(got)
	lines := strings.Split(got, "\n")
	// 12 content lines + 1 marker line
	if len(lines) != 13 {
		t.Errorf("truncated preview line count = %d, want 13 (12 content + 1 marker)", len(lines))
	}
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("over-limit preview should include [truncated] marker; got:\n%s", got)
	}
}

// TestTruncatePreview_ColumnLimit pins the horizontal bound —
// long lines get sliced at previewMaxCols runes (not bytes).
func TestTruncatePreview_ColumnLimit(t *testing.T) {
	long := strings.Repeat("x", 120)
	got := truncatePreview(long, previewMaxLines, previewMaxCols)
	got = stripAnsi(got)
	lines := strings.Split(got, "\n")
	// First line should be exactly previewMaxCols chars.
	if firstLine := lines[0]; len([]rune(firstLine)) != previewMaxCols {
		t.Errorf("first line rune-length = %d, want %d", len([]rune(firstLine)), previewMaxCols)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("over-column preview should include [truncated]; got:\n%s", got)
	}
}

// TestTruncatePreview_WithinBoundsUnchanged: small previews pass
// through verbatim — no spurious "[truncated]" marker.
func TestTruncatePreview_WithinBoundsUnchanged(t *testing.T) {
	preview := "row1\nrow2\nrow3"
	got := truncatePreview(preview, previewMaxLines, previewMaxCols)
	if strings.Contains(stripAnsi(got), "[truncated]") {
		t.Errorf("within-bounds preview should NOT have [truncated]; got:\n%s", got)
	}
}

// TestTruncatePreview_Empty: empty input → empty output (no
// trailing newlines, no marker).
func TestTruncatePreview_Empty(t *testing.T) {
	if got := truncatePreview("", 5, 80); got != "" {
		t.Errorf("empty preview should return empty; got %q", got)
	}
}

// TestTruncatePreview_PreservesMultibyteRunes verifies CJK / box-
// drawing characters get treated as 1 rune each (not 3 bytes).
// Without rune-aware slicing, "中" would consume 3 of the 80-col
// budget and could get sliced mid-byte producing invalid UTF-8.
func TestTruncatePreview_PreservesMultibyteRunes(t *testing.T) {
	// 100 CJK chars — 100 runes, 300 bytes.
	preview := strings.Repeat("中", 100)
	got := truncatePreview(preview, 5, 80)
	got = stripAnsi(got)
	// Should truncate to 80 runes worth of CJK + [truncated].
	first := strings.Split(got, "\n")[0]
	if rc := len([]rune(first)); rc != 80 {
		t.Errorf("CJK truncation got %d runes, want 80", rc)
	}
	// Also must be valid UTF-8.
	if !isValidUTF8(first) {
		t.Errorf("truncated CJK should be valid UTF-8; got bytes: %x", []byte(first))
	}
}

// TestRenderUserQuestion_AppendsInlinePreviewOnNarrowTerminal:
// when m.width < 100, preview renders as an indented block under
// the picker rather than a side panel.
func TestRenderUserQuestion_AppendsInlinePreviewOnNarrowTerminal(t *testing.T) {
	q := askuser.Question{
		Question: "Pick style",
		Options: []askuser.Option{
			{ID: "glass", Label: "Glass", Preview: "█▓▒░"},
			{ID: "flat", Label: "Flat"},
		},
	}
	m := emptyModel()
	m.pendingQuestion = &askuser.Request{Question: q}
	m.pendingQuestionSelected = map[int]bool{}
	m.pendingQuestionCursor = 0 // on 'glass'
	m.width = 80                // narrow

	out := stripAnsi(m.renderUserQuestion())
	if !strings.Contains(out, "█▓▒░") {
		t.Errorf("preview content should render under narrow picker; got:\n%s", out)
	}
}

// TestRenderUserQuestion_NoPreviewWhenCursorOnOther: cursor on
// the auto-appended Other row must NOT trigger preview rendering
// (Other has no Preview field — free-text input is its own UX).
func TestRenderUserQuestion_NoPreviewWhenCursorOnOther(t *testing.T) {
	q := askuser.Question{
		Question: "Pick",
		Options: []askuser.Option{
			{ID: "a", Label: "A", Preview: "PREVIEW-A"},
			{ID: "b", Label: "B", Preview: "PREVIEW-B"},
		},
	}
	m := emptyModel()
	m.pendingQuestion = &askuser.Request{Question: q}
	m.pendingQuestionSelected = map[int]bool{}
	m.pendingQuestionCursor = 2 // on auto-appended Other row
	m.width = 120

	out := stripAnsi(m.renderUserQuestion())
	if strings.Contains(out, "PREVIEW-A") || strings.Contains(out, "PREVIEW-B") {
		t.Errorf("cursor on Other row should NOT render any preview; got:\n%s", out)
	}
}

// TestRenderUserQuestion_NoPreviewBlockWhenOptionHasNone: cursor
// on an option without a Preview — render the picker normally
// without an empty preview box.
func TestRenderUserQuestion_NoPreviewBlockWhenOptionHasNone(t *testing.T) {
	q := askuser.Question{
		Question: "Pick",
		Options:  []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
	}
	m := emptyModel()
	m.pendingQuestion = &askuser.Request{Question: q}
	m.pendingQuestionSelected = map[int]bool{}
	m.pendingQuestionCursor = 0 // 'a' has no Preview
	m.width = 200

	out := stripAnsi(m.renderUserQuestion())
	// Picker should render; no "border line" artifacts from an
	// empty preview box would've showed up via ─ chars in the
	// border (NormalBorder has ─ and │). Confirm picker text is
	// present and nothing else unexpected.
	if !strings.Contains(out, "Pick") {
		t.Errorf("picker header missing: %s", out)
	}
}

// stripAnsi removes the lipgloss-emitted ANSI escape sequences so
// string-matching tests aren't fooled by colour codes.
func stripAnsi(s string) string {
	var out strings.Builder
	skip := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			skip = true
		case skip && r == 'm':
			skip = false
		case skip:
			// inside escape — drop
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == 0xFFFD { // replacement char = invalid UTF-8 byte
			return false
		}
	}
	return true
}
