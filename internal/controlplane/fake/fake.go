// Package fake is the default-build fake Frontend of the remote
// control plane: a transport-shaped no-op that exists so `seek serve`
// can be started, health-checked and shut down before any real
// channel adapter lands (M1b acceptance), and so plane-layer tests
// have a frontend with zero SDK surface. It never receives or
// delivers anything real.
//
// This is NOT the feishu placeholder (internal/controlplane/feishu,
// behind the `feishu` build tag, D-F1): that one proves the SDK
// isolation gate; this one proves the plane's own lifecycle. It lives
// in the default build on purpose — no dependency to isolate.
package fake

import "context"

import "github.com/whyiyhw/seek/internal/controlplane"

// Frontend is the fake: up while ctx lives, silent throughout.
type Frontend struct{}

// Compile-time port checks: the fake IS a controlplane.Frontend (and
// thereby a ServeSource[Inbound] structurally — serve.go's assertion).
var _ controlplane.Frontend = (*Frontend)(nil)

// New returns the fake frontend. Name is "fake" — the ConvID prefix
// this frontend would own.
func New() *Frontend { return &Frontend{} }

// Name implements the Frontend port.
func (f *Frontend) Name() string { return "fake" }

// Run implements the Frontend port: parks until ctx ends, pushing
// nothing — the transport-shaped lifecycle without the transport.
// A nil return on cancellation is the contract's "clean shutdown",
// not a crash (serve.go only counts Run errors while ctx is live).
func (f *Frontend) Run(ctx context.Context, in chan<- controlplane.Inbound) error {
	<-ctx.Done()
	return nil
}

// Send implements the Frontend port: swallows the reply and reports
// success — nothing is listening on the other side. Keeping it a
// no-op (rather than an error) makes the fake safe to leave wired in
// while the plane is otherwise empty.
func (f *Frontend) Send(ctx context.Context, conv controlplane.ConvID, r controlplane.Reply) error {
	return nil
}
