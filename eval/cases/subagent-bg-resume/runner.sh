#!/usr/bin/env bash
# runner.sh — subagent-bg-resume (eval v2 E4, archive/feature-eval-v2.md §2.2/:108,
# card c_vollxv). Replaces the default harness invocation for the same
# reason session-id-contract does: the default passes -no-save, and in
# --no-save seek skips subagent wiring entirely (cmd/seek: "the `agent`
# tool is simply absent from the registry in that mode") — nothing this
# case measures can even happen. The contract needs the opposite shape,
# a saved session plus a -resume of it, because the background delivery
# surfaces only fire across process boundaries:
#
#   run 1 (fresh, saved):  model spawns ONE run_in_background subagent
#                          (trivial no-tool task), keeps working, run
#                          ends. The child must finish BEFORE the parent
#                          process exits — Manager.Shutdown cancels
#                          still-active children (failed reason=canceled),
#                          which this case judges red (bg_completed 0).
#                          That race is the case's known flake source;
#                          see README.md.
#   run 2 (-resume <sid>): startup RecoverNotices (M-O1.3 D3) rehydrates
#                          the completed-but-un-notified child; the
#                          first Prompt drains it (Notifier OnPrePrompt)
#                          → <context> message in the main transcript +
#                          `notified` marker in subagents.jsonl. The
#                          prompt then asks the model to continue the
#                          child via session_id=<sub-sid> (sync Continue)
#                          → new `started` event carrying resumed_from.
#
# Disk-side assertions (the PRD's three, all SHAPE/FIELD-level — never
# prose): runner_fact lines {"type":"runner_fact","check":<metric>,
# "value":N} on stdout, consumed by run.sh's bg_* metric family. The
# check string MUST equal the metric name verbatim (extract_metric
# matches on it — a suffix-less name reads as 0, the mismatch class
# this case's first debug round produced):
#   bg_started_events   started events with background=true
#   bg_completed_events completed terminals joined to those sub_sids
#   bg_notified_events  notified markers joined to the COMPLETED bg set
#   bg_notified_transcript  parent-session <context> user messages
#                     containing the "[agent: background done
#                     sub-sid=<full>]" wire prefix for those sub_sids
#   bg_resumed_lineage  started events whose resumed_from is non-empty
#                     AND names an existing sub_sid in the index
#   bg_artifacts_missing completed runs whose transcript.jsonl is absent
#                     from projects/<pid>/sessions/<sid>/subagents/<sub>/
#
# ORDERING: the index fold for notified/lineage/completed runs AFTER
# run 2 — those events are written by run 2's startup/first turn and by
# the continuation itself; only the bg-sub_sid discovery (for run 2's
# prompt) reads the index between the two invocations.
#
# Env contract (eval/run.sh): SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT,
# SEEK_EVAL_PROMPT_FILE, SEEK_EVAL_MAX_TURNS; the provider lane rides
# ${SEEK_EVAL_PROVIDER_ARGS-} (word-split flag list, empty = none).
# SEEK_HOME points at an isolated scratch (wiped per round) so nothing
# touches the real ~/.seek; config.json is copied in as the auth
# fallback for key-in-config setups (same tradeoff as session-id-
# contract's runner — the sandbox is gitignored eval/tmp).
set -u

scratch="$SEEK_EVAL_REPO_ROOT/eval/tmp/subagent-bg-resume"
home_dir="$scratch/home"
rm -rf "$scratch"
mkdir -p "$home_dir"
cp "$HOME/.seek/config.json" "$home_dir/config.json" 2>/dev/null || true

prompt1=$(cat "$SEEK_EVAL_PROMPT_FILE")
run1=$(mktemp)
run2=$(mktemp)

# Word-split by intent: SEEK_EVAL_PROVIDER_ARGS is a space-separated
# flag list ("-provider opencode -model …") exactly as run.sh builds it.
SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" ${SEEK_EVAL_PROVIDER_ARGS-} \
  -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt1" > "$run1" 2>/dev/null
rc1=$?

sid=$(jq -rs 'map(select(.type=="agent_end")) | .[0].session_id // empty' "$run1")

# Locate the project lifecycle index. The isolated SEEK_HOME owns at
# most one project (this repo), so first match is deterministic.
index=$(find "$home_dir/projects" -name subagents.jsonl 2>/dev/null | head -1)

# Pick the continuation target from run 1's state: the first background
# sub_sid (spawn order). A completed one is preferred — continuing a
# canceled child fails loudly inside run 2, which is the honest red
# either way.
continue_sid=""
if [[ -n "$index" && -f "$index" ]]; then
  continue_sid=$(jq -rs \
    '[.[] | select(.event=="started" and .background==true) | .sub_sid] | .[0] // empty' \
    "$index" 2>/dev/null)
fi

rc2=0
if [[ -n "$sid" && -n "$continue_sid" ]]; then
  prompt2="A background subagent notification should have arrived in the context above. Continue that subagent: call the agent tool exactly once with session_id=\"$continue_sid\" and the follow-up prompt \"Reply with exactly the single word halfway. Do not use any tools.\" Do not set run_in_background for this call. Do not spawn anything else; finish your reply afterwards."
  SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" ${SEEK_EVAL_PROVIDER_ARGS-} \
    -resume "$sid" -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt2" > "$run2" 2>/dev/null
  rc2=$?
else
  # Run 1 produced no session_id or no background spawn — the contract
  # is already broken. Report loudly (bound: max_nonzero_exits 0) and
  # skip run 2: nothing to resume or continue.
  rc2=87
fi

cat "$run1" "$run2"
printf '{"type":"runner_fact","invocation":1,"exit_code":%d}\n' "$rc1"
printf '{"type":"runner_fact","invocation":2,"exit_code":%d}\n' "$rc2"

# --- full index fold, AFTER run 2 (see ORDERING above). Joins are on
# sub_sid values (event kind + field identity); nothing here reads
# summaries. jq truthiness note: index() returns 0 for a first match,
# and 0 is falsy — the joins compare against null, not truthiness.
bg_started=0
bg_completed=0
bg_notified=0
bg_resumed_lineage=0
bg_sids=""
completed_sids=""
if [[ -n "$index" && -f "$index" ]]; then
  stats=$(jq -s '
    [.[] | select(.event=="started")] as $starts
    | [$starts[] | select(.background==true) | .sub_sid] as $bg
    | [$starts[] | .sub_sid] as $all
    | [.[] | select(.event=="completed") | .sub_sid] as $done
    | [.[] | select(.event=="notified") | .sub_sid] as $notified
    | [$starts[] | (.resumed_from // "") as $rf
                    | select($rf != "" and (($all | index($rf)) != null))
                    | .sub_sid] as $lineage
    | [$done[] | . as $s | select(($bg | index($s)) != null)] as $bgdone
    # notified joins the COMPLETED bg set, not every bg sid: a canceled
    # child still gets a (failure-notice) notified marker from the real
    # product — assertion ① is about the COMPLETION notice specifically.
    | [$notified[] | . as $s | select(($bgdone | index($s)) != null)] as $bgnot
    | {bg_started: ($bg | length),
       bg_completed: ($bgdone | length),
       bg_notified: ($bgnot | length),
       lineage: ($lineage | length)}
  ' "$index" 2>/dev/null) || stats=""
  if [[ -n "$stats" ]]; then
    bg_started=$(printf '%s' "$stats" | jq -r '.bg_started')
    bg_completed=$(printf '%s' "$stats" | jq -r '.bg_completed')
    bg_notified=$(printf '%s' "$stats" | jq -r '.bg_notified')
    bg_resumed_lineage=$(printf '%s' "$stats" | jq -r '.lineage')
  fi
  bg_sids=$(jq -rs '[.[] | select(.event=="started" and .background==true) | .sub_sid] | join(" ")' "$index" 2>/dev/null)
  completed_sids=$(jq -rs '[.[] | select(.event=="completed") | .sub_sid] | join(" ")' "$index" 2>/dev/null)
fi

# --- transcript-side delivery check (reads the SAVED session file —
# the state after run 2's first Prompt drained the notice). A delivered
# notice is a user-role message whose content starts with <context> and
# carries the wire prefix + the full sub_sid — shape and fields only;
# the summary prose between them is never asserted.
bg_notified_transcript=0
sess_file="$home_dir/sessions/$sid.jsonl"
if [[ -n "$sid" && -f "$sess_file" ]]; then
  for s in $bg_sids; do
    # -e is load-bearing: without it jq exits 0 for BOTH true and false,
    # so the `if` would count every bg sid as delivered. >/dev/null is
    # also load-bearing: this runner's stdout IS the metric stream — a
    # bare `true`/`false` line from jq would pollute it and break every
    # select(.type==...) downstream in run.sh.
    if jq -ers --arg s "$s" \
      'any(.[]; .role=="user"
           and ((.content // "") | startswith("<context>"))
           and ((.content // "") | contains("[agent: background done sub-sid=" + $s)))' \
      "$sess_file" >/dev/null 2>&1; then
      bg_notified_transcript=$((bg_notified_transcript + 1))
    fi
  done
fi

# --- artifact-path assertion: every COMPLETED run (background child +
# continuation alike) must have left transcript.jsonl at
# projects/<pid>/sessions/<parent-sid>/subagents/<sub-sid>/ — the
# paths.SubagentSessionDir layout.
bg_artifacts_missing=0
if [[ -n "$index" && -n "$sid" ]]; then
  proj_dir=$(dirname "$index")
  for s in $completed_sids; do
    if [[ ! -f "$proj_dir/sessions/$sid/subagents/$s/transcript.jsonl" ]]; then
      bg_artifacts_missing=$((bg_artifacts_missing + 1))
    fi
  done
fi

printf '{"type":"runner_fact","check":"bg_started_events","value":%d}\n' "$bg_started"
printf '{"type":"runner_fact","check":"bg_completed_events","value":%d}\n' "$bg_completed"
printf '{"type":"runner_fact","check":"bg_notified_events","value":%d}\n' "$bg_notified"
printf '{"type":"runner_fact","check":"bg_notified_transcript","value":%d}\n' "$bg_notified_transcript"
printf '{"type":"runner_fact","check":"bg_resumed_lineage","value":%d}\n' "$bg_resumed_lineage"
printf '{"type":"runner_fact","check":"bg_artifacts_missing","value":%d}\n' "$bg_artifacts_missing"

rm -f "$run1" "$run2"
