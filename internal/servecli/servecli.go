// Package servecli implements the `seek serve` command (remote
// control plane, docs/prd/feature-remote-control-plane.md — M1b CLI
// skeleton). Mirrors routinescli / keyscli structure: Run(args,
// stdout, stderr) entry, dispatched ahead of flag.Parse in cmd/seek.
//
// What it owns: flag parsing (--frontend multi-value), frontend
// construction from the registry, config loading (the `serve` section
// with top-level backward compat), SIGINT/SIGTERM → graceful drain,
// and the exit-code handoff — controlplane.ExitCode is the single
// mapping (0 clean / 1 work dropped / 2 frontend-layer or bad
// config); cmd/seek routes serve errors through it verbatim.
//
// What it deliberately does NOT own: routing and reply delivery. The
// inbound handler is a stub until the M2 router lands — every message
// is logged and dropped, loudly, so an empty plane never pretends to
// have processed work.
package servecli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/controlplane"
	"github.com/whyiyhw/seek/internal/controlplane/fake"
)

// frontends maps --frontend names to constructors. v1 registers only
// the fake (M1b acceptance). Real adapters live behind their own build
// tags (D-F1): the feishu frontend (M4/M5) will register itself from
// a `//go:build feishu` file's init() in THIS package, so the default
// binary stays SDK-free while `-tags feishu` gains "feishu" here.
var frontends = map[string]func() controlplane.Frontend{
	"fake": func() controlplane.Frontend { return fake.New() },
}

// frontendList is a repeatable string flag: --frontend=fake
// --frontend=feishu. flag's builtin String can't accumulate.
type frontendList []string

func (f *frontendList) String() string { return strings.Join(*f, ",") }

func (f *frontendList) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// Run is the public entry. args is everything after `seek serve`.
// Every error returned is already printed to stderr — cmd/seek maps
// it onto the process exit code via controlplane.ExitCode WITHOUT
// re-printing.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			printHelp(stdout)
			return nil
		}
	}

	var fes frontendList
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&fes, "frontend", "frontend to launch; repeat the flag for multiple (v1: fake)")
	fs.Usage = func() { printHelp(stderr) }
	if err := fs.Parse(args); err != nil {
		// flag already printed the error + help to fs.Output().
		return badConfig(nil, err)
	}
	if fs.NArg() > 0 {
		return badConfig(stderr, fmt.Errorf("unexpected argument %q (try --frontend=<name>)", fs.Arg(0)))
	}

	srcs, err := buildFrontends(fes)
	if err != nil {
		return badConfig(stderr, err)
	}

	cfg, err := config.Load()
	if err != nil {
		// A corrupted config is fatal, same posture as seek core:
		// never silently run a plane off a file we can't read.
		return badConfig(stderr, fmt.Errorf("read config: %w", err))
	}

	// SIGINT/SIGTERM start the graceful drain; Serve owns the
	// choreography (stop ingress → drain → settle, bounded).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := servePlane(ctx, srcs, cfg, stderr); err != nil {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return err
	}
	return nil
}

// buildFrontends resolves --frontend names against the registry.
// Duplicates are left to Serve to reject (it owns the "two frontends
// sharing a ConvID prefix is crosstalk" rule); unknown names die here.
func buildFrontends(names []string) ([]controlplane.Frontend, error) {
	if len(names) == 0 {
		return nil, errors.New("no frontends selected — pass --frontend=<name> (v1: fake)")
	}
	out := make([]controlplane.Frontend, 0, len(names))
	for _, n := range names {
		ctor, ok := frontends[n]
		if !ok {
			return nil, fmt.Errorf("unknown frontend %q (registered: %s)", n, registeredNames())
		}
		out = append(out, ctor())
	}
	return out, nil
}

func registeredNames() string {
	names := make([]string, 0, len(frontends))
	for n := range frontends {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// servePlane runs the empty plane to completion: frontends fan into
// the Serve supervisor, inbound messages hit the stub handler, ctx
// cancellation (signal or test) triggers the drain. Split from Run so
// tests drive it with their own ctx instead of real signals.
func servePlane(ctx context.Context, fes []controlplane.Frontend, cfg config.Config, stderr io.Writer) error {
	// Config → plane wiring. MaxConcurrency passes through as-is: the
	// zero value means "unset" and controlplane.NewExecutor owns the
	// default, so the number lives in exactly one place (executor.go).
	maxParallel := 0
	if cfg.Serve != nil {
		maxParallel = cfg.Serve.MaxConcurrency
	}
	exec := controlplane.NewExecutor(controlplane.ExecutorConfig{MaxParallel: maxParallel})

	// The executor is wired from config now (the M1b mapping
	// executor.go's comment promises) but has nothing to run yet: the
	// M2 router takes it over once bindings exist. Until then the
	// handler is a stub — dropped messages are logged loudly, never
	// silently swallowed by an empty plane.
	handle := func(ctx context.Context, m controlplane.Inbound) {
		fmt.Fprintf(stderr, "serve: no router yet (M2): dropped message from %s (%d chars)\n", m.Conv, len(m.Text))
	}

	names := make([]string, len(fes))
	sources := make([]controlplane.ServeSource[controlplane.Inbound], len(fes))
	for i, f := range fes {
		names[i] = f.Name()
		sources[i] = f
	}
	projects := 0
	if cfg.Serve != nil {
		projects = len(cfg.Serve.Projects)
	}
	fmt.Fprintf(stderr, "serve: plane up: frontends=[%s] max_parallel=%d yolo=%v candidate_projects=%d\n",
		strings.Join(names, ","), exec.MaxParallel(), cfg.ServeYolo(), projects)

	// ServeSource[Inbound] ← Frontend is the structural seam asserted
	// in controlplane/serve.go; Serve handles the rest.
	err := controlplane.Serve(ctx, sources, handle, controlplane.ServeConfig{})
	if err == nil {
		fmt.Fprintln(stderr, "serve: plane down: drained cleanly")
	}
	return err
}

// badConfig prints (unless out is nil — the flag package already
// printed) and returns a usage/config-shaped error wrapped so
// controlplane.ExitCode routes it to exit 2 (frontend-layer / bad
// config) instead of the generic 1.
func badConfig(out io.Writer, err error) error {
	if out != nil {
		fmt.Fprintf(out, "serve: %v\n", err)
	}
	return fmt.Errorf("%w: %v", controlplane.ErrNoFrontends, err)
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, `seek serve — run the remote control plane

Usage:
  seek serve [--frontend=<name>]...

      Run the plane: the named frontends fan into one inbound queue,
      SIGINT/SIGTERM trigger a graceful drain, and the process exits
      0 (clean) / 1 (work dropped at the drain deadline) / 2 (bad
      config or every frontend failed).

      Flags:
        --frontend N   frontend to launch; repeat for multiple.
                       Registered in the default build: fake.
                       (feishu arrives with -tags feishu, D-F1.)

      Config: the optional "serve" section of ~/.seek/config.json
      (projects / max_concurrency / yolo — default yolo=false).
      Older configs without the section work unchanged.`)
}
