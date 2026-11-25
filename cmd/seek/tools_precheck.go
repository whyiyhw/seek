package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/whyiyhw/seek/internal/mcpconfig"
	"github.com/whyiyhw/seek/internal/tools"
)

// postMCPTBuiltInTools names seek's own tools that register AFTER the
// MCP loading block in run() but BEFORE applyToolsWhitelist — memory
// (gated on !-no-save), agent, plan/propose and memory_observe (gated
// on the D13/auto-distill knobs). The pre-MCP -tools check must treat
// these as valid so `--tools plan` survives to the real validation.
//
// ⚠ Maintenance contract: every NEW reg.Add below the MCP block in
// run() must land its tool name here too, or `--tools <new-name>`
// starts exiting 2 before MCP ever loads. The inverse (a stale name
// here) is harmless — it only defers to the post-load check, which
// stays the single authority on what finally registers.
var postMCPTBuiltInTools = []string{
	"memory_recall",   // memorytool.NewRecall, run() ~M-layer block
	"memory_remember", // memorytool.NewRemember
	"memory_archive",  // memorytool.NewArchive
	"memory_amend",    // memorytool.NewAmend
	"agent",           // agenttool.New — the subagent dispatcher
	"plan",            // plantool — plan-mode progress tool
	"propose",         // propose — plan-mode confirmation tool
	"memory_observe",  // memorytool.NewObserve — gated on auto-distill
}

// checkToolsWhitelistPreMCP is the fail-fast half of -tools validation
// (judge 前置④, c_xujwvq): it runs in run() BEFORE MCP servers fork, and
// rejects a whitelist that no possible registry composition could
// satisfy — sparing judge/CI lanes the subprocess cost of loading
// servers only to die in applyToolsWhitelist afterwards.
//
// It must NEVER reject a value the post-load check would accept, so the
// decision is a three-way split:
//
//  1. built-in (already in reg, or a post-MCP built-in per the list
//     above)           → valid, no opinion.
//  2. provably not MCP-suppliable → rejected HERE, same stderr shape as
//     applyToolsWhitelist → errToolsUsage → exit 2, zero subprocesses.
//  3. unknown but MCP might supply it → DEFERRED (nil): only loading the
//     server can answer which tools it exposes.
//
// "MCP might supply it" is decided from the config alone, and it is
// deliberately generous — the cost of a false deferral is one wasted
// load (the old behaviour), the cost of a false rejection is a correct
// invocation that cannot start:
//
//   - the name carries a configured server's "<server>__" prefix
//     (mcptool's default registration form), or
//   - some configured server opts out of prefixes ("prefix": false)
//     and may therefore register ANY bare name, unknowable before
//     ListTools.
//
// Both rules are pinned by the table in tools_precheck_test.go: the
// "prefix" contract lives in mcptool.loadOne and mcpconfig.ServerEntry
// .UsePrefix, and a change there without a change here would let a
// legitimate invocation start failing at flag-parse time.
//
// An unreadable mcp.json arrives as a zero Config (its error was already
// printed): no servers can load, so no name defers on MCP's account —
// matching the old behaviour where a broken config skipped loading
// entirely.
func checkToolsWhitelistPreMCP(flagVal string, reg *tools.Registry, mcpCfg mcpconfig.Config) error {
	names := parseToolsFlag(flagVal)
	if len(names) == 0 {
		return nil
	}
	// The zero-tool sentinel is a valid -tools value but not a tool name,
	// and it lives on its own branch (c_lh6tlq, not yet merged): `none`
	// alone means "empty registry", anything mixed with names is a usage
	// error there, not an unknown name. Both decisions belong to
	// applyToolsWhitelist, so this check must not form its own opinion —
	// deferring the WHOLE value keeps the sentinel's exit 2 and message
	// where its card asserts them, in either merge order. The cost is
	// nil: `--tools none` is a program lane, not the CI typo path this
	// optimisation targets.
	if slices.Contains(names, "none") {
		return nil
	}
	known := make(map[string]bool, len(postMCPTBuiltInTools))
	for _, n := range reg.Names() {
		known[n] = true
	}
	for _, n := range postMCPTBuiltInTools {
		known[n] = true
	}
	anyBareNames := false
	var prefixes []string
	for server, e := range mcpCfg.MCPServers {
		if !e.UsePrefix() {
			anyBareNames = true
			continue
		}
		prefixes = append(prefixes, server+"__")
	}
	var unknown []string
	for _, n := range names {
		if known[n] {
			continue
		}
		if anyBareNames {
			continue // a prefix-less server may register any bare name
		}
		deferred := slices.ContainsFunc(prefixes, func(p string) bool {
			return strings.HasPrefix(n, p)
		})
		if !deferred {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	avail := append(reg.Names(), postMCPTBuiltInTools...)
	slices.Sort(avail)
	fmt.Fprintf(os.Stderr, "-tools: unknown tool(s): %s\n", strings.Join(unknown, ", "))
	fmt.Fprintf(os.Stderr, "available tools: %s\n", strings.Join(avail, ", "))
	return errToolsUsage
}
