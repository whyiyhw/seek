// Package teatest is a minimal local replacement for the upstream
// github.com/charmbracelet/x/exp/teatest test harness, rebuilt against
// Bubble Tea v2 (charm.land/bubbletea/v2).
//
// Upstream teatest still pins bubbletea v1 (as of 2026-08), so the two
// cannot coexist in one module graph. This package implements the small
// subset the seek TUI's render tests need (NewTestModel, Send, Output,
// WaitFor, WaitFinished, FinalOutput) with the same semantics, using the
// v2 Program API. Remove it when upstream teatest ships a v2.
package teatest

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestModelOptions defines all options available to the test function.
type TestModelOptions struct {
	size tea.WindowSizeMsg
}

// TestOption is a functional option.
type TestOption func(opts *TestModelOptions)

// WithInitialTermSize sets the initial terminal size for the program.
func WithInitialTermSize(x, y int) TestOption {
	return func(opts *TestModelOptions) {
		opts.size = tea.WindowSizeMsg{Width: x, Height: y}
	}
}

// WaitingForContext is the context for a WaitFor.
type WaitingForContext struct {
	Duration      time.Duration
	CheckInterval time.Duration
}

// WaitForOption changes how a WaitFor will behave.
type WaitForOption func(*WaitingForContext)

// WithCheckInterval sets how much time a WaitFor should sleep between
// every check.
func WithCheckInterval(d time.Duration) WaitForOption {
	return func(wf *WaitingForContext) {
		wf.CheckInterval = d
	}
}

// WithDuration sets how much time a WaitFor will wait for the condition.
func WithDuration(d time.Duration) WaitForOption {
	return func(wf *WaitingForContext) {
		wf.Duration = d
	}
}

// WaitFor keeps reading from r until the condition matches.
// Default duration is 1s, default check interval is 50ms.
// These defaults can be changed with WithDuration and WithCheckInterval.
func WaitFor(
	tb testing.TB,
	r io.Reader,
	condition func(bts []byte) bool,
	options ...WaitForOption,
) {
	tb.Helper()
	if err := doWaitFor(r, condition, options...); err != nil {
		tb.Fatal(err)
	}
}

func doWaitFor(r io.Reader, condition func(bts []byte) bool, options ...WaitForOption) error {
	wf := WaitingForContext{
		Duration:      time.Second,
		CheckInterval: 50 * time.Millisecond,
	}

	for _, opt := range options {
		opt(&wf)
	}

	var b bytes.Buffer
	start := time.Now()
	for time.Since(start) <= wf.Duration {
		if _, err := io.ReadAll(io.TeeReader(r, &b)); err != nil {
			return fmt.Errorf("WaitFor: %w", err)
		}
		if condition(b.Bytes()) {
			return nil
		}
		time.Sleep(wf.CheckInterval)
	}
	return fmt.Errorf("WaitFor: condition not met after %s. Last output:\n%s", wf.Duration, b.String())
}

// TestModel is a model that is being tested.
type TestModel struct {
	program *tea.Program

	in  *bytes.Buffer
	out io.ReadWriter

	modelCh chan tea.Model
	model   tea.Model

	done   sync.Once
	doneCh chan bool
}

// NewTestModel makes a new TestModel which can be used for tests.
func NewTestModel(tb testing.TB, m tea.Model, options ...TestOption) *TestModel {
	tb.Helper()

	var opts TestModelOptions
	for _, opt := range options {
		opt(&opts)
	}

	tm := &TestModel{
		in:      bytes.NewBuffer(nil),
		out:     safe(bytes.NewBuffer(nil)),
		modelCh: make(chan tea.Model, 1),
		doneCh:  make(chan bool, 1),
	}

	programOpts := []tea.ProgramOption{
		tea.WithInput(tm.in),
		tea.WithOutput(tm.out),
		tea.WithoutSignals(),
	}
	if opts.size.Width != 0 {
		programOpts = append(programOpts, tea.WithWindowSize(opts.size.Width, opts.size.Height))
	}

	tm.program = tea.NewProgram(m, programOpts...)

	go func() {
		model, err := tm.program.Run()
		if err != nil {
			// tb.Errorf panics if the test already completed by the
			// time Run returns ("Log in goroutine after test has
			// completed"); the recover keeps a late program failure
			// from masking the original test result with a panic.
			defer func() { _ = recover() }()
			tb.Errorf("app failed: %s", err)
		}
		tm.modelCh <- model
		tm.doneCh <- true
	}()

	tb.Cleanup(func() {
		// Make sure the program is shut down even if the test fails
		// mid-way, so no goroutines leak into other tests.
		select {
		case <-tm.doneCh:
		default:
			tm.program.Quit()
			select {
			case <-tm.doneCh:
			case <-time.After(2 * time.Second):
				tb.Log("teatest: program did not exit after Quit; forcing kill")
				tm.program.Kill()
			}
		}
	})

	return tm
}

// safe wraps a buffer so concurrent reads and writes don't race.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Read(p)
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func safe(b *bytes.Buffer) *safeBuffer {
	return &safeBuffer{b: *b}
}

// FinalOpts represents the options for FinalModel and FinalOutput.
type FinalOpts struct {
	timeout   time.Duration
	onTimeout func(tb testing.TB)
}

// FinalOpt changes FinalOpts.
type FinalOpt func(opts *FinalOpts)

// WithTimeoutFn allows to define what happens when WaitFinished times out.
func WithTimeoutFn(fn func(tb testing.TB)) FinalOpt {
	return func(opts *FinalOpts) {
		opts.onTimeout = fn
	}
}

// WithFinalTimeout allows to set a timeout for how long FinalModel and
// FinalOutput should wait for the program to complete.
func WithFinalTimeout(d time.Duration) FinalOpt {
	return func(opts *FinalOpts) {
		opts.timeout = d
	}
}

func (tm *TestModel) waitDone(tb testing.TB, opts []FinalOpt) {
	tm.done.Do(func() {
		fopts := FinalOpts{}
		for _, opt := range opts {
			opt(&fopts)
		}
		if fopts.timeout > 0 {
			select {
			case <-time.After(fopts.timeout):
				if fopts.onTimeout == nil {
					tb.Fatalf("timeout after %s", fopts.timeout)
				}
				fopts.onTimeout(tb)
			case <-tm.doneCh:
			}
		} else {
			<-tm.doneCh
		}
	})
}

// WaitFinished waits for the app to finish.
// This method only returns once the program has finished running or when
// it times out.
func (tm *TestModel) WaitFinished(tb testing.TB, opts ...FinalOpt) {
	tm.waitDone(tb, opts)
}

// FinalModel returns the resulting model, resulting from program.Run().
// This method only returns once the program has finished running or when
// it times out.
func (tm *TestModel) FinalModel(tb testing.TB, opts ...FinalOpt) tea.Model {
	tm.waitDone(tb, opts)
	select {
	case m := <-tm.modelCh:
		if m != nil {
			tm.model = m
		}
		return tm.model
	default:
		return tm.model
	}
}

// FinalOutput returns the program's final output io.Reader.
// This method only returns once the program has finished running or when
// it times out.
func (tm *TestModel) FinalOutput(tb testing.TB, opts ...FinalOpt) io.Reader {
	tm.waitDone(tb, opts)
	return tm.Output()
}

// Output returns the program's current output io.Reader.
func (tm *TestModel) Output() io.Reader {
	return tm.out
}

// Send sends messages to the underlying program.
func (tm *TestModel) Send(m tea.Msg) {
	tm.program.Send(m)
}

// Quit quits the program and releases the terminal.
func (tm *TestModel) Quit() error {
	tm.program.Quit()
	return nil
}

// GetProgram gets the TestModel's program.
func (tm *TestModel) GetProgram() *tea.Program {
	return tm.program
}
