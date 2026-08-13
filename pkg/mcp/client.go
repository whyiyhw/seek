package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/whyiyhw/seek/internal/childenv"
)

// ServerConfig specifies how to launch an MCP server subprocess.
type ServerConfig struct {
	Command string
	Args    []string
	// Env holds extra "KEY=VALUE" pairs layered on top of the SCRUBBED
	// parent environment (see childenv): credential-shaped variables and
	// seek's own SEEK_* namespace are never passed implicitly, because an
	// MCP server is a third-party binary named in user config.
	//
	// A server that genuinely needs a credential declares it here — that
	// is the explicit channel, and entries here win over anything the
	// parent had (cmd.Env is last-wins).
	Env []string
}

// Client is a JSON-RPC 2.0 client for the MCP stdio transport.
//
// Requests are serialised — one in-flight at a time — which matches
// the agent's sequential tool dispatch pattern. The decoder does not
// honour context cancellation during a Read; a per-call timeout should
// be imposed at the call-site if needed.
type Client struct {
	mu  sync.Mutex
	enc *json.Encoder
	dec *json.Decoder
	seq int64

	cmd    *exec.Cmd
	cancel context.CancelFunc // cancels srvCtx which kills the subprocess
}

// NewClient wraps existing read/write streams. Useful in tests.
func NewClient(r io.Reader, w io.Writer) *Client {
	return &Client{
		enc: json.NewEncoder(w),
		dec: json.NewDecoder(bufio.NewReader(r)),
	}
}

// StartServer launches cfg.Command as an MCP server subprocess and
// returns a Client that speaks to it over stdin/stdout. The subprocess
// is killed when ctx is cancelled or Close is called.
func StartServer(ctx context.Context, cfg ServerConfig) (*Client, error) {
	srvCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(srvCtx, cfg.Command, cfg.Args...)
	// Always explicit — a nil Env would hand this third-party binary the
	// full parent environment, seek's API key included.
	cmd.Env = append(childenv.Sanitized(), cfg.Env...)
	cmd.Stderr = io.Discard // servers write diagnostics to stderr; not our concern

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcp: stdin pipe for %q: %w", cfg.Command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcp: stdout pipe for %q: %w", cfg.Command, err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("mcp: start %q: %w", cfg.Command, err)
	}
	return &Client{
		enc:    json.NewEncoder(stdin),
		dec:    json.NewDecoder(bufio.NewReader(stdout)),
		cmd:    cmd,
		cancel: cancel,
	}, nil
}

// Close terminates the subprocess (if any) and waits for it to exit.
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.cmd != nil {
		return c.cmd.Wait()
	}
	return nil
}

// Initialize runs the MCP handshake. Must be called once before any
// tool operations. Returns the server's InitializeResult.
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	p, _ := json.Marshal(initializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    clientCapabilities{},
		ClientInfo:      clientInfo{Name: "seek", Version: "0.1"},
	})
	raw, err := c.call(ctx, "initialize", p)
	if err != nil {
		return nil, fmt.Errorf("mcp: initialize: %w", err)
	}
	var result InitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: initialize: decode result: %w", err)
	}
	// Required follow-up: notify the server that initialisation is done.
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return nil, fmt.Errorf("mcp: notifications/initialized: %w", err)
	}
	return &result, nil
}

// ListTools fetches all tools the server exposes.
func (c *Client) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.call(ctx, "tools/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, fmt.Errorf("mcp: list tools: %w", err)
	}
	var result listToolsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: list tools: decode result: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a named tool and returns its result.
// arguments may be nil (treated as empty object).
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*CallToolResult, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	p, err := json.Marshal(callToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("mcp: call tool %q: marshal params: %w", name, err)
	}
	raw, err := c.call(ctx, "tools/call", p)
	if err != nil {
		return nil, fmt.Errorf("mcp: call tool %q: %w", name, err)
	}
	var result CallToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp: call tool %q: decode result: %w", name, err)
	}
	return &result, nil
}

// TextContent joins all text-type content blocks into one string.
func (r *CallToolResult) TextContent() string {
	var parts []string
	for _, c := range r.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// call sends a request and blocks until the matching response arrives.
// Notifications from the server (ID == 0) are silently discarded.
// The method is serialised by c.mu; concurrent callers queue up safely.
func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.seq++
	id := c.seq

	if err := c.enc.Encode(request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp response
		if err := c.dec.Decode(&resp); err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		// Notifications carry no ID (decodes as 0); skip them.
		if resp.ID == 0 {
			continue
		}
		if resp.ID != id {
			// Stale response from a prior call — shouldn't happen in
			// sequential mode but skip rather than panic.
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// notify sends a JSON-RPC notification (no response is expected or read).
func (c *Client) notify(ctx context.Context, method string, params json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	return c.enc.Encode(request{
		JSONRPC: "2.0",
		// ID intentionally zero (omitempty) — the encoder will omit it,
		// producing a valid JSON-RPC 2.0 notification.
		Method: method,
		Params: params,
	})
}
