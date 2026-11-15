package agenttypes

// builtin.go holds the three compile-time default agent types
// (general-purpose / explore / plan).
//
// This is where internal/subagent's hardcoded template map retired to
// (feature-orchestration.md D11 "内置三 type 保留为编译期默认"): the
// canonical declaration of the built-ins is the DATA here; subagent's
// templates.go adapts Definition → spawn-time Template. With no
// agents/*.md files the spawn path is byte-identical to the pre-P2
// hardcoded map — locked by TestBuiltinTemplatesByteIdentical over in
// internal/subagent, which asserts the adapter output against the
// historical values.
//
// Invariants (guarded there and in agenttypes tests):
//   - exactly the three names, matching subagent.Type* constants;
//   - Body strings byte-identical to the retired Template.Extra
//     values — they land verbatim in child system prompts
//     (sysprompt.ComposeSubagent), so any edit is a prompt-cache
//     reset for every spawned subagent and must be deliberate;
//   - explore/plan pin Workflow "plan-analyze" (the load-bearing
//     "PrefYolo + explore" safety property).

// Builtins returns the three compile-time defaults in a stable order.
// The returned Definitions are shared read-only; callers must not
// mutate them (Set.Add stores the pointer as-is).
func Builtins() []*Definition {
	return builtinDefs
}

var builtinDefs = []*Definition{
	{
		Name:        "general-purpose",
		Description: "Full tool set (minus agent/ask_user); inherits the parent session's model, effort, and permissions.",
		// nil Tools = inherit the parent registry minus the universal
		// agent/ask_user exclusions.
		Tools:  nil,
		Body:   "", // no extra clause; the standard subagent role intro alone
		Origin: OriginBuiltin,
		Source: "builtin:general-purpose",
	},
	{
		Name:        "explore",
		Description: "Read-only parallel research (read/grep/list_dir/git/webfetch/think) forced into plan-analyze; returns bulleted findings.",
		Tools: []string{
			"read", "grep", "list_dir", "git", "webfetch", "think",
		},
		Workflow: "plan-analyze",
		Body:     "You are in research-only mode. You cannot write, edit, or run mutating commands. Return findings as bulleted summary.",
		Origin:   OriginBuiltin,
		Source:   "builtin:explore",
	},
	{
		Name:        "plan",
		Description: "Read-only investigation that returns a numbered, structured action plan the parent (or a human reviewer) can execute step by step.",
		Tools: []string{
			"read", "grep", "list_dir", "git", "webfetch", "think",
		},
		Workflow: "plan-analyze",
		Body:     "You are in plan-analyze mode. Investigate the task and return a numbered, structured plan in your final summary — explicit steps the parent (or a human reviewer) can execute. You cannot run mutating tools yourself.",
		Origin:   OriginBuiltin,
		Source:   "builtin:plan",
	},
}
