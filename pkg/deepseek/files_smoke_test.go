package deepseek_test

// Real-API acceptance for card F4 (feature-deepseek-app-api §3.3):
// upload → reference → the model answers from the document; the same
// history keeps working after a session round-trip (resume); and the
// file_id's participation in the prefix cache is MEASURED rather than
// assumed (open question ② in the PRD; the reading lands in
// docs/pitfalls.md either way).
//
// Gated exactly like the ASR smoke and pkg/agent's cache e2e: skipped
// unless DEEPSEEK_API_KEY is set and not under -short, so CI and
// `go test ./...` stay hermetic and free.
//
//	DEEPSEEK_API_KEY=... in .env (gitignored), then
//	set -a && . ./.env && set +a
//	go test ./pkg/deepseek/ -run TestFiles_RealAPI -v
//
// Expected spend: one 2-page parse (¥1.5/千页 → ≈¥0.003) plus three
// short chat calls (well under ¥0.05 at list price).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// smokeSystemPrompt is deliberately long: DeepSeek's prefix cache
// operates on 64-token blocks, and a two-word system prompt could leave
// the shareable prefix below one block — a legitimate zero-hit reading
// that would look like "file_id breaks caching".
const smokeSystemPrompt = "You are a terse assistant used in an automated document-input test. " +
	"Answer strictly from the attached document. Never invent a value the document does not " +
	"state. When a question has a short factual answer, reply with that answer verbatim and " +
	"nothing else — no preamble, no markdown, no quoting of the question. If the document does " +
	"not contain the answer, say exactly: NOT IN DOCUMENT. Keep every reply under twenty words. " +
	"Do not apologise. Do not restate the question. Do not explain how you found the answer. " +
	"Do not mention that a document was attached. Stay in the language of the question."

// smokePDF builds a two-page, densely-texted PDF: enough unique body
// text that the parsed document dominates the prompt (the cache
// reading is only meaningful when the document's tokens outweigh the
// fixed system prefix), with two facts a reader can only get from the
// page. Hand-built so the test needs no fixture file and no external
// tool — xref offsets are computed, not guessed.
func smokePDF() []byte {
	var sb strings.Builder
	sb.WriteString("%PDF-1.4\n")

	page1 := []string{
		"ZEPHYR-42 FIELD MANUAL - SECTION ONE: MISSION PARAMETERS",
		"",
		"The launch code for the Zephyr-42 mission is KESTREL-7781.",
		"Chief engineer: Dr. Ines Vasquez. Deputy engineer: Tomas Berg.",
		"Primary telemetry band: 2.4 GHz. Backup band: 433 MHz.",
		"",
	}
	page1 = append(page1, fillerLines("Section one covers the mission profile, its constraints, and the roles of the launch crew.")...)

	// Section two is bulk plus a fact the assertions deliberately do NOT
	// lean on: on 2026-11-18 one run's parse exposed only page one (a
	// page-2-only fact came back NOT IN DOCUMENT while prompt_tokens
	// halved), so the smoke asserts page-1 facts only — see
	// docs/pitfalls.md "同一份 PDF 的解析结果并不总是完整的".
	page2 := []string{
		"ZEPHYR-42 FIELD MANUAL - SECTION TWO: CONTINGENCIES",
		"",
		"Emergency shutdown phrase: halt-and-seal.",
		"",
	}
	page2 = append(page2, fillerLines("Section two covers contingencies, abort criteria, and the recovery checklist.")...)

	stream1 := pdfTextStream(page1)
	stream2 := pdfTextStream(page2)

	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream1), stream1),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream2), stream2),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = sb.Len() // byte offset of "N 0 obj", which xref must match exactly
		fmt.Fprintf(&sb, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := sb.Len()
	fmt.Fprintf(&sb, "xref\n0 %d\n", len(objects)+1)
	sb.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&sb, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&sb, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(sb.String())
}

// fillerLines expands one topic sentence into enough body text to make
// the document dominate the prompt. Deterministic, no parentheses or
// backslashes (PDF literal-string escaping would only obscure the test).
func fillerLines(topic string) []string {
	lines := []string{topic}
	for i := 0; i < 28; i++ {
		lines = append(lines, fmt.Sprintf(
			"Note %02d: the crew rehearses this sequence until every step is committed to memory, "+
				"and the checklist is read aloud twice before any irreversible action is approved "+
				"by the range officer on duty.", i+1))
	}
	return lines
}

// pdfTextStream renders lines as a Helvetica text block; T* advances by
// the leading (TL) each iteration, so the page is a plain wall of text.
func pdfTextStream(lines []string) string {
	var b strings.Builder
	b.WriteString("BT\n/F1 9 Tf\n40 760 Td\n12 TL\n")
	for _, ln := range lines {
		b.WriteString("(" + strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(ln) + ") Tj\nT*\n")
	}
	b.WriteString("ET")
	return b.String()
}

// chatRound is one logged Chat call: the usage the provider reported,
// the reply text, and the wall-clock label used in the log table.
type chatRound struct {
	label string
	usage deepseek.Usage
	reply string
}

func (r chatRound) log(t *testing.T) {
	t.Helper()
	t.Logf("%-14s prompt=%-5d hit=%-5d miss=%-5d completion=%-4d cost=%s reply=%q",
		r.label, r.usage.PromptTokens, r.usage.PromptCacheHitTokens,
		r.usage.PromptCacheMissTokens, r.usage.CompletionTokens,
		pricing.FormatCost(pricing.Cost(deepseek.ModelV41Flash, pricing.CurrentTier(time.Now()), r.usage)),
		truncate(r.reply, 40))
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// TestFiles_RealAPI_UploadReferenceResumeCache is the card's real-API
// acceptance, in four phases:
//
//  1. upload → the id, page count (billing basis) and expiry are real;
//  2. reference → the model answers a fact that exists ONLY in the
//     parsed document (proves the parse, not just the id);
//  3. resume → the same history survives a JSON round-trip (what the
//     session JSONL does) and keeps referencing the document;
//  4. prefix cache → a second request whose prefix is request #1's
//     bytes reports prompt_cache_hit_tokens covering the document too,
//     measured against a no-document control arm.
func TestFiles_RealAPI_UploadReferenceResumeCache(t *testing.T) {
	if testing.Short() {
		t.Skip("real-API test; skipped under -short")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set; see this file's header for how to run the F4 real-API acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := deepseek.New(deepseek.WithAPIKey(key))

	// ---- 1. upload (the parse) ----
	pdf := smokePDF()
	fo, err := c.UploadFile(ctx, &deepseek.FileUploadRequest{
		Filename: "seek-f4-smoke.pdf",
		Data:     pdf,
		Purpose:  deepseek.PurposeTextRecognition,
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if fo.ID == "" {
		t.Fatal("upload returned an empty file id")
	}
	t.Logf("upload: id=%s pages=%d bytes=%d expires_at=%d parse_cost=%s",
		fo.ID, fo.PageCount, fo.Bytes, fo.ExpiresAt,
		pricing.FormatCost(pricing.ParseCost(fo.PageCount)))
	if fo.PageCount < 1 || fo.ExpiresAt == 0 {
		// Live finding 2026-11-18: POST /files returns NEITHER
		// page_count NOR expires_at (both documented, neither
		// delivered — checked with both recognition purposes, see
		// docs/pitfalls.md). Not fatal here: the id is what matters,
		// and 0-expiry means "no expiry advertised" → no proactive
		// re-upload. The F3 parse-fee consequence is recorded too.
		t.Logf("NOTE: upload response omitted page_count/expires_at (live API, documented-only fields) — " +
			"F3's per-page parse fee records $0 and F4's expiry heal stays dormant until they ship")
	}

	// ---- 1b. the F4 send path itself: an asset-only part (submit-time
	// upload failed, say) is lazily uploaded by ResolveFiles against the
	// REAL client — this is the station every agent turn goes through,
	// not a hand-built part. Costs one more parse (≈¥0.003).
	resolver := deepseek.NewFileResolver(func(string) ([]byte, error) { return pdf, nil }, c)
	resolved := deepseek.ResolveFiles(ctx, []deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "warm-up",
		Files:   []deepseek.FilePart{{Asset: "seek-f4-smoke.pdf", Purpose: deepseek.PurposeTextRecognition}},
	}}, resolver)
	if len(resolved[0].Files) != 1 || resolved[0].Files[0].FileID == "" {
		t.Fatalf("send-path lazy upload did not produce a file_id: %+v", resolved[0])
	}
	docPart := resolved[0].Files[0]
	t.Logf("send-path lazy upload: id=%s (asset %q — the same bytes, a second parse)",
		docPart.FileID, docPart.Asset)
	sys := deepseek.Message{Role: deepseek.RoleSystem, Content: smokeSystemPrompt}
	q1 := deepseek.Message{
		Role:    deepseek.RoleUser,
		Content: "文档里 Zephyr-42 任务的发射代码是什么？只回答代码本身。",
		Files:   []deepseek.FilePart{docPart},
	}

	// ---- 2. reference: the answer exists only in the document ----
	resp1, err := c.Chat(ctx, &deepseek.ChatRequest{
		Model:    deepseek.ModelV41Flash,
		Messages: []deepseek.Message{sys, q1},
		Thinking: &deepseek.ThinkingMode{Type: "disabled"}, // short answers; the cache reading is thinking-independent
	})
	if err != nil {
		t.Fatalf("chat with document: %v", err)
	}
	if len(resp1.Choices) == 0 {
		t.Fatal("chat returned no choices")
	}
	reply1 := resp1.Choices[0].Message.Content
	round1 := chatRound{label: "doc #1", usage: resp1.Usage, reply: reply1}
	round1.log(t)
	if !strings.Contains(reply1, "KESTREL-7781") {
		t.Fatalf("reply %q does not carry the document's launch code — the model did not read the parsed document", reply1)
	}

	// ---- 4a. cache: request #2 replays request #1's prefix ----
	// StripReasoningContent mirrors the agent's send station: a prior
	// reasoning_content, however empty here, must never be echoed back.
	docHistory := deepseek.StripReasoningContent([]deepseek.Message{sys, q1, resp1.Choices[0].Message})
	q2 := deepseek.Message{Role: deepseek.RoleUser, Content: "文档里指定的备用频段是多少？只回答频率本身。"}
	resp2, err := c.Chat(ctx, &deepseek.ChatRequest{
		Model:    deepseek.ModelV41Flash,
		Messages: append(append([]deepseek.Message{}, docHistory...), q2),
		Thinking: &deepseek.ThinkingMode{Type: "disabled"},
	})
	if err != nil {
		t.Fatalf("chat round 2 with document: %v", err)
	}
	reply2 := resp2.Choices[0].Message.Content
	round2 := chatRound{label: "doc #2", usage: resp2.Usage, reply: reply2}
	round2.log(t)
	if !strings.Contains(reply2, "433") {
		t.Fatalf("reply %q does not carry the document's backup band", reply2)
	}

	// ---- 4b. control arm: same two-call shape, no document ----
	// Padded to a few hundred tokens on purpose: DeepSeek's cache works
	// in 64-token blocks and a two-sentence prompt is too short to be
	// cacheable at all — it produced hit=0 on 2026-11-18, which says
	// nothing about file_id. Same system prompt as the file arm, so the
	// two arms' shared prefix behaves identically.
	var ctrlText strings.Builder
	ctrlText.WriteString("下面是一段用于测试的固定说明，请只回答最后的问题。\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&ctrlText, "第 %d 条：前缀缓存按请求字节序列的最长公共前缀复用，命中部分按更低价格计费，未命中部分按原价计费。\n", i+1)
	}
	ctrlText.WriteString("问题：前缀缓存复用什么？一句话回答。")
	ctrlQ1 := deepseek.Message{Role: deepseek.RoleUser, Content: ctrlText.String()}
	respC1, err := c.Chat(ctx, &deepseek.ChatRequest{
		Model:    deepseek.ModelV41Flash,
		Messages: []deepseek.Message{sys, ctrlQ1},
		Thinking: &deepseek.ThinkingMode{Type: "disabled"},
	})
	if err != nil {
		t.Fatalf("control chat 1: %v", err)
	}
	ctrlHistory := deepseek.StripReasoningContent([]deepseek.Message{sys, ctrlQ1, respC1.Choices[0].Message})
	respC2, err := c.Chat(ctx, &deepseek.ChatRequest{
		Model: deepseek.ModelV41Flash,
		Messages: append(append([]deepseek.Message{}, ctrlHistory...),
			deepseek.Message{Role: deepseek.RoleUser, Content: "再补一句。"}),
		Thinking: &deepseek.ThinkingMode{Type: "disabled"},
	})
	if err != nil {
		t.Fatalf("control chat 2: %v", err)
	}
	ctrl1 := chatRound{label: "ctrl #1", usage: respC1.Usage, reply: respC1.Choices[0].Message.Content}
	ctrl2 := chatRound{label: "ctrl #2", usage: respC2.Usage, reply: respC2.Choices[0].Message.Content}
	ctrl1.log(t)
	ctrl2.log(t)

	// The document's token weight is the file arm's prompt advantage
	// over the control at call #1 (same system prompt, same shape).
	docTokens := round1.usage.PromptTokens - ctrl1.usage.PromptTokens
	t.Logf("document token weight ≈ %d (file arm %d vs control %d)",
		docTokens, round1.usage.PromptTokens, ctrl1.usage.PromptTokens)

	if round2.usage.PromptCacheHitTokens == 0 {
		t.Errorf("doc arm call #2: prompt_cache_hit_tokens = 0 — the file_id request did not reuse the prefix at all")
	}
	switch {
	case ctrl2.usage.PromptCacheHitTokens == 0:
		t.Logf("INCONCLUSIVE: control arm call #2 also missed (hit=0) — this environment's cache is cold, " +
			"so the file arm's reading cannot attribute anything to file_id. Re-run to warm it.")
	case docTokens <= 0:
		t.Logf("INCONCLUSIVE: the file arm's prompt was not larger than the control's (doc tokens ≈ %d) — "+
			"the parse output may not be accounted as prompt tokens. Record this reading.", docTokens)
	default:
		// The sharp assertion: if the file_id participates in the cache,
		// call #2's hits must cover the document too — roughly control
		// hits + doc tokens (cache blocks round, hence the half-way bar).
		bar := ctrl2.usage.PromptCacheHitTokens + docTokens/2
		if round2.usage.PromptCacheHitTokens < bar {
			t.Errorf("doc arm call #2: hit=%d < %d (control %d + half the document's %d tokens) — "+
				"the file_id's document does NOT come back as cached tokens",
				round2.usage.PromptCacheHitTokens, bar, ctrl2.usage.PromptCacheHitTokens, docTokens)
		}
	}

	// ---- 3. resume: JSON round-trip, then keep referencing ----
	// internal/session stores one deepseek.Message per JSONL line; this
	// is that round-trip at the message level.
	raw, err := json.Marshal(docHistory)
	if err != nil {
		t.Fatalf("session-form marshal: %v", err)
	}
	var reloaded []deepseek.Message
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatalf("session-form unmarshal: %v", err)
	}
	if len(reloaded) != 3 {
		t.Fatalf("reloaded %d messages, want 3: %s", len(reloaded), raw)
	}
	if got := reloaded[1].Files; len(got) != 1 || got[0] != docPart {
		t.Fatalf("file part did not survive the round-trip: %+v (want %+v)", got, docPart)
	}
	resp3, err := c.Chat(ctx, &deepseek.ChatRequest{
		Model: deepseek.ModelV41Flash,
		Messages: append(append([]deepseek.Message{}, reloaded...),
			deepseek.Message{Role: deepseek.RoleUser, Content: "文档里写的首席工程师是谁？只回答人名。"}),
		Thinking: &deepseek.ThinkingMode{Type: "disabled"},
	})
	if err != nil {
		t.Fatalf("chat from resumed history: %v", err)
	}
	reply3 := resp3.Choices[0].Message.Content
	round3 := chatRound{label: "resumed #3", usage: resp3.Usage, reply: reply3}
	round3.log(t)
	if !strings.Contains(reply3, "Vasquez") {
		t.Fatalf("resumed reply %q lost the document's chief engineer — resume did not keep the reference", reply3)
	}
}
