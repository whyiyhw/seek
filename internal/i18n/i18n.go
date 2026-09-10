// Package i18n is seek's view-layer message catalogue: a stdlib-only
// map-per-language bundle with an English terminal fallback. It exists
// so human-facing prose (banner notices, hints, slash-command
// feedback) can render in the user's language without pulling in
// gotext / x-text machinery.
//
// Scope boundary (docs/prd/feature-i18n.md §1.2):
//
//   - LOCALISED: view-layer prose — strings rendered by the TUI for
//     the human to read.
//   - NOT LOCALISED: anything the LLM sees (tool results, agent error
//     text fed back into the conversation — DeepSeek's prefix cache
//     keys on exact bytes, and mid-session language switches must not
//     churn that surface), and the status bar (deliberately ASCII for
//     width-oracle determinism — see statusbar.go's foldStatusBar
//     note). Slash-command descriptions in the menu stay English in
//     v1.
//
// Session discipline: the language is resolved ONCE per process at
// startup (cmd/seek, right after the first config.Load) and stored as
// the package default — mirroring sysprompt.Header.Date, never
// recomputed per turn. The one sanctioned mutation is /lang, which
// swaps the default bundle at runtime; only view-layer strings read
// it, so the LLM prompt prefix stays byte-stable.
package i18n

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// Supported language codes. "en" is the source language: every key
// MUST exist in messagesEn (TestCatalogueParity enforces that the
// other catalogues neither miss nor invent keys).
const (
	LangEn = "en"
	LangZh = "zh"
)

// Normalize folds a locale tag ("zh_CN.UTF-8", "zh-Hans", "EN_us")
// down to a supported language code, or "" when the language has no
// catalogue. Region / script / encoding suffixes are stripped before
// lookup so the common environment formats resolve without a mapping
// table.
func Normalize(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return ""
	}
	if i := strings.IndexAny(tag, "_-."); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case LangEn:
		return LangEn
	case LangZh:
		return LangZh
	}
	return ""
}

// Resolve picks the session language. Precedence (high → low):
//
//  1. SEEK_LANG        — per-launch override, beats everything on disk
//  2. configLang       — `language` in ~/.seek/config.json (set by /lang)
//  3. LC_ALL, then LANG — the shell's own locale, so a zh_CN terminal
//     gets zh with zero configuration
//  4. English          — the source language; always the resting state
//
// configLang is whatever config.Load produced ("" when unset or the
// file is missing) — callers don't pre-normalise.
func Resolve(configLang string) string {
	if code := Normalize(os.Getenv("SEEK_LANG")); code != "" {
		return code
	}
	if code := Normalize(configLang); code != "" {
		return code
	}
	if code := Normalize(os.Getenv("LC_ALL")); code != "" {
		return code
	}
	if code := Normalize(os.Getenv("LANG")); code != "" {
		return code
	}
	return LangEn
}

// Bundle is an immutable, concurrency-safe message catalogue for one
// language. Construct via New; never mutate the maps after (they are
// package-level and shared).
type Bundle struct {
	lang string
	msgs map[string]string
}

// New returns the bundle for lang. Unknown / unsupported languages
// resolve to English rather than an error — a typo in config.json
// should cost the user a translation, not a working CLI.
func New(lang string) *Bundle {
	if Normalize(lang) == LangZh {
		return &Bundle{lang: LangZh, msgs: messagesZh}
	}
	return &Bundle{lang: LangEn, msgs: messagesEn}
}

// Lang reports the bundle's resolved language code.
func (b *Bundle) Lang() string { return b.lang }

// T looks key up in the bundle's catalogue, then in English, then
// gives up and returns the key itself — a missing entry degrades to a
// visible (if ugly) label instead of an empty string, so a catalogue
// bug can't blank out the UI. With args, the located string is
// treated as a fmt format string; placeholders are positional %s/%d
// and must stay stable across translations.
func (b *Bundle) T(key string, args ...any) string {
	s, ok := b.msgs[key]
	if !ok {
		s = messagesEn[key]
	}
	if s == "" {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// defaultBundle is the process-wide bundle every view-layer call site
// reads via the package-level T. Swapped only by cmd/seek at startup
// and by /lang at runtime; atomic so the swap is race-clean under
// -race even mid-render.
var defaultBundle atomic.Pointer[Bundle]

func init() {
	defaultBundle.Store(&Bundle{lang: LangEn, msgs: messagesEn})
}

// SetDefault installs b as the process-wide bundle. Nil resets to
// English (also the state before any SetDefault call — tests rely on
// that to run hermetically without startup wiring).
func SetDefault(b *Bundle) {
	if b == nil {
		b = &Bundle{lang: LangEn, msgs: messagesEn}
	}
	defaultBundle.Store(b)
}

// Default returns the process-wide bundle.
func Default() *Bundle { return defaultBundle.Load() }

// T translates key through the process-wide bundle. See Bundle.T.
func T(key string, args ...any) string { return Default().T(key, args...) }

// Lang reports the process-wide bundle's language code.
func Lang() string { return Default().Lang() }
