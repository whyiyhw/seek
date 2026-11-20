package autopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestParseTasks_Plain(t *testing.T) {
	got, err := parseTasks(`[{"title":"A","prompt":"do a"},{"title":"B","prompt":"do b"}]`, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "task-1" || got[0].Title != "A" || got[1].Prompt != "do b" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseTasks_ToleratesFencesAndProse(t *testing.T) {
	content := "Sure! Here are the tasks:\n```json\n[{\"title\":\"X\",\"prompt\":\"do x\"}]\n```\nHope that helps."
	got, err := parseTasks(content, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "X" {
		t.Fatalf("should extract array from fenced+prose reply, got %+v", got)
	}
}

func TestParseTasks_ClampToMax(t *testing.T) {
	var arr string
	for i := range 20 {
		if i > 0 {
			arr += ","
		}
		arr += fmt.Sprintf(`{"title":"t%d","prompt":"p%d"}`, i, i)
	}
	got, _ := parseTasks("["+arr+"]", 5)
	if len(got) != 5 {
		t.Fatalf("clamp to max=5, got %d", len(got))
	}
}

func TestParseTasks_DropsEmptyPrompt(t *testing.T) {
	got, _ := parseTasks(`[{"title":"A","prompt":""},{"title":"B","prompt":"do b"}]`, 8)
	if len(got) != 1 || got[0].Title != "B" {
		t.Fatalf("empty-prompt task must be dropped, got %+v", got)
	}
}

func TestParseTasks_TitleFallback(t *testing.T) {
	got, _ := parseTasks(`[{"prompt":"fix the flaky test in foo_test.go"}]`, 8)
	if len(got) != 1 || got[0].Title == "" {
		t.Fatalf("missing title should fall back to first line of prompt, got %+v", got)
	}
}

func TestParseTasks_NoArray(t *testing.T) {
	if _, err := parseTasks("I cannot help with that.", 8); err == nil {
		t.Fatal("reply with no JSON array should error")
	}
}

// TestParseTasks_DoneWhen: v8 柱 R R2 — the decomposer's done_when
// rides through to Task.DoneWhen (trimmed, empties dropped, capped at
// maxDoneWhenPerTask); omission is tolerated (the verdict for such a
// task lands on doubt, by design).
func TestParseTasks_DoneWhen(t *testing.T) {
	got, err := parseTasks(`[{"title":"A","prompt":"do a","done_when":["  grep -n X y.go  ","","go test ./... passes "]}]`, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	dw := got[0].DoneWhen
	if len(dw) != 2 || dw[0] != "grep -n X y.go" || dw[1] != "go test ./... passes" {
		t.Fatalf("done_when = %#v, want trimmed 2 entries", dw)
	}

	// Omitted → nil, tolerated (verdict will be doubt, not an error).
	got, _ = parseTasks(`[{"title":"B","prompt":"do b"}]`, 8)
	if got[0].DoneWhen != nil {
		t.Fatalf("omitted done_when must stay nil, got %#v", got[0].DoneWhen)
	}

	// Over-cap → clamped, in order.
	var arr string
	for i := range 9 {
		if i > 0 {
			arr += ","
		}
		arr += fmt.Sprintf(`"dw%d"`, i)
	}
	got, _ = parseTasks(`[{"title":"C","prompt":"do c","done_when":[`+arr+`]}]`, 8)
	if len(got[0].DoneWhen) != maxDoneWhenPerTask || got[0].DoneWhen[0] != "dw0" {
		t.Fatalf("done_when must clamp to %d in order, got %#v", maxDoneWhenPerTask, got[0].DoneWhen)
	}
}

// Full round-trip through a fake DeepSeek backend (seek's standard test
// pattern: httptest server + WithBaseURL).
func TestDeepSeekDecomposer_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"[{\"title\":\"fix bug\",\"prompt\":\"fix the bug in x.go\",\"done_when\":[\"grep -n fix x.go\"]},{\"title\":\"add test\",\"prompt\":\"add a test\"}]"}}],"usage":{}}`)
	}))
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	d := NewDeepSeekDecomposer(c, "deepseek-v4-flash")
	tasks, err := d.Decompose(context.Background(), "fix and test x.go", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].Title != "fix bug" || tasks[1].Prompt != "add a test" {
		t.Fatalf("round-trip = %+v", tasks)
	}
	if len(tasks[0].DoneWhen) != 1 || tasks[0].DoneWhen[0] != "grep -n fix x.go" || tasks[1].DoneWhen != nil {
		t.Fatalf("round-trip done_when = %+v / %+v", tasks[0].DoneWhen, tasks[1].DoneWhen)
	}
}
