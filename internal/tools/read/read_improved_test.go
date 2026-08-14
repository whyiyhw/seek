package read

// L1 spec tests for the read-tool improvements — see
// docs/test-plan-read-tool.md §1 (I1–I4) and §5.
//
// These tests encode the IMPROVED behaviour and MUST FAIL on the baseline
// implementation; they turn green when the improvements land (Phase 3).
// Do not edit them to match current behaviour — they are the spec.
//
// Spec decisions pinned here (the implementation must match exactly):
//   - I1 EOF marker: the header ends with ", EOF at line N" whenever the
//     scan reached EOF, so the model can distinguish "file ends here"
//     from "more pages exist" without a probing read.
//   - I2 whole-read: files with ≤ 200 lines are emitted in full in one
//     call regardless of any limit, so small files are always complete
//     and never carry a TRUNCATED notice.
//   - I4 byte cap: a read result stays ≤ 64 KiB; over-long single lines
//     are elided in-band with a marker containing "elided" instead of
//     failing the whole read (baseline: bufio token-too-long hard error).
//   - I3 observation: fsobserve marks a file observed only after a FULL
//     read; a partial read must not vouch for the whole file, so the
//     write guard still refuses a blind whole-file overwrite.
//
// Deliberately NOT here (cannot compile against the baseline — Phase 3):
//   - TestRead_LimitConfigurable (needs the read.maxLimit config key).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/fsobserve"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools/write"
)

const (
	specWholeReadMax = 200       // I2: files ≤ this many lines are read whole
	specMaxBytes     = 64 * 1024 // I4: read result byte cap
)

// testObservedTool returns a read tool with a Yolo policy (reads and
// writes inside dir are always permitted) plus its observer store.
func testObservedTool(t *testing.T) (Tool, *fsobserve.Store, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := permission.New(dir, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	obs := fsobserve.New()
	return New(p).WithObserver(obs), obs, dir
}

// writeLines writes n numbered lines ("line001\n" …) into dir/f.txt.
func writeLines(t *testing.T, dir string, n int) string {
	t.Helper()
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "line%03d\n", i+1)
	}
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeLongLines writes n lines with ~200-byte content each, so the file
// exceeds the whole-read size threshold (32 KiB) and limit/offset
// windowing actually applies. Used by the I3 tests: with a short-line
// fixture the whole-read path would ignore limit entirely and the
// "partial read" half of the test would vacuously observe the file.
func writeLongLines(t *testing.T, dir string, n int) string {
	t.Helper()
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "key_%03d = \"value-%d-%s\" // padding padding padding padding\n",
			i+1, i+1, strings.Repeat("x", 170))
	}
	p := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustArgs(t *testing.T, a map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// headerOf returns the first line of a read result (the header).
func headerOf(out string) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		return out[:i]
	}
	return out
}

// I1 — the header must report EOF at the final line when the scan
// reached the end of the file.
func TestRead_HeaderReportsEOFAtFinalLine(t *testing.T) {
	tool, _, dir := testObservedTool(t)
	p := writeLines(t, dir, 137)

	out, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ", EOF at line 137") {
		t.Errorf("header must report EOF at the final line so the model can stop paging (I1):\n%s", headerOf(out))
	}
	if !strings.Contains(out, "137 lines emitted") {
		t.Errorf("expected all 137 lines emitted:\n%s", out)
	}
}

// I2 — a file within the whole-read threshold comes back in one call,
// complete, with no TRUNCATED notice.
func TestRead_SmallFileReadWhole(t *testing.T) {
	tool, _, dir := testObservedTool(t)
	p := writeLines(t, dir, 150) // above the old 50-line cap, within the threshold

	out, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "TRUNCATED") {
		t.Errorf("files ≤ %d lines must be read whole, no TRUNCATED (I2):\n%s", specWholeReadMax, headerOf(out))
	}
	if !strings.Contains(out, "150 lines emitted") {
		t.Errorf("expected all 150 lines emitted in one call:\n%s", out)
	}
}

// I4 — a 2 MiB single line must not hard-fail the read (baseline: scan
// token-too-long error); it is elided in-band and the result stays
// within the byte cap.
func TestRead_ByteCapElidesLongLines(t *testing.T) {
	tool, _, dir := testObservedTool(t)
	p := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 2*1024*1024)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p}))
	if err != nil {
		t.Fatalf("over-long line must not fail the read (I4): %v", err)
	}
	if !strings.Contains(out, "elided") {
		t.Errorf("over-long line must carry an in-band elision marker (I4):\n%s", headerOf(out))
	}
	if len(out) > specMaxBytes+1024 {
		t.Errorf("read output must stay ≤ %d bytes, got %d (I4)", specMaxBytes, len(out))
	}
}

// I4 — a 1.2 MiB single line (minified bundle shape) must not hard-error.
func TestRead_MinifiedLine_NoHardError(t *testing.T) {
	tool, _, dir := testObservedTool(t)
	p := filepath.Join(dir, "min.js")
	if err := os.WriteFile(p, []byte("var a="+strings.Repeat("1", 1200*1024)+";\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p}))
	if err != nil {
		t.Fatalf("minified single-line file must not hard-error (I4): %v", err)
	}
	if !strings.Contains(out, "elided") {
		t.Errorf("expected in-band elision marker:\n%s", headerOf(out))
	}
}

// I1 — a probe past EOF returns 0 lines but still reports the final
// line, so the model can stop instead of guessing.
func TestRead_OffsetBeyondEOF_ReportsFinalLine(t *testing.T) {
	tool, _, dir := testObservedTool(t)
	p := writeLines(t, dir, 50)

	out, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p, "offset": 1000}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0 lines emitted") {
		t.Errorf("expected 0 lines emitted:\n%s", headerOf(out))
	}
	if !strings.Contains(out, ", EOF at line 50") {
		t.Errorf("a probe past EOF must still report the final line so the model can stop (I1):\n%s", headerOf(out))
	}
}

// I3 — a partial read must NOT mark the file observed; a full read must.
// Fixture uses long lines (> 16 KiB total) so the file is above the
// whole-read threshold and limit windowing actually applies.
func TestRead_ObservedOnlyOnFullRead(t *testing.T) {
	tool, obs, dir := testObservedTool(t)
	p := writeLongLines(t, dir, 200)

	// 10 of 200 lines — a peek, not a view.
	if _, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p, "limit": 10})); err != nil {
		t.Fatal(err)
	}
	if got := obs.Check(p); got != fsobserve.StatusUnseen {
		t.Fatalf("partial read must not mark the file observed (I3): Check = %v, want StatusUnseen", got)
	}

	// Full read (200 lines == default limit, scan reaches EOF) — now the
	// model has seen it all.
	if _, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p})); err != nil {
		t.Fatal(err)
	}
	if got := obs.Check(p); got != fsobserve.StatusOK {
		t.Fatalf("full read must mark the file observed (I3): Check = %v, want StatusOK", got)
	}
}

// I3 — integration: a whole-file write after a PARTIAL read is refused
// (baseline: silently allowed — the defect), and allowed after a FULL
// read. Long-line fixture so limit windowing applies (see above).
func TestWrite_PartialReadDenied_Integration(t *testing.T) {
	dir := t.TempDir()
	p, err := permission.New(dir, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	obs := fsobserve.New()
	r := New(p).WithObserver(obs)
	w := write.New(p).WithObserver(obs)
	target := writeLongLines(t, dir, 200)

	// Model read 10 of 200 lines, then attempts a whole-file write.
	if _, err := r.Execute(context.Background(), mustArgs(t, map[string]any{"path": target, "limit": 10})); err != nil {
		t.Fatal(err)
	}
	_, werr := w.Execute(context.Background(), mustArgs(t, map[string]any{"path": target, "content": "clobbered"}))
	if werr == nil || !strings.Contains(werr.Error(), "write refused") {
		t.Fatalf("whole-file write after a PARTIAL read must be refused (I3); err = %v", werr)
	}

	// After a full read the same write is legal.
	if _, err := r.Execute(context.Background(), mustArgs(t, map[string]any{"path": target})); err != nil {
		t.Fatal(err)
	}
	if _, werr := w.Execute(context.Background(), mustArgs(t, map[string]any{"path": target, "content": "clobbered"})); werr != nil {
		t.Fatalf("whole-file write after a FULL read must be allowed (I3): %v", werr)
	}
}

// ---- I3×I4 interaction (landed after the A/B battery) ----
//
// The tests below pin the invariant the original I3 wording already
// claimed but the first implementation missed: "only a read that covered
// the WHOLE file vouches for it". A read can cover the whole file (scan
// reached EOF, nothing truncated) while the MODEL still has not seen
// every byte, because I4 elided parts of the result — in-band (an
// over-long line) or at the result level (the 64 KiB middle cut). Such
// a read must record an elided note, not an observation.

// writeHugeLines writes n lines of ~len bytes each, so the file is above
// the whole-read threshold and every line is under the 1 KiB in-band
// cap — the only elision that can fire is the result-level one.
func writeHugeLines(t *testing.T, dir string, n, lineLen int) string {
	t.Helper()
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, "line %04d %s\n", i, strings.Repeat("y", lineLen-10))
	}
	p := filepath.Join(dir, "huge.txt")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// I3×I4 — a read that reaches EOF with nothing truncated, but whose
// result exceeds the 64 KiB cap and gets middle-elided, must NOT vouch
// for the file: the model never saw the elided middle.
func TestRead_ResultElidedFullReadDoesNotVouch(t *testing.T) {
	tool, obs, dir := testObservedTool(t)
	// 100 × 800 B ≈ 78 KiB: above the 32 KiB whole-read threshold, under
	// the 200-line default limit (so the default read reaches EOF), each
	// line under the 1 KiB line cap (no in-band elision) — the result
	// alone exceeds 64 KiB and triggers the middle cut.
	p := writeHugeLines(t, dir, 100, 800)

	out, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "read output elided") {
		t.Fatalf("precondition: expected the result-level elision marker:\n%s", headerOf(out))
	}
	if got := obs.Check(p); got != fsobserve.StatusElided {
		t.Fatalf("a full read whose result was elided must not vouch for the file (I3×I4): Check = %v, want StatusElided", got)
	}
}

// I3×I4 — the in-band variant: a small file (whole-read path) whose
// single line exceeds the 1 KiB line cap. The scan reaches EOF and
// nothing is "truncated", but the model saw 1 KiB of a 20 KiB file.
func TestRead_LineElidedFullReadDoesNotVouch(t *testing.T) {
	tool, obs, dir := testObservedTool(t)
	p := filepath.Join(dir, "min.txt")
	if err := os.WriteFile(p, []byte(strings.Repeat("z", 20*1024)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": p}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "of this line elided") {
		t.Fatalf("precondition: expected the in-band elision marker:\n%s", headerOf(out))
	}
	if got := obs.Check(p); got != fsobserve.StatusElided {
		t.Fatalf("a whole-file read with an elided line must not vouch for the file (I3×I4): Check = %v, want StatusElided", got)
	}
}

// I3×I4 — integration: after an elided full read the whole-file write is
// refused, and the refusal must NOT tell the model to read again (that
// can never clear an elided note — the same parts get elided); it must
// point at `edit` instead.
func TestWrite_ElidedReadDenied_Integration(t *testing.T) {
	dir := t.TempDir()
	p, err := permission.New(dir, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	obs := fsobserve.New()
	r := New(p).WithObserver(obs)
	w := write.New(p).WithObserver(obs)
	target := writeHugeLines(t, dir, 100, 800)

	if _, err := r.Execute(context.Background(), mustArgs(t, map[string]any{"path": target})); err != nil {
		t.Fatal(err)
	}
	_, werr := w.Execute(context.Background(), mustArgs(t, map[string]any{"path": target, "content": "clobbered"}))
	if werr == nil {
		t.Fatal("whole-file write after an ELIDED read must be refused (I3×I4)")
	}
	if !strings.Contains(werr.Error(), "elided") {
		t.Errorf("refusal must name the elision, not a generic unread-file message: %v", werr)
	}
	if !strings.Contains(werr.Error(), "edit") {
		t.Errorf("refusal must point at `edit` as the recovery, not at re-reading: %v", werr)
	}
	got, _ := os.ReadFile(target)
	if strings.HasPrefix(string(got), "clobbered") {
		t.Error("file was modified despite the refusal")
	}
}
