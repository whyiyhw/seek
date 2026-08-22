package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/permission"
)

// buildDiff returns a unified diff with a known shape: 2 file-header
// lines, `hunks` hunk headers, `adds` added lines, `dels` deleted
// lines, and enough context lines to reach `total` raw lines.
func buildDiff(hunks, adds, dels, total int) string {
	var sb strings.Builder
	sb.WriteString("--- a/f.go\n+++ b/f.go\n")
	n := 2
	for i := range hunks {
		fmt.Fprintf(&sb, "@@ -%d,4 +%d,4 @@\n", i*10+1, i*10+1)
		n++
	}
	for i := range adds {
		fmt.Fprintf(&sb, "+added line %d\n", i)
		n++
	}
	for i := range dels {
		fmt.Fprintf(&sb, "-deleted line %d\n", i)
		n++
	}
	for ; n < total; n++ {
		fmt.Fprintf(&sb, " context line %d\n", n)
	}
	return sb.String()
}

func approvalModel(a permission.Action) (Model, chan bool) {
	reply := make(chan bool, 1)
	m := testModel().Build()
	m.pendingApproval = &permission.ApprovalRequest{Action: a, Reply: reply}
	return m, reply
}

// The footer must offer the NARROW per-Kind grant, not the retired
// session-yolo escalation — one keypress from "approve this edit" to
// "everything auto-approved" was the pre-v6 trust gap.
func TestRenderApprovalPrompt_FooterIsPerKindNotYolo(t *testing.T) {
	m, _ := approvalModel(permission.Action{Kind: permission.KindEdit, Path: "/tmp/x.go"})
	out := stripANSI(m.renderApprovalPrompt())
	if !strings.Contains(out, "[a] always: edit (this session)") {
		t.Errorf("footer missing per-Kind always label: %q", out)
	}
	if strings.Contains(out, "yolo") {
		t.Errorf("footer still mentions yolo: %q", out)
	}

	// The label names the Kind being granted — a bash prompt must say
	// bash, not edit.
	mb, _ := approvalModel(permission.Action{Kind: permission.KindBash, Command: "make deploy"})
	if out := stripANSI(mb.renderApprovalPrompt()); !strings.Contains(out, "[a] always: bash (this session)") {
		t.Errorf("footer not Kind-specific: %q", out)
	}
}

// The header carries the diff's size so approving a truncated diff is
// an informed act, not a blind one.
func TestRenderApprovalPrompt_DiffStatsHeader(t *testing.T) {
	short := buildDiff(2, 3, 1, 12)
	m, _ := approvalModel(permission.Action{
		Kind: permission.KindEdit, Path: "/tmp/x.go",
		Display: permission.Display{Diff: short},
	})
	out := stripANSI(m.renderApprovalPrompt())
	if !strings.Contains(out, "2 hunks · +3 -1") {
		t.Errorf("missing hunk/± stats: %q", out)
	}
	if strings.Contains(out, "lines") && strings.Contains(out, "· 12 lines") {
		t.Errorf("line total should only show when the window truncates: %q", out)
	}

	long := buildDiff(4, 21, 9, 61)
	ml, _ := approvalModel(permission.Action{
		Kind: permission.KindEdit, Path: "/tmp/x.go",
		Display: permission.Display{Diff: long},
	})
	outl := stripANSI(ml.renderApprovalPrompt())
	if !strings.Contains(outl, "4 hunks · +21 -9 · 61 lines") {
		t.Errorf("missing truncation-honest stats: %q", outl)
	}
}

func TestRenderDiff_ShortDiffNoMarkers(t *testing.T) {
	out := stripANSI(renderDiff(buildDiff(1, 2, 1, 10), 0))
	if strings.Contains(out, "top of diff") || strings.Contains(out, "lines below") {
		t.Errorf("short diff must render whole, no window markers: %q", out)
	}
	if got := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); got != 10 {
		t.Errorf("short diff rendered %d lines, want 10", got)
	}
}

// The window's height must be CONSTANT at every scroll position —
// the input box below must not twitch while the user reads the diff
// (same fixed-height discipline as menuMaxRows).
func TestRenderDiff_WindowConstantHeightAcrossScroll(t *testing.T) {
	diff := buildDiff(4, 21, 9, 61)
	maxOff := 61 - (maxDiffLines - 2) // 39

	for _, off := range []int{0, 1, 17, maxOff, maxOff + 100} {
		out := stripANSI(renderDiff(diff, off))
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != maxDiffLines {
			t.Errorf("offset %d: window height %d, want constant %d", off, len(lines), maxDiffLines)
		}
	}

	top := stripANSI(renderDiff(diff, 0))
	if !strings.Contains(top, "(top of diff)") || !strings.Contains(top, "39 lines below · [j/k] scroll") {
		t.Errorf("offset 0 markers wrong: %q", top)
	}

	end := stripANSI(renderDiff(diff, maxOff+100)) // over-scroll clamps to end
	if !strings.Contains(end, "39 lines above") || !strings.Contains(end, "(end of diff)") {
		t.Errorf("end-of-diff markers wrong: %q", end)
	}
	// The clamped window must end at the diff's actual last line.
	if !strings.Contains(end, "context line 60") {
		t.Errorf("end window missing the diff's last line: %q", end)
	}
}

// [a] must grant exactly the prompted Kind — and never touch yolo.
func TestHandleApprovalKey_AlwaysGrantsKindNotYolo(t *testing.T) {
	m, reply := approvalModel(permission.Action{Kind: permission.KindEdit, Path: "/tmp/x.go"})

	var granted []permission.Kind
	yoloCalls := 0
	m.opts.AlwaysAllowKind = func(k permission.Kind) { granted = append(granted, k) }
	m.opts.SetYolo = func(bool) { yoloCalls++ }

	updated, _ := m.handleApprovalKey(tea.KeyPressMsg{Text: "a"})
	m = updated.(Model)

	select {
	case allow := <-reply:
		if !allow {
			t.Error("[a] must reply allow=true")
		}
	default:
		t.Fatal("[a] sent no reply — agent goroutine would hang")
	}
	if len(granted) != 1 || granted[0] != permission.KindEdit {
		t.Errorf("granted kinds = %v, want [edit]", granted)
	}
	if yoloCalls != 0 {
		t.Errorf("[a] escalated to yolo (%d SetYolo calls) — the escalation was removed", yoloCalls)
	}
	if m.pendingApproval != nil {
		t.Error("pendingApproval not cleared after answer")
	}
}

// [y] allows once — no grant.
func TestHandleApprovalKey_AllowOnceDoesNotGrant(t *testing.T) {
	m, reply := approvalModel(permission.Action{Kind: permission.KindBash, Command: "ls"})
	granted := 0
	m.opts.AlwaysAllowKind = func(permission.Kind) { granted++ }

	if updated, _ := m.handleApprovalKey(tea.KeyPressMsg{Text: "y"}); updated.(Model).pendingApproval != nil {
		t.Error("pendingApproval not cleared")
	}
	if allow := <-reply; !allow {
		t.Error("[y] must reply allow=true")
	}
	if granted != 0 {
		t.Errorf("[y] must not grant; AlwaysAllowKind called %d times", granted)
	}
}

// j/k scroll the diff window without answering the prompt.
func TestHandleApprovalKey_ScrollDoesNotAnswer(t *testing.T) {
	diff := buildDiff(4, 21, 9, 61)
	m, reply := approvalModel(permission.Action{
		Kind: permission.KindEdit, Path: "/tmp/x.go",
		Display: permission.Display{Diff: diff},
	})

	for _, k := range []string{"j", "j", "j", "k"} {
		updated, _ := m.handleApprovalKey(tea.KeyPressMsg{Text: k})
		m = updated.(Model)
	}
	if m.approvalDiffOffset != 2 {
		t.Errorf("offset = %d after jjj k, want 2", m.approvalDiffOffset)
	}
	if m.pendingApproval == nil {
		t.Error("scrolling must not answer the prompt")
	}
	select {
	case <-reply:
		t.Error("scrolling sent a reply")
	default:
	}

	// k at the top clamps to 0, never negative.
	m.approvalDiffOffset = 0
	updated, _ := m.handleApprovalKey(tea.KeyPressMsg{Text: "k"})
	if got := updated.(Model).approvalDiffOffset; got != 0 {
		t.Errorf("offset = %d after k at top, want clamped 0", got)
	}
}
