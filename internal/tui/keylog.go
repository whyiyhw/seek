package tui

// Opt-in raw-key diagnostic logger (debug aid, not a feature).
//
// Set SEEK_KEYLOG=<file> before starting seek and every tea.KeyMsg is
// appended with a timestamp, BEFORE any routing decides what to do with
// it. Used to diagnose "keys arrive as the wrong thing" bugs — e.g. a
// terminal/IME bridge delivering Enter as a \n character event instead
// of VK_RETURN — where the symptom (typing works, backspace dead,
// Enter inserts a newline) is only explainable at the input layer.
//
// Zero cost when the env var is unset: keylogFile stays nil and
// logKeyMsg returns immediately.

import (
	"fmt"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var (
	keylogOnce sync.Once
	keylogFile *os.File
)

func keylogInit() {
	if path := os.Getenv("SEEK_KEYLOG"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			keylogFile = f
			fmt.Fprintf(f, "--- keylog start %s ---\n", time.Now().Format(time.RFC3339))
		}
	}
}

// logKeyMsg records one raw key event. String() is the canonical key
// name seek's keymap resolves against; Type+Runes are the raw fields, so
// a "\n delivered as runes" shows up as Type=KeyRunes Runes=[a] with
// String()= "\n" — distinguishable from Type=KeyEnter "enter".
func logKeyMsg(msg tea.KeyMsg) {
	if keylogFile == nil {
		keylogOnce.Do(keylogInit)
		if keylogFile == nil {
			return
		}
	}
	fmt.Fprintf(keylogFile, "%s type=%d runes=%q str=%q paste=%v\n",
		time.Now().Format("15:04:05.000"), msg.Type, string(msg.Runes), msg.String(), msg.Paste)
}
