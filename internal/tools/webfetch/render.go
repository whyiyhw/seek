// render.go — turn an HTTP response body into a model-friendly text
// blob. For most Content-Types we just hand the raw bytes back; HTML
// is the special case where we strip away script/style/template
// boilerplate via a streaming tokeniser, leaving only the visible
// text. The model can parse 30 KiB of "real content" much more
// cheaply than 200 KiB of HTML wrapping the same 30 KiB.
//
// Why not goldmark / pandoc / a real DOM walker:
//   - golang.org/x/net/html is already in the indirect dep tree
//     (see go.mod), so we get tokenisation for free with no new
//     direct dep.
//   - We don't need round-trip fidelity: this is a one-way "send to
//     the LLM" pipeline. The model doesn't need our headings to look
//     like Markdown; it just needs to not waste tokens on <div class="…">
//     soup.
//   - A full DOM walker (rebuilding markdown, inline-code detection,
//     etc.) is its own project. v1 stays simple: tokenise → emit text
//     between non-blacklist tags → collapse whitespace.

package webfetch

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

// blockedHTMLContent is the set of HTML elements whose text content
// should NOT bleed through into the simplified output. Inline script
// content, CSS, browser-fallback `<noscript>` blocks, and `<template>`
// definitions are all noise from the model's perspective.
var blockedHTMLContent = map[string]bool{
	"script":   true,
	"style":    true,
	"noscript": true,
	"template": true,
	"iframe":   true,
	"object":   true,
	"embed":    true,
}

// blockTags trigger a paragraph break in the output so the simplified
// text preserves some structure. Inline tags (a, em, strong, code, span)
// don't.
var blockTags = map[string]bool{
	"p":          true,
	"div":        true,
	"section":    true,
	"article":    true,
	"header":     true,
	"footer":     true,
	"nav":        true,
	"main":       true,
	"aside":      true,
	"li":         true,
	"ul":         true,
	"ol":         true,
	"dl":         true,
	"dt":         true,
	"dd":         true,
	"h1":         true,
	"h2":         true,
	"h3":         true,
	"h4":         true,
	"h5":         true,
	"h6":         true,
	"blockquote": true,
	"pre":        true,
	"tr":         true,
	"table":      true,
	"thead":      true,
	"tbody":      true,
	"hr":         true,
	"br":         true,
}

// simplifyBody is the entry point called by Execute. For text/html
// it strips boilerplate; for everything else (text/plain, JSON, XML,
// markdown) it returns the raw bytes verbatim — those formats are
// already model-friendly.
//
// ct is the response Content-Type header (may include parameters);
// only the prefix matters.
func simplifyBody(ct string, body []byte) string {
	media := ct
	if idx := strings.Index(ct, ";"); idx >= 0 {
		media = ct[:idx]
	}
	media = strings.TrimSpace(strings.ToLower(media))

	switch media {
	case "text/html", "application/xhtml+xml":
		return htmlToText(body)
	}
	return string(body)
}

// htmlToText walks the HTML token stream emitting only visible text.
// Blocks like <script> / <style> are skipped wholesale (we drop the
// content between the opening and closing tag rather than tag-only).
// Block-level open/close inserts a paragraph break; inline tags are
// transparent.
func htmlToText(body []byte) string {
	tok := html.NewTokenizer(bytes.NewReader(body))
	var (
		out         strings.Builder
		skipStack   int  // when > 0, we're inside a blocked element; drop content
		lastEmitted byte // tracks last char emitted so we can collapse whitespace
		_           = lastEmitted
	)
	emitBreak := func() {
		// Ensure exactly one blank line between block elements.
		// strings.Builder doesn't easily support readback; collapse
		// via TrimRight at the very end, but in the loop we just
		// always emit "\n\n" and clean up later.
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
	}
	for {
		switch tok.Next() {
		case html.ErrorToken:
			return collapseWhitespace(out.String())
		case html.TextToken:
			if skipStack > 0 {
				continue
			}
			text := string(tok.Text())
			out.WriteString(text)
		case html.StartTagToken:
			name, _ := tok.TagName()
			tag := string(name)
			if blockedHTMLContent[tag] {
				skipStack++
				continue
			}
			if blockTags[tag] {
				emitBreak()
			}
		case html.EndTagToken:
			name, _ := tok.TagName()
			tag := string(name)
			if blockedHTMLContent[tag] {
				if skipStack > 0 {
					skipStack--
				}
				continue
			}
			if blockTags[tag] {
				emitBreak()
			}
		case html.SelfClosingTagToken:
			name, _ := tok.TagName()
			tag := string(name)
			if blockTags[tag] {
				emitBreak()
			}
		case html.CommentToken, html.DoctypeToken:
			// Drop.
		}
	}
}

// collapseWhitespace normalises whitespace in the simplified output:
//   - sequences of intra-paragraph whitespace → single space
//   - sequences of newlines → at most 2 (one blank line between
//     paragraphs)
//   - trim leading + trailing whitespace
func collapseWhitespace(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	var (
		newlines int  // consecutive '\n' seen
		space    bool // pending space (collapsed run of horizontal ws)
		written  bool // anything written to out yet
	)
	flushSpace := func() {
		if space && written && newlines == 0 {
			out.WriteByte(' ')
		}
		space = false
	}
	flushNewlines := func() {
		if newlines > 0 && written {
			if newlines >= 2 {
				out.WriteString("\n\n")
			} else {
				out.WriteByte('\n')
			}
		}
		newlines = 0
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t':
			space = true
		case '\n':
			newlines++
			space = false
		case '\r':
			// CR alone or part of CRLF — treat as newline.
			newlines++
			space = false
		default:
			flushNewlines()
			flushSpace()
			out.WriteByte(c)
			written = true
		}
	}
	return strings.TrimSpace(out.String())
}
