// Prompt assembly for the system-prompt MCP section (MCP-D3,
// docs/prd/feature-mcp-client.md §3.2). PromptSection is a pure
// function of its input: byte-deterministic for identical inputs, which
// is what the prefix-cache discipline demands of anything that lands in
// sysprompt.Header. The caller (session-start wiring, MCP-D2) feeds it
// the servers that registered successfully and injects the returned
// text once per session; it must never be recomputed per turn.
package mcptool

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// ToolInfo is one registered MCP tool as seen by the prompt layer.
// Name is the REGISTERED name (post-MCP-D1 that is "<server>__<tool>",
// e.g. "semble__search") — the same string the model sees in the tools
// array, so the prose and the schema always agree.
type ToolInfo struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// ServerInfo is one loaded MCP server as seen by the prompt layer: its
// configured name, the optional mcp.json "hint" (tier-1 copy, verbatim
// from the user), and its tools in registration order.
type ServerInfo struct {
	Name  string
	Hint  string
	Tools []ToolInfo
}

// sectionHeader leads the MCP section; the leading blank separation is
// added by the sysprompt glue (same shape as SkillManifest), not here.
const sectionHeader = "MCP tools (external servers loaded this session):"

// knownServer is a curated copy entry for a server seek knows well
// (tier 2 of 3). Guidance is stored as pre-wrapped lines WITHOUT the
// two-space indent — PromptSection indents them uniformly so the block
// stays aligned regardless of rendering changes. Deliberately NOT a
// template on tool names: the guidance prose names tools the way the
// curated copy was written; the factual Tools line right above it is
// what reflects the actually-registered tools.
type knownServer struct {
	role     string // parenthetical on the "- name (role):" line
	guidance string // rendered lines, '\n'-joined, no indent
}

// knownServers is seek's built-in copy table (§3.2 tier 2). First
// version ships exactly one entry: semble. Anything not in this table
// and without a user hint falls to the listing-only tier 3 — seek
// lists what it sees, never invents a purpose for an unknown server
// (§八.6).
var knownServers = map[string]knownServer{
	"semble": {
		role: "semantic code search, local & indexed",
		guidance: `Routing: natural-language intent ("where is auth handled", "find the retry
logic") → semble__search; known exact symbol/string → grep (cheaper, exact).
After reading a hit, semble__find_related(file, line) finds semantically
similar code. Follow up with read() for exact line context before editing.`,
	},
}

// PromptSection assembles the MCP section for the system prompt from
// the successfully-loaded servers. Empty input yields "" (the caller
// then emits no section at all — byte-identical to an MCP-less session).
//
// Shape: one header line, then one block per server in server-name
// dictionary order (closing the map-iteration-order hole chapter-12
// §12.6 recorded at the prompt layer). Per-server copy tier priority
// (§3.2): 1) user hint from mcp.json, verbatim; 2) knownServers table;
// 3) generic listing fallback — registered tool names with their
// parameter signatures plus the first sentence of the first tool's
// description (MCP has no server-level description in-protocol), with
// no interpretation.
func PromptSection(servers []ServerInfo) string {
	if len(servers) == 0 {
		return ""
	}
	sorted := make([]ServerInfo, len(servers))
	copy(sorted, servers)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var sb strings.Builder
	sb.WriteString(sectionHeader)
	for _, s := range sorted {
		var block strings.Builder
		writeServerBlock(&block, s)
		sb.WriteString("\n\n")
		sb.WriteString(strings.TrimSuffix(block.String(), "\n"))
	}
	sb.WriteString("\n") // trailing newline, same shape as skill.Manifest
	return sb.String()
}

func writeServerBlock(sb *strings.Builder, s ServerInfo) {
	hint := strings.TrimSpace(s.Hint)
	known, isKnown := knownServers[s.Name]

	switch {
	case hint != "": // tier 1 — the user's own words, verbatim
		sb.WriteString("- " + s.Name + ":\n")
		writeToolsLine(sb, s.Tools)
		writeIndented(sb, hint)
	case isKnown: // tier 2 — curated copy; golden per §3.2
		sb.WriteString("- " + s.Name + " (" + known.role + "):\n")
		writeToolsLine(sb, s.Tools)
		writeIndented(sb, known.guidance)
	default: // tier 3 — list what is there, interpret nothing
		sb.WriteString("- " + s.Name + " (external MCP server):\n")
		writeToolsLine(sb, s.Tools)
		if first := firstSentence(s.Tools); first != "" {
			writeIndented(sb, first)
		}
	}
}

// writeToolsLine renders the factual tool inventory: registered names
// with parameter signatures, e.g. "  Tools: semble__search(repo,
// query, content?), semble__find_related(file, line)." — one line; a
// long inventory is allowed to run long rather than wrap differently
// per screen (byte determinism beats aesthetics here). Tools render in
// the caller-given registration order.
func writeToolsLine(sb *strings.Builder, tools []ToolInfo) {
	parts := make([]string, 0, len(tools))
	for _, t := range tools {
		parts = append(parts, signature(t))
	}
	line := "  Tools:"
	if len(parts) > 0 {
		line += " " + strings.Join(parts, ", ") + "."
	}
	sb.WriteString(line + "\n")
}

// writeIndented appends text with every line prefixed by two spaces,
// keeping multi-line hint/guidance copy aligned under its bullet.
// Blank lines stay empty (no trailing-space indent bytes).
func writeIndented(sb *strings.Builder, text string) {
	for _, line := range strings.Split(text, "\n") {
		if line != "" {
			sb.WriteString("  ")
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
}

// signature renders one tool as "name(param, param?)" — required
// parameters bare, optional ones suffixed "?". Parameter order is the
// schema's document order, NOT map order: encoding/json into a map
// would shuffle properties and poison the byte determinism this whole
// section exists to guarantee (see docs/pitfalls.md).
func signature(t ToolInfo) string {
	params, required := schemaParams(t.Schema)
	var sb strings.Builder
	sb.WriteString(t.Name)
	sb.WriteByte('(')
	for i, p := range params {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(p)
		if !required[p] {
			sb.WriteByte('?')
		}
	}
	sb.WriteByte(')')
	return sb.String()
}

// schemaParams extracts (ordered property names, required set) from a
// JSON-Schema object input schema. Malformed or missing schemas yield
// empty results — the tool then renders as "name()". Only the key
// order of "properties" and the "required" array are read; property
// schemas themselves are skipped as raw values.
func schemaParams(schema json.RawMessage) ([]string, map[string]bool) {
	var s struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Properties) == 0 {
		return nil, nil
	}

	dec := json.NewDecoder(bytes.NewReader(s.Properties))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, nil
	}
	var names []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			break
		}
		key, ok := keyTok.(string)
		if !ok {
			break
		}
		var skip json.RawMessage // consume the property's value subtree
		if err := dec.Decode(&skip); err != nil {
			break
		}
		names = append(names, key)
	}
	if names == nil {
		return nil, nil
	}
	req := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		req[r] = true
	}
	return names, req
}

// firstSentence returns the first sentence of the first tool's
// description — the closest thing to a server self-description the MCP
// protocol offers (initialize reports only name/version). Normalised
// to a single line; "" when the server said nothing usable.
func firstSentence(tools []ToolInfo) string {
	for _, t := range tools {
		desc := strings.Join(strings.Fields(t.Description), " ")
		if desc == "" {
			continue
		}
		if i := strings.IndexByte(desc, '.'); i >= 0 {
			return desc[:i+1]
		}
		return desc
	}
	return ""
}
