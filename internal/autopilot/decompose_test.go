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
	for i := 0; i < 20; i++ {
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

// Full round-trip through a fake DeepSeek backend (seek's standard test
// pattern: httptest server + WithBaseURL).
func TestDeepSeekDecomposer_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"[{\"title\":\"fix bug\",\"prompt\":\"fix the bug in x.go\"},{\"title\":\"add test\",\"prompt\":\"add a test\"}]"}}],"usage":{}}`)
	}))
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	d := NewDeepSeekDecomposer(c, "deepseek-chat")
	tasks, err := d.Decompose(context.Background(), "fix and test x.go", 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].Title != "fix bug" || tasks[1].Prompt != "add a test" {
		t.Fatalf("round-trip = %+v", tasks)
	}
}
