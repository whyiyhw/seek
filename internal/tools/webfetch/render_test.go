package webfetch

import (
	"strings"
	"testing"
)

func TestSimplifyBody_PassThroughNonHTML(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ct   string
		body string
	}{
		{"text/plain", "hello\nworld"},
		{"text/plain; charset=utf-8", "exact verbatim"},
		{"application/json", `{"a":1,"b":[2,3]}`},
		{"application/xml", `<?xml version="1.0"?><root/>`},
		{"text/markdown", "# Title\n\nBody."},
		{"text/csv", "a,b,c\n1,2,3"},
	}
	for _, c := range cases {
		t.Run(c.ct, func(t *testing.T) {
			got := simplifyBody(c.ct, []byte(c.body))
			if got != c.body {
				t.Errorf("non-HTML body should pass through; got %q want %q", got, c.body)
			}
		})
	}
}

func TestSimplifyBody_StripsScriptStyleNoscript(t *testing.T) {
	t.Parallel()
	body := `<html><head>
<style>body { background: hotpink; }</style>
<script>alert("xss")</script>
</head><body>
<p>Visible content</p>
<noscript>fallback junk</noscript>
<p>Another paragraph</p>
</body></html>`

	got := simplifyBody("text/html", []byte(body))

	for _, want := range []string{"Visible content", "Another paragraph"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}
	for _, banned := range []string{"hotpink", "alert", "xss", "fallback junk"} {
		if strings.Contains(got, banned) {
			t.Errorf("expected %q NOT in output, got:\n%s", banned, got)
		}
	}
}

func TestSimplifyBody_PreservesInlineTagsText(t *testing.T) {
	t.Parallel()
	body := `<p>This is <em>important</em> and <a href="/foo">linked</a>.</p>`
	got := simplifyBody("text/html", []byte(body))
	want := "This is important and linked."
	if !strings.Contains(got, want) {
		t.Errorf("inline tags should be transparent; got %q, want contains %q", got, want)
	}
}

func TestSimplifyBody_BlockTagsCreateParagraphBreaks(t *testing.T) {
	t.Parallel()
	body := `<h1>Title</h1><p>Paragraph one.</p><p>Paragraph two.</p>`
	got := simplifyBody("text/html", []byte(body))
	if !strings.Contains(got, "Title\n") && !strings.Contains(got, "Title\n\n") {
		t.Errorf("h1 should be followed by a break, got:\n%s", got)
	}
	if !strings.Contains(got, "Paragraph one.") || !strings.Contains(got, "Paragraph two.") {
		t.Errorf("paragraph bodies missing:\n%s", got)
	}
}

func TestSimplifyBody_CollapsesWhitespace(t *testing.T) {
	t.Parallel()
	body := `<p>Hello    world.</p>
<p>Next   line.</p>


<p>Far    apart.</p>`
	got := simplifyBody("text/html", []byte(body))
	// Internal whitespace collapsed to single spaces.
	if !strings.Contains(got, "Hello world.") {
		t.Errorf("expected collapsed 'Hello world.', got:\n%s", got)
	}
	if strings.Contains(got, "Hello    world") {
		t.Errorf("multiple intra-paragraph spaces should collapse, got:\n%s", got)
	}
	// Max two consecutive newlines (single blank line between paragraphs).
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("more than two consecutive newlines, got:\n%s", got)
	}
}

func TestSimplifyBody_EmptyAndTagOnly(t *testing.T) {
	t.Parallel()
	if got := simplifyBody("text/html", []byte("")); got != "" {
		t.Errorf("empty body should produce empty output, got %q", got)
	}
	got := simplifyBody("text/html", []byte("<html><body></body></html>"))
	if strings.TrimSpace(got) != "" {
		t.Errorf("tag-only body should produce empty visible text, got %q", got)
	}
}

func TestSimplifyBody_NestedBlockedTags(t *testing.T) {
	t.Parallel()
	// Nested <style> inside <head> inside <html>: the script-content
	// drop must not bleed back into visible text.
	body := `<html><head><style>x{color:red}</style></head><body><p>OK</p></body></html>`
	got := simplifyBody("text/html", []byte(body))
	if strings.Contains(got, "x{color:red}") {
		t.Errorf("nested style content leaked:\n%s", got)
	}
	if !strings.Contains(got, "OK") {
		t.Errorf("expected 'OK' in output, got:\n%s", got)
	}
}

func TestSimplifyBody_PreservesPreContent(t *testing.T) {
	t.Parallel()
	// <pre> is treated as a block tag but its content should survive
	// — code blocks are exactly what docs pages use to communicate
	// non-trivial info.
	body := `<p>Run this:</p><pre>go vet ./...</pre><p>Done.</p>`
	got := simplifyBody("text/html", []byte(body))
	if !strings.Contains(got, "go vet ./...") {
		t.Errorf("<pre> content lost:\n%s", got)
	}
}

func TestSimplifyBody_XhtmlAlsoStripped(t *testing.T) {
	t.Parallel()
	body := `<html xmlns="http://www.w3.org/1999/xhtml"><body><script>nope</script><p>yes</p></body></html>`
	got := simplifyBody("application/xhtml+xml", []byte(body))
	if strings.Contains(got, "nope") {
		t.Errorf("application/xhtml+xml should be HTML-stripped too:\n%s", got)
	}
	if !strings.Contains(got, "yes") {
		t.Errorf("xhtml visible text missing:\n%s", got)
	}
}

// collapseWhitespace unit tests cover the helper directly.

func TestCollapseWhitespace_RunsOfSpacesShrinkToOne(t *testing.T) {
	t.Parallel()
	got := collapseWhitespace("a    b\tc \td")
	want := "a b c d"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCollapseWhitespace_MaxTwoNewlines(t *testing.T) {
	t.Parallel()
	got := collapseWhitespace("a\n\n\n\nb")
	want := "a\n\nb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCollapseWhitespace_SingleNewlinePreserved(t *testing.T) {
	t.Parallel()
	got := collapseWhitespace("a\nb")
	want := "a\nb"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCollapseWhitespace_TrimsEdges(t *testing.T) {
	t.Parallel()
	got := collapseWhitespace("\n\n   hello   \n\n")
	want := "hello"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
