package skilltool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/skill"
)

func newSet(t *testing.T, sks ...*skill.Skill) *skill.Set {
	t.Helper()
	s := skill.NewSet()
	for _, sk := range sks {
		s.Add(sk)
	}
	return s
}

func TestSkill_ReturnsBodyForKnownName(t *testing.T) {
	set := newSet(t, &skill.Skill{
		Name:        "go-test-runner",
		Description: "tests",
		Body:        "1. Run go test.\n",
		Source:      "builtin:go-test-runner",
	})
	tool := New(set)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"go-test-runner"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"go-test-runner", "builtin:go-test-runner", "Run go test"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSkill_UnknownNameListsAvailable(t *testing.T) {
	set := newSet(t,
		&skill.Skill{Name: "alpha", Description: "a", Body: "", Source: "x"},
		&skill.Skill{Name: "beta", Description: "b", Body: "", Source: "y"},
	)
	tool := New(set)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"missing"}`))
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
	// The model relies on the available-list hint to self-correct; if
	// we ever drop it, tool calls turn into a loop.
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "beta") {
		t.Errorf("err = %v, want available-names hint", err)
	}
}

func TestSkill_NilSetReportsCleanly(t *testing.T) {
	tool := New(nil)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "no skills are loaded") {
		t.Errorf("err = %v", err)
	}
}

func TestSkill_RejectsEmptyName(t *testing.T) {
	tool := New(newSet(t))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("err = %v", err)
	}
}

func TestSkill_SchemaIsStable(t *testing.T) {
	// Schema must be a package-level constant — re-fetching it must
	// return the same backing bytes (pointer-equal slice header is
	// fine for cache purposes; we don't depend on it here, but we DO
	// depend on identical content turn over turn).
	a := Tool{}.Schema()
	b := Tool{}.Schema()
	if string(a) != string(b) {
		t.Errorf("schema content drift between calls")
	}
	// Sanity: schema must reference the `name` field, otherwise the
	// model has no idea what to send.
	if !strings.Contains(string(a), `"name"`) {
		t.Errorf("schema missing name field: %s", a)
	}
	// D1 guard (docs/prd/feature-code-review.md): the Skill tool stays
	// {name}-only. Per-skill parameters like /code-review's effort/flags
	// must live in the invoking slash command, NOT be lowered into this
	// schema — that would make the cached schema bytes polymorphic per
	// skill and break the prefix-cache invariant asserted above. If this
	// trips, you're adding a parameter that belongs in the caller.
	for _, banned := range []string{`"effort"`, `"fix"`, `"comment"`, `"args"`} {
		if strings.Contains(string(a), banned) {
			t.Errorf("Skill schema must stay name-only; found per-skill param %s: %s", banned, a)
		}
	}
}
