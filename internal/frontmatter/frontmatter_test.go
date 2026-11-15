package frontmatter

import (
	"errors"
	"strings"
	"testing"
)

func TestSplit_ScalarAndQuoted(t *testing.T) {
	fields, lists, body, err := Split([]byte("---\nname: my-thing\ndescription: \"has: colon\"\nmodel: 'glm'\n---\n\nBody here.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if fields["name"] != "my-thing" {
		t.Errorf("name = %q", fields["name"])
	}
	if fields["description"] != "has: colon" {
		t.Errorf("quoted scalar with colon = %q", fields["description"])
	}
	if fields["model"] != "glm" {
		t.Errorf("single-quoted scalar = %q", fields["model"])
	}
	if len(lists) != 0 {
		t.Errorf("unexpected lists: %v", lists)
	}
	if body != "Body here.\n" {
		t.Errorf("body = %q", body)
	}
}

func TestSplit_ListShapes(t *testing.T) {
	// Inline list + block list in one header.
	fields, lists, _, err := Split([]byte("---\ntools: [read, \"grep\", bash]\nsteps:\n  - one\n  - two\n---\nx"))
	if err != nil {
		t.Fatal(err)
	}
	if got := lists["tools"]; strings.Join(got, "|") != "read|grep|bash" {
		t.Errorf("inline list = %v", got)
	}
	if got := lists["steps"]; strings.Join(got, "|") != "one|two" {
		t.Errorf("block list = %v", got)
	}
	if _, ok := fields["tools"]; ok {
		t.Error("inline-list key leaked into scalars")
	}
}

func TestSplit_BlockScalar(t *testing.T) {
	fields, _, _, err := Split([]byte("---\nnote: |\n  line 1\n  line 2\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if fields["note"] != "line 1\nline 2" {
		t.Errorf("block scalar = %q", fields["note"])
	}
}

func TestSplit_StructuralErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"no frontmatter", "# just markdown\n", ErrNoFrontmatter},
		{"unterminated", "---\nname: x\n", ErrUnterminated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := Split([]byte(c.in))
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestSplit_BOMAndBlankTolerance(t *testing.T) {
	// BOM + blank lines before the opener must still parse — the
	// Windows-editor pathology the skill parser has always absorbed.
	fields, _, body, err := Split([]byte("\ufeff\n\n---\nname: bom\n---\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if fields["name"] != "bom" || body != "body" {
		t.Errorf("fields=%v body=%q", fields, body)
	}
}

// TestSplit_SkillDialectParity locks the reason this package exists:
// whatever shape a SKILL.md accepts, an agents/<name>.md accepts too.
// The canonical shape coverage lives in internal/skill's Parse tests;
// this asserts the shared Split produces both maps from a kitchen-sink
// header.
func TestSplit_SkillDialectParity(t *testing.T) {
	header := "---\n" +
		"name: verifier\n" +
		"description: Runs tests\n" +
		"keywords: [a, b]\n" +
		"allowed-tools:\n  - read\n  - bash\n" +
		"multi: |\n  one\n  two\n" +
		"# comment\n" +
		"---\n" +
		"role prompt"
	fields, lists, body, err := Split([]byte(header))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"name":        "verifier",
		"description": "Runs tests",
		"multi":       "one\ntwo",
	} {
		if fields[k] != want {
			t.Errorf("fields[%s] = %q, want %q", k, fields[k], want)
		}
	}
	if strings.Join(lists["keywords"], ",") != "a,b" || strings.Join(lists["allowed-tools"], ",") != "read,bash" {
		t.Errorf("lists = %v", lists)
	}
	if body != "role prompt" {
		t.Errorf("body = %q", body)
	}
}
