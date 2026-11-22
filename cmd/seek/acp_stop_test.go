package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// acp_stop_test.go — the fixture states for structured turn termination
// (c_vfgeam ③): normal / truncated / cancelled are the card's required
// three, timeout and internal-error pin the remaining enum values. Each
// fixture drives the REAL acpBackend.Prompt (agent + deepseek client)
// against a canned SSE upstream and asserts exactly one _seek_turn_stop
// frame, its classification, and the spec-side session/prompt response.

// stopTestBackend wires an acpBackend to an agent whose upstream is the
// given SSE server. store/sess stay nil (saveTurn no-ops — persistence
// is not what these fixtures assert).
func stopTestBackend(t *testing.T, srvURL string) *acpBackend {
	t.Helper()
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srvURL)),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &acpBackend{ag: ag}
}

// stopFixture is one fixture's expectation: the frame classification,
// the response stopReason, and whether Prompt returns an error.
type stopFixture struct {
	name string
	// serve scripts the upstream SSE body.
	serve func(w http.ResponseWriter, r *http.Request)
	// ctx builds the turn's context (nil → background + cancel hook).
	ctx func(t *testing.T, cancelled *bool) context.Context

	wantFrame    acp.TurnStopUpdate // StopReason + FinishReason (+ Detail substring)
	wantDetailIn string
	wantResp     string
	wantErr      bool
}

// lastStopFrame finds the _seek_turn_stop frames among the recorded
// updates and returns the single one (nil-terminated slice otherwise).
func lastStopFrame(t *testing.T, ups []acp.SessionUpdate) acp.TurnStopUpdate {
	t.Helper()
	var frames []acp.TurnStopUpdate
	for _, u := range ups {
		if ts, ok := u.Update.(acp.TurnStopUpdate); ok {
			frames = append(frames, ts)
		}
	}
	if len(frames) != 1 {
		t.Fatalf("turn-stop frames = %d, want exactly 1 per session/prompt", len(frames))
	}
	if frames[0].SessionUpdate != acp.TurnStopSessionUpdate {
		t.Fatalf("frame tag = %q, want %q (an untagged update is dropped by every client)", frames[0].SessionUpdate, acp.TurnStopSessionUpdate)
	}
	// The frame must be the LAST update — it is emitted immediately
	// before the response, so anything after it would mean a stream
	// event slipped past the termination.
	if _, ok := ups[len(ups)-1].Update.(acp.TurnStopUpdate); !ok {
		t.Fatalf("termination frame is not the final update: %+v", ups[len(ups)-1])
	}
	return frames[0]
}

func TestACPBackend_Prompt_TerminationFixtures(t *testing.T) {
	fixtures := []stopFixture{
		{
			name: "normal completion",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, strings.Join([]string{
					`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"hello there"}}]}`,
					``,
					`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
					``,
					`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
					``,
					`data: [DONE]`,
					``,
				}, "\n"))
			},
			wantFrame: acp.TurnStopUpdate{StopReason: acp.FrameStopEndTurn, FinishReason: "stop"},
			wantResp:  acp.StopEndTurn,
		},
		{
			// The c_4ap2ra killer: upstream ends the stream with
			// finish_reason=length (opencode lane's truncation family).
			// The turn COMPLETED — partial answer delivered — so the
			// client must see max_tokens, not a generic failure.
			name: "length truncation",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, strings.Join([]string{
					`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"partial answ"}}]}`,
					``,
					`data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
					``,
					`data: {"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":5,"total_tokens":14}}`,
					``,
					`data: [DONE]`,
					``,
				}, "\n"))
			},
			wantFrame:    acp.TurnStopUpdate{StopReason: acp.FrameStopMaxTokens, FinishReason: "length"},
			wantDetailIn: "finish_reason=length",
			wantResp:     acp.StopMaxTokens,
		},
		{
			// Client hit stop mid-stream: the upstream stalls after one
			// delta; the test cancels when the delta lands.
			name: "client cancel",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"beginni"}}]}`+"\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done() // stall until the cancel aborts the request
			},
			ctx: func(t *testing.T, cancelled *bool) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				return context.WithValue(ctx, cancelKey{}, hook{func() { *cancelled = true; cancel() }})
			},
			wantFrame: acp.TurnStopUpdate{StopReason: acp.FrameStopCancelled},
			wantResp:  acp.StopCancelled,
		},
		{
			// A driver-imposed deadline runs out while the upstream
			// stalls — the in-process analogue of the silent death
			// (the 5-minute provider HTTP Timeout lands the same
			// shape). Response answers cancelled (the spec enum has no
			// timeout value); the frame names silent_timeout.
			name: "deadline timeout",
			serve: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"beginni"}}]}`+"\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			},
			ctx: func(t *testing.T, _ *bool) context.Context {
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			wantFrame: acp.TurnStopUpdate{StopReason: acp.FrameStopSilentTimeout},
			wantResp:  acp.StopCancelled,
		},
		{
			name: "internal error",
			serve: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "upstream exploded", http.StatusInternalServerError)
			},
			wantFrame: acp.TurnStopUpdate{StopReason: acp.FrameStopInternalError},
			wantErr:   true,
		},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(fx.serve))
			defer srv.Close()
			b := stopTestBackend(t, srv.URL)

			ctx := context.Background()
			cancelled := false
			if fx.ctx != nil {
				ctx = fx.ctx(t, &cancelled)
			}
			var ups []acp.SessionUpdate
			sendU := func(u acp.SessionUpdate) {
				if m, ok := u.Update.(map[string]any); ok && m["sessionUpdate"] == "agent_message_chunk" {
					if h, ok2 := ctx.Value(cancelKey{}).(hook); ok2 && !cancelled {
						h.fire()
					}
				}
				ups = append(ups, u)
			}
			res, err := b.Prompt(ctx, acp.PromptParams{SessionID: "stop-test", Prompt: []acp.ContentBlock{{Type: "text", Text: "hi"}}}, sendU)

			if fx.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, fx.wantErr)
			}
			frame := lastStopFrame(t, ups)
			if frame.StopReason != fx.wantFrame.StopReason {
				t.Errorf("frame stopReason = %q, want %q", frame.StopReason, fx.wantFrame.StopReason)
			}
			if frame.FinishReason != fx.wantFrame.FinishReason {
				t.Errorf("frame finishReason = %q, want %q", frame.FinishReason, fx.wantFrame.FinishReason)
			}
			if fx.wantDetailIn != "" && !strings.Contains(frame.Detail, fx.wantDetailIn) {
				t.Errorf("frame detail = %q, want it to contain %q", frame.Detail, fx.wantDetailIn)
			}
			if err == nil && res.StopReason != fx.wantResp {
				t.Errorf("response stopReason = %q, want %q", res.StopReason, fx.wantResp)
			}
			if fx.wantErr && !strings.Contains(err.Error(), "upstream exploded") {
				t.Errorf("err = %v, want the upstream detail", err)
			}
		})
	}
}

// cancelKey / hook — a tiny vehicle so the cancel fixture can fire from
// inside sendU when the first streamed chunk lands.
type cancelKey struct{}
type hook struct{ fire func() }

// TestACPBackend_Prompt_EmptyPromptTerminates: the degenerate prompt
// (no text after routing) is also a termination — the frame invariant
// ("exactly one per session/prompt, on EVERY exit path") must hold.
func TestACPBackend_Prompt_EmptyPromptTerminates(t *testing.T) {
	b := stopTestBackend(t, "http://unused")
	var ups []acp.SessionUpdate
	res, err := b.Prompt(context.Background(), acp.PromptParams{
		SessionID: "empty",
		Prompt:    []acp.ContentBlock{{Type: "text", Text: ""}},
	}, func(u acp.SessionUpdate) { ups = append(ups, u) })
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != acp.StopEndTurn {
		t.Fatalf("response = %q, want end_turn", res.StopReason)
	}
	frame := lastStopFrame(t, ups)
	if frame.StopReason != acp.FrameStopEndTurn {
		t.Fatalf("frame = %+v, want end_turn", frame)
	}
}
