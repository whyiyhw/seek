package mcptool

import (
	"encoding/json"
	"strings"
	"testing"
)

// sembleTools mirrors the real Semble MCP server's tool list (2026-11-16
// verified, PRD §7.3): search(repo, query, content?) and
// find_related(file, line), registered post-MCP-D1 with the server
// prefix. The property key order in these schemas is load-bearing for
// the signature test — schemaParams must preserve document order.
func sembleTools() []ToolInfo {
	return []ToolInfo{
		{
			Name:        "semble__search",
			Description: "Search code in a repo using hybrid BM25 + semantic retrieval.",
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"repo":   {"type": "string"},
					"query":  {"type": "string"},
					"content": {"type": "string"}
				},
				"required": ["repo", "query"]
			}`),
		},
		{
			Name:        "semble__find_related",
			Description: "Find code semantically similar to a given file and line.",
			Schema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string"},
					"line": {"type": "integer"}
				},
				"required": ["file", "line"]
			}`),
		},
	}
}

// goldenSemble is docs/prd/feature-mcp-client.md §3.2's example copy,
// byte for byte (D3 acceptance: §3.2 示例文案为 golden).
const goldenSemble = `MCP tools (external servers loaded this session):

- semble (semantic code search, local & indexed):
  Tools: semble__search(repo, query, content?), semble__find_related(file, line).
  Routing: natural-language intent ("where is auth handled", "find the retry
  logic") → semble__search; known exact symbol/string → grep (cheaper, exact).
  After reading a hit, semble__find_related(file, line) finds semantically
  similar code. Follow up with read() for exact line context before editing.
`

func TestPromptSection_GoldenSemble(t *testing.T) {
	got := PromptSection([]ServerInfo{{Name: "semble", Tools: sembleTools()}})
	if got != goldenSemble {
		t.Errorf("PromptSection(semble) mismatch:\n--- want ---\n%s\n--- got ---\n%s", goldenSemble, got)
	}
}

func TestPromptSection_Empty(t *testing.T) {
	if got := PromptSection(nil); got != "" {
		t.Errorf("PromptSection(nil) = %q, want empty", got)
	}
	if got := PromptSection([]ServerInfo{}); got != "" {
		t.Errorf("PromptSection(empty) = %q, want empty", got)
	}
}

// D3 acceptance: 文案字节确定性 — same input, same bytes; and server
// blocks always render in name dictionary order regardless of input
// order (map-iteration hole stays closed at the prompt layer).
func TestPromptSection_Deterministic(t *testing.T) {
	servers := []ServerInfo{
		{Name: "zeta", Tools: []ToolInfo{{Name: "zeta__one", Schema: json.RawMessage(`{"type":"object"}`)}}},
		{Name: "semble", Tools: sembleTools()},
		{Name: "alpha", Hint: "alpha server", Tools: []ToolInfo{{Name: "alpha__run"}}},
	}
	first := PromptSection(servers)
	if again := PromptSection(servers); again != first {
		t.Error("same input produced different bytes across two calls")
	}

	shuffled := []ServerInfo{servers[2], servers[0], servers[1]}
	if got := PromptSection(shuffled); got != first {
		t.Error("input order leaked into output; servers must render in dictionary order")
	}

	// Dictionary order visible in the block sequence itself.
	ai := strings.Index(first, "- alpha")
	si := strings.Index(first, "- semble")
	zi := strings.Index(first, "- zeta")
	if !(ai < si && si < zi) {
		t.Errorf("blocks not in dictionary order: alpha@%d semble@%d zeta@%d", ai, si, zi)
	}
}

// Tier 1 of 3: a user hint beats both the known-server table and the
// fallback — even for a server seek knows (semble).
func TestPromptSection_Tier1HintWins(t *testing.T) {
	got := PromptSection([]ServerInfo{
		{Name: "semble", Hint: "My own guidance\nsecond line", Tools: sembleTools()},
	})
	want := `MCP tools (external servers loaded this session):

- semble:
  Tools: semble__search(repo, query, content?), semble__find_related(file, line).
  My own guidance
  second line
`
	if got != want {
		t.Errorf("tier-1 hint block mismatch:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
	if strings.Contains(got, "semantic code search") {
		t.Error("known-server copy leaked through a tier-1 hint")
	}
}

// Tier 3 of 3: unknown server without hint — list what is registered,
// interpret nothing (no invented routing, no invented purpose).
func TestPromptSection_Tier3Fallback(t *testing.T) {
	got := PromptSection([]ServerInfo{
		{
			Name: "weather",
			Tools: []ToolInfo{
				{
					Name:        "weather__get_forecast",
					Description: "Get the weather forecast for a city. Extended hourly data available via hours=true.",
					Schema:      json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
				},
			},
		},
	})
	want := `MCP tools (external servers loaded this session):

- weather (external MCP server):
  Tools: weather__get_forecast(city).
  Get the weather forecast for a city.
`
	if got != want {
		t.Errorf("tier-3 fallback block mismatch:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
	if strings.Contains(got, "Routing:") {
		t.Error("fallback fabricated routing guidance for an unknown server")
	}
}

// The fallback stays honest when the server exposes no description:
// tools list only, no invented prose.
func TestPromptSection_Tier3NoDescription(t *testing.T) {
	got := PromptSection([]ServerInfo{
		{Name: "silent", Tools: []ToolInfo{{Name: "silent__do", Schema: json.RawMessage(`{}`)}}},
	})
	want := `MCP tools (external servers loaded this session):

- silent (external MCP server):
  Tools: silent__do().
`
	if got != want {
		t.Errorf("no-description fallback mismatch:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// schemaParams must keep the schema's document order for parameter
// names — map-based unmarshalling would shuffle them and break byte
// determinism. Malformed schemas degrade to "name()", not an error.
func TestSignature_ParamOrderAndMalformed(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"zulu":{"type":"string"},"alpha":{"type":"string"},"mike":{"type":"integer"}},"required":["zulu"]}`)
	got := signature(ToolInfo{Name: "t", Schema: schema})
	if want := "t(zulu, alpha?, mike?)"; got != want {
		t.Errorf("signature = %q, want %q (document order, optional markers)", got, want)
	}

	for _, bad := range []json.RawMessage{nil, []byte(`{`), []byte(`{"type":"string"}`), []byte(`{"properties":{}}`)} {
		if got := signature(ToolInfo{Name: "t", Schema: bad}); got != "t()" {
			t.Errorf("signature(bad=%s) = %q, want %q", bad, got, "t()")
		}
	}
}
