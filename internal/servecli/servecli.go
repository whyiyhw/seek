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
// What it deliberately does NOT own: routing and reply delivery —
// those are controlplane.Router's. servePlane wires the frontends,
// binding store, executor and config into one Router and hands it to
// Serve as the inbound handler (M2).
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

// frontends maps --frontend names to constructors. The default build
// registers the fake (M1b acceptance) and the web frontend (net/http
// is stdlib, so the D-F1 SDK gate does not apply — card c_qucxtu).
// SDK-backed adapters live behind their own build tags (D-F1): a chat
// frontend registers itself from its `//go:build <name>` file's init()
// in THIS package, so the default binary stays SDK-free while tagged
// builds gain the frontend here. (The feishu adapter M4/M5 was descoped
// 2026-11-13 — the slot stays open for any channel.)
var frontends = map[string]func() controlplane.Frontend{
	"fake": func() controlplane.Frontend { return fake.New() },
	"web":  func() controlplane.Frontend { return controlplane.NewWeb(controlplane.WebConfig{}) },
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
	fs.Var(&fes, "frontend", "frontend to launch; repeat the flag for multiple (v1: fake, web)")
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
		return nil, errors.New("no frontends selected — pass --frontend=<name> (v1: fake, web)")
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

// servePlane runs the plane to completion: frontends fan into the
// Serve supervisor, the Router answers each inbound (commands, bound
// prompts via the executor), ctx cancellation (signal or test)
// triggers the drain. Split from Run so tests drive it with their own
// ctx instead of real signals.
func servePlane(ctx context.Context, fes []controlplane.Frontend, cfg config.Config, stderr io.Writer) error {
	// Config → plane wiring. MaxConcurrency passes through as-is: the
	// zero value means "unset" and controlplane.NewExecutor owns the
	// default, so the number lives in exactly one place (executor.go).
	maxParallel := 0
	var projects []string
	if cfg.Serve != nil {
		maxParallel = cfg.Serve.MaxConcurrency
		projects = cfg.Serve.Projects
	}
	exec := controlplane.NewExecutor(controlplane.ExecutorConfig{MaxParallel: maxParallel})

	store, err := controlplane.OpenBindingStore()
	if err != nil {
		return fmt.Errorf("open bindings store: %w", err)
	}

	// The M2 router is the inbound handler: dedup → DirectAt →
	// command/binding route → exec → session writeback → Send.
	router := controlplane.NewRouter(fes, store, exec, controlplane.RouterConfig{
		Projects: projects,
		Yolo:     cfg.ServeYolo(),
	})

	names := make([]string, len(fes))
	sources := make([]controlplane.ServeSource[controlplane.Inbound], len(fes))
	for i, f := range fes {
		names[i] = f.Name()
		sources[i] = f
	}
	fmt.Fprintf(stderr, "serve: plane up: frontends=[%s] max_parallel=%d yolo=%v candidate_projects=%d\n",
		strings.Join(names, ","), exec.MaxParallel(), cfg.ServeYolo(), len(projects))

	// ServeSource[Inbound] ← Frontend is the structural seam asserted
	// in controlplane/serve.go; Serve handles the rest.
	err = controlplane.Serve(ctx, sources, router.Handle, controlplane.ServeConfig{})
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
                       Registered in the default build: fake, web.
                       (feishu arrives with -tags feishu, D-F1.)

      The web frontend listens on 127.0.0.1:8790 and prints its
      token-bearing URL to stderr on startup; the token also lives
      in ~/.seek/serve/web.token (regenerate by deleting the file).

      Config: the optional "serve" section of ~/.seek/config.json
      (projects / max_concurrency / yolo — default yolo=false).
      Older configs without the section work unchanged.`)
}
