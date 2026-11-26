package main

import (
	"os"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestResolveTitle_FallbackChain pins the single precedence rule shared
// by all three list surfaces: human rename > generated header title >
// derived first user line. The three-way fixture lives in one session
// store so the test proves the chain degrades to the exact derivation
// that predates generated titles.
func TestResolveTitle_FallbackChain(t *testing.T) {
	dir := t.TempDir()
	st, err := session.NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("m", "/tmp", "", false, false)
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "帮我看看 WebSocket 重连"}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, header, override, want string
	}{
		{"both empty → derived", "", "", "帮我看看 WebSocket 重连"},
		{"header only", "修复重连逻辑", "", "修复重连逻辑"},
		{"override beats header", "修复重连逻辑", "人工改名", "人工改名"},
		{"override only", "", "人工改名", "人工改名"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := session.ResolveTitle(st.Dir(), sess.ID, tc.header, tc.override); got != tc.want {
				t.Fatalf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPrintSessionList_ShowsTitle captures stdout around printSessionList
// to pin the new TITLE column and the header-title fallback, without
// touching the real home directory (sessmeta.Open tolerates a missing
// ~/.seek/serve side file — best-effort read).
func TestPrintSessionList_ShowsTitle(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir()) // isolate sessmeta + session store
	st, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("deepseek-chat", "/tmp", "", false, false)
	sess.Title = "修复 WebSocket 重连"
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "原始首行"}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}

	out := capturePrintSessionList(t, func() error { return printSessionList(st) })
	if !strings.Contains(out, "TITLE") {
		t.Fatalf("header missing TITLE column: %s", out)
	}
	if !strings.Contains(out, "修复 WebSocket 重连") {
		t.Fatalf("generated title not shown: %s", out)
	}
	if strings.Contains(out, "原始首行") {
		t.Fatalf("derived fallback leaked past generated title: %s", out)
	}
}

func capturePrintSessionList(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	got := <-done
	_ = r.Close()
	if runErr != nil {
		t.Fatalf("printSessionList: %v", runErr)
	}
	return got
}
