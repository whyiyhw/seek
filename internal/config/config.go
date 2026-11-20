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
	"strconv"
	"strings"

	"github.com/whyiyhw/seek/internal/budget"
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
	// values: "deepseek", "anthropic", "openai", "gemini", "glm") to
	// its stored credentials. Nil map = no providers configured yet.
	Providers map[string]ProviderConfig `json:"providers,omitempty"`

	// PathPromptDone is set to true after the first-run "add to PATH?"
	// prompt has been shown (or dismissed) on Windows. Prevents
	// nagging on every startup.
	PathPromptDone bool `json:"path_prompt_done,omitempty"`

	// SuggestReply gates the v4 柱 D suggested-reply subsystem (single
	// switch covers prediction + UI + calibration injection — PRD
	// docs/prd/archive/feature-suggested-reply.md §4.7). Pointer so we can
	// distinguish "field absent → default on" from "field present and
	// false → user explicitly turned it off"; a bool would silently
	// default to false for any config file written before v4.
	SuggestReply *bool `json:"suggest_reply,omitempty"`

	// PushWebhooks are the v6 柱 M mobile-push targets: cron / trigger
	// terminal events are POSTed to each (in ADDITION to the OS desktop
	// notification) so the user is reachable away from the machine.
	// Empty / absent = no webhooks. See docs/prd/archive/feature-mobile-push.md.
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

	// Language selects the interface language for view-layer text
	// ("en" | "zh" — internal/i18n is the catalogue). Empty = resolve
	// from the environment (SEEK_LANG, then LC_ALL/LANG, then English).
	// /lang writes this field; SEEK_LANG beats it for one-off launches.
	// Only human-facing prose follows it — LLM-visible strings and the
	// status bar stay English by design (docs/prd/feature-i18n.md §1.2).
	Language string `json:"language,omitempty"`

	// SafetyCheck opts the DeepSeek lane into the model safety check
	// (docs/prd/archive/feature-deepseek-app-api.md §5): DeepSeek documents one
	// tier, "high"; empty/absent = off — the self-hosting default,
	// because a refusal is pure capability loss for the agent's own work
	// (security research included) plus latency. It exists for users who
	// expose seek as a service: turning it on is then their compliance
	// obligation, seek only supplies the switch (docs/guide-config.md).
	//
	// Read ONCE at session startup, never switched in-session: the value
	// rides every request body, so a mid-session flip would change the
	// shape of every subsequent request mid-conversation — the same
	// discipline /lang and the retired server_tools switch follow. See
	// SafetyCheckLevel for the wire mapping.
	SafetyCheck string `json:"safety_check,omitempty"`

	// Serve configures the remote control plane (`seek serve` —
	// docs/prd/feature-remote-control-plane.md, wired in M1b). Nil
	// (section absent, i.e. every config written before the plane
	// existed) means all defaults; loading an old config must not
	// error — top-level backward compat is the file-format contract.
	Serve *ServeConfig `json:"serve,omitempty"`

	// Agents is the `agents` section (feature-orchestration.md D6/D14,
	// P2-c): per-type DEFAULT ROUTING overrides so a lane pin doesn't
	// have to ride every `agent` tool call ("免每次传参的默认路由").
	// Keys are agent type names; values carry routing knobs ONLY —
	// tools/prompt/workflow stay file-layer (agents/*.md) territory
	// because config routes brains, never permissions (D6: "路由只换
	// 脑子不换权限"). Priority: built-in default < this section <
	// agents/*.md — cmd/seek threads it into agenttypes.Load between
	// the file scopes and the built-ins. Nil/absent = built-ins only.
	Agents map[string]AgentConfig `json:"agents,omitempty"`

	// AutoCompact arms the automatic /compact trigger (see
	// AutoCompactConfig). Nil = off, which is the shipped default: an
	// automatic history rewrite is a behaviour change to the
	// conversation, so it stays opt-in.
	AutoCompact *AutoCompactConfig `json:"auto_compact,omitempty"`
}

// AutoCompactConfig is the `auto_compact` section: when the last turn's
// context usage crosses this point, seek runs the same summariser
// `/compact` runs (fork-snapshot, so the full history stays on disk) at
// the end of the turn instead of waiting for the user to notice the
// status bar.
//
// Two ways to say "when", because they answer different questions:
//
//   - AtTokens is model-INDEPENDENT: "compact at 350k" holds whichever
//     lane is live. Wins when both are set.
//   - AtFraction scales the CURRENT model's window: 0.35 of a 1M window
//     is 350k, and a /model switch moves the trigger with the window.
//
// Both zero/absent = disabled. Values are clamped to a sane floor
// (a trigger below ~1k tokens would compact constantly, and the
// summariser call itself costs more than the history it replaces).
type AutoCompactConfig struct {
	AtTokens   int     `json:"at_tokens,omitempty"`
	AtFraction float64 `json:"at_fraction,omitempty"`
}

// minAutoCompactTokens is the floor under any configured trigger. The
// summariser request re-sends the whole history, so compacting a
// handful of tokens is a net loss; 1k is well under any useful working
// set and well over the cost of the call.
const minAutoCompactTokens = 1000

// AutoCompactThreshold returns the configured trigger in tokens for the
// given model, or 0 when auto-compact is off. Nil-safe. The fraction
// path is resolved against the LIVE model so /model moves the trigger;
// an explicit at_tokens ignores the model entirely.
//
// $SEEK_AUTO_COMPACT overrides the file for one launch, same precedence
// shape as SEEK_LANG: a bare number with a dot ("0.35") is a fraction of
// the model's window, a plain integer ("350000") is an absolute token
// count, and "0" / "off" / "false" disables it. An empty or unparsable
// value is ignored (the file's setting stands) rather than guessed at —
// an empty env var is the shell's way of saying "nothing here", not of
// saying "off".
func (c Config) AutoCompactThreshold(model string) int {
	tokens, fraction := 0, 0.0
	if c.AutoCompact != nil {
		tokens, fraction = c.AutoCompact.AtTokens, c.AutoCompact.AtFraction
	}
	if raw, ok := os.LookupEnv("SEEK_AUTO_COMPACT"); ok {
		switch v := strings.ToLower(strings.TrimSpace(raw)); {
		case v == "0", v == "off", v == "false":
			tokens, fraction = 0, 0
		case strings.ContainsAny(v, ".e"):
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				tokens, fraction = 0, f
			}
		default:
			if n, err := strconv.Atoi(v); err == nil {
				tokens, fraction = n, 0
			}
		}
	}
	at := budget.AutoCompactAt(model, tokens, fraction)
	if at < minAutoCompactTokens {
		return 0
	}
	return at
}

// AgentConfig is one `agents`-section entry: the routing knobs a
// config override may set on a built-in type. Field semantics mirror
// the agents/*.md frontmatter contract (D11): empty = inherit the
// parent session. agenttypes.Load validates effort/max_turns shapes
// and warns+ignores bad values; provider/model lane names are the
// routing table's jurisdiction (subagent.ResolveRoute, checked at
// startup-warning + spawn-gate time, same as file types).
type AgentConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Effort is low | medium | high (the /effort tiers). Parsed and
	// validated; not yet consumed by the Runner — declared for parity
	// with the frontmatter contract (D11), which is also data-only
	// today.
	Effort string `json:"effort,omitempty"`
	// MaxTurns overrides the child turn cap when > 0.
	MaxTurns int `json:"max_turns,omitempty"`
}

// ServeConfig is the `serve` section of the config file: the plane's
// standing posture, read once at `seek serve` startup. Per-conversation
// state (bindings, sessions) lives in ~/.seek/serve/, never here — this
// section is only what the user configures by hand.
type ServeConfig struct {
	// Projects lists candidate project directories a conversation can
	// bind to (the /projects command renders this list, M2). Directories
	// are used as given; the router resolves to absolute paths at bind
	// time. Empty = a plane with no pre-declared candidates.
	Projects []string `json:"projects,omitempty"`

	// MaxConcurrency caps globally concurrent seek subprocesses across
	// ALL conversations (PRD D-C4). <= 0 / absent = the executor's own
	// default (mirrored by controlplane.NewExecutor — the zero value is
	// deliberately passed through so the default lives in ONE place).
	MaxConcurrency int `json:"max_concurrency,omitempty"`

	// Yolo opts the plane's child seek runs into --yolo (all permission
	// gates open). Default false — deliberate (PRD D-C5): print mode is
	// PrefDeny, so a remote run can read/write the bound project but
	// cannot run shell commands unless the user explicitly accepts that
	// exposure here. Per-binding overrides are future work.
	Yolo bool `json:"yolo,omitempty"`

	// WebAddr overrides the web frontend's listen address (`seek serve
	// --frontend=web`); the --listen flag folds over it at the CLI
	// (flag > config). Empty = the shipped loopback default
	// 127.0.0.1:8790 (controlplane.DefaultWebAddr — the value lives
	// there, one place). Scope is pinned to loopback (127.0.0.1/::1/
	// localhost) and tailnet CGNAT 100.64.0.0/10 (2026-11-15 decision):
	// 0.0.0.0, LAN/public IPs and other hostnames fail `seek serve` at
	// startup with the allowlist reason; a malformed address fails the
	// listen itself. Neither failure falls back to the default. Point
	// it at a tailnet IP (e.g. "100.x.y.z:8790") to serve the plane
	// straight to the tailnet — or keep loopback and
	// `tailscale serve --tcp=8790` instead.
	WebAddr string `json:"web_addr,omitempty"`
}

// ServeYolo reports whether serve-mode child runs start with --yolo.
// Nil-safe: no serve section = the safe default (false).
func (c Config) ServeYolo() bool {
	return c.Serve != nil && c.Serve.Yolo
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

// SafetyCheckLevel returns the level to send in
// deepseek.ChatRequest.SafetyCheck: "high" when the user opted in, ""
// (off) otherwise. Only "high" is documented
// (docs/prd/archive/feature-deepseek-app-api.md §5), so any other value reads as
// off instead of being forwarded as a guess — a typo must not silently
// select some half-supported tier. Matching is whitespace-trimmed and
// case-insensitive so hand-edited config files stay forgiving.
func (c Config) SafetyCheckLevel() string {
	if strings.EqualFold(strings.TrimSpace(c.SafetyCheck), "high") {
		return "high"
	}
	return ""
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

// ProviderConfig holds the per-provider state. APIKey and BaseURL are
// the only fields today; future additions (organization IDs, model
// defaults) slot in here without changing the file location.
//
// BaseURL is an optional endpoint override for lanes that ship a pinned
// default. Wired for glm today (the Coding Plan / pay-as-you-go split —
// see buildProvider's glm case); other lanes still use their compiled
// default or the --base-url flag. The value is the FULL POST target, not
// a base URL — the OpenAI-compatible client POSTs it verbatim. Empty
// means "use the lane's compiled default".
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
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
	case "glm":
		return "GLM_API_KEY"
	case "opencode":
		return "OPENCODE_API_KEY"
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

// baseURLEnvFor returns the canonical environment variable for a
// provider's endpoint override, mirroring envVarFor for keys. glm needs
// one for the Coding Plan / pay-as-you-go split (docs/guide-config.md
// §3); opencode has one so a proxy/mirror can front the Go gateway
// without editing config.json.
func baseURLEnvFor(providerName string) string {
	switch providerName {
	case "glm":
		return "GLM_BASE_URL"
	case "opencode":
		return "OPENCODE_BASE_URL"
	}
	return ""
}

// BaseURLFor resolves an optional endpoint override for providerName
// with the same env > config precedence as KeyFor. An empty result
// means "no override" — callers then fall back to their own flag /
// compiled default (buildProvider's order: config/env > --base-url >
// lane default).
func BaseURLFor(cfg Config, providerName string) string {
	if env := baseURLEnvFor(providerName); env != "" {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	if p, ok := cfg.Providers[providerName]; ok && p.BaseURL != "" {
		return p.BaseURL
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
