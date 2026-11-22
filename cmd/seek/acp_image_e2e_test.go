package main

// ACP·P1-3 (card c_4pczu3) end-to-end fixtures through the REAL binary:
// `seek acp` against a fake OpenAI-compatible backend that records the
// request bodies it is sent. Two fixture states, because they are the
// two ways an image reaches seek:
//
//	① image-only     — the pre-M-P.5 behaviour was "text == \"\" →
//	                   end_turn, nothing happens". Now the block must
//	                   reach the model as an in-band marker inside a
//	                   real turn.
//	② text+image+text — the marker must sit BETWEEN the two sentences in
//	                   the request seek actually sends, not be appended
//	                   at the tail.
//
// The wire params come from cmd/seek/testdata/acp-image/*.json — one
// file per state, so the shape a client (Zed) sends is a reviewed
// artifact rather than an inline literal, and the smoke script can post
// the very same bytes.
//
// The child is pinned to the GLM-compatible lane with a NON-vision model
// id (e2e-model), so the deterministic expectation is the switch-model
// note rather than a native attach — the fixture asserts the transport,
// not the vision table (that one is modelcap's, already covered).
//
// Sandbox-only: httptest backend + temp SEEK_HOME/HOME, no keys, no
// network.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingBackend answers every request with one canned text turn and
// keeps the raw request bodies so the test can inspect what the model
// was actually told.
type recordingBackend struct {
	srv *httptest.Server
	mu  sync.Mutex
	raw [][]byte
}

func newRecordingBackend(t *testing.T) *recordingBackend {
	t.Helper()
	b := &recordingBackend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.raw = append(b.raw, body)
		n := len(b.raw)
		b.mu.Unlock()
		if n > 2 { // a turn that keeps calling back is a test bug, not a lane
			return
		}
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"ack"},"finish_reason":null}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// firstBody returns the first captured body (the prompt itself).
func (b *recordingBackend) firstBody(t *testing.T) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.raw) == 0 {
		t.Fatal("backend captured no requests — the turn never reached the model")
	}
	return string(b.raw[0])
}

// awaitBody waits until at least one request has landed.
func (b *recordingBackend) awaitBody(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.raw)
		b.mu.Unlock()
		if n > 0 {
			return b.firstBody(t)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no request reached the backend within %s", timeout)
	return ""
}

// tinyPNGBase64 is a 2x2 RGBA PNG — a real decodable image, so a
// vision-capable lane would store genuine bytes and the marker's
// dimension reading would see a real header.
const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAEUlEQVR4nGP4z8AAQv8ZYAwAQ84H+VjtZqAAAAAASUVORK5CYII="

// acpImageFixture loads one of the two prompt fixtures
// (cmd/seek/testdata/acp-image/{image-only,mixed}.json) and returns the
// prompt array as raw JSON, ready to drop into session/prompt params.
func acpImageFixture(t *testing.T, state string) json.RawMessage {
	t.Helper()
	path := filepath.Join("testdata", "acp-image", state+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s: %v", path, err)
	}
	var f struct {
		Prompt json.RawMessage `json:"prompt"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("fixture %s: %v", path, err)
	}
	if len(f.Prompt) == 0 {
		t.Fatalf("fixture %s carries no prompt array", path)
	}
	return f.Prompt
}

// promptFixtureAndAwaitTurn sends one session/prompt built from a
// fixture and waits for its response (id 3 — the shared helper's id for
// the prompt request).
func promptFixtureAndAwaitTurn(t *testing.T, c *acpChild, sid, state string) {
	t.Helper()
	c.rpc(t, 3, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt":    acpImageFixture(t, state),
	})
	if reason := c.awaitTurnEnd(t); reason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", reason)
	}
}

// ① Pure image: no text block at all. Before M-P.5 this returned
// end_turn with an empty prompt and never touched the model.
func TestACPImageE2E_ImageOnlyReachesModel(t *testing.T) {
	workDir := t.TempDir()
	b := newRecordingBackend(t)
	c := startACPChild(t, b.srv.URL, workDir)
	sid := c.handshake(t, workDir)

	promptFixtureAndAwaitTurn(t, c, sid, "image-only")
	body := b.awaitBody(t, 60*time.Second)
	if !strings.Contains(body, "[image: pasted-image.png — ") {
		t.Fatalf("image-only prompt must reach the model with a marker; body = %s", body)
	}
	// The lane is non-vision, so the marker is the switch-model note and
	// NO image bytes may be forwarded (the 柱 Q invariant).
	if strings.Contains(body, "image_url") || strings.Contains(body, tinyPNGBase64[:24]) {
		t.Fatalf("image bytes leaked to a non-vision lane: %s", body)
	}
}

// ② Text + image + text: order preserved in the actual wire request.
func TestACPImageE2E_MixedOrderPreserved(t *testing.T) {
	workDir := t.TempDir()
	b := newRecordingBackend(t)
	c := startACPChild(t, b.srv.URL, workDir)
	sid := c.handshake(t, workDir)

	promptFixtureAndAwaitTurn(t, c, sid, "mixed")
	body := b.awaitBody(t, 60*time.Second)
	// The body is JSON with escaped newlines; search on the tokens only
	// (the separators are asserted positionally, not literally).
	first := strings.Index(body, "first part")
	marker := strings.Index(body, "[image: pasted-image.png")
	second := strings.Index(body, "second part")
	if first < 0 || marker < 0 || second < 0 {
		t.Fatalf("a piece of the prompt is missing from the request: %s", body)
	}
	if !(first < marker && marker < second) {
		t.Fatalf("wire order broken — want text/image/text, got idx %d/%d/%d in %s", first, marker, second, body)
	}
}

// The fixtures are load-bearing: a corrupted base64 literal would make
// both fixtures pass vacuously through the "corrupt base64 is skipped"
// path, which is the one way these tests could lie. Decode the bytes and
// check the real PNG signature, and check the block decodes into the
// protocol type (guards against a typo'd key).
func TestACPImageFixtures_AreRealPNGBlocks(t *testing.T) {
	for _, state := range []string{"image-only", "mixed"} {
		t.Run(state, func(t *testing.T) {
			raw := acpImageFixture(t, state)
			var blocks []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Data     string `json:"data"`
				MimeType string `json:"mimeType"`
			}
			if err := json.Unmarshal(raw, &blocks); err != nil {
				t.Fatalf("fixture blocks: %v", err)
			}
			var images int
			for _, blk := range blocks {
				if blk.Type != "image" {
					continue
				}
				images++
				if blk.MimeType != "image/png" {
					t.Fatalf("image block mimeType = %q, want image/png", blk.MimeType)
				}
				data, err := base64.StdEncoding.DecodeString(blk.Data)
				if err != nil {
					t.Fatalf("image block base64: %v", err)
				}
				if !strings.HasPrefix(string(data), "\x89PNG\r\n\x1a\n") {
					t.Fatal("image block data is not a PNG")
				}
			}
			if images == 0 {
				t.Fatal("fixture carries no image block")
			}
			if state == "mixed" {
				if len(blocks) != 3 || blocks[0].Type != "text" || blocks[2].Type != "text" {
					t.Fatalf("mixed fixture must be text/image/text, got %+v", blocks)
				}
			} else if len(blocks) != 1 {
				t.Fatalf("image-only fixture must carry exactly one block, got %d", len(blocks))
			}
		})
	}
}

// The fixture files must stay syntactically valid JSON — the smoke
// script posts them verbatim, so a comment or trailing comma here would
// break the shell lane instead of this test.
func TestACPImageFixtures_ParseAsPromptParams(t *testing.T) {
	for _, state := range []string{"image-only", "mixed"} {
		params := fmt.Sprintf(`{"sessionId":"s","prompt":%s}`, acpImageFixture(t, state))
		var p struct {
			SessionID string `json:"sessionId"`
			Prompt    []struct {
				Type string `json:"type"`
			} `json:"prompt"`
		}
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		if len(p.Prompt) == 0 {
			t.Fatalf("%s: no blocks after decode", state)
		}
	}
}
