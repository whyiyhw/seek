package pricing

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// toolLedgerRows are the ledger rows that price non-model constants
// (search/ASR/TTS/parse). They participate in staleness patrols but
// never in the orphan check, which is about model ids.
var toolLedgerRows = map[string]bool{
	"tool:search": true,
	"tool:asr":    true,
	"tool:tts":    true,
	"tool:parse":  true,
}

// TestLedger_ParityWithStandardRates pins the 1:1 invariant: every
// priced card has a ledger row and vice versa (tools rows excepted).
// Adding a standardRates entry without a verification row fails here —
// that is the enforcement that keeps the ledger real rather than
// advisory.
func TestLedger_ParityWithStandardRates(t *testing.T) {
	for id := range standardRates {
		if _, ok := verifiedOn[id]; !ok {
			t.Errorf("standardRates entry %q has no verifiedOn ledger row", id)
		}
	}
	for id := range verifiedOn {
		if toolLedgerRows[id] {
			continue
		}
		if _, ok := standardRates[id]; !ok {
			t.Errorf("verifiedOn row %q has no standardRates card", id)
		}
	}
	for id := range toolLedgerRows {
		if _, ok := verifiedOn[id]; !ok {
			t.Errorf("expected tools ledger row %q is missing", id)
		}
	}
}

// TestLedger_FieldValidity checks each row parses as a real date (not
// in the future), an https URL, and a known currency. A typo like
// "2026-11-32" or "usd" would otherwise blind the patrol silently.
func TestLedger_FieldValidity(t *testing.T) {
	now := time.Now().UTC()
	for id, v := range verifiedOn {
		when, err := time.Parse("2006-01-02", v.VerifiedOn)
		if err != nil {
			t.Errorf("%s: VerifiedOn %q is not YYYY-MM-DD: %v", id, v.VerifiedOn, err)
			continue
		}
		if when.After(now.AddDate(0, 0, 1)) {
			t.Errorf("%s: VerifiedOn %q is in the future", id, v.VerifiedOn)
		}
		if !strings.HasPrefix(v.SourceURL, "https://") || strings.ContainsAny(v.SourceURL, " \t") {
			t.Errorf("%s: SourceURL %q must be https and whitespace-free", id, v.SourceURL)
		}
		if v.Currency != "USD" && v.Currency != "CNY" {
			t.Errorf("%s: Currency %q must be USD or CNY", id, v.Currency)
		}
	}
}

// auditLineRe mirrors the parse contract in scripts/price-audit.sh:
// one entry per line, fixed field order. The script depends on the
// textual shape; this test re-reads the SOURCE with the same shape so
// a gofmt/reformat that would blind the bash patrol fails the Go
// suite first (wire-format-string discipline: once something parses
// it, the format is a contract).
var auditLineRe = regexp.MustCompile(`^\s*"([^"]+)":\s*\{VerifiedOn:\s*"(\d{4}-\d{2}-\d{2})",\s*SourceURL:\s*"(https://[^"]+)",\s*Currency:\s*"(USD|CNY)"\},\s*$`)

func TestLedger_SourceParseableByAuditScript(t *testing.T) {
	src, err := os.ReadFile("ledger.go")
	if err != nil {
		t.Fatalf("reading ledger.go: %v", err)
	}
	parsed := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		m := auditLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id := m[1]
		if parsed[id] {
			t.Errorf("ledger row %q spans or repeats lines — audit script sees one-line entries only", id)
		}
		v, ok := verifiedOn[id]
		if !ok {
			t.Errorf("source line for %q does not match any verifiedOn entry", id)
			continue
		}
		if m[2] != v.VerifiedOn || m[3] != v.SourceURL || m[4] != v.Currency {
			t.Errorf("source line for %q disagrees with the map value: line=%q map=%+v", id, line, v)
		}
		parsed[id] = true
	}
	if len(parsed) != len(verifiedOn) {
		t.Errorf("audit script would parse %d rows, ledger has %d — some entry broke the one-line format", len(parsed), len(verifiedOn))
	}
}
