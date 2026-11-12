// Package feishu is the Feishu adapter of seek's remote control plane
// — the first Frontend implementation (PRD docs/prd/feature-remote-
// control-plane.md D-F1/D-F2/D-F3).
//
// # Build-tag contract (D-F1)
//
// Everything real in this package lives behind the `feishu` build tag:
//
//	//go:build feishu
//
// The default seek build compiles ONLY this doc file: the larksuite
// SDK (github.com/larksuite/oapi-sdk-go/v3) enters the binary only via
// `go build -tags feishu`, so the shipped seek stays stdlib-only at
// this layer and its size is unchanged (single-binary doctrine). An
// untagged .go file joining this package would let the SDK leak into
// the default build — internal/controlplane/buildtags_test.go fails
// the build if that ever happens, by asserting the default GoFiles of
// this package is exactly [doc.go].
//
// The mechanism is already load-bearing: M6a ships a tagged placeholder
// (Fake) that proves the gate both ways — invisible under default
// tags, compiled and driven through the serve fan-in under
// `-tags feishu`. M4 (larkws Run) and M5 (larkim Send) replace the
// placeholder's body under the same tag; the tag itself never moves.
package feishu
