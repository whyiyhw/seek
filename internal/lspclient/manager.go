package lspclient

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const initTimeout = 30 * time.Second // gopls cold-start can take 5-15s

// language describes one supported LSP server.
type language struct {
	id          string // LSP languageId
	name        string // human / binary name for messages
	command     string // executable
	args        []string
	install     string // hint shown when the binary is missing
	exts        []string
	rootMarkers []string // files that mark the workspace root
}

var languages = []language{
	{
		id: "go", name: "gopls", command: "gopls", args: nil,
		install:     "go install golang.org/x/tools/gopls@latest",
		exts:        []string{".go"},
		rootMarkers: []string{"go.work", "go.mod"},
	},
	{
		id: "python", name: "pyright", command: "pyright-langserver", args: []string{"--stdio"},
		install:     "npm i -g pyright (or pip install pyright)",
		exts:        []string{".py", ".pyi"},
		rootMarkers: []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"},
	},
	{
		id: "typescript", name: "typescript-language-server", command: "typescript-language-server", args: []string{"--stdio"},
		install:     "npm i -g typescript-language-server typescript",
		exts:        []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"},
		rootMarkers: []string{"tsconfig.json", "jsconfig.json", "package.json"},
	},
}

func detectLang(file string) (language, bool) {
	ext := strings.ToLower(filepath.Ext(file))
	for _, l := range languages {
		for _, e := range l.exts {
			if e == ext {
				return l, true
			}
		}
	}
	return language{}, false
}

// MissingBinaryError is returned when a language server binary isn't on
// PATH. The references tool turns it into the `[references: <server> not
// found; install: …]` wire message that points the user at a fix.
type MissingBinaryError struct {
	Command string
	Install string
}

func (e *MissingBinaryError) Error() string {
	return fmt.Sprintf("%s not found in PATH; install: %s", e.Command, e.Install)
}

// serverEntry is one cached language server. The server process is bound
// to the manager's SESSION ctx; init runs in the background (also session-
// bound) so a turn cancel during cold-start doesn't kill or orphan it.
type serverEntry struct {
	lang    language
	client  *Client
	ready   chan struct{} // closed once init finishes (ok or error)
	initErr error         // set before ready closes; read after <-ready
}

// Manager owns the session's language servers: lazy start, one per
// language, crash-restart, Shutdown-kills-all. Build one per seek session
// and wire Shutdown into teardown (mirrors bgjob.Manager — see
// feature-lsp.md §3 "与柱 K 的生命周期类比").
type Manager struct {
	rootDir string
	ctx     context.Context // SESSION ctx — passed to launch (D4), NOT a turn ctx

	mu      sync.Mutex
	servers map[string]*serverEntry

	// launch creates a connected, not-yet-initialized client for lang.
	// Default = LookPath + StartServer; overridable in tests.
	launch func(ctx context.Context, lang language) (*Client, error)
}

// New returns a Manager rooted at rootDir. sessionCtx must outlive
// individual turns — when it's cancelled every server dies.
func New(rootDir string, sessionCtx context.Context) *Manager {
	return &Manager{
		rootDir: rootDir,
		ctx:     sessionCtx,
		servers: map[string]*serverEntry{},
		launch:  defaultLaunch,
	}
}

func defaultLaunch(ctx context.Context, lang language) (*Client, error) {
	if _, err := exec.LookPath(lang.command); err != nil {
		return nil, &MissingBinaryError{Command: lang.command, Install: lang.install}
	}
	return StartServer(ctx, ServerConfig{Command: lang.command, Args: lang.args})
}

// References resolves all references to the symbol at pos (0-based LSP
// position) in absFile, whose current content is passed for didOpen sync.
// The references tool handles 1-based→0-based and symbol-column location
// before calling here.
func (m *Manager) References(ctx context.Context, absFile, content string, pos Position) ([]Location, error) {
	lang, ok := detectLang(absFile)
	if !ok {
		return nil, fmt.Errorf("no LSP server configured for %s files", filepath.Ext(absFile))
	}
	cli, err := m.ready(ctx, lang, absFile)
	if err != nil {
		return nil, err
	}
	uri := pathToURI(absFile)
	if err := cli.DidOpen(ctx, uri, lang.id, content); err != nil {
		return nil, err
	}
	return cli.References(ctx, uri, pos, true)
}

// ready returns an initialized client for lang, starting one if needed.
// Crucial D4 behaviour: the server + its init are bound to the SESSION
// ctx; the per-call ctx only bounds how long THIS caller waits. If ctx is
// cancelled mid-cold-start, the caller bails but the server keeps
// initializing and stays cached for the next query.
func (m *Manager) ready(ctx context.Context, lang language, absFile string) (*Client, error) {
	m.mu.Lock()
	e := m.servers[lang.id]
	if e != nil && !e.client.Alive() {
		delete(m.servers, lang.id) // dead → drop and restart
		e = nil
	}
	if e == nil {
		cli, err := m.launch(m.ctx, lang) // SESSION ctx, never the turn ctx
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		e = &serverEntry{lang: lang, client: cli, ready: make(chan struct{})}
		m.servers[lang.id] = e
		go m.initEntry(e, absFile)
	}
	m.mu.Unlock()

	select {
	case <-e.ready:
		if e.initErr != nil {
			return nil, e.initErr
		}
		return e.client, nil
	case <-ctx.Done():
		return nil, ctx.Err() // turn cancelled; server keeps initializing, stays cached
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	}
}

func (m *Manager) initEntry(e *serverEntry, absFile string) {
	initCtx, cancel := context.WithTimeout(m.ctx, initTimeout)
	defer cancel()
	if err := e.client.Initialize(initCtx, m.rootURIFor(absFile, e.lang)); err != nil {
		e.initErr = err
		_ = e.client.Close()
		m.mu.Lock()
		if m.servers[e.lang.id] == e { // don't clobber a newer entry
			delete(m.servers, e.lang.id)
		}
		m.mu.Unlock()
	}
	close(e.ready)
}

// rootURIFor walks up from the file's directory to the nearest workspace
// marker for lang; falls back to the manager's rootDir.
func (m *Manager) rootURIFor(absFile string, lang language) string {
	for d := filepath.Dir(absFile); ; {
		for _, marker := range lang.rootMarkers {
			if _, err := os.Stat(filepath.Join(d, marker)); err == nil {
				return pathToURI(d)
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return pathToURI(m.rootDir)
}

// Shutdown gracefully tears down every server, then kills it. Wire into
// the session teardown so no language server is orphaned (PRD §3, D4).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	entries := make([]*serverEntry, 0, len(m.servers))
	for _, e := range m.servers {
		entries = append(entries, e)
	}
	m.servers = map[string]*serverEntry{}
	m.mu.Unlock()

	for _, e := range entries {
		if e.client.Alive() {
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			e.client.Shutdown(sctx)
			cancel()
		}
		_ = e.client.Close()
	}
}

// pathToURI converts an absolute path to a file:// URI. Unix-shaped; the
// Windows form (file:///C:/…) is a known gap for the degraded Windows
// path (feature-lsp.md §8).
func pathToURI(p string) string {
	return "file://" + p
}
