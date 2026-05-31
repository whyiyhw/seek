package lspclient

import "encoding/json"

// Hand-written minimal LSP type set — only what references-only needs
// (v6 柱 L 瘦身版). Deliberately NOT pulling go.lsp.dev/protocol: that
// package drags in go.uber.org/zap etc., and for ~a handful of message
// shapes hand-writing is lighter and matches seek's zero-dep ethos.

// --- JSON-RPC 2.0 envelope ------------------------------------------

// rpcRequest is an outgoing request or notification. A nil ID makes it a
// notification (no response expected); the encoder omits the field.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// incoming is any message the server sends us. The combination of ID and
// Method disambiguates the three cases:
//   - Method != "" && ID != nil  → server→client REQUEST (we reply MethodNotFound)
//   - Method != "" && ID == nil  → NOTIFICATION (diagnostics/log/progress → dropped)
//   - Method == "" && ID != nil  → RESPONSE to one of our calls
type incoming struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Result json.RawMessage  `json:"result"`
	Error  *rpcError        `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// callResult is what the read loop hands back to a waiting Call.
type callResult struct {
	Result json.RawMessage
	Error  *rpcError
}

// --- LSP method types (minimal) -------------------------------------

type initializeParams struct {
	ProcessID    int                `json:"processId"`
	RootURI      string             `json:"rootUri"`
	Capabilities clientCapabilities `json:"capabilities"`
	ClientInfo   *clientInfo        `json:"clientInfo,omitempty"`
}

// clientCapabilities is intentionally empty. A minimal client advertises
// no dynamic registration / workspace configuration / workDoneProgress,
// so well-behaved servers (gopls) won't send us client/registerCapability,
// workspace/configuration, or window/workDoneProgress/create requests we'd
// have to answer. Stray server→client requests are still handled
// defensively (MethodNotFound) in the read loop.
type clientCapabilities struct{}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Position is an LSP source position. Both fields are 0-based — line AND
// character. Callers working in 1-based (grep / editor) coordinates MUST
// convert at the boundary (the references tool does, see feature-lsp.md §8).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is one reference / definition site.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type referenceParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      referenceContext       `json:"context"`
}

type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}
