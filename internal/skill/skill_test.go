package skill

import (
	"strings"
	"testing"
)

func TestParse_HappyPath(t *testing.T) {
	in := []byte(`---
name: go-test-runner
description: Use when the user asks to run, debug, or analyze Go tests.
---

# Body
Step 1.
Step 2.
`)
	got, err := Parse(in, "x.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "go-test-runner" {
		t.Errorf("Name = %q", got.Name)
	}
	if !strings.HasPrefix(got.Description, "Use when") {
		t.Errorf("Description = %q", got.Description)
	}
	if !strings.HasPrefix(got.Body, "# Body") {
		t.Errorf("Body should start with header, got %q", got.Body)
	}
	if got.Source != "x.md" {
		t.Errorf("Source = %q", got.Source)
	}
}

func TestParse_MissingFrontmatter(t *testing.T) {
	_, err := Parse([]byte("just markdown, no header\n"), "x")
	if err == nil || !strings.Contains(err.Error(), "missing frontmatter") {
		t.Errorf("err = %v", err)
	}
}

func TestParse_UnterminatedFrontmatter(t *testing.T) {
	_, err := Parse([]byte("---\nname: foo\n"), "x")
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("err = %v", err)
	}
}

func TestParse_MissingName(t *testing.T) {
	in := []byte("---\ndescription: hi\n---\nbody\n")
	_, err := Parse(in, "x")
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Errorf("err = %v", err)
	}
}

func TestParse_InvalidName(t *testing.T) {
	in := []byte("---\nname: NotKebab\ndescription: x\n---\nbody\n")
	_, err := Parse(in, "x")
	if err == nil || !strings.Contains(err.Error(), "kebab-case") {
		t.Errorf("err = %v", err)
	}
}

func TestParse_MissingDescription(t *testing.T) {
	in := []byte("---\nname: foo\n---\nbody\n")
	_, err := Parse(in, "x")
	if err == nil || !strings.Contains(err.Error(), "description") {
		t.Errorf("err = %v", err)
	}
}

func TestParse_QuotedValuesAndExtraFields(t *testing.T) {
	// Quoted description (humans do this when their text contains a
	// colon) and an unknown extra field — should be tolerated.
	in := []byte(`---
name: my-skill
description: "Use when: the user does X"
version: 1
---

body`)
	got, err := Parse(in, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "Use when: the user does X" {
		t.Errorf("Description = %q", got.Description)
	}
}

func TestParse_TolerantToBOMAndLeadingBlankLines(t *testing.T) {
	in := []byte("\ufeff\n\n---\nname: foo\ndescription: d\n---\nbody\n")
	if _, err := Parse(in, "x"); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestParse_AllowedToolsInlineList(t *testing.T) {
	// PRD v2 §4.1 — recognise the inline `[a, b, c]` form because some
	// authors prefer it (it's still valid YAML 1.2). The state machine
	// must tolerate trailing whitespace and quoted entries.
	in := []byte(`---
name: foo
description: d
allowed-tools: [Read, "Grep", Bash]
---

body`)
	got, err := Parse(in, "x")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Read", "Grep", "Bash"}
	if len(got.AllowedTools) != len(want) {
		t.Fatalf("AllowedTools = %v, want %v", got.AllowedTools, want)
	}
	for i, v := range want {
		if got.AllowedTools[i] != v {
			t.Errorf("AllowedTools[%d] = %q, want %q", i, got.AllowedTools[i], v)
		}
	}
}

func TestParse_AllowedToolsBlockList(t *testing.T) {
	// The Anthropic Agent Skills spec example uses the block-style list.
	// The body MUST start exactly at the first non-list line — the parser
	// must not swallow content into the list by mistake.
	in := []byte(`---
name: foo
description: d
allowed-tools:
  - Read
  - Grep
  - Bash
---

# Body header
not a list item
`)
	got, err := Parse(in, "x")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Read", "Grep", "Bash"}
	if len(got.AllowedTools) != len(want) {
		t.Fatalf("AllowedTools = %v, want %v", got.AllowedTools, want)
	}
	if !strings.HasPrefix(got.Body, "# Body header") {
		t.Errorf("body bled into frontmatter; got body=%q", got.Body)
	}
}

func TestParse_DescriptionBlockScalar(t *testing.T) {
	// `description: |` literal block — preserves line breaks. PRD §4.1
	// example uses this for multi-line descriptions. The parser must
	// stop the block when indentation drops to ≤ the key's column.
	in := []byte(`---
name: foo
description: |
  When the user asks how to do X with library Y,
  follow these steps carefully.
version: 1.0.0
---

body`)
	got, err := Parse(in, "x")
	if err != nil {
		t.Fatal(err)
	}
	wantDesc := "When the user asks how to do X with library Y,\nfollow these steps carefully."
	if got.Description != wantDesc {
		t.Errorf("Description = %q\nwant %q", got.Description, wantDesc)
	}
	if got.Version != "1.0.0" {
		t.Errorf("Version = %q after block scalar; the parser likely consumed past the block", got.Version)
	}
}

func TestParse_RecognisedOptionalFields(t *testing.T) {
	// PRD v2 §4.1 — version / license / author are optional scalars
	// that loader records (no behavioural effect in v2, but they show
	// up in `seek skill status`).
	in := []byte(`---
name: foo
description: d
version: 2.1.0
license: MIT
author: foo@example.com
---

body`)
	got, err := Parse(in, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "2.1.0" {
		t.Errorf("Version = %q", got.Version)
	}
	if got.License != "MIT" {
		t.Errorf("License = %q", got.License)
	}
	if got.Author != "foo@example.com" {
		t.Errorf("Author = %q", got.Author)
	}
}

func TestParse_UnknownFieldsLandInExtra(t *testing.T) {
	// Forward-compat: when Anthropic adds new frontmatter fields, we
	// preserve them in Extra rather than erroring. `status` lets the
	// user see what we ignored.
	in := []byte(`---
name: foo
description: d
some-future-field: hello
---

body`)
	got, err := Parse(in, "x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Extra["some-future-field"] != "hello" {
		t.Errorf("Extra[some-future-field] = %q, want %q",
			got.Extra["some-future-field"], "hello")
	}
}

func TestSet_AddAndShadow(t *testing.T) {
	s := NewSet()
	if !s.Add(&Skill{Name: "a", Description: "first", Source: "1"}) {
		t.Fatal("first Add returned false")
	}
	if s.Add(&Skill{Name: "a", Description: "second", Source: "2"}) {
		t.Errorf("duplicate Add returned true; expected shadowed=false")
	}
	got := s.Get("a")
	if got.Source != "1" {
		t.Errorf("first writer didn't win: %+v", got)
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
}

func TestSet_Manifest(t *testing.T) {
	s := NewSet()
	s.Add(&Skill{Name: "alpha", Description: "use for A"})
	s.Add(&Skill{Name: "beta", Description: "use for B"})
	out := s.Manifest()
	for _, want := range []string{
		"# Available skills",
		"- alpha: use for A",
		"- beta: use for B",
		"call the `Skill` tool",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("manifest missing %q:\n%s", want, out)
		}
	}
}

func TestSet_ManifestEmptySetReturnsEmpty(t *testing.T) {
	if got := NewSet().Manifest(); got != "" {
		t.Errorf("empty set manifest = %q, want empty string", got)
	}
}
