package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/pricing"
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

func TestRenderCommittedToolErr_PlainErrorUnchangedShape(t *testing.T) {
	forceColor(t)
	got := renderCommittedToolErr("edit", "args", "0 matches found", 250*time.Millisecond)
	// Structural invariants from the old single-Render version: the chrome
	// is present, the body is present, the duration tail is present.
	for _, want := range []string{"↳ edit(args)", "ERROR:", "0 matches found", " · 0.2s"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

func TestRenderCommittedToolErr_DiffBlockGetsColored(t *testing.T) {
	forceColor(t)
	err := "0 matches\n\n```diff\n-old\n+new\n```\nTip: copy +."
	got := renderCommittedToolErr("edit", "args", err, time.Second)
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
	got := renderCommittedToolOk("edit", "/tmp/x", result, 200*time.Millisecond, "")
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
	got := renderCommittedToolOk("grep", "pattern", result, 50*time.Millisecond, "")
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
