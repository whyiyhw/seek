// Package servecli implements the `seek serve` command (remote
// control plane, docs/prd/feature-remote-control-plane.md — M1b CLI
// skeleton). Mirrors routinescli / keyscli structure: Run(args,
// stdout, stderr) entry, dispatched ahead of flag.Parse in cmd/seek.
//
// What it owns: flag parsing (--frontend multi-value, --listen), frontend
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
	"github.com/whyiyhw/seek/internal/rpc/methods"
)

// frontends maps --frontend names to constructors. The default build
// registers the fake (M1b acceptance) and the web frontend (net/http
// is stdlib, so the D-F1 SDK gate does not apply — card c_qucxtu).
// SDK-backed adapters live behind their own build tags (D-F1): a chat
// frontend registers itself from its `//go:build <name>` file's init()
// in THIS package, so the default binary stays SDK-free while tagged
// builds gain the frontend here. (The feishu adapter M4/M5 was descoped
// 2026-11-13 — the slot stays open for any channel.)
//
// Constructors take the serve section (nil = absent) so config-file
// settings reach every frontend: web maps serve.web_addr onto
// WebConfig.Addr ("" → DefaultWebAddr). The section they get is
// already flag-folded — Run's --listen overrides web_addr before
// construction (effectiveServe) — so the one mapping serves both
// sources. The address is scope-gated to loopback + tailnet CGNAT
// (controlplane.checkWebAddr); what slips past the gate dies at the
// listen — startup fails loud either way, never a silent fallback to
// the default.
var frontends = map[string]func(*config.ServeConfig) controlplane.Frontend{
	"fake": func(*config.ServeConfig) controlplane.Frontend { return fake.New() },
	"web": func(sc *config.ServeConfig) controlplane.Frontend {
		addr := ""
		if sc != nil {
			addr = sc.WebAddr
		}
		fe := controlplane.NewWeb(controlplane.WebConfig{Addr: addr})
		// W4.2/W4.3/W4.6: hang the asset-plane method sets on /rpc
		// before Run (the §3.2 phase contract — NewWeb itself registers
		// nothing, so its empty-registry contract test stays true).
		// Store resolution is lazy inside the methods, so this wiring
		// stays free of filesystem side effects (buildFrontends runs
		// in tests).
		reg := fe.RPCRegistry()
		methods.NewSessionsFromEnv().RegisterAll(reg)
		methods.NewMemories().RegisterAll(reg)
		methods.NewProjectsFromEnv().RegisterAll(reg)
		methods.NewStatsFromEnv().RegisterAll(reg)
		methods.NewHooksFromEnv().RegisterAll(reg)
		// inspect-rpc §6 #12 close-out: subagent/list|get on the /rpc
		// plane (the M12.1 rows). GET /subagents stays as the panel's
		// compatibility口 — both read the same disk fold.
		methods.NewSubagents().RegisterAll(reg)
		return fe
	},
	// Voice v2 push-to-talk (feature-deepseek-app-api §4.3 V2): rides
	// the web page's recorder UI, so it only starts together with web
	// (buildFrontends attaches the two below). The ASR adapter reads
	// DEEPSEEK_API_KEY like every other DeepSeek consumer; a missing
	// key fails per upload with a visible error in the page, not at
	// startup.
	"voice": func(*config.ServeConfig) controlplane.Frontend {
		return controlplane.NewVoice(controlplane.NewDeepSeekVoiceASR())
	},
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
	// Help is honoured anywhere before the "--" terminator, not just at
	// args[0]: `seek serve --frontend=web -h` used to fall through to
	// flag.Parse, whose -h comes back as flag.ErrHelp — routed through
	// badConfig that read as exit 2 with the usage text buried on
	// stderr. A requested help is a clean exit 0 on stdout (the
	// skillcli/keyscli posture). "help" stays first-position-only: it
	// is the verb alias, and a stray positional must keep failing as one.
	for i, a := range args {
		if a == "--" {
			break
		}
		if (i == 0 && a == "help") || a == "-h" || a == "--help" {
			printHelp(stdout)
			return nil
		}
	}

	var fes frontendList
	var listen string
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Var(&fes, "frontend", "frontend to launch; repeat the flag for multiple (v1: fake, web, voice)")
	fs.StringVar(&listen, "listen", "", "web frontend listen address; overrides serve.web_addr (loopback + tailnet CGNAT only)")
	fs.Usage = func() { printHelp(stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Belt and braces: the scan above covers -h/--help, so this
			// is unreachable today — but if a future flag-set change
			// lets one through, fs.Usage has already printed the text,
			// and a help request must still be a clean exit 0, never
			// badConfig's exit 2.
			return nil
		}
		// flag already printed the error + help to fs.Output().
		return badConfig(nil, err)
	}
	if fs.NArg() > 0 {
		return badConfig(stderr, fmt.Errorf("unexpected argument %q (try --frontend=<name>)", fs.Arg(0)))
	}

	cfg, err := config.Load()
	if err != nil {
		// A corrupted config is fatal, same posture as seek core:
		// never silently run a plane off a file we can't read.
		return badConfig(stderr, fmt.Errorf("read config: %w", err))
	}

	// Built AFTER the config load so the serve section reaches the
	// constructors (web_addr → WebConfig.Addr); a --listen flag folds
	// over web_addr first (flag > config).
	srcs, err := buildFrontends(fes, effectiveServe(cfg.Serve, listen))
	if err != nil {
		return badConfig(stderr, err)
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

// effectiveServe folds the --listen flag over the loaded serve
// section: an empty flag returns the section untouched (nil included —
// the absent-section compat path), a set one wins over serve.web_addr
// (flag > config, core's --provider-over-default_provider posture) by
// copy, never mutation — cfg stays the file's truth for servePlane.
// The folded WebAddr walks the exact same constructor path as the
// config value, so the scope gate (checkWebAddr) and the listen own
// bad --listen values with the same exit-2, no-fallback posture.
func effectiveServe(sc *config.ServeConfig, listen string) *config.ServeConfig {
	if listen == "" {
		return sc
	}
	folded := new(config.ServeConfig)
	if sc != nil {
		*folded = *sc
	}
	folded.WebAddr = listen
	return folded
}

// buildFrontends resolves --frontend names against the registry.
// Duplicates are left to Serve to reject (it owns the "two frontends
// sharing a ConvID prefix is crosstalk" rule); unknown names die here.
// After construction, cross-frontend wiring runs: voice hosts its
// upload endpoint on web (pre-Run only, the mount contract).
func buildFrontends(names []string, sc *config.ServeConfig) ([]controlplane.Frontend, error) {
	if len(names) == 0 {
		return nil, errors.New("no frontends selected — pass --frontend=<name> (v1: fake, web, voice)")
	}
	out := make([]controlplane.Frontend, 0, len(names))
	for _, n := range names {
		ctor, ok := frontends[n]
		if !ok {
			return nil, fmt.Errorf("unknown frontend %q (registered: %s)", n, registeredNames())
		}
		out = append(out, ctor(sc))
	}

	// V2 wiring pass: the voice frontend has no transport of its own —
	// attach it to the web frontend built in THIS call (the page is its
	// recorder UI). Voice without web is a config mistake: fail here,
	// not later as a dead frontend.
	var web *controlplane.WebFrontend
	for _, f := range out {
		if w, ok := f.(*controlplane.WebFrontend); ok {
			web = w
		}
	}
	for _, f := range out {
		if v, ok := f.(*controlplane.VoiceFrontend); ok {
			if web == nil {
				return nil, errors.New("--frontend=voice needs --frontend=web in the same serve (voice uses the web page as its recorder UI)")
			}
			v.Attach(web)
		}
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
                       Registered in the default build: fake, web,
                       voice (voice needs web too — it uses the web
                       page's push-to-talk button as its recorder UI).

        --listen ADDR  web frontend listen address; overrides
                       serve.web_addr for this run. Loopback
                       (127.0.0.1/::1/localhost) and tailnet CGNAT
                       100.64.0.0/10 only — anything else refuses
                       to start (exit 2).

      The web frontend listens on 127.0.0.1:8790 and prints its
      token-bearing URL to stderr on startup; the token also lives
      in ~/.seek/serve/web.token (regenerate by deleting the file).
      To reach it from a tailnet: keep loopback and run
      "tailscale serve --tcp=8790", or set serve.web_addr (one-off:
      --listen) to the machine's tailnet IP (e.g. "100.x.y.z:8790").
      Both accept loopback and tailnet CGNAT (100.64.0.0/10) hosts
      only — 0.0.0.0 and any other address are refused at startup.

      --frontend=voice adds push-to-talk to the web page (hold 🎤,
      speak ≤120 s, release): the clip is transcribed by DeepSeek's
      ASR (DEEPSEEK_API_KEY, billed per second) and enters the plane
      as a message of its own conversation voice:<device-id> — bind
      it separately with "/bind <项目路径>" by voice or from the
      same page after a voice message. Microphone access needs a
      secure context: localhost works out of the box; over a tailnet
      use an HTTPS front ("tailscale serve", not --tcp) or browsers
      deny getUserMedia.

      Config: the optional "serve" section of ~/.seek/config.json
      (projects / max_concurrency / yolo / web_addr — default
      yolo=false, web_addr=127.0.0.1:8790).
      Older configs without the section work unchanged.`)
}
