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
