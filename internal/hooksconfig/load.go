package hooksconfig

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// TrustPrompt is the interface main.go (TUI / CLI) implements to ask
// the user whether to trust a project's hooks.toml. The contract is:
// return true to approve (which records the sha into the trust store),
// false to refuse. Refusing means the project hooks are dropped for
// this session only — they'll be asked again next launch.
//
// Implementations:
//   - TUI: bridges to internal/askuser picker (y/n/details).
//   - CLI / piped stdin: prompts via stdin or auto-denies if not tty.
//
// PRD §3.5: trust 询问之前**不会**调用任何 `bash -c` — Gate guarantees this
// by performing the sha256 + IsTrusted check BEFORE returning hooks to
// the runner, and the prompt is the only path that can produce an
// approval. There is no `bash` exec anywhere in this file.
type TrustPrompt interface {
	AskTrustHooks(req TrustRequest) bool
}

// TrustRequest is the read-only summary handed to the prompt. It
// includes enough metadata for a helpful picker ("project at X
// defines N hooks for events E1, E2, ...") without the prompt needing
// to re-parse the file.
type TrustRequest struct {
	ProjectPath string
	HookCount   int
	Events      []string // unique events touched, e.g. ["pre_tool", "post_tool"]
	Names       []string // hook names in declaration order
	SHA256      string   // hex; the value that will be saved on Approve
	Path        string   // absolute path to hooks.toml for "show me the file" UX
}

// Gate is the all-in-one loader for the wiring layer. Given a user
// hooks path, a project hooks path, and a TrustStore + TrustPrompt
// it:
//
//  1. Reads the user-level hooks.toml (no trust gate — see PRD §3.5).
//  2. If the project-level hooks.toml exists:
//     a. computes its sha256
//     b. checks trust store; if not trusted, prompts the user
//     c. on approval, records the sha and includes project hooks in
//     the merged config; on refusal, omits them entirely
//  3. Merges + validates (drops malformed hooks with a warning).
//  4. Runs StaticCheck (`bash -n`) and marks failing hooks.
//
// Returns: (merged config, list of human-readable warnings).
//
// Warnings include the file-not-found-but-could-not-read cases,
// validation failures, bash -n failures, and trust-refused notes. The
// wiring layer prints them all to stderr at startup so the user gets
// one consolidated view of what's wrong.
func Gate(userPath, projectPath, projectAbsDir string, store *TrustStore, prompt TrustPrompt, syntax SyntaxChecker) (Config, []string) {
	var warnings []string

	user, err := Load(userPath, SourceUser)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("hooks: user file %s: %v", userPath, err))
		// continue with empty user config
		user = Config{}
	}

	project, projectWarnings, projectAllowed := loadProjectGated(projectPath, projectAbsDir, store, prompt)
	warnings = append(warnings, projectWarnings...)
	if !projectAllowed {
		project = Config{}
	}

	merged := Merge(project, user)

	if err := Validate(merged); err != nil {
		warnings = append(warnings, fmt.Sprintf("hooks: %v", err))
		// Drop malformed entries individually so the rest still load.
		merged = dropMalformed(merged)
	}

	if !merged.IsEmpty() {
		warnings = append(warnings, StaticCheck(&merged, syntax)...)
	}

	return merged, warnings
}

func loadProjectGated(projectPath, projectAbsDir string, store *TrustStore, prompt TrustPrompt) (Config, []string, bool) {
	if projectPath == "" {
		return Config{}, nil, true
	}
	// Read the bytes ourselves so we can compute the sha BEFORE asking
	// the toml parser to look at it. The PRD's anti-supply-chain
	// guarantee ("trust 询问之前**不会**调用任何 `bash -c`") is upheld by
	// the fact that this whole function path only reads bytes; no
	// `bash` invocation happens here, only in StaticCheck after Gate
	// has returned and the user has approved.
	body, err := os.ReadFile(projectPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil, true
		}
		return Config{}, []string{fmt.Sprintf("hooks: project file %s: %v", projectPath, err)}, true
	}
	sha := Sha256Hex(body)

	if store == nil {
		// Defensive: without a store we have no way to remember
		// approval. Skip project hooks rather than re-prompt every
		// command.
		return Config{}, []string{"hooks: trust store unavailable; project hooks disabled"}, false
	}
	if !store.IsTrusted(projectAbsDir, sha) {
		// Need to ask. If no prompt available (non-interactive
		// launches: -p / piped stdin), refuse with a clear note.
		if prompt == nil {
			return Config{}, []string{fmt.Sprintf(
				"hooks: project hooks at %s are untrusted (no interactive prompt available — run `seek hooks trust` in TUI to approve)",
				projectPath)}, false
		}
		// Decode just enough to produce a summary, but DO NOT shell
		// out yet — the toml parse is byte-level safe per BurntSushi/toml.
		preview, decodeErr := DecodeBytes(body, SourceProject)
		if decodeErr != nil {
			return Config{}, []string{fmt.Sprintf("hooks: project file %s parse: %v", projectPath, decodeErr)}, false
		}
		req := buildTrustRequest(projectAbsDir, projectPath, sha, preview)
		ok := prompt.AskTrustHooks(req)
		if !ok {
			return Config{}, []string{fmt.Sprintf("hooks: project hooks at %s declined (use `seek hooks trust --reset` to clear)", projectPath)}, false
		}
		if err := store.Approve(projectAbsDir, sha, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return Config{}, []string{fmt.Sprintf("hooks: trust save: %v", err)}, true
		}
	}

	cfg, err := DecodeBytes(body, SourceProject)
	if err != nil {
		return Config{}, []string{fmt.Sprintf("hooks: project file %s: %v", projectPath, err)}, true
	}
	return cfg, nil, true
}

func buildTrustRequest(projectAbsDir, path, sha string, cfg Config) TrustRequest {
	all := cfg.All()
	req := TrustRequest{
		ProjectPath: projectAbsDir,
		HookCount:   len(all),
		SHA256:      sha,
		Path:        path,
	}
	seenEvent := map[string]bool{}
	for _, h := range all {
		if !seenEvent[h.Event] {
			req.Events = append(req.Events, h.Event)
			seenEvent[h.Event] = true
		}
		req.Names = append(req.Names, h.Name)
	}
	return req
}

func dropMalformed(c Config) Config {
	drop := func(h []Hook) []Hook {
		out := make([]Hook, 0, len(h))
		for _, e := range h {
			if e.Name == "" || e.Command == "" {
				continue
			}
			out = append(out, e)
		}
		return out
	}
	return Config{
		PreTool:      drop(c.PreTool),
		PostTool:     drop(c.PostTool),
		PrePrompt:    drop(c.PrePrompt),
		SessionStart: drop(c.SessionStart),
		SessionEnd:   drop(c.SessionEnd),
	}
}
