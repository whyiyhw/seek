// Package lspclient is a minimal, hand-rolled LSP client over stdio —
// just enough for find-references (v6 柱 L 瘦身版). It mirrors the
// JSON-RPC pattern of pkg/mcp/client.go but adds the two things LSP needs
// that MCP doesn't: Content-Length framing (framing.go) and an async read
// loop that demuxes server-pushed notifications / requests from the
// responses to our calls.
//
// Lifecycle note (PRD feature-lsp.md §4 D4): StartServer binds the
// subprocess to the ctx it's given — callers (the manager) MUST pass a
// SESSION-scoped ctx, never a turn ctx, or a long gopls cold-start would
// be killed the instant the launching turn ends. The per-call ctx passed
// to Call/References is the turn ctx (Esc cancels the query, not the
// server).
package lspclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// ServerConfig specifies how to launch a language server subprocess.
type ServerConfig struct {
	Command string
	Args    []string
	// Env holds extra "KEY=VALUE" pairs merged onto the parent env. nil =
	// inherit unchanged. gopls needs GOPATH/GOFLAGS, pyright needs node on
	// PATH — those come from the inherited parent env.
	Env []string
}

// Client is a JSON-RPC 2.0 LSP client. Safe for concurrent Call/Notify;
// requests are correlated by id via a read loop, so callers don't
// serialise. A dead server (crash / EOF) fails all in-flight and future
// calls with the read error.
type Client struct {
	mu      sync.Mutex
	w       io.Writer
	seq     int64
	pending map[int64]chan callResult
	readErr error // set once the read loop dies; guarded by mu

	cmd    *exec.Cmd
	cancel context.CancelFunc

	closeOnce sync.Once
	closed    chan struct{} // closed when the read loop exits (crash or Close)
}

// newClient wires existing streams (r = server→client, w = client→server)
// and starts the read loop. Used directly by tests; StartServer wraps a
// real subprocess.
func newClient(r io.Reader, w io.Writer) *Client {
	c := &Client{
		w:       w,
		pending: map[int64]chan callResult{},
		closed:  make(chan struct{}),
	}
	go c.readLoop(bufio.NewReader(r))
	return c
}

// StartServer launches cfg.Command as a language server and returns a
// Client speaking to it over stdin/stdout. The subprocess dies when ctx
// is cancelled or Close is called — ctx MUST be session-scoped (see
// package doc / PRD D4).
func StartServer(ctx context.Context, cfg ServerConfig) (*Client, error) {
	srvCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(srvCtx, cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}
	cmd.Stderr = io.Discard // servers log to stderr; not our concern

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("lspclient: stdin pipe for %q: %w", cfg.Command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("lspclient: stdout pipe for %q: %w", cfg.Command, err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("lspclient: start %q: %w", cfg.Command, err)
	}
	c := &Client{
		w:       stdin,
		pending: map[int64]chan callResult{},
		closed:  make(chan struct{}),
		cmd:     cmd,
		cancel:  cancel,
	}
	go c.readLoop(bufio.NewReader(stdout))
	return c, nil
}

// readLoop demuxes incoming messages until the stream dies. Responses go
// to the waiting Call; notifications are dropped; server→client requests
// get a MethodNotFound reply so the server never blocks on us.
func (c *Client) readLoop(r *bufio.Reader) {
	for {
		body, err := readMessage(r)
		if err != nil {
			c.fail(err)
			return
		}
		var msg incoming
		if err := json.Unmarshal(body, &msg); err != nil {
			continue // malformed frame — skip, don't kill the loop
		}
		switch {
		case msg.Method != "" && msg.ID != nil:
			// server→client request: we advertise no capabilities that
			// would prompt these, but answer defensively so a stray one
			// (e.g. client/registerCapability) can't hang the server.
			c.replyMethodNotFound(*msg.ID)
		case msg.Method != "":
			// notification (publishDiagnostics / logMessage / $/progress) → drop
		case msg.ID != nil:
			var id int64
			if err := json.Unmarshal(*msg.ID, &id); err == nil {
				c.deliver(id, callResult{Result: msg.Result, Error: msg.Error})
			}
		}
	}
}

func (c *Client) deliver(id int64, res callResult) {
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- res // ch is buffered (cap 1); never blocks
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.closed) })
}

// Call sends a request and blocks until the response arrives, ctx is
// cancelled, or the server dies. ctx here is the per-call (turn) ctx.
func (c *Client) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, fmt.Errorf("lspclient: marshal %s params: %w", method, err)
	}

	c.mu.Lock()
	if c.readErr != nil {
		c.mu.Unlock()
		return nil, c.readErr
	}
	c.seq++
	id := c.seq
	ch := make(chan callResult, 1)
	c.pending[id] = ch
	werr := c.writeLocked(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: raw})
	c.mu.Unlock()
	if werr != nil {
		c.drop(id)
		return nil, fmt.Errorf("lspclient: write %s: %w", method, werr)
	}

	select {
	case <-ctx.Done():
		c.drop(id)
		return nil, ctx.Err()
	case <-c.closed:
		return nil, c.deadErr()
	case res := <-ch:
		if res.Error != nil {
			return nil, fmt.Errorf("lspclient: rpc error %d: %s", res.Error.Code, res.Error.Message)
		}
		return res.Result, nil
	}
}

// Notify sends a JSON-RPC notification (no id, no response).
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return fmt.Errorf("lspclient: marshal %s params: %w", method, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.writeLocked(rpcRequest{JSONRPC: "2.0", Method: method, Params: raw})
}

func (c *Client) drop(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) deadErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	return errors.New("lspclient: server closed")
}

// writeLocked frames and writes one message. Caller holds c.mu so
// header+body never interleave with another message.
func (c *Client) writeLocked(req rpcRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return writeMessage(c.w, body)
}

func (c *Client) replyMethodNotFound(id json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return
	}
	body, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   rpcError        `json:"error"`
	}{"2.0", id, rpcError{Code: -32601, Message: "method not handled by seek"}})
	if err != nil {
		return
	}
	_ = writeMessage(c.w, body)
}

// Alive reports whether the read loop is still running — i.e. the server
// hasn't crashed / EOF'd. The manager checks this before reusing a cached
// client and restarts a dead one.
func (c *Client) Alive() bool {
	select {
	case <-c.closed:
		return false
	default:
		return true
	}
}

// Shutdown attempts a graceful LSP teardown — `shutdown` request then
// `exit` notification — before the caller Closes. Best-effort: errors are
// ignored (the process is killed by Close / session-ctx cancel anyway).
func (c *Client) Shutdown(ctx context.Context) {
	_, _ = c.Call(ctx, "shutdown", nil)
	_ = c.Notify(ctx, "exit", nil)
}

// Close terminates the subprocess (if any) and waits for it to exit.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	if c.cancel != nil {
		c.cancel()
	}
	if c.cmd != nil {
		return c.cmd.Wait()
	}
	return nil
}

// --- LSP method helpers ---------------------------------------------

// Initialize runs the LSP handshake: initialize request + initialized
// notification. rootURI is the workspace root as a file:// URI. Minimal
// capabilities (see clientCapabilities) keep the server from sending us
// requests we'd have to answer.
func (c *Client) Initialize(ctx context.Context, rootURI string) error {
	params := initializeParams{
		ProcessID:    os.Getpid(),
		RootURI:      rootURI,
		Capabilities: clientCapabilities{},
		ClientInfo:   &clientInfo{Name: "seek", Version: "0.1"},
	}
	if _, err := c.Call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("lspclient: initialize: %w", err)
	}
	if err := c.Notify(ctx, "initialized", struct{}{}); err != nil {
		return fmt.Errorf("lspclient: initialized: %w", err)
	}
	return nil
}

// DidOpen tells the server the current on-disk content of a file. seek
// edits files without live didChange sync (PRD D6), so we didOpen the
// fresh bytes before each query to keep the server's view current.
func (c *Client) DidOpen(ctx context.Context, uri, languageID, text string) error {
	return c.Notify(ctx, "textDocument/didOpen", didOpenParams{
		TextDocument: textDocumentItem{URI: uri, LanguageID: languageID, Version: 1, Text: text},
	})
}

// References returns all references to the symbol at pos (includeDecl
// controls whether the declaration itself is included). A null result
// (no references) returns (nil, nil).
func (c *Client) References(ctx context.Context, uri string, pos Position, includeDecl bool) ([]Location, error) {
	raw, err := c.Call(ctx, "textDocument/references", referenceParams{
		TextDocument: textDocumentIdentifier{URI: uri},
		Position:     pos,
		Context:      referenceContext{IncludeDeclaration: includeDecl},
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var locs []Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		return nil, fmt.Errorf("lspclient: decode references: %w", err)
	}
	return locs, nil
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(params)
}
