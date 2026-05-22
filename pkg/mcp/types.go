// Package mcp implements a JSON-RPC 2.0 client for the Model Context
// Protocol (MCP) stdio transport. It covers only the tools capability
// (resources and prompts are deferred to v1.1+).
package mcp

import "encoding/json"

// ProtocolVersion is the MCP protocol version this client speaks.
const ProtocolVersion = "2024-11-05"

// ---------- JSON-RPC 2.0 wire types ----------

// request is an outbound JSON-RPC 2.0 call. ID=0 means notification.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is an inbound JSON-RPC 2.0 reply. ID=0 on notifications.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

// ---------- MCP initialize ----------

type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    clientCapabilities `json:"capabilities"`
	ClientInfo      clientInfo         `json:"clientInfo"`
}

type clientCapabilities struct{}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the server's response to the initialize request.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

type serverCapabilities struct {
	Tools *toolsCapability `json:"tools,omitempty"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo is the name/version pair the server reports.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ---------- MCP tools/list ----------

type listToolsResult struct {
	Tools []ToolDef `json:"tools"`
}

// ToolDef is one tool as returned by tools/list.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ---------- MCP tools/call ----------

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// CallToolResult is the response from tools/call.
type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is one piece of tool output. Type is "text", "image", etc.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
