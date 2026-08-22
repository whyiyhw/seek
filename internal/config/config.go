// Package config persists per-provider API keys (and a default
// provider selection) to ~/.seek/config.json so a freshly-installed
// seek doesn't need any environment variable setup before the first
// run.
//
// Resolution order at lookup time (high → low):
//
//  1. The provider's official environment variable (e.g. DEEPSEEK_API_KEY).
//     This always wins so CI / secret-manager / one-off invocations
//     don't accidentally pick up a stale on-disk key.
//  2. ~/.seek/config.json (or $SEEK_HOME/config.json) written by the
//     first-run wizard or hand-edited by the user.
//  3. Nothing — the caller treats this as "no key configured" and
//     either prompts the user or falls back to the wizard.
//
// JSON (not TOML/YAML) is the on-disk format because the Go standard
// library handles JSON without a third-party dependency, in line with
// the project's "stdlib first" convention. End-users almost never
// hand-edit this file — the wizard writes it; subsequent edits go
// through the TUI.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/whyiyhw/seek/internal/paths"
)

// Filename is the config file's basename. Exported so tests and any
// future "where is my config" diagnostic can reference it without
// hardcoding the string.
const Filename = "config.json"

// Config is the on-disk shape. Adding new top-level fields is
// backwards-compatible (older binaries ignore unknown JSON keys);
// removing or renaming is not.
type Config struct {
	// DefaultProvider is which provider seek picks when no
	// --provider flag and no API-key env var nudges it. Empty
	// string means "no preference" — the auto-detect logic in
	// cmd/seek still applies.
	DefaultProvider string `json:"default_provider,omitempty"`

	// Providers maps provider name (matching the --provider flag
	// values: "deepseek", "anthropic", "openai", "gemini") to its
	// stored credentials. Nil map = no providers configured yet.
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// PathPromptDone is set to true after the first-run "add to PATH?"
	// prompt has been shown (or dismissed) on Windows. Prevents
	// nagging on every startup.
	PathPromptDone bool `json:"path_prompt_done,omitempty"`

	// SuggestReply gates the v4 柱 D suggested-reply subsystem (single
	// switch covers prediction + UI + calibration injection — PRD
	// docs/prd/feature-suggested-reply.md §4.7). Pointer so we can
	// distinguish "field absent → default on" from "field present and
	// false → user explicitly turned it off"; a bool would silently
	// default to false for any config file written before v4.
	SuggestReply *bool `json:"suggest_reply,omitempty"`

	// PushWebhooks are the v6 柱 M mobile-push targets: cron / trigger
	// terminal events are POSTed to each (in ADDITION to the OS desktop
	// notification) so the user is reachable away from the machine.
	// Empty / absent = no webhooks. See docs/prd/feature-mobile-push.md.
	PushWebhooks []PushWebhook `json:"push_webhooks,omitempty"`

	// SessionNotifySeconds gates the interactive "task finished" push
	// (柱 M extension): when an interactive TUI turn runs at least this
	// many seconds AND a push webhook subscribes to the session.completed
	// event, seek pings that webhook when the turn ends. Pointer so we
	// distinguish "unset → default 60s (on)" from "0 → disabled".
	SessionNotifySeconds *int `json:"session_notify_seconds,omitempty"`

	// The former `ocr` section (v7 柱 Q) was removed with 柱 Q's
	// decommission (feature-vision M-V.0, 2026-08): stale sections in
	// existing config files are ignored silently — unknown JSON keys
	// don't error the loader — so old configs keep working untouched.

	// Read tunes the `read` tool's output limits (read-tool v0.10.x
	// improvements — docs/test-plan-read-tool.md §1). All fields
	// optional.
	Read *ReadConfig `json:"read,omitempty"`

	// BashEnvPassthrough names environment variables that survive the
	// credential scrub applied to commands the MODEL runs (internal/
	// childenv). By default anything whose name looks credential-bearing
	// — API_KEY, GH_TOKEN, AWS_SECRET_ACCESS_KEY, *PASSWORD* — plus the
	// SEEK_* namespace is withheld from the shell, so a model-issued
	// command (and every build script it triggers) cannot read seek's own
	// API key.
	//
	// List a name here when a workflow genuinely needs it: `gh pr create`
	// wanting GH_TOKEN is the common case. Matching is exact and
	// case-insensitive — listing "TOKEN" allows only a variable literally
	// named TOKEN, never GH_TOKEN, so one allowance cannot widen into
	// another.
	//
	// Empty / absent = scrub everything that matches (the safe default).
	BashEnvPassthrough []string `json:"bash_env_passthrough,omitempty"`
}

// ReadConfig configures the read tool (internal/tools/read). All fields
// optional; zero values keep the package defaults.
type ReadConfig struct {
	// MaxLimit caps one read call's emitted lines (default 200).
	MaxLimit int `json:"max_limit,omitempty"`
	// WholeReadBytes: regular files at or below this size are emitted
	// whole in one call regardless of limit (default 16 KiB).
	WholeReadBytes int `json:"whole_read_bytes,omitempty"`
}

// ReadMaxLimit returns the per-call line cap (default 200).
func (c Config) ReadMaxLimit() int {
	if c.Read == nil || c.Read.MaxLimit <= 0 {
		return 200
	}
	return c.Read.MaxLimit
}

// ReadWholeReadBytes returns the whole-read size threshold (default
// 32 KiB — measured sweet spot, see docs/test-plan-read-tool.md §7.0).
func (c Config) ReadWholeReadBytes() int {
	if c.Read == nil || c.Read.WholeReadBytes <= 0 {
		return 32 * 1024
	}
	return c.Read.WholeReadBytes
}

// SessionNotifySecondsOrDefault returns the interactive-notify duration
// gate in seconds — 60 when unset (the default-on threshold), or the
// explicit value (0 disables interactive notify). Use this so the
// default lives in one place.
func (c Config) SessionNotifySecondsOrDefault() int {
	if c.SessionNotifySeconds == nil {
		return 60
	}
	return *c.SessionNotifySeconds
}

// PushWebhook is one mobile-push destination (v6 柱 M). cmd/seek maps
// this onto routines.WebhookTarget when wiring the dispatcher, keeping
// the routines package independent of this config package.
type PushWebhook struct {
	// URL is the POST target. http/https only; private/LAN addresses
	// are allowed (the user configured it — self-hosted ntfy / intranet
	// relays are a use case, unlike webfetch's model-driven SSRF gate).
	// For format "feishu" the URL is ignored — delivery goes through
	// the Feishu open-platform IM API (im/v1/messages), addressed by
	// ReceiveID below.
	URL string `json:"url"`
	// Format selects the payload shape: ntfy | slack | discord | feishu |
	// template | raw. Empty defaults to raw. (feishu = 企业自建应用 bot
	// via IM API; needs AppID/AppSecret/ReceiveID/ReceiveIDType. template
	// = bring your own JSON.)
	Format string `json:"format,omitempty"`
	// Template is the raw JSON body for format "template": your own
	// payload with {{title}} / {{body}} / {{event}} placeholders
	// (JSON-escaped on substitution). The general escape hatch for any
	// webhook whose schema the built-in formats don't match.
	Template string `json:"template,omitempty"`
	// Events filters which terminal events fire this webhook
	// (e.g. "cron.failed", "trigger.completed", "session.completed").
	// Empty = every event.
	Events []string `json:"events,omitempty"`

	// Feishu 企业自建应用 bot credentials (only used when Format ==
	// "feishu"). Stored in config.json per project decision; treat the
	// file as sensitive (0600 recommended on ~/.seek/config.json).
	// Obtain from the Feishu developer console → your 企业自建应用 →
	// "凭证与基础信息". See docs/guide-webhooks.md §飞书.
	AppID     string `json:"app_id,omitempty"`
	AppSecret string `json:"app_secret,omitempty"`
	// ReceiveID is the target conversation: a chat_id (group), an
	// open_id / user_id / union_id (private message), or an email.
	// Which one it is, is declared by ReceiveIDType.
	ReceiveID string `json:"receive_id,omitempty"`
	// ReceiveIDType declares the kind of ReceiveID: chat_id | open_id |
	// user_id | union_id | email. Empty defaults to chat_id (the common
	// "send to a group" case).
	ReceiveIDType string `json:"receive_id_type,omitempty"`
}

// SuggestReplyEnabled returns whether the suggested-reply feature
// should run, with a default of true when SuggestReply is unset.
// Use this rather than reading the pointer directly so the
// default-on policy lives in one place.
func (c *Config) SuggestReplyEnabled() bool {
	if c == nil || c.SuggestReply == nil {
		return true
	}
	return *c.SuggestReply
}

// ProviderConfig holds the per-provider state. APIKey is the only
// field today; future additions (base URL overrides, organization IDs,
// model defaults) slot in here without changing the file location.
type ProviderConfig struct {
	APIKey string `json:"api_key,omitempty"`
}

// envVarFor returns the canonical environment variable a provider
// uses for its API key. Empty string for unknown providers (the
// `compatible` adapter, custom names) — callers handle absence.
func envVarFor(providerName string) string {
	switch providerName {
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	}
	return ""
}

// Path returns the resolved config file path. Returns the path even
// when the file doesn't exist — useful for "write a fresh config" and
// for diagnostic messages.
func Path() (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, Filename), nil
}

// Load reads ~/.seek/config.json. A missing file returns an empty
// Config (not an error) — that's the "fresh install" state and the
// wizard handles it. JSON parse errors ARE returned as fatal: a
// corrupted config is the user's problem to investigate, not
// something we silently overwrite.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save atomically writes cfg to ~/.seek/config.json with 0600 perms
// so the API key isn't readable by other users on the machine. The
// parent directory is created if missing (same model as
// session.NewStore — first run shouldn't require manual mkdir).
//
// Atomicity comes from the tmp + rename pattern: on POSIX rename is
// atomic within a filesystem, so the file is either the old content
// or the new content, never a partially-written mix. On Windows
// rename can fail if the target exists; os.Rename in Go handles that
// by using ReplaceFile under the hood.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("config: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: rename %s: %w", path, err)
	}
	return nil
}

// KeyFor resolves the API key for a provider using the documented
// precedence (env var > config file). Returns the empty string when
// neither source has one — callers should treat that as "trigger the
// wizard" or "show an error", not as a silent fallback.
//
// providerName matches the --provider flag values; for unknown
// provider names (the compatible adapter / custom names) only the
// config file's APIKey is consulted, since there's no canonical env
// var to fall back to.
func KeyFor(cfg Config, providerName string) string {
	if env := envVarFor(providerName); env != "" {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	if p, ok := cfg.Providers[providerName]; ok && p.APIKey != "" {
		return p.APIKey
	}
	return ""
}

// SetKey writes the key for providerName into cfg.Providers, creating
// the map if needed. Pure mutation — doesn't touch disk; pair with
// Save() to persist.
func SetKey(cfg *Config, providerName, apiKey string) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	p := cfg.Providers[providerName]
	p.APIKey = apiKey
	cfg.Providers[providerName] = p
}
