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
