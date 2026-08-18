package tui

// Opt-in raw-key diagnostic logger (debug aid, not a feature).
//
// Set SEEK_KEYLOG=<file> before starting seek and every tea.KeyPressMsg is
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

	tea "charm.land/bubbletea/v2"
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
// name seek's keymap resolves against; Code+Text are the raw fields, so
// a "\n delivered as text" shows up as Code=0x0a Text="\n" with
// String()="\n" — distinguishable from Code=KeyEnter "enter".
func logKeyMsg(msg tea.KeyPressMsg) {
	if keylogFile == nil {
		keylogOnce.Do(keylogInit)
		if keylogFile == nil {
			return
		}
	}
	fmt.Fprintf(keylogFile, "%s code=%#x text=%q str=%q mod=%d\n",
		time.Now().Format("15:04:05.000"), msg.Code, msg.Text, msg.String(), msg.Mod)
}
