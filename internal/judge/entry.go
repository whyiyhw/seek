// entry.go — the input contract (PRD §3.2): one JSON object per line,
// {id, text, meta}. `id` is the CALLER's; seek never generates one (a
// generated id is state, and the judge face is zero-state). `meta` is
// passed through verbatim — this package never inspects its shape, which
// is why it is json.RawMessage and not a map.
package judge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Item is one parsed input line.
type Item struct {
	// ID is the caller's identifier, echoed back on the output row.
	// Never synthesised, never rewritten.
	ID string `json:"id"`
	// Text is the content under judgement, plain text.
	Text string `json:"text"`
	// Meta is opaque caller metadata, re-emitted byte-for-byte. Raw so
	// there is no possible "helpful normalisation" step.
	Meta json.RawMessage `json:"meta,omitempty"`
}

// Entry is one input line as the batch consumes it: either a usable
// Item, or Err explaining why the line is unusable. A line-level error
// stays a line-level error — it becomes one error ROW, never a batch
// failure (PRD §3.3 invariant 5).
type Entry struct {
	Item Item
	// Err is non-empty when the line itself could not be used
	// (invalid JSON, missing/empty id, missing/empty text). The row it
	// produces carries this text verbatim.
	Err string
}

// ParseEntries parses a JSONL blob into entries. Blank lines are
// skipped (a trailing newline is not an input line); every other line
// yields an Entry — a bad line yields one with Err set, so the caller's
// row count always matches the number of non-blank input lines it fed
// in. A UTF-8 BOM on the first line is tolerated.
func ParseEntries(data []byte) []Entry {
	lines := strings.Split(string(data), "\n")
	out := make([]Entry, 0, len(lines))
	first := true
	for _, line := range lines {
		s := strings.TrimSpace(line)
		if first {
			s = strings.TrimPrefix(s, "\ufeff")
			s = strings.TrimSpace(s)
			first = false
		}
		if s == "" {
			continue
		}
		out = append(out, parseEntry(s))
	}
	return out
}

// ParseEntry parses one line. Exposed so a caller streaming lines from
// its own reader can feed Batch without buffering the whole file.
func ParseEntry(line []byte) Entry { return parseEntry(string(line)) }

func parseEntry(line string) Entry {
	var raw struct {
		ID   *string         `json:"id"`
		Text *string         `json:"text"`
		Meta json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		// Deliberately no id salvage: a line we could not decode is a
		// line we cannot attribute, and guessing an id from a fragment
		// would corrupt the caller's join.
		return Entry{Err: "invalid JSON line: " + err.Error()}
	}
	item := Item{}
	if raw.ID != nil {
		item.ID = strings.TrimSpace(*raw.ID)
	}
	if raw.Text != nil {
		item.Text = *raw.Text
	}
	item.Meta = raw.Meta

	switch {
	case item.ID == "":
		return Entry{Item: item, Err: "missing id (id is required and caller-owned; seek never generates one)"}
	case strings.TrimSpace(item.Text) == "":
		return Entry{Item: item, Err: fmt.Sprintf("entry %s: empty text", item.ID)}
	}
	return Entry{Item: item}
}
