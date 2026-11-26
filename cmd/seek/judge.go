// judge.go — the `seek judge` lane: PRD
// docs/prd/feature-judge-engine.md §6 work item 5, back half (card
// c_jdo7rp). It is the CLI wiring around internal/judge's kernel:
// flags, JSONL in/out, bounded concurrency, and the 0/1/2 exit codes.
//
// Dispatch shape follows the other verb lanes (skill / memory / cron):
// `os.Args[1] == "judge"` is caught in run() BEFORE flag.Parse and
// before any agent runtime, session store, tool registry or MCP load.
// That is not cosmetic — a judge run must fork nothing and must not
// depend on ~/.seek being warm. The autopilot-lane pitfall (flag.Parse
// stops at the first positional, so `seek <verb> ... --flag` would fold
// the flag into the positional) is exactly why this lane owns its own
// FlagSet and rejects leftovers: `seek judge --rubric x` parses, and
// `seek judge positional --rubric y` exits 2 instead of silently
// swallowing the flag.
//
// --isolated (§6 item 6) is still a later card: passing it is an UNKNOWN
// FLAG → exit 2. --max-cost IS wired here (§6 item 7, card c_qjuags): once
// accumulated USD cost reaches the cap the feeder stops dispatching,
// in-flight entries finish, the file is closed with rows + _meta, and the
// lane exits 3 ("collect what you have; the next batch resumes").
//
// I/O contract (PRD §3.1): --in and --out default to "-", i.e. stdin /
// stdout. Input is read line by line (never buffered whole), judged by
// at most judgeMaxParallel workers, and written in INPUT ORDER as
// contiguous prefixes become available. The row ORDER is an
// implementation invariant, not a caller contract — PRD §3.3 invariant 1
// says the caller joins on `id`; keeping the order is just what makes
// the output diffable. One marshaling authority (judge.MarshalRow /
// judge.MarshalMeta) means a streamed file and a buffered Result.JSONL()
// are byte-identical for the same rows.
package main

// The imports of this file stay deliberately narrow: no agent, no
// tools, no MCP, no session store. Anything added here must justify why
// the judge lane needs it.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/whyiyhw/seek/internal/judge"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/session"
)

const (
	// judgeDefaultParallel mirrors PRD §4 D3 (and controlplane's
	// MaxParallel posture): 4 in flight by default.
	judgeDefaultParallel = 4
	// judgeMaxParallel is the ceiling. It is a CONSTANT, not a flag
	// default that can be raised from the command line: an unbounded
	// fan-out on a metered lane is a cost incident, and PRD §3.4's
	// caller contract has no way to express "I meant to do that". Values
	// above it exit 2 (usage), never silently clamp.
	judgeMaxParallel = 16
)

// errJudgeUsage is the exit-code-2 sentinel: the complaint has already
// been printed to stderr by the validation site, so main() routes it
// straight to os.Exit(2) without the "seek:" prefix — the same contract
// errToolsUsage / errFlagUsage keep for CI ("you wrote the command
// wrong", not "the run failed").
var errJudgeUsage = errors.New("judge usage")

// errJudgePartial is the exit-code-1 sentinel: the output file was
// written in full (rows + `_meta`) and at least one row carries an
// `error`. main() exits 1 silently — the actionable error text is IN
// the JSONL, which is what PRD §3.4 tells the caller to read.
var errJudgePartial = errors.New("judge partial failure")

// errJudgeBudget is the exit-code-3 sentinel (PRD §3.4): the USD cap was
// reached. The file is COMPLETE — rows for every judged entry plus _meta —
// and the caller resumes with the next batch. It outranks errJudgePartial:
// "we stopped on purpose" is a different CI signal from "some rows were
// malformed", even when both are true.
var errJudgeBudget = errors.New("judge budget exhausted")

// judgeOptions is the parsed command line. One struct so the flag-shape
// table test can assert the whole surface without touching I/O.
type judgeOptions struct {
	rubric       string
	in           string
	out          string
	provider     string
	model        string
	baseURL      string
	providerName string
	parallel     int
	maxCost      float64 // 0 = no cap (the c_jdo7rp behaviour)
}

// judgeUsage is the lane's own help text. Kept short and honest: every
// flag listed here exists; --isolated (a later card) is called out so a
// caller reading the PRD does not discover its absence through an exit code.
const judgeUsage = `seek judge — judge a JSONL batch against a caller-owned rubric

Usage:
  seek judge --rubric <file> [--in <file|->] [--out <file|->]
             [--provider <p>] [--model <id>] [--parallel N]
             [--max-cost <USD>]

Reading:
  --rubric <file>   REQUIRED. The rubric file the caller owns; its
                    sha256 is echoed in the trailing _meta line.
  --in <file|->     Input JSONL: one {"id","text","meta"} per line.
                    "-" (default) reads stdin.
  --out <file|->    Output JSONL: one row per input line, then _meta.
                    "-" (default) writes stdout.

Model lane:
  --provider <p>    deepseek (default) | anthropic | openai | gemini |
                    glm | opencode | opendesign | compatible
  --model <id>      Model id; defaults to the lane's own default.
  --base-url <u>    Endpoint override (compatible lanes / mirrors).
  --provider-name   Display name for --provider=compatible.

Concurrency:
  --parallel N      Entries judged in flight (default 4, max 16).

Budget:
  --max-cost <USD>  Stop dispatching new entries once accumulated cost
                    reaches this cap (in-flight entries still finish).
                    Omit for no cap. <=0 / non-numeric is a usage error.

Exit codes (PRD §3.4):
  0  every entry judged
  1  the file is complete but at least one row has "error"
  2  usage/configuration error — fix the command, do not retry
  3  --max-cost reached — collect the complete file, resume next batch`

// runJudge is the lane entry. stdin is injected (rather than read from
// os.Stdin) so the flag-shape tests can drive it without a subprocess.
func runJudge(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(stdout, judgeUsage)
		return nil
	}
	opts, err := parseJudgeFlags(args, stderr)
	if err != nil {
		return err
	}

	// ① rubric: caller-owned file (PRD §4 D1). A missing/unreadable file
	// is a configuration error (exit 2): retrying the identical command
	// cannot help.
	raw, err := os.ReadFile(opts.rubric)
	if err != nil {
		fmt.Fprintf(stderr, "judge: rubric: %v\n", err)
		return errJudgeUsage
	}
	if strings.TrimSpace(string(raw)) == "" {
		fmt.Fprintf(stderr, "judge: rubric %s is empty — the rubric is caller-owned (PRD §4 D1)\n", opts.rubric)
		return errJudgeUsage
	}

	// ② provider lane. buildProvider is the one construction point every
	// lane shares; it reads config/env and never forks anything.
	provider, dsClient, _, modelDefault, err := buildProvider(opts.provider, opts.baseURL, opts.providerName)
	if err != nil {
		fmt.Fprintf(stderr, "judge: %v\n", err)
		return errJudgeUsage
	}
	model := opts.model
	if model == "" {
		model = modelDefault
	}
	if model == "" {
		fmt.Fprintln(stderr, "judge: --model is required for this provider (it has no lane default)")
		return errJudgeUsage
	}
	var chat judge.Chatter
	switch {
	case dsClient != nil:
		chat = dsClient
	case provider != nil:
		// Second-tier lane: the same adapter autopilot's decomposer and
		// verifier use, with a per-run wire identity for the OpenCode
		// lane's x-opencode-session contract. EphemeralID writes nothing
		// to disk — the judge face is zero-state.
		chat = llmChatClient{p: provider, sessionID: session.EphemeralID()}
	default:
		fmt.Fprintln(stderr, "judge: no model provider resolved")
		return errJudgeUsage
	}

	// ③ kernel. temperature is pinned to 0 (PRD §4 D6) — an explicit
	// zero is a real, different request from "no key at all", and it is
	// echoed in _meta so two runs' drift is explainable.
	zero := 0.0
	j, err := judge.New(chat, model, string(raw),
		judge.WithTemperature(&zero),
		judge.WithTier(pricing.CurrentTier(time.Now())),
	)
	if err != nil {
		fmt.Fprintf(stderr, "judge: %v\n", err)
		return errJudgeUsage
	}

	in, closeIn, err := openJudgeInput(opts.in, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "judge: input: %v\n", err)
		return errJudgeUsage
	}
	defer closeIn()
	out, closeOut, err := openJudgeOutput(opts.out, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "judge: output: %v\n", err)
		return errJudgeUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fold, budgetHit, runErr := judgeStream(ctx, j, in, out, opts.parallel, opts.maxCost)
	if cerr := closeOut(); cerr != nil && runErr == nil {
		runErr = fmt.Errorf("judge: closing output: %w", cerr)
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "judge: %v\n", runErr)
		return runErr
	}
	if budgetHit {
		// The file is COMPLETE (all judged rows + _meta). Exit 3 is the
		// "resume next batch" CI signal — it OUTRANKS partial failure:
		// a deliberate stop is never reported as "rows went wrong".
		fmt.Fprintf(stderr, "judge: --max-cost %s reached after %d row(s) (spent %s) — collect this file, resume the rest in a new batch\n",
			pricing.FormatCost(opts.maxCost), fold.Meta().Items, pricing.FormatCost(fold.Cost()))
		return errJudgeBudget
	}
	if n := fold.Errors(); n > 0 {
		// The text stays on stderr so stdout/the --out file remains pure
		// JSONL (PRD §3.3 / the --json contract announced the same rule).
		fmt.Fprintf(stderr, "judge: %d of %d row(s) carried an error — see the error rows in the output\n", n, fold.Meta().Items)
		return errJudgePartial
	}
	return nil
}

// parseJudgeFlags owns the lane's flag surface. It is a separate,
// I/O-free function because the shape of the command line is a
// published contract (card acceptance ②): the table test drives it
// directly, including the two failure shapes that used to be silent
// under flag.Parse (leftover positionals and unknown flags).
func parseJudgeFlags(args []string, stderr io.Writer) (judgeOptions, error) {
	var opts judgeOptions
	fs := flag.NewFlagSet("judge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, judgeUsage) }
	fs.StringVar(&opts.rubric, "rubric", "", "rubric file (required)")
	fs.StringVar(&opts.in, "in", "-", "input JSONL file, - for stdin")
	fs.StringVar(&opts.out, "out", "-", "output JSONL file, - for stdout")
	fs.StringVar(&opts.provider, "provider", "", "provider lane")
	fs.StringVar(&opts.model, "model", "", "model id")
	fs.StringVar(&opts.baseURL, "base-url", "", "endpoint override")
	fs.StringVar(&opts.providerName, "provider-name", "Compatible", "display name for --provider=compatible")
	fs.IntVar(&opts.parallel, "parallel", judgeDefaultParallel, "entries judged in flight")
	// --max-cost is a string so a bad value (non-numeric, <=0, NaN/Inf)
	// is OUR validation error with an honest message, not flag's generic
	// "invalid value". Absent/empty means no cap (the c_jdo7rp default).
	var maxCostRaw string
	fs.StringVar(&maxCostRaw, "max-cost", "", "USD cap (>0); stop dispatching once reached (absent = no cap)")
	if err := fs.Parse(args); err != nil {
		// The flag package already printed "flag provided but not
		// defined: -x" and the usage text. Wrap it as the usage
		// sentinel so main() exits 2 rather than 1.
		return opts, errJudgeUsage
	}
	if fs.NArg() > 0 {
		// `seek judge <positional> --rubric y` is the autopilot-lane
		// trap: flag.Parse stops at the first positional, so the flag
		// becomes an argument of the verb instead of a flag. Reject it
		// loudly instead of judging a batch the caller did not describe.
		fmt.Fprintf(stderr, "judge: unexpected positional argument(s) %q — judge takes flags only (see `seek judge help`)\n", fs.Args())
		return opts, errJudgeUsage
	}
	if strings.TrimSpace(opts.rubric) == "" {
		fmt.Fprintln(stderr, "judge: --rubric is required (the rubric is a file the caller owns, PRD §4 D1)")
		return opts, errJudgeUsage
	}
	if opts.parallel < 1 || opts.parallel > judgeMaxParallel {
		fmt.Fprintf(stderr, "judge: --parallel must be between 1 and %d (got %d)\n", judgeMaxParallel, opts.parallel)
		return opts, errJudgeUsage
	}
	if strings.TrimSpace(maxCostRaw) != "" {
		v, err := strconv.ParseFloat(strings.TrimSpace(maxCostRaw), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			fmt.Fprintf(stderr, "judge: --max-cost must be a positive USD amount (got %q)\n", maxCostRaw)
			return opts, errJudgeUsage
		}
		opts.maxCost = v
	}
	if opts.in == "" {
		opts.in = "-"
	}
	if opts.out == "" {
		opts.out = "-"
	}
	return opts, nil
}

// openJudgeInput resolves the "-" sentinel to the injected reader, or
// opens the named file. The returned closer is a no-op for the "-" case
// so callers can defer it unconditionally.
func openJudgeInput(path string, fallback io.Reader) (io.Reader, func() error, error) {
	if path == "-" {
		return fallback, func() error { return nil }, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// openJudgeOutput resolves the "-" sentinel to the injected writer, or
// creates the named file.
func openJudgeOutput(path string, fallback io.Writer) (io.Writer, func() error, error) {
	if path == "-" {
		return fallback, func() error { return nil }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, f.Close, nil
}

// judgeStream is the pipeline: read → bounded worker pool → ordered
// emitter. It returns the fold (so the caller can render `_meta` and
// derive the exit code), whether the --max-cost cap was reached, and any
// write/read error. A per-entry failure never appears here as an error —
// it is a row.
//
// Budget gate (PRD §6 item 7): the emitter is the single point that knows
// the running cost (it folds each outcome), so IT decides when the cap is
// hit and flips budgetStopped. The feeder checks that flag before each
// dispatch: once set, no NEW entry is sent, but every already-dispatched
// entry still finishes and is emitted (in-flight rows are never dropped).
// maxCost <= 0 disables the gate entirely — the c_jdo7rp behaviour.
func judgeStream(ctx context.Context, j *judge.Judge, in io.Reader, out io.Writer, parallel int, maxCost float64) (*judge.Folder, bool, error) {
	type job struct {
		idx   int
		entry judge.Entry
	}
	type result struct {
		idx int
		o   judge.Outcome
	}

	jobs := make(chan job)
	results := make(chan result)
	foldDone := make(chan *judge.Folder, 1)
	writeDone := make(chan error, 1)
	// budgetDone is closed exactly once by the emitter the moment the
	// cumulative cost crosses the cap. The feeder SELECTs on it while
	// dispatching, so an entry that no worker has picked up yet is NOT
	// started after the cap — "stop dispatching" is precise, while the
	// entries already in flight still drain to the file.
	budgetDone := make(chan struct{})
	var budgetStopped atomic.Bool

	var workers sync.WaitGroup
	for i := 0; i < parallel; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for jb := range jobs {
				// JudgeEntry never returns a Go error: ctx cancellation,
				// bad lines, transport failures and malformed replies all
				// become that row's own error text.
				results <- result{idx: jb.idx, o: j.JudgeEntry(ctx, jb.entry)}
			}
		}()
	}

	// Ordered emitter. It is the ONLY writer, so rows can never interleave
	// a half line even though N workers produce them; it also folds the
	// same outcomes into `_meta`, which is why the counts cannot disagree
	// with the rows on disk.
	var outMu sync.Mutex
	emit := func(line []byte) error {
		outMu.Lock()
		defer outMu.Unlock()
		if _, err := out.Write(append(line, '\n')); err != nil {
			return err
		}
		return nil
	}
	emitRow := func(o judge.Outcome) error {
		line, err := judge.MarshalRow(o.Row)
		if err != nil {
			// A row whose meta cannot be re-marshaled must still not
			// poison the batch: replace it with an error row (the same
			// per-entry isolation the kernel guarantees for judgment
			// failures).
			line, err = judge.MarshalRow(judge.Row{ID: o.Row.ID, Error: "marshal row: " + err.Error()})
			if err != nil {
				return err
			}
		}
		return emit(line)
	}

	go func() {
		pending := make(map[int]judge.Outcome)
		fold := j.NewFolder()
		next := 0
		var writeErr error
		for r := range results {
			pending[r.idx] = r.o
			for {
				o, ok := pending[next]
				if !ok {
					break
				}
				delete(pending, next)
				next++
				fold.Add(o)
				// The emitter is the one place that sees the running
				// cost. When the cap is reached it flips the gate: the
				// feeder stops dispatching new entries, while these
				// already-in-flight results keep flowing to the file.
				if maxCost > 0 && fold.Cost() >= maxCost && !budgetStopped.Swap(true) {
					close(budgetDone)
				}
				if writeErr == nil {
					writeErr = emitRow(o)
				}
			}
		}
		// Every result arrived and every index was < next, so pending is
		// empty by construction; flush defensively anyway — losing a row
		// silently is the one failure this lane must not have.
		if len(pending) > 0 {
			writeDone <- fmt.Errorf("judge: %d result(s) were never emitted (ordering bug)", len(pending))
			foldDone <- fold
			return
		}
		foldDone <- fold
		writeDone <- writeErr
	}()

	// Feeder (this goroutine). Lines are read one at a time and handed
	// straight to the pool — the input is never buffered whole, so a
	// stream on stdin keeps the memory profile flat.
	rd := bufio.NewReaderSize(in, 64*1024)
	first := true
	idx := 0
	var readErr error
	for {
		// Budget gate: once the emitter has flipped the flag, stop
		// dispatching NEW entries. Whatever is already in the pool keeps
		// running — close(jobs) below lets those workers drain — and its
		// rows still reach the file. Checking before the read also means
		// we never pull an extra line off stdin that we won't judge.
		if budgetStopped.Load() {
			break
		}
		line, err := rd.ReadBytes('\n')
		if len(line) > 0 {
			s := strings.TrimSpace(string(line))
			if first {
				s = strings.TrimSpace(strings.TrimPrefix(s, "\ufeff"))
				first = false
			}
			// Re-check right before the send: the flag may have flipped
			// while an earlier dispatch blocked on a busy worker pool.
			// The SELECT makes that check race-free — a job is either
			// handed to a worker or the budget gate wins, never both.
			if s != "" && !budgetStopped.Load() {
				// Priority check: if the gate already fired, do not enter
				// the send select at all (where a ready worker could win
				// the coin-flip and start one more entry past the cap).
				select {
				case <-budgetDone:
					// cap fired between the Load and here — do not dispatch
				default:
					select {
					case jobs <- job{idx: idx, entry: judge.ParseEntry([]byte(s))}:
						idx++
					case <-budgetDone:
						// cap fired while we waited for a worker — the
						// entry was never started. Drop it.
					}
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			readErr = fmt.Errorf("read input: %w", err)
			break
		}
	}
	close(jobs)
	workers.Wait()
	close(results)

	fold := <-foldDone
	if writeErr := <-writeDone; writeErr != nil {
		return fold, budgetStopped.Load(), fmt.Errorf("write output: %w", writeErr)
	}
	if readErr != nil {
		return fold, budgetStopped.Load(), readErr
	}

	metaLine, err := judge.MarshalMeta(fold.Meta())
	if err != nil {
		return fold, budgetStopped.Load(), fmt.Errorf("marshal _meta: %w", err)
	}
	if err := emit(metaLine); err != nil {
		return fold, budgetStopped.Load(), fmt.Errorf("write _meta: %w", err)
	}
	return fold, budgetStopped.Load(), nil
}
