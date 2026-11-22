// Package mcptool bridges MCP tool definitions (from a running MCP
// server) into the seek tools.Tool interface so they can be registered
// in a tools.Registry and called by the agent exactly like built-in tools.
package mcptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/pkg/mcp"
)

// Bridge wraps one MCP tool as a seek Tool.
type Bridge struct {
	client     *mcp.Client
	serverName string
	effectName string // may be prefixed to avoid registry conflicts
	def        mcp.ToolDef
}

// New wraps one MCP tool definition. effectName is the name under which
// the tool will be registered — by convention "<server>__<tool>" (e.g.
// semble__search); loadOne computes it, see the naming policy there.
func New(client *mcp.Client, serverName string, def mcp.ToolDef, effectName string) *Bridge {
	return &Bridge{
		client:     client,
		serverName: serverName,
		effectName: effectName,
		def:        def,
	}
}

func (b *Bridge) Name() string            { return b.effectName }
func (b *Bridge) Description() string     { return b.def.Description }
func (b *Bridge) Schema() json.RawMessage { return b.def.InputSchema }

// Server returns the MCP server name this tool was loaded from. The
// sysprompt MCPSection assembly (cmd/seek, post-LoadServers) groups
// registered tools by this to present per-server routing context —
// the effective Name() may carry a conflict prefix, but the owning
// server is what the model needs for "which external server provides
// this". Read-only accessor; registration policy stays in
// LoadServers/loadOne.
func (b *Bridge) Server() string { return b.serverName }

func (b *Bridge) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("mcptool %s: bad arguments: %v", b.effectName, err)
	}
	result, err := b.client.CallTool(ctx, b.def.Name, args)
	if err != nil {
		return "", fmt.Errorf("mcptool %s: %w", b.effectName, err)
	}
	text := result.TextContent()
	if result.IsError {
		return "", fmt.Errorf("mcptool %s: tool error: %s", b.effectName, text)
	}
	if text == "" {
		return fmt.Sprintf("mcptool %s: (empty response)", b.effectName), nil
	}
	return text, nil
}

// ServerConfig is the per-server configuration passed to LoadServers.
// It mirrors mcpconfig.ServerEntry to avoid an import cycle between
// this package and mcpconfig.
type ServerConfig struct {
	Command string
	Args    []string
	// Env holds additional "KEY=VALUE" pairs to merge on top of the
	// inherited environment. nil means no additions.
	Env []string
	// NoPrefix opts this server out of the default "<server>__<tool>"
	// registration name, keeping the server's bare tool names instead.
	// It is the escape hatch behind mcp.json's "prefix": false. If a
	// bare name collides with something already registered, the tool
	// falls back to the prefixed form — the bare name is simply not
	// available. Zero value (false) = prefix on, the default.
	NoPrefix bool
	// NamePrefix overrides the registration prefix, whose default is
	// "<server>__". Set only by callers that need a roster to land in a
	// namespace of its own: the ACP lane registers client-injected
	// servers as "mcp__<server>__<tool>" so an editor-supplied server is
	// distinguishable from the user's mcp.json roster in the tools
	// array. Ignored when NoPrefix keeps a bare, uncontested name.
	NamePrefix string
}

// LoadResult holds the Bridges, per-server Errors and the live clients
// from LoadServers.
//
// Clients exists for the callers that OWN the servers' lifetime: the
// mcp.json path leaks them deliberately (they live until the process
// exits, chapter-12 §12.7), while the ACP passthrough (session/new's
// mcpServers) must close them when the session ends — a client is the
// only handle on a spawned child process. One client per server that
// connected; bridges of the same server share it.
type LoadResult struct {
	Bridges []*Bridge
	Errors  []error
	Clients []*mcp.Client
}

// Close terminates every connected server (SIGKILL the child, then
// reap it). Safe on a zero-width result; safe to call once per result.
// The first REAL error is returned, the remaining ones are still
// attempted — partial teardown beats leaking a child because a
// neighbour failed.
//
// A child that exited non-zero is NOT an error here: Close kills it by
// design (pkg/mcp cancels the exec context, then Waits), so *exec.ExitError
// is the normal outcome of a deliberate teardown. Only failures of the
// reaping machinery itself surface.
func (lr LoadResult) Close() error {
	var first error
	for _, c := range lr.Clients {
		if c == nil {
			continue
		}
		err := c.Close()
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) && first == nil {
			first = err
		}
	}
	return first
}

// LoadServers starts each MCP server, runs the initialize handshake,
// lists tools, and returns Bridge instances ready for registration.
//
// Servers are visited in lexicographic order of their names so the
// same config produces a byte-identical registration roster (names
// and order) on every startup — map iteration order never leaks in.
// Each server's error is collected independently — one broken server
// does not block the others. existingNames is the set of tool names
// already in the seek registry.
func LoadServers(ctx context.Context, servers map[string]ServerConfig, existingNames map[string]bool) LoadResult {
	return loadServers(ctx, servers, existingNames, func(ctx context.Context, cfg ServerConfig) (*mcp.Client, error) {
		return mcp.StartServer(ctx, mcp.ServerConfig{
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
		})
	})
}

// connectFn launches one MCP server subprocess and returns a client.
// A seam so tests can substitute in-process fake servers.
type connectFn func(ctx context.Context, cfg ServerConfig) (*mcp.Client, error)

func loadServers(ctx context.Context, servers map[string]ServerConfig, existingNames map[string]bool, connect connectFn) LoadResult {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var result LoadResult
	for _, serverName := range names {
		bridges, client, err := loadOne(ctx, serverName, servers[serverName], existingNames, connect)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("mcp server %q: %w", serverName, err))
			continue
		}
		for _, b := range bridges {
			existingNames[b.effectName] = true
		}
		result.Bridges = append(result.Bridges, bridges...)
		// A server that connected but exposed zero tools still owns a
		// child process — record its client so Close can reap it.
		if client != nil {
			result.Clients = append(result.Clients, client)
		}
	}
	return result
}

func loadOne(ctx context.Context, serverName string, cfg ServerConfig, existing map[string]bool, connect connectFn) ([]*Bridge, *mcp.Client, error) {
	client, err := connect(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	if _, err := client.Initialize(ctx); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("initialize: %w", err)
	}

	defs, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("list tools: %w", err)
	}

	bridges := make([]*Bridge, 0, len(defs))
	for _, def := range defs {
		// Default: register as "<server>__<tool>" (e.g. semble__search).
		// The prefix carries ownership in the name and makes cross-server
		// collisions impossible. NoPrefix (mcp.json's "prefix": false
		// escape hatch) keeps the bare name when it is free, falling
		// back to the prefixed form when something already holds it.
		// NamePrefix swaps the leading segment (the ACP lane's
		// "mcp__<server>__"), leaving the shape identical.
		prefix := serverName + "__"
		if cfg.NamePrefix != "" {
			prefix = cfg.NamePrefix
		}
		effectName := prefix + def.Name
		if cfg.NamePrefix != "" {
			// A caller that asked for its own namespace (the ACP lane's
			// "mcp__<server>__") is naming tools for a model API, so the
			// WHOLE candidate is folded into ^[a-zA-Z0-9_-]{1,64}$: MCP
			// allows dots in server and tool names, the model APIs do
			// not, and a rejected name kills the session's first request.
			// Rosters registered the default way keep their exact bytes.
			effectName = safeToolName(effectName)
			if effectName == prefix {
				// Every rune of the tool's name folded away (e.g. a name
				// made only of punctuation). Keep the tool callable
				// instead of registering a bare prefix.
				effectName = prefix + "tool"
			}
		}
		if cfg.NoPrefix && !existing[def.Name] {
			effectName = def.Name
		}
		bridges = append(bridges, New(client, serverName, def, effectName))
	}
	return bridges, client, nil
}

// maxToolName is the provider ceiling on tools[].function.name
// (^[a-zA-Z0-9_-]{1,64}$ — DeepSeek, OpenAI, Anthropic agree). A longer
// name is not a cosmetic problem: the first request 400s and the whole
// session is dead. Names short enough pass through byte-identically, so
// every existing roster is unaffected.
const maxToolName = 64

// safeToolName folds a registration name into the provider alphabet and
// truncates it to the ceiling. Used only when a caller takes
// responsibility for naming tools itself (NamePrefix): the default
// "<server>__<tool>" path is left byte-identical, because mcp.json
// rosters are a shipped contract and a user's server may legitimately
// rely on an exotic name working today.
func safeToolName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > maxToolName {
		out = out[:maxToolName]
	}
	return out
}

// EnvMapToSlice converts a map[string]string into "KEY=VALUE" pairs
// suitable for appending to an os.Environ() slice.
func EnvMapToSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// FormatErrors joins a slice of errors into a multi-line string for
// stderr startup output.
func FormatErrors(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	lines := make([]string, 0, len(errs))
	for _, e := range errs {
		lines = append(lines, "  "+e.Error())
	}
	return strings.Join(lines, "\n")
}
