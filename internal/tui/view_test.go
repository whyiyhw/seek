package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// forceColor pins lipgloss to a colour-emitting profile for the duration of
// a test. Without this, lipgloss detects "not a TTY" in `go test` and
// silently strips all SGR codes — which makes any assertion about colour
// output trivially fail.
func forceColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

func TestFormatCommittedDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		// Sub-100ms is suppressed — bytes/exit-code is enough info for
		// near-instant operations and the noise hurts readability.
		{0, ""},
		{50 * time.Millisecond, ""},
		{99 * time.Millisecond, ""},

		// 100ms..999ms with one decimal — readable, stable.
		{100 * time.Millisecond, "0.1s"},
		{800 * time.Millisecond, "0.8s"},

		// Whole seconds.
		{time.Second, "1s"},
		{12 * time.Second, "12s"},
		{59 * time.Second, "59s"},

		// Minutes + seconds.
		{time.Minute, "1m0s"},
		{125 * time.Second, "2m5s"},
		{10 * time.Minute, "10m0s"},
	}
	for _, c := range cases {
		if got := formatCommittedDuration(c.in); got != c.want {
			t.Errorf("formatCommittedDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatToolElapsed_SuppressesSubSecond(t *testing.T) {
	if got := formatToolElapsed(500 * time.Millisecond); got != "" {
		t.Errorf("sub-1s should be empty, got %q", got)
	}
	if got := formatToolElapsed(2 * time.Second); got != "2s" {
		t.Errorf("got %q, want 2s", got)
	}
}

func TestDurationTail(t *testing.T) {
	if got := durationTail(50 * time.Millisecond); got != "" {
		t.Errorf("sub-100ms should be empty: %q", got)
	}
	if got := durationTail(2 * time.Second); got != " · 2s" {
		t.Errorf("got %q", got)
	}
}

func TestFormatTokensK(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{99600, "99.6k"},
		{670000, "670.0k"},
	}
	for _, c := range cases {
		if got := formatTokensK(c.n); got != c.want {
			t.Errorf("formatTokensK(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestRenderTurnFooter_Format(t *testing.T) {
	tracker := cache.New()
	tracker.Record(deepseek.Usage{
		PromptTokens:          99600,
		PromptCacheHitTokens:  82000,
		PromptCacheMissTokens: 17600,
		CompletionTokens:      1700,
	}, deepseek.ModelV4Flash, pricing.TierStandard)
	m := Model{
		opts: Options{
			Model:   deepseek.ModelChat,
			Tracker: tracker,
		},
		turns:     3,
		toolCalls: 7,
		now:       time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai),
	}
	footer := stripANSI(m.renderTurnFooter())
	for _, frag := range []string{
		"turn 3",
		"7 tools",
		"↑99.6k prompt",
		"(82% cache)",
		"↓1.7k tok",
	} {
		if !strings.Contains(footer, frag) {
			t.Errorf("missing %q in footer: %q", frag, footer)
		}
	}
}

func TestRenderTurnFooter_NoCacheNote_WhenNoHits(t *testing.T) {
	tracker := cache.New()
	tracker.Record(deepseek.Usage{
		PromptTokens:          5000,
		PromptCacheHitTokens:  0,
		PromptCacheMissTokens: 5000,
		CompletionTokens:      200,
	}, deepseek.ModelV4Flash, pricing.TierStandard)
	m := Model{
		opts: Options{
			Model:   deepseek.ModelChat,
			Tracker: tracker,
		},
		turns:     1,
		toolCalls: 0,
		now:       time.Date(2026, time.January, 15, 9, 0, 0, 0, pricing.Shanghai),
	}
	footer := stripANSI(m.renderTurnFooter())
	if strings.Contains(footer, "cache)") {
		t.Errorf("should not show cache note when no cache hits: %q", footer)
	}
	if !strings.Contains(footer, "↑5.0k prompt") {
		t.Errorf("missing prompt tokens: %q", footer)
	}
}

func TestStreamingLabel_SubSecond(t *testing.T) {
	m := Model{streamStartTime: time.Now()}
	if got := m.streamingLabel(); got != "thinking…" {
		t.Errorf("sub-1s should return 'thinking…', got %q", got)
	}
}

func TestStreamingLabel_OverSecond(t *testing.T) {
	m := Model{streamStartTime: time.Now().Add(-10 * time.Second)}
	got := m.streamingLabel()
	if !strings.HasPrefix(got, "thinking… ") {
		t.Errorf("should have 'thinking… ' prefix, got %q", got)
	}
	if !strings.Contains(got, "s") {
		t.Errorf("should contain elapsed seconds, got %q", got)
	}
}

// ANSI SGR for 256-colour foreground "n" is "\x1b[38;5;Nm". The palette
// values are defined in styles.go (darkPalette: Ok=114, ToolErr=203,
// Muted=241). When lipgloss is pinned to ANSI256 these are deterministic.
const (
	sgrOk      = "\x1b[38;5;114m" // styleDiffAdd / colourOk
	sgrToolErr = "\x1b[38;5;203m" // styleToolError / `-` lines
	sgrMuted   = "\x1b[38;5;241m" // styleMuted / fence + context
)

func TestColorizeDiffBlocks_NoFenceIsTransparent(t *testing.T) {
	forceColor(t)
	in := "plain error message\nsecond line"
	got := colorizeDiffBlocks(in, styleToolError)
	// Without a ```diff fence the helper takes the fast path and wraps
	// the whole input in defaultStyle — equivalent to the pre-existing
	// renderer's behaviour. Single-style render is what we want here.
	if !strings.Contains(got, sgrToolErr) {
		t.Errorf("plain error should be rendered with toolErr colour, got: %q", got)
	}
	if strings.Contains(got, sgrOk) {
		t.Errorf("no diff fence → no add-line colour should appear, got: %q", got)
	}
}

func TestColorizeDiffBlocks_PerLineColors(t *testing.T) {
	forceColor(t)
	in := strings.Join([]string{
		"edit: 0 matches",
		"",
		"closest candidate at lines 487-490:",
		"```diff",
		"--- a/lines 487-490",
		"+++ b/lines 487-490",
		"@@ -1,4 +1,4 @@",
		"-    .typing-cursor.done {",
		"+  .typing-cursor.done {",
		" unchanged context",
		"```",
		"Tip: copy `+` lines verbatim.",
	}, "\n")

	got := colorizeDiffBlocks(in, styleToolError)

	// The two load-bearing assertions: `+` and `-` lines must end up with
	// DIFFERENT colours, otherwise the whole point of this change is lost.
	if !strings.Contains(got, sgrOk+"+  .typing-cursor.done {") {
		t.Errorf("`+` add line should use ok/green colour. Output:\n%q", got)
	}
	if !strings.Contains(got, sgrToolErr+"-    .typing-cursor.done {") {
		t.Errorf("`-` del line should use toolErr/red colour. Output:\n%q", got)
	}

	// Structural lines (fence open/close, file headers, context, hunk
	// header) get muted so the eye is drawn to +/- instead.
	mutedLines := []string{
		"```diff",
		"--- a/lines 487-490",
		"+++ b/lines 487-490",
		"@@ -1,4 +1,4 @@",
		" unchanged context",
	}
	for _, ln := range mutedLines {
		if !strings.Contains(got, sgrMuted+ln) {
			t.Errorf("structural line %q should be muted. Output:\n%q", ln, got)
		}
	}

	// Text outside the fence uses the default (tool-error) style.
	if !strings.Contains(got, sgrToolErr+"edit: 0 matches") {
		t.Errorf("text before fence should use defaultStyle. Output:\n%q", got)
	}
	if !strings.Contains(got, sgrToolErr+"Tip: copy `+` lines verbatim.") {
		t.Errorf("text after fence should use defaultStyle. Output:\n%q", got)
	}

	// Sanity: the `+++` file-header MUST come out muted, not green.
	// HasPrefix("+++", "+") is true; the switch order in the helper
	// handles file headers first to avoid mis-classifying them as adds.
	if strings.Contains(got, sgrOk+"+++") {
		t.Errorf("`+++` file header must not be rendered as an add line. Output:\n%q", got)
	}
	if strings.Contains(got, sgrToolErr+"---") {
		// styleToolError is the `-` line colour — `---` must not collide.
		// `---` appears inside the diff block as a file header; outside it,
		// the only `-` would be at start of a needle line, but our fixture
		// has none. So any sgrToolErr+`---` means the file header was
		// mis-classified.
		t.Errorf("`---` file header must not be rendered as a del line. Output:\n%q", got)
	}
}

func TestColorizeDiffBlocks_MultipleFences(t *testing.T) {
	forceColor(t)
	in := strings.Join([]string{
		"first error",
		"```diff",
		"-old1",
		"+new1",
		"```",
		"between fences (plain)",
		"```diff",
		"-old2",
		"+new2",
		"```",
		"tail",
	}, "\n")
	got := colorizeDiffBlocks(in, styleToolError)
	// Both fences should colourise their `+` lines.
	if !strings.Contains(got, sgrOk+"+new1") {
		t.Errorf("first fence `+` line missing colour: %q", got)
	}
	if !strings.Contains(got, sgrOk+"+new2") {
		t.Errorf("second fence `+` line missing colour: %q", got)
	}
	// Between-fences text must use defaultStyle (not stuck in diff mode).
	if !strings.Contains(got, sgrToolErr+"between fences (plain)") {
		t.Errorf("text between fences should use defaultStyle, got: %q", got)
	}
}

// --- Shared committed-block renderers ---------------------------------
//
// These pin the contracts of renderUserBlock / renderAssistantBlock /
// renderToolResultLine — the SHARED functions called by both the live
// commit path (update_agent.go) and the replay path (replay.go).
// Divergences between live and replay (Markdown skipped, duplicate
// tool rows, untruncated args, spurious "▸ seek" on tool-only turns)
// were the bug cluster Option A of the v0.3.x review was meant to
// retire.

func TestRenderUserBlock_StartsWithNewline(t *testing.T) {
	t.Parallel()
	out := renderUserBlock("foo", 0)
	if !strings.HasPrefix(out, "\n") {
		t.Errorf("user block must start with newline; got %q", out)
	}
}

func TestRenderUserBlock_WidthZeroSkipsWrapping(t *testing.T) {
	t.Parallel()
	// width==0 path is only hit by replay (terminal size unknown before
	// tea.NewProgram). Body should pass through highlightRefs but skip
	// lipgloss.Width.
	out := renderUserBlock("hello", 0)
	if !strings.Contains(out, "hello") {
		t.Errorf("missing body in %q", out)
	}
}

func TestRenderAssistantBlock_EmptyContentReturnsEmpty(t *testing.T) {
	t.Parallel()
	// Pure tool-call turn (no narrative content) MUST return "" — the
	// `↳ tool(...)` lines emitted by ToolExecEnd / RoleTool render
	// already convey what happened. Live's applyAgentEvent gates on
	// curContent != "" at the caller; this is the defense-in-depth
	// check at the helper.
	if got := renderAssistantBlock("", "reasoning text", false, 80, nil); got != "" {
		t.Errorf("empty content must return empty, got %q", got)
	}
}

func TestRenderAssistantBlock_NoLeadingNewline(t *testing.T) {
	t.Parallel()
	// Matches the appendHistory contract: the previous tea.Println's
	// trailing \n already positioned the cursor at column 0, so the
	// block starts directly with its label.
	out := renderAssistantBlock("hi", "", false, 80, nil)
	if strings.HasPrefix(out, "\n") {
		t.Errorf("must NOT start with newline, got %q", out)
	}
	if !strings.Contains(stripANSI(out), "▸ seek") {
		t.Errorf("must start with seek label, got %q", out)
	}
}

func TestRenderAssistantBlock_ReasoningShownWhenToggled(t *testing.T) {
	t.Parallel()
	withShown := renderAssistantBlock("answer", "step 1", true, 80, nil)
	withHidden := renderAssistantBlock("answer", "step 1", false, 80, nil)
	if !strings.Contains(stripANSI(withShown), "step 1") {
		t.Errorf("showReasoning=true must surface body; got %q", withShown)
	}
	if strings.Contains(stripANSI(withHidden), "step 1") {
		t.Errorf("showReasoning=false must NOT show body; got %q", withHidden)
	}
	if !strings.Contains(stripANSI(withHidden), "reasoning hidden") {
		t.Errorf("showReasoning=false must show hidden placeholder; got %q", withHidden)
	}
}

// TestRenderAssistantBlock_ReasoningUsesGutter pins the visual-separation
// contract: shown reasoning renders as a "▸ reasoning" header over a
// left-gutter (│) block so it reads as a distinct aside from the
// assistant prose. A refactor that drops the gutter (back to a bare
// indent) silently undoes the separation, so guard it here.
func TestRenderAssistantBlock_ReasoningUsesGutter(t *testing.T) {
	t.Parallel()
	out := stripANSI(renderAssistantBlock("answer", "first\nsecond", true, 80, nil))
	if !strings.Contains(out, "▸ reasoning") {
		t.Errorf("reasoning block must carry the ▸ reasoning header; got %q", out)
	}
	if !strings.Contains(out, "│ first") || !strings.Contains(out, "│ second") {
		t.Errorf("every reasoning line must be gutter-prefixed (│ ); got %q", out)
	}
}

func TestRenderToolResultLine_ErrorShape(t *testing.T) {
	forceColor(t)
	got := renderToolResultLine("edit", "args", "", errors.New("0 matches found"), 250*time.Millisecond, 0)
	// Structural invariants from the old single-Render version: the chrome
	// is present, the body is present, the duration tail is present.
	for _, want := range []string{"↳ edit(args)", "ERROR:", "0 matches found", " · 0.2s"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

func TestRenderToolResultLine_ErrorDiffColored(t *testing.T) {
	forceColor(t)
	errBody := "0 matches\n\n```diff\n-old\n+new\n```\nTip: copy +."
	got := renderToolResultLine("edit", "args", "", errors.New(errBody), time.Second, 0)
	if !strings.Contains(got, sgrOk+"+new") {
		t.Errorf("`+` line inside embedded diff should be green. Output:\n%q", got)
	}
	if !strings.Contains(got, sgrToolErr+"-old") {
		t.Errorf("`-` line inside embedded diff should be red. Output:\n%q", got)
	}
}

func TestExtractDiffSection_StripsFence(t *testing.T) {
	in := "edited /tmp/x: 1 replacement, 10 → 12 bytes\n" +
		"```diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n```"
	body := extractDiffSection(in)
	if body == "" {
		t.Fatal("expected diff body, got empty")
	}
	// Fence lines themselves must be stripped — the TUI shouldn't show
	// markdown delimiters to the user.
	if strings.Contains(body, "```") {
		t.Errorf("body should not contain fence markers, got: %q", body)
	}
	// The diff content must survive intact.
	for _, want := range []string{"--- a/x", "+++ b/x", "@@ -1 +1 @@", "-old", "+new"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %q", want, body)
		}
	}
	// The pre-fence header line must NOT leak through — the TUI's
	// summary line already covers it.
	if strings.Contains(body, "edited /tmp/x") {
		t.Errorf("body should not include pre-fence header, got: %q", body)
	}
}

func TestExtractDiffSection_ReturnsEmptyForNoFence(t *testing.T) {
	// Non-edit tools (read, grep, bash, …) return text without any ```diff
	// fence. extractDiffSection must report "no diff" so renderCommittedToolOk
	// stays on the one-line summary path.
	if got := extractDiffSection("just some output\nno fences here"); got != "" {
		t.Errorf("expected empty for non-fenced input, got %q", got)
	}
	if got := extractDiffSection(""); got != "" {
		t.Errorf("expected empty for empty input, got %q", got)
	}
}

func TestRenderCommittedToolOk_DiffResultShowsColoredDiff(t *testing.T) {
	forceColor(t)
	result := "edited /tmp/x: 1 replacement(s), 10 → 12 bytes\n" +
		"```diff\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new\n```"
	got := renderToolResultLine("edit", "/tmp/x", result, nil, 200*time.Millisecond, 0)
	// Summary line stays on top.
	if !strings.Contains(got, "↳ edit(/tmp/x)") {
		t.Errorf("summary line missing: %q", got)
	}
	// Coloured diff body sits beneath it.
	if !strings.Contains(got, sgrOk+"+new") {
		t.Errorf("`+` line should be green under success line. Output:\n%q", got)
	}
	if !strings.Contains(got, sgrToolErr+"-old") {
		t.Errorf("`-` line should be red under success line. Output:\n%q", got)
	}
	// The markdown fence itself must NOT appear in the rendered output —
	// it's a parsing artifact, not user-facing content.
	if strings.Contains(got, "```diff") {
		t.Errorf("rendered output should not contain ```diff fence marker. Output:\n%q", got)
	}
}

func TestHighlightRefs_HighlightsAtRefs(t *testing.T) {
	forceColor(t)
	// Structural check: the @-prefixed token must be separated from adjacent
	// text by ANSI escapes on both sides.
	got := highlightRefs("see @CLAUDE.md for details")

	if !strings.Contains(got, "@CLAUDE.md") {
		t.Fatalf("output must contain the reference text: %q", got)
	}
	// The output should have at least 4 ANSI escape sequences: one opening
	// "see ", one reset+reopen for "@CLAUDE.md", then another for " for ...".
	// Count \x1b occurrences as a proxy.
	if n := strings.Count(got, "\x1b"); n < 4 {
		t.Errorf("expected >=4 ANSI escapes (2 user-text segments + 1 ref), got %d in %q", n, got)
	}
	// The whole output must be wrapped in ANSI styling (starts with escape).
	if !strings.HasPrefix(got, "\x1b[") {
		t.Errorf("output should start with an ANSI escape for user colour")
	}
}

func TestHighlightRefs_PlainTextHasNoChange(t *testing.T) {
	got := highlightRefs("hello world, no references here")
	if !strings.Contains(got, "hello world") {
		t.Errorf("plain text must be preserved, got %q", got)
	}
}

func TestHighlightRefs_MultipleRefsAllHighlighted(t *testing.T) {
	forceColor(t)
	got := highlightRefs("check @a.go and @b_test.go")
	if !strings.Contains(got, "@a.go") || !strings.Contains(got, "@b_test.go") {
		t.Errorf("both references should appear, got %q", got)
	}
	// Both @-refs should have ANSI escapes before them, but the joining text
	// " and " should be between two reset+reopen sequences.
	count := strings.Count(got, "\x1b[")
	if count < 4 {
		t.Errorf("expected at least 4 ANSI sequences (two for user text, two for highlights), got %d", count)
	}
}

func TestHighlightRefs_StyleCorrectness(t *testing.T) {
	forceColor(t)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no refs",
			in:   "hello world",
			want: styleUserText.Render("hello world"),
		},
		{
			name: "single ref",
			in:   "see @file.txt here",
			want: styleUserText.Render("see ") +
				styleRefHighlight.Render("@file.txt") +
				styleUserText.Render(" here"),
		},
		{
			name: "multiple refs",
			in:   "@a and @b",
			want: styleRefHighlight.Render("@a") +
				styleUserText.Render(" and ") +
				styleRefHighlight.Render("@b"),
		},
		{
			name: "adjacent refs",
			in:   "@first@second",
			want: styleRefHighlight.Render("@first") +
				styleRefHighlight.Render("@second"),
		},
		{
			name: "refs with dots and slashes",
			in:   "path @my.project/file@v1.0 here",
			want: styleUserText.Render("path ") +
				styleRefHighlight.Render("@my.project/file") +
				styleRefHighlight.Render("@v1.0") +
				styleUserText.Render(" here"),
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "ref at start",
			in:   "@start mid",
			want: styleRefHighlight.Render("@start") +
				styleUserText.Render(" mid"),
		},
		{
			name: "ref at end",
			in:   "end @ref",
			want: styleUserText.Render("end ") +
				styleRefHighlight.Render("@ref"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := highlightRefs(tt.in)
			if got != tt.want {
				t.Errorf("highlightRefs(%q):\ngot:  %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRenderHighlightedPath(t *testing.T) {
	forceColor(t)
	tests := []struct {
		name     string
		token    string
		path     string
		selected bool
		want     string
	}{
		{
			name:  "empty token",
			token: "",
			path:  "/some/file.txt",
			want:  styleMenuItem.Render("  /some/file.txt"),
		},
		{
			name:     "empty token selected",
			token:    "",
			path:     "/some/file.txt",
			selected: true,
			want:     styleMenuSelected.Render("▸ /some/file.txt"),
		},
		{
			name:  "basename prefix match",
			token: "file",
			path:  "/some/file.txt",
			want: styleMenuItem.Render("  /some/") +
				styleMatchHighlight.Render("file") +
				styleMenuItem.Render(".txt"),
		},
		{
			name:     "basename prefix selected",
			token:    "file",
			path:     "/some/file.txt",
			selected: true,
			want: styleMenuSelected.Render("▸ /some/") +
				styleMatchHighlight.Render("file") +
				styleMenuSelected.Render(".txt"),
		},
		{
			name:  "full path substring",
			token: "some",
			path:  "/some/file.txt",
			want: styleMenuItem.Render("  /") +
				styleMatchHighlight.Render("some") +
				styleMenuItem.Render("/file.txt"),
		},
		{
			name:  "no match plain",
			token: "xyz",
			path:  "/some/file.txt",
			want:  styleMenuItem.Render("  /some/file.txt"),
		},
		{
			name:     "no match selected",
			token:    "xyz",
			path:     "/some/file.txt",
			selected: true,
			want:     styleMenuSelected.Render("▸ /some/file.txt"),
		},
		{
			name:  "case mismatch",
			token: "FILE",
			path:  "/some/file.txt",
			want: styleMenuItem.Render("  /some/") +
				styleMatchHighlight.Render("file") +
				styleMenuItem.Render(".txt"),
		},
		{
			name:     "basename match at root",
			token:    "main",
			path:     "main.go",
			selected: true,
			want: styleMenuSelected.Render("▸ ") +
				styleMatchHighlight.Render("main") +
				styleMenuSelected.Render(".go"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{
				pathPicker: pathCompleterState{
					token: tt.token,
				},
			}
			got := m.renderHighlightedPath(tt.path, tt.selected)
			if got != tt.want {
				t.Errorf("renderHighlightedPath(%q, %v):\ngot:  %q\nwant: %q", tt.path, tt.selected, got, tt.want)
			}
		})
	}
}

func TestRenderCommittedToolOk_NonDiffResultStaysOneLine(t *testing.T) {
	forceColor(t)
	// Simulate a non-edit tool's result (e.g. `read`, `grep`). Should
	// render exactly as before — a single summary line with no extra rows.
	result := "found 3 matches in /tmp/x\n  /tmp/x:10:hit one\n  /tmp/x:14:hit two\n  /tmp/x:22:hit three"
	got := renderToolResultLine("grep", "pattern", result, nil, 50*time.Millisecond, 0)
	if strings.Count(got, "\n") != 0 {
		t.Errorf("non-diff result should render as single line, got %d newlines:\n%q",
			strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, "↳ grep(pattern)") {
		t.Errorf("summary line missing: %q", got)
	}
	// And the byte count must be the FULL result length, not just the
	// summary-line length — the signature now derives from len(result).
	if !strings.Contains(got, fmt.Sprintf("%d bytes", len(result))) {
		t.Errorf("byte count should be len(result)=%d: %q", len(result), got)
	}
}

// sgrToolSkill is what styleToolSkill (accent magenta + bold) emits
// under ANSI256. Bold + colour 177 → "\x1b[1;38;5;177m". Used by the
// Skill rendering tests below to assert the dedicated style was applied
// instead of the default styleToolLine (amber 180).
const sgrToolSkill = "\x1b[1;38;5;177m"

func TestRenderToolResultLine_SkillUsesDedicatedFormat(t *testing.T) {
	forceColor(t)
	// Args is the JSON the Skill tool's schema requires (PRD v0 §4.6.3:
	// one required field, "name").
	got := renderToolResultLine("Skill", `{"name":"dual-model"}`, "(body)", nil, 300*time.Millisecond, 0)

	// The skill-specific header replaces the generic "↳ Name(args)"
	// form so the user reads "which skill" directly. The dual-model
	// name must appear inline.
	if !strings.Contains(got, "✦ skill: dual-model") {
		t.Errorf("expected dedicated skill header, got: %q", got)
	}
	// Negative: the generic "↳ Skill(...)" form must NOT leak through
	// — that would mean the branch didn't trigger and we'd be back to
	// the indistinguishable amber line.
	if strings.Contains(got, "↳ Skill(") {
		t.Errorf("skill call should NOT render with the generic tool form: %q", got)
	}
	// Colour: accent magenta + bold, not amber.
	if !strings.Contains(got, sgrToolSkill) {
		t.Errorf("skill line should use styleToolSkill (sgr=%q), got: %q", sgrToolSkill, got)
	}
}

func TestRenderToolResultLine_NonSkillUnchanged(t *testing.T) {
	forceColor(t)
	// Regression guard: every non-Skill tool must keep its existing
	// "↳ name(args) → N bytes" shape. The accent-magenta sgr must NOT
	// appear on a plain read call.
	got := renderToolResultLine("read", "foo.go", "(body)", nil, 100*time.Millisecond, 0)
	if !strings.Contains(got, "↳ read(foo.go)") {
		t.Errorf("non-Skill tools must keep the generic form, got: %q", got)
	}
	if strings.Contains(got, sgrToolSkill) {
		t.Errorf("non-Skill tools must NOT use styleToolSkill, got: %q", got)
	}
}

func TestRenderToolResultLine_SkillErrorKeepsHeaderAndStaysRed(t *testing.T) {
	forceColor(t)
	got := renderToolResultLine("Skill", `{"name":"missing"}`, "", errors.New(`"missing" not found`), 250*time.Millisecond, 0)

	// Skill errors still get the dedicated "✦ skill: <name>" header
	// so the failed call is attributable; the red colour itself is
	// what marks it as "failed".
	if !strings.Contains(got, "✦ skill: missing") {
		t.Errorf("skill error should keep the dedicated header, got: %q", got)
	}
	if !strings.Contains(got, "ERROR:") {
		t.Errorf("ERROR: tag missing, got: %q", got)
	}
	// Red, not accent magenta: failure colour takes precedence over
	// the per-tool accent.
	if !strings.Contains(got, sgrToolErr) {
		t.Errorf("skill error should render in styleToolError (red), got: %q", got)
	}
	if strings.Contains(got, sgrToolSkill) {
		t.Errorf("skill error should NOT also carry the accent style: %q", got)
	}
}

func TestParseSkillName(t *testing.T) {
	cases := []struct {
		args string
		want string
	}{
		// Canonical args produced by the Skill tool's schema.
		{`{"name":"dual-model"}`, "dual-model"},
		{`{"name":"go-test-runner"}`, "go-test-runner"},
		// Truncated mid-value (truncateOneLine clips at 80 chars on the
		// active-tool line) — must not panic; falls back to "?".
		{`{"name":"some-really-long-name-tha`, "?"},
		// Empty / missing name — also "?", never a blank rendered line.
		{`{}`, "?"},
		{``, "?"},
		// Wrong shape entirely (shouldn't happen in practice, but the
		// fallback is what keeps render-side panics off the table).
		{`not json`, "?"},
	}
	for _, tc := range cases {
		if got := parseSkillName(tc.args); got != tc.want {
			t.Errorf("parseSkillName(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestFormatActiveToolLabel_SkillBranch(t *testing.T) {
	label, style := formatActiveToolLabel("Skill", `{"name":"dual-model"}`)
	if !strings.Contains(label, "✦ skill: dual-model") {
		t.Errorf("active skill label missing dedicated form: %q", label)
	}
	// Style must be the dedicated one. Compare by rendering a probe
	// string — lipgloss styles are not comparable by ==.
	forceColor(t)
	if style.Render("x") != styleToolSkill.Render("x") {
		t.Errorf("expected styleToolSkill for Skill name, got a different style")
	}
}

func TestFormatActiveToolLabel_NonSkillUnchanged(t *testing.T) {
	label, style := formatActiveToolLabel("read", "foo.go")
	if !strings.Contains(label, "read(foo.go) …") {
		t.Errorf("non-Skill active label changed shape: %q", label)
	}
	forceColor(t)
	if style.Render("x") != styleToolLine.Render("x") {
		t.Errorf("non-Skill active label should use styleToolLine, got different style")
	}
}

// TestView_IdleStateIsStableAcrossRedraws locks the load-bearing
// "no jumping" invariant for inline mode: View() called twice on
// identical model state must return byte-identical output. If a
// future change introduces time-dependent rendering inside View()
// (live timestamps, frame counters, etc.) or accidentally non-
// deterministic ordering, this test catches it. The whole reason
// we left alt-screen is to fix scroll/copy — that win is undone if
// each redraw subtly shuffles the live region.
func TestView_IdleStateIsStableAcrossRedraws(t *testing.T) {
	SetTheme("dark")
	m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
	m.width = 120
	m.height = 40
	m.ready = true
	m.input.SetWidth(118)
	// Pin a fixed `now` so the status bar's tier label / off-peak
	// computation doesn't fluctuate between calls.
	m.now = time.Date(2026, time.January, 15, 12, 0, 0, 0, pricing.Shanghai)

	first := m.View()
	second := m.View()
	if first != second {
		t.Errorf("View() is non-deterministic across redraws on identical state;\nfirst:  %q\nsecond: %q", first, second)
	}
}

// TestView_WelcomeBannerOnFreshSession verifies that a model with
// turns==0 renders the meta line (CWD, version, creator).
func TestView_WelcomeBannerOnFreshSession(t *testing.T) {
	SetTheme("dark")
	m := testModel().Build()

	out := m.View()

	if !strings.Contains(out, m.opts.CWD) {
		t.Errorf("welcome banner should contain cwd %q", m.opts.CWD)
	}
	if !strings.Contains(out, "seek") {
		t.Error("welcome banner should mention 'seek'")
	}
	if !strings.Contains(out, Creator) {
		t.Errorf("welcome banner should show creator %q", Creator)
	}
}

// TestView_WelcomeBannerHiddenAfterFirstTurn verifies that once a
// conversation starts (turns>0), the welcome meta line is absent from
// the live region. It should NOT reappear mid-conversation.
func TestView_WelcomeBannerHiddenAfterFirstTurn(t *testing.T) {
	SetTheme("dark")
	m := testModel().WithTurns(1).Build()

	out := m.View()

	if strings.Contains(out, Creator) {
		t.Error("welcome meta line must NOT appear in View() when turns>0")
	}
}

// TestView_WelcomeBannerHiddenAfterFirstSubmit verifies that the
// welcome banner disappears on the FIRST Enter, gated on promptHistory,
// rather than waiting for TurnEnd.
func TestView_WelcomeBannerHiddenAfterFirstSubmit(t *testing.T) {
	SetTheme("dark")
	// turns still 0 — TurnEnd has not fired yet — but the user has
	// submitted once, so promptHistory is non-empty.
	m := testModel().WithPromptHistory("hello").Build()

	out := m.View()

	if strings.Contains(out, Creator) {
		t.Error("welcome meta line must NOT appear after the first submit, even while turns==0")
	}
}

// TestView_NoBottomFloorPadding hard-guards the load-bearing "no
// floor-pin" decision for inline mode. The previous inline attempt
// padded sb with `strings.Repeat("\n", pad)` to push the input to
// the absolute terminal floor, which caused the M3-era drift class.
// Any reintroduction of that pattern will leave a long trailing run
// of \n in View() output — this test fails on the first such regression.
func TestView_NoBottomFloorPadding(t *testing.T) {
	SetTheme("dark")
	m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
	m.width = 120
	m.height = 40 // tall terminal — old code padded ~30 lines here
	m.ready = true
	m.input.SetWidth(118)

	out := m.View()
	// Allow a small handful of trailing newlines (one per logical row
	// like input's `\n` + status's absence of `\n`), but never the kind
	// of long run that floor-pinning produces.
	const maxTrailingNewlines = 3
	trailing := 0
	for i := len(out) - 1; i >= 0 && out[i] == '\n'; i-- {
		trailing++
	}
	if trailing > maxTrailingNewlines {
		t.Errorf("View() ends with %d consecutive '\\n' — floor-pin padding has been reintroduced (max allowed: %d)", trailing, maxTrailingNewlines)
	}
}

// TestView_SeparatorAlwaysPresent locks the "separator is always
// present above the input" invariant. Under the reserved-popup-zone
// design (see view.go), the bottom block always has content above
// the input (filter popup if open, blank zone otherwise), so the
// separator is a stable demarcation rather than an on/off element.
// Holding the separator constant is what eliminates the per-popup-
// open shift the user complained about.
func TestView_SeparatorAlwaysPresent(t *testing.T) {
	SetTheme("dark")
	const hbar = "─"

	t.Run("idle has separator", func(t *testing.T) {
		m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
		m.width = 80
		m.height = 30
		m.ready = true
		m.input.SetWidth(78)

		out := stripANSI(m.View())
		if !strings.Contains(out, strings.Repeat(hbar, 10)) {
			t.Errorf("idle View() must render the separator (reserved-zone invariant); got:\n%s", out)
		}
	})

	t.Run("popup forces separator", func(t *testing.T) {
		m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
		m.width = 80
		m.height = 30
		m.ready = true
		m.input.SetWidth(78)
		m.commandMenuOpen = true
		m.commandMenuFiltered = []command{{usage: "/help", description: "Show this help."}}

		out := stripANSI(m.View())
		if !strings.Contains(out, strings.Repeat(hbar, 10)) {
			t.Errorf("View() with popup open must render the separator; got:\n%s", out)
		}
	})
}

// TestRenderCommandMenu_FixedHeight locks the load-bearing "input does
// not move as user filters" invariant. Before this fix, typing a
// filter into the slash menu would shrink the visible list, which
// under inline mode visibly shifted the input upward; backspacing
// would grow the list back, shifting the input downward. Now the
// menu always emits menuMaxRows item rows + one footer row, padded
// with blank lines when there are fewer matches.
func TestRenderCommandMenu_FixedHeight(t *testing.T) {
	SetTheme("dark")

	// stripped row count = newlines in the rendered string. The menu
	// emits one row per item (or blank), plus one footer row; total
	// must be menuMaxRows + 1 regardless of how many items match.
	rowsIn := func(s string) int { return strings.Count(s, "\n") }
	const wantRows = menuMaxRows + 1

	cases := []struct {
		name  string
		items []command
	}{
		{"one item", []command{{usage: "/help", description: "show help"}}},
		{"three items", []command{
			{usage: "/help", description: "show help"},
			{usage: "/clear", description: "clear screen"},
			{usage: "/quit", description: "exit"},
		}},
		{"full window", func() []command {
			out := make([]command, menuMaxRows)
			for i := range out {
				out[i] = command{usage: "/cmd", description: "desc"}
			}
			return out
		}()},
		{"overflow", func() []command {
			out := make([]command, menuMaxRows+5)
			for i := range out {
				out[i] = command{usage: "/cmd", description: "desc"}
			}
			return out
		}()},
		{"no match", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
			m.commandMenuFiltered = tc.items
			out := m.renderCommandMenu()
			if got := rowsIn(out); got != wantRows {
				t.Errorf("rendered %d rows, want %d (menuMaxRows + footer); output:\n%s", got, wantRows, out)
			}
		})
	}
}

// TestRenderPathPicker_FixedHeight is the @-completer sibling of the
// slash-menu test above. Same invariant: filtering must not change
// total popup height.
func TestRenderPathPicker_FixedHeight(t *testing.T) {
	SetTheme("dark")
	rowsIn := func(s string) int { return strings.Count(s, "\n") }
	const wantRows = menuMaxRows + 1

	cases := []struct {
		name     string
		filtered []string
	}{
		{"two paths", []string{"main.go", "go.mod"}},
		{"overflow", func() []string {
			out := make([]string, menuMaxRows+3)
			for i := range out {
				out[i] = "file.go"
			}
			return out
		}()},
		{"no match", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
			m.pathPicker.filtered = tc.filtered
			out := m.renderPathPicker()
			if got := rowsIn(out); got != wantRows {
				t.Errorf("rendered %d rows, want %d; output:\n%s", got, wantRows, out)
			}
		})
	}
}

// TestMenuWindow_FollowsSelection locks the cursor-stays-visible
// behaviour: when the list overflows menuMaxRows and the user
// navigates with ↑/↓, the window slides so the selected index is
// always inside [start, end). Without this, paging breaks once
// selected goes past the initial visible page.
func TestMenuWindow_FollowsSelection(t *testing.T) {
	const total = menuMaxRows + 10 // forces a windowed list

	for selected := 0; selected < total; selected++ {
		start, end := menuWindow(total, selected)
		if end-start != menuMaxRows {
			t.Errorf("selected=%d: window size = %d, want %d", selected, end-start, menuMaxRows)
		}
		if selected < start || selected >= end {
			t.Errorf("selected=%d not in window [%d, %d)", selected, start, end)
		}
	}
}

// TestView_PopupZoneStablyHigh locks the load-bearing "popup open
// does not shift the input" invariant. Without the reserved zone,
// opening a filter popup grows the bottom block by ~9 rows and the
// input visibly jumps. With the reserved zone, the View()'s total
// row count is byte-stable between idle and any filter-popup-open
// state — only the popup region's CONTENT changes, not its size.
func TestView_PopupZoneStablyHigh(t *testing.T) {
	SetTheme("dark")
	build := func(setup func(*Model)) string {
		m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
		m.width = 80
		m.height = 30
		m.ready = true
		m.input.SetWidth(78)
		setup(&m)
		return m.View()
	}

	idle := build(func(_ *Model) {})
	withMenu := build(func(m *Model) {
		m.commandMenuOpen = true
		m.commandMenuFiltered = []command{
			{usage: "/help", description: "show help"},
		}
	})
	withModel := build(func(m *Model) {
		m.modelPickerOpen = true
		m.modelPickerFiltered = []modelChoice{{id: "deepseek-chat", description: "default"}}
	})
	withPath := build(func(m *Model) {
		m.pathPicker.open = true
		m.pathPicker.filtered = []string{"main.go"}
	})

	idleRows := strings.Count(idle, "\n")
	for _, c := range []struct {
		name string
		out  string
	}{
		{"command menu", withMenu},
		{"model picker", withModel},
		{"path picker", withPath},
	} {
		got := strings.Count(c.out, "\n")
		if got != idleRows {
			t.Errorf("%s: %d rows, want %d (same as idle) — input position is shifting", c.name, got, idleRows)
		}
	}
}

// TestView_ToolSlotPersistsUntilStreamEnd checks that ToolExecEnd
// keeps the slot visible (marked finished) instead of removing it,
// and that handleStreamEnd clears the list at turn end. Without this,
// the input twitches up and down on every per-tool completion.
func TestView_ToolSlotPersistsUntilStreamEnd(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	// Simulate two tools running in sequence within one turn.
	m.applyAgentEvent(agent.ToolExecStart{CallID: "c1", Name: "read", Args: `"a"`})
	m.applyAgentEvent(agent.ToolExecStart{CallID: "c2", Name: "grep", Args: `"b"`})

	if len(m.activeTools) != 2 {
		t.Fatalf("after two ToolExecStart, activeTools = %d, want 2", len(m.activeTools))
	}

	// First tool ends — slot must REMAIN (just flipped to finished).
	m.applyAgentEvent(agent.ToolExecEnd{CallID: "c1", Name: "read", Result: "ok"})

	if len(m.activeTools) != 2 {
		t.Errorf("after one ToolExecEnd, activeTools = %d, want 2 (finished slot must stay)", len(m.activeTools))
	}
	// Find the finished slot.
	var foundFinished, foundRunning bool
	for _, t := range m.activeTools {
		if t.callID == "c1" && t.finished {
			foundFinished = true
		}
		if t.callID == "c2" && !t.finished {
			foundRunning = true
		}
	}
	if !foundFinished {
		t.Error("c1 must be marked finished after ToolExecEnd")
	}
	if !foundRunning {
		t.Error("c2 must still be marked running")
	}

	// handleStreamEnd is the single point that should clear the list.
	out, _ := m.handleStreamEnd(streamEndMsg{})
	if got := len(out.(Model).activeTools); got != 0 {
		t.Errorf("handleStreamEnd must clear activeTools, got %d remaining", got)
	}
}

// TestView_PopupRendersAboveSeparator locks the popup-in-sb position:
// a menu's footer string lands BEFORE the separator row in View()
// output. If a future change accidentally re-puts the popup back into
// bottomBuf below the separator, this test catches it.
func TestView_PopupRendersAboveSeparator(t *testing.T) {
	SetTheme("dark")
	m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
	m.width = 120
	m.height = 40
	m.ready = true
	m.commandMenuOpen = true
	m.commandMenuFiltered = []command{{usage: "/help", description: "Show this help."}}
	m.input.SetWidth(118)

	out := stripANSI(m.View())
	footerIdx := strings.Index(out, "Tab to complete")
	if footerIdx < 0 {
		t.Fatalf("menu footer not found in view output")
	}
	// Find the separator that precedes the input — it's a long run of
	// the horizontal-bar rune. Take the LAST one in the output.
	sepIdx := strings.LastIndex(out, strings.Repeat("─", 10))
	if sepIdx < 0 {
		t.Fatalf("separator row not found in view output")
	}
	if footerIdx > sepIdx {
		t.Errorf("menu footer (idx %d) appears AFTER separator (idx %d) — popup is below separator instead of above",
			footerIdx, sepIdx)
	}
}

// TestAppendHistory_ReturnsPrintlnCmd locks the load-bearing
// contract under inline mode: every appendHistory call must return a
// non-nil tea.Cmd carrying tea.Println. That cmd is the entire
// mechanism for getting committed content into terminal scrollback —
// if a caller drops the returned cmd (or appendHistory returns nil
// for a non-empty line), the line vanishes silently.
func TestAppendHistory_ReturnsPrintlnCmd(t *testing.T) {
	t.Parallel()
	m := emptyModel()
	cmd := m.appendHistory("hello")
	if cmd == nil {
		t.Errorf("appendHistory of non-empty line returned nil cmd — line will not reach scrollback")
	}

	// Empty / whitespace-only line is a no-op (and returns nil cmd) —
	// nothing for tea.Println to commit.
	cmd2 := m.appendHistory("")
	if cmd2 != nil {
		t.Errorf("appendHistory of empty line should return nil cmd, got non-nil")
	}
}

// _ legacy_alt_screen_tests_removed:
// The following tests existed only to assert alt-screen-era invariants
// (banner inside live region, View() fills the viewport exactly, input
// pinned to the absolute terminal floor, in-app history viewport with
// PgUp scrolling, mouse-wheel routing). They were deleted alongside
// the migration back to inline mode; see docs/pitfalls.md for context.
// Stability of the inline live region is now covered by
// TestView_IdleStateIsStableAcrossRedraws,
// TestView_NoBottomFloorPadding, and TestView_SeparatorAlwaysPresent.

// TestView_MenuCloseRemovesPopupRows confirms that once
// commandMenuOpen flips back to false, the next View() output no
// longer contains the menu's footer string — the popup is gone
// from the live region. Catches a regression where popup rendering
// could leak into the next frame if the state machine forgets to
// reset it.
func TestView_MenuCloseRemovesPopupRows(t *testing.T) {
	SetTheme("dark")
	m := New(Options{Tracker: cache.New(), Model: "deepseek-chat"})
	m.width = 120
	m.height = 40
	m.ready = true
	m.input.SetWidth(118)

	// Menu open → footer is present.
	m.commandMenuOpen = true
	m.commandMenuFiltered = []command{{usage: "/help", description: "Show this help."}}
	if !strings.Contains(stripANSI(m.View()), "Tab to complete") {
		t.Fatalf("precondition: menu footer should appear while menu is open")
	}

	// Menu closed → footer is gone.
	m.commandMenuOpen = false
	m.commandMenuFiltered = nil
	if strings.Contains(stripANSI(m.View()), "Tab to complete") {
		t.Errorf("menu footer still rendered after commandMenuOpen=false")
	}
}
