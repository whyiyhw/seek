// reply.go — parsing the model's verdict reply.
//
// The prompt (prompt.go) asks for one bare JSON object; the 2026-11-24
// baseline (PRD §8.1-1) measured 100% compliance, which is exactly why
// ResponseFormat stays unwired (PRD §4 D4). Tolerance is therefore
// deliberately narrow — surround prose / ```json fences, case-insensitive
// verdict token, and a last-ditch regex sweep for a quoted verdict. A
// reply that survives none of those is NOT guessed at: it becomes the
// row's error, because a made-up verdict silently corrupts the caller's
// keep/drop piles while a visible error costs one retry.
package judge

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Reply is a parsed, validated verdict.
type Reply struct {
	Verdict string                     `json:"verdict"`
	Reasons []string                   `json:"reasons"`
	Signals map[string]json.RawMessage `json:"signals,omitempty"`
}

// errMalformedVerdict is the documented error text for a reply that has
// no usable verdict token (PRD §3.3's example row wording).
var errMalformedVerdict = errors.New("model returned malformed verdict")

// verdictToken catches a verdict that survived as text but not as JSON —
// e.g. the model wrapped the object in a code fence that lost a brace,
// or answered "VERDICT: KEEP". Anchored to the `verdict` key so a
// rubric word inside a reason can never be mistaken for the verdict.
var verdictToken = regexp.MustCompile(`"verdict"\s*:\s*"([A-Za-z]+)"`)

// looseVerdict catches the shape without quotes: `verdict: keep`.
var looseVerdict = regexp.MustCompile(`(?:^|[^A-Za-z])verdict\s*[:=]\s*[\x60"']?([A-Za-z]+)`)

func parseReply(content string) (Reply, error) {
	if strings.TrimSpace(content) == "" {
		return Reply{}, errors.New("model returned empty content")
	}

	obj, ok := extractObject(content)
	if ok {
		var raw struct {
			Verdict string                     `json:"verdict"`
			Reasons []string                   `json:"reasons"`
			Signals map[string]json.RawMessage `json:"signals"`
		}
		if err := json.Unmarshal([]byte(obj), &raw); err == nil {
			r := Reply{
				Verdict: normaliseVerdict(raw.Verdict),
				Reasons: trimReasons(raw.Reasons),
				Signals: raw.Signals,
			}
			if r.Verdict == "" {
				return Reply{}, fmt.Errorf("%w: verdict %q not in keep|drop|gray", errMalformedVerdict, raw.Verdict)
			}
			if len(r.Reasons) == 0 {
				return Reply{}, errors.New("model returned no reasons (reasons must be non-empty short sentences)")
			}
			if len(r.Signals) == 0 {
				r.Signals = nil // omitempty keeps a signal-less row byte-clean
			}
			return r, nil
		}
		// Fall through: the object was found but did not decode. A
		// verdict regex may still rescue the one load-bearing field.
	}

	if v := regexVerdict(content); v != "" {
		// The verdict survived as text but the reasons did not decode.
		// Deliberately do NOT synthesise reasons: the "short sentences"
		// contract is what makes reasons auditable, and a fabricated
		// reason is worse than a visible error.
		return Reply{}, fmt.Errorf("%w: reply had verdict %q but no decodable reasons", errMalformedVerdict, v)
	}
	return Reply{}, fmt.Errorf("%w: %s", errMalformedVerdict, excerpt(content))
}

// normaliseVerdict maps a case/punctuation-tolerant token onto the
// three-value enum. Anything else returns "" (the caller errors).
func normaliseVerdict(v string) string {
	tok := strings.TrimFunc(strings.ToLower(strings.TrimSpace(v)), func(r rune) bool {
		return !unicode.IsLetter(r)
	})
	switch tok {
	case VerdictKeep:
		return VerdictKeep
	case VerdictDrop:
		return VerdictDrop
	case VerdictGray:
		return VerdictGray
	}
	return ""
}

func trimReasons(in []string) []string {
	out := make([]string, 0, len(in))
	for _, r := range in {
		if s := strings.TrimSpace(r); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// extractObject returns the first balanced {...} span in content,
// tolerating surrounding prose and code fences. Braces inside JSON
// strings are handled — a reason may legitimately contain one.
func extractObject(content string) (string, bool) {
	start := strings.IndexByte(content, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		c := content[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[start : i+1], true
			}
		}
	}
	return "", false
}

func regexVerdict(content string) string {
	if m := verdictToken.FindStringSubmatch(content); m != nil {
		if v := normaliseVerdict(m[1]); v != "" {
			return v
		}
	}
	if m := looseVerdict.FindStringSubmatch(content); m != nil {
		if v := normaliseVerdict(m[1]); v != "" {
			return v
		}
	}
	return ""
}

// excerpt bounds the error row: a 4KB runaway reply must not be pasted
// into the caller's JSONL as a 4KB error string.
func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= 80 {
		return fmt.Sprintf("%.120q", s)
	}
	return fmt.Sprintf("%q…", string([]rune(s)[:80]))
}
