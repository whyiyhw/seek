#!/usr/bin/env bash
# eval/run.sh — run one or all eval cases against a seek binary.
#
# Usage:
#   eval/run.sh                              # all cases, ./seek
#   eval/run.sh <case-name>                  # one case, ./seek
#   eval/run.sh <case-name> <binary-path>    # one case, named binary
#   eval/run.sh '' <binary-path>             # all cases, named binary
#   eval/run.sh --rounds 5 <case-name>       # N-round distribution sampling
#   eval/run.sh --lane release               # curated case list (E6)
#   eval/run.sh --lane core --rounds 2       # lane + rounds compose
#
# Each case appends one JSON row PER ROUND to:
#   eval/results/<UTC-date>-<binary-version>.jsonl
#
# --rounds N (eval v2 E1, docs/prd/feature-eval-v2.md §2.1): defaults to
# 1, which is the historical single-sample behaviour BYTE FOR BYTE —
# no `round` field, no aggregate row, no extra stdout. With N > 1 each
# round is a full case lifecycle (setup → seek → teardown) appending its
# own row carrying `round: k`, and judgement reads the round set's pass
# rate against the case's `min_pass_rate` (expect.json, default 1.0,
# INCLUSIVE `>=`); `record_only: true` cases are recorded, never gated.
# Cost warning: rounds multiply API spend — see the cost table in
# eval/README.md before raising N on the full case set.
#
# --lane NAME (eval v2 E6, docs/prd/feature-eval-v2.md §2.3/§三): run the
# curated case list in eval/lane-<name>.txt instead of all cases — one
# case name per line, '#' comments and blank lines ignored. Every entry
# must name an existing eval/cases/<name>/ dir (a name with no case dir
# exits 2 at startup, before any case runs — the list is policy, a typo
# in it must fail loud, not skip silently). Mutually exclusive with an
# explicit <case-name> positional. Shipped lanes (eval/README.md §
# "发版前跑什么"): release = load-bearing axes + calibration-stable
# cases (the pre-release human gate), core = the low-cost nightly
# subset. No --lane = all cases, byte for byte the historical behaviour
# (guard: scripts/eval-lane-test.sh pins the no-flag output against the
# pre-E6 run.sh blob from git history).
#
# Env knobs (ablation lane steering, see eval/ablate.sh + eval/README.md
# § Ablation; both UNSET = historical behaviour, byte for byte):
#   SEEK_EVAL_RESULTS_DIR   redirect the JSONL sink away from
#                           eval/results (the regression-trend data
#                           source — ablation rows must never land there)
#   SEEK_EVAL_EXTRA_ARGS    extra seek args appended to the default
#                           invocation (word-split on spaces, e.g.
#                           "-tools read,edit,write,bash,grep"); also
#                           passed through to runner.sh cases in the env
#
# SEEK_HOME is pinned to a sandbox (eval/tmp/seek-home, gitignored;
# a caller-set non-empty value is respected) so no eval write can
# reach the real ~/.seek — see the isolation block below and
# eval/README.md § Isolation. Unattended lanes run fake-key by rule
# (docs/prd/v8.md §9.1).
# Env knobs (provider lane, see eval/README.md § "Provider lane"; all
# four UNSET = DEFAULT opencode lane — the OpenCode Go subscription,
# NOT the metered key; falling back to the metered DeepSeek lane
# requires the explicit opt-in SEEK_EVAL_PROVIDER=deepseek):
#   SEEK_EVAL_PROVIDER      deepseek | opencode | compatible — which
#                           lane eval traffic bills. Unset = opencode
#                           (subscription, default lane model
#                           deepseek-v4.1-flash); deepseek = explicit
#                           fallback to the metered key lane (the only
#                           way back to it — batch/unattended runs must
#                           never burn the metered key by default)
#   SEEK_EVAL_MODEL         model id override (passed as -model)
#   SEEK_EVAL_BASE_URL      endpoint override (full POST target)
#   SEEK_EVAL_API_KEY       lane credential (exported as the lane's
#                           canonical env var)
#
# Exit code: 0 if every executed case passed, 1 if any failed, 2 on
# setup error (missing binary, unparseable expect.json, etc.).

set -u  # treat unset vars as errors; do NOT set -e — we want to
        # iterate every case even if one fails.

repo_root=$(cd "$(dirname "$0")/.." && pwd)
eval_root="$repo_root/eval"
cases_dir="$eval_root/cases"
results_dir="$eval_root/results"

# --- Windows/git-bash path compatibility --------------------------------
# A Windows-native jq.exe (WinGet's, resolved below when it is the only
# jq on PATH) cannot open MSYS paths: /tmp/... and /d/... are not
# Windows paths, so EVERY jq invocation on such a path fails — expect
# bounds read as garbage (max_turns becomes an error string), and the
# mktemp'd stream file is unreadable, so every metric extracts as 0 and
# seek itself exits instantly on the garbage -max-turns. Under MSYS,
# rewrite the path vocabulary into mixed drive-letter form (D:/...),
# which both Windows binaries and MSYS binaries open. WSL/Linux have no
# cygpath and are untouched. (Sibling of the b041d62 WSL+jq.exe pitfall:
# the same class of break, on the git-bash side the old comment assumed
# worked.)
if command -v cygpath >/dev/null 2>&1; then
  repo_root=$(cygpath -m "$repo_root")
  eval_root=$(cygpath -m "$eval_root")
  cases_dir=$(cygpath -m "$cases_dir")
  results_dir=$(cygpath -m "$results_dir")
fi

# --- SEEK_HOME isolation (card c_clip4i) --------------------------------
# Structural guarantee: nothing an eval run writes may land in the real
# ~/.seek. seek resolves ALL user-data paths (sessions, projects/,
# dream-state, cron/, skill stats, upgrade-check, …) through
# paths.Home() = $SEEK_HOME-or-~/.seek, so pinning SEEK_HOME at a
# sandbox under eval/tmp (gitignored) makes real-HOME writes impossible
# from the eval lane. This is not hypothetical hygiene: a probe
# (2026-11-18) showed a single invocation whose API call fails
# INSTANTLY on a fake key still writes four things under ~/.seek
# (dream-state.json, projects/<id>/manifest.json, sessions/,
# cron/tick.lock). An unattended lane (CI / batch / steward loop) has
# no human watching for that pollution — and a real HOME would also
# leak machine-local user state (skills, memory, mcp.json) INTO the
# measurements. Hermetic-by-default is a fidelity win too.
#
# A caller-provided non-empty SEEK_HOME is respected verbatim (an outer
# sandbox redirecting the redirect). Empty-but-set is treated as unset:
# os.Getenv treats "" as unset as well, and exporting "" would silently
# re-open the real ~/.seek.
#
# SEEK_SESSIONS_DIR ranks ABOVE SEEK_HOME in session.NewStore's
# resolution ladder, so an inherited stray value would bypass the
# sandbox for sessions specifically — unset it. No eval lane uses it
# (ablation lanes steer via SEEK_EVAL_* knobs only).
#
# Silent by design — no output line is added: scripts/
# eval-ablate-test.sh byte-compares run.sh's no-env stdout/stderr/
# result rows against a pre-knob blob extracted from git history.
# The mechanism itself is pinned by scripts/eval-home-isolation-test.sh.
if [[ -z "${SEEK_HOME-}" ]]; then
  export SEEK_HOME="$eval_root/tmp/seek-home"
fi
unset SEEK_SESSIONS_DIR
mkdir -p "$SEEK_HOME"

# v8·T2 ablation hooks — both default to the historical behaviour when
# unset, so the no-env output stays byte-identical (card c_w3vhcm
# acceptance ②).
results_dir=${SEEK_EVAL_RESULTS_DIR-"$results_dir"}
seek_extra_args=${SEEK_EVAL_EXTRA_ARGS-}

# --- Provider lane (c_ffzic2, 2026-11-19; default flipped 2026-11-20 ---
# --- by the user's call, 疑点 A option (a)): batch/unattended evals    ---
# --- bill the OpenCode Go subscription, never the metered DeepSeek     ---
# --- key — now by DEFAULT, not only when steered.                      ---
# 口径 (docs/prd/v8.md §9.1): 测试默认 OpenCode Go 套餐 DeepSeek（订阅
# 内）；计量真 key 仅 --only 人工指名或卡面明示授权——回到计量车道必须
# 显式设 SEEK_EVAL_PROVIDER=deepseek（人工指名的 env 形式）。All four
# knobs unset = opencode lane（缺省订阅车道；scripts/eval-provider-test.sh
# ① pins the default and the explicit-fallback legs）。
#
#   SEEK_EVAL_PROVIDER   deepseek | opencode | compatible (unset =
#                        opencode). opencode = OpenCode Go plan ($10/mo
#                        subscription) riding seek's own -provider
#                        opencode lane, whose default model IS
#                        deepseek-v4.1-flash — pass SEEK_EVAL_MODEL only
#                        to override it. Credentials resolve inside seek:
#                        OPENCODE_API_KEY env > providers.opencode.api_key
#                        > the stored browser login. This harness NEVER
#                        reads — and NEVER refreshes —
#                        ~/.seek/opencode-auth.json itself: the refresh
#                        token is single-use, so a side refresh would
#                        invalidate the copy seek holds. An expired login
#                        surfaces as loud API errors and a human re-logs
#                        in; it is never self-healed here. deepseek =
#                        explicit fallback to the metered key lane — the
#                        historical invocation shape (no -provider flag,
#                        env DEEPSEEK_API_KEY left alone); it bills the
#                        metered key, so setting it is a human decision.
#                        compatible = any OpenAI-compatible endpoint;
#                        requires SEEK_EVAL_BASE_URL + SEEK_EVAL_MODEL.
#   SEEK_EVAL_MODEL      model id, passed through as -model.
#   SEEK_EVAL_BASE_URL   endpoint override (the FULL POST target), passed
#                        as -base-url AND exported as the lane's own env
#                        override (OPENCODE_BASE_URL) so a config.json
#                        preset can't out-rank the flag (config.BaseURLFor
#                        beats --base-url inside seek; env beats config).
#   SEEK_EVAL_API_KEY    lane credential, exported as the lane's canonical
#                        env var (DEEPSEEK_API_KEY for deepseek,
#                        OPENCODE_API_KEY for opencode, OPENAI_API_KEY for
#                        compatible). Never write a real key into card
#                        text, logs or expect files — source it from the
#                        machine environment.
#
# While the opencode/compatible lane is active, DEEPSEEK_API_KEY is
# scrubbed from this shell so no child (seek invocation, runner.sh,
# setup/teardown) can see it: the metered key must have ZERO reach into
# a switched lane — seek's `compatible` case would otherwise read
# DEEPSEEK_API_KEY as a fallback credential. The deepseek lane is the
# metered lane itself, so there (and only there) the key survives.
# runner.sh cases that build their own invocations forward the lane
# with the exported SEEK_EVAL_PROVIDER_ARGS (empty = no provider flags
# to forward; reference as ${SEEK_EVAL_PROVIDER_ARGS-} under set -u) —
# contract in eval/README.md § "Provider lane".
seek_eval_provider=${SEEK_EVAL_PROVIDER:-opencode}
seek_eval_model=${SEEK_EVAL_MODEL-}
seek_eval_base_url=${SEEK_EVAL_BASE_URL-}
seek_eval_api_key=${SEEK_EVAL_API_KEY-}
provider_args=""
case "$seek_eval_provider" in
  opencode)
    # No further requirements: model defaults to the lane's
    # deepseek-v4.1-flash inside seek; credentials may legitimately be
    # absent from this harness when seek's stored login serves them.
    ;;
  deepseek)
    # Explicit fallback to the metered key lane: the historical
    # invocation shape (no -provider flag — deepseek IS seek's default
    # provider; env DEEPSEEK_API_KEY untouched by the scrub below).
    ;;
  compatible)
    if [ -z "$seek_eval_base_url" ] || [ -z "$seek_eval_model" ]; then
      echo "eval: SEEK_EVAL_PROVIDER=compatible needs SEEK_EVAL_BASE_URL and SEEK_EVAL_MODEL" >&2
      exit 2
    fi
    ;;
  *)
    echo "eval: unknown SEEK_EVAL_PROVIDER '$seek_eval_provider' (deepseek | opencode | compatible)" >&2
    exit 2
    ;;
esac
if [ "$seek_eval_provider" != deepseek ]; then
  provider_args="-provider $seek_eval_provider"
fi
[ -n "$seek_eval_model" ] && provider_args="$provider_args -model $seek_eval_model"
[ -n "$seek_eval_base_url" ] && provider_args="$provider_args -base-url $seek_eval_base_url"
if [ -n "$seek_eval_api_key" ]; then
  case "$seek_eval_provider" in
    deepseek) export DEEPSEEK_API_KEY="$seek_eval_api_key" ;;
    opencode) export OPENCODE_API_KEY="$seek_eval_api_key" ;;
    *)        export OPENAI_API_KEY="$seek_eval_api_key" ;;
  esac
fi
if [ "$seek_eval_provider" = opencode ] && [ -n "$seek_eval_base_url" ]; then
  export OPENCODE_BASE_URL="$seek_eval_base_url"
fi
if [ "$seek_eval_provider" != deepseek ]; then
  unset DEEPSEEK_API_KEY || true
fi
export SEEK_EVAL_PROVIDER_ARGS="$provider_args"

# --- --rounds / --lane flag parsing (E1 / E6) ----------------------------
# Positional interface (<case> <binary>) is unchanged; flags are stripped
# wherever they appear in argv before the positional reads. rounds=1 and
# lane="" are the historical behaviour, byte for byte (guard tests:
# scripts/eval-rounds-test.sh + scripts/eval-lane-test.sh, siblings of
# eval-ablate-test's no-env equivalence pin). Malformed values exit 2 at
# startup — never silently degrade (same doctrine as min_pass_rate below).
rounds=1
lane=""
positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --rounds)
      # POSIX-glob digit check (bash 3.2-safe, no =~): empty or any
      # non-digit char lands in the reject arm.
      case "${2-}" in
        ''|*[!0-9]*) ;;
        *) [[ "$2" -ge 1 ]] && { rounds=$2; shift 2; continue; } ;;
      esac
      echo "eval: --rounds needs an integer >= 1 (got: ${2-<missing>})" >&2
      exit 2
      ;;
    --rounds=*)
      arg_rounds=${1#--rounds=}
      case "$arg_rounds" in
        ''|*[!0-9]*) ;;
        *) [[ "$arg_rounds" -ge 1 ]] && { rounds=$arg_rounds; shift; continue; } ;;
      esac
      echo "eval: --rounds needs an integer >= 1 (got: $arg_rounds)" >&2
      exit 2
      ;;
    --lane)
      # Charset gate [a-z0-9-] doubles as the path-safety gate: '/' and
      # '.' are outside it, so no --lane value can escape eval/lane-*.txt.
      # Any name with a matching lane file is valid (release/core are the
      # shipped two, not a hardcoded pair).
      case "${2-}" in
        ''|*[!a-z0-9-]*)
          echo "eval: --lane needs a lane name [a-z0-9-], e.g. release | core (got: ${2-<missing>})" >&2
          exit 2
          ;;
        *) lane=$2; shift 2; continue ;;
      esac
      ;;
    --lane=*)
      arg_lane=${1#--lane=}
      case "$arg_lane" in
        ''|*[!a-z0-9-]*)
          echo "eval: --lane needs a lane name [a-z0-9-], e.g. release | core (got: $arg_lane)" >&2
          exit 2
          ;;
        *) lane=$arg_lane; shift; continue ;;
      esac
      ;;
    *)
      positional+=("$1")
      shift
      ;;
  esac
done
case_filter=${positional[0]-}
binary=${positional[1]-$repo_root/seek}

if [[ ! -x "$binary" ]]; then
  echo "eval: binary not found or not executable: $binary" >&2
  echo "      (build it first: go build -o seek ./cmd/seek)" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1 && ! command -v jq.exe >/dev/null 2>&1; then
  echo "eval: jq is required but not installed" >&2
  exit 2
fi
# Resolve jq. Prefer a native jq; the jq.exe fallback only works when
# the script runs under MSYS (git-bash) with Windows-visible paths —
# under WSL a Windows jq.exe cannot open /mnt/... or /tmp/... paths
# (install the Linux jq instead: sudo apt install jq). See
# docs/test-plan-read-tool.md §8.3.
if command -v jq >/dev/null 2>&1; then
  JQ=jq
else
  JQ=jq.exe
fi

# --- lane list loading (E6) ------------------------------------------------
# Read + validate the WHOLE list before running anything: a lane entry
# naming a nonexistent case, a --lane paired with an explicit case name,
# or an effectively empty list all exit 2 here, before mkdir/results side
# effects — same startup-rejection doctrine as malformed --rounds /
# min_pass_rate. Lane files always resolve against the REPO's eval/ dir
# ($eval_root), never against a redirected SEEK_EVAL_RESULTS_DIR: the
# list is lane policy (repo content), the results dir is data.
lane_cases=()
if [[ -n "$lane" ]]; then
  if [[ -n "$case_filter" ]]; then
    echo "eval: --lane and an explicit case name are mutually exclusive (the lane runs its whole list)" >&2
    exit 2
  fi
  lane_file="$eval_root/lane-$lane.txt"
  if [[ ! -f "$lane_file" ]]; then
    echo "eval: no such lane file: $lane_file (see eval/README.md for the lane list format)" >&2
    exit 2
  fi
  while read -r lane_raw || [[ -n "$lane_raw" ]]; do
    lane_entry=${lane_raw%%#*}
    lane_entry=$(printf '%s' "$lane_entry" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
    [[ -z "$lane_entry" ]] && continue
    if [[ ! -d "$cases_dir/$lane_entry" ]]; then
      echo "eval: lane '$lane' lists unknown case: $lane_entry (no eval/cases/$lane_entry/)" >&2
      exit 2
    fi
    lane_cases+=("$lane_entry")
  done < "$lane_file"
  if [[ ${#lane_cases[@]} -eq 0 ]]; then
    echo "eval: lane '$lane' list has no active entries: $lane_file" >&2
    exit 2
  fi
fi

mkdir -p "$results_dir"

# Stamp the binary identity into the results filename so multi-binary
# runs land in distinct files. `seek -version` formats as "vX.Y.Z · hash"
# or "dev · hash+" — we keep just the hash so the filename stays safe.
version_full=$("$binary" -version 2>/dev/null || echo "unknown")
version_tag=$(echo "$version_full" | awk -F' · ' '{print $1}' | sed 's/[^A-Za-z0-9._-]/_/g')
version_rev=$(echo "$version_full" | awk -F' · ' '{print $2}' | sed 's/[^A-Za-z0-9._-]/_/g')
date_stamp=$(date -u +%Y-%m-%d)
results_file="$results_dir/${date_stamp}-${version_tag}-${version_rev}.jsonl"

echo "eval: binary  $binary ($version_full)"
echo "eval: results $results_file"
if [[ -n "$lane" ]]; then
  echo "eval: lane    $lane (${#lane_cases[@]} cases from lane-$lane.txt)"
fi
echo

# extract_metric NAME JSONL_PATH → integer
# Keep this table in sync with eval/README.md's "Metric vocabulary".
extract_metric() {
  local name=$1 path=$2
  case "$name" in
    unknown_field_errors)
      $JQ -s '[.[] | select(.type=="tool_end" and (.error // "" | contains("unknown field")))] | length' "$path"
      ;;
    think_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="think")] | length' "$path"
      ;;
    total_tool_calls)
      $JQ -s '[.[] | select(.type=="tool_start")] | length' "$path"
      ;;
    read_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="read")] | length' "$path"
      ;;
    grep_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="grep")] | length' "$path"
      ;;
    mcp_calls)
      # tool_start events whose tool name CONTAINS the "__" infix —
      # MCP bridge tools register under "<server>__<tool>" (D1's
      # default prefix policy, internal/tools/mcptool/bridge.go; no
      # built-in tool name contains "__"). Assumes D1 has landed:
      # before it the bridge registered bare server-side names
      # (prefix only on conflict), so this reads 0 even when MCP
      # tools are actively used. Basis of the mcp-semble-routing
      # three-arm comparison, docs/prd/feature-mcp-client.md §5.2.
      $JQ -s '[.[] | select(.type=="tool_start" and ((.name // "") | test("__")))] | length' "$path"
      ;;
    list_dir_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="list_dir")] | length' "$path"
      ;;
    agent_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="agent")] | length' "$path"
      ;;
    agent_bg_calls)
      # agent tool_starts whose parsed args carry run_in_background=true —
      # the D1 detached-spawn shape (feature-orchestration §4.1). fromjson?
      # swallows a malformed args blob (event drops out rather than
      # erroring the metric).
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="agent" and ((.args // "{}" | fromjson? | .run_in_background) == true))] | length' "$path"
      ;;
    agent_continue_calls)
      # agent tool_starts whose parsed args carry a non-empty session_id —
      # the P1-b continuation shape (Manager.Continue / ContinueBackground,
      # the `session_id` field of the agent tool schema).
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="agent" and (((.args // "{}" | fromjson? | .session_id) // "") != ""))] | length' "$path"
      ;;
    bash_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="bash")] | length' "$path"
      ;;
    edit_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="edit")] | length' "$path"
      ;;
    git_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="git")] | length' "$path"
      ;;
    git_subcommand_dupes)
      # The git tool auto-fixes a duplicated subcommand (args[0] ==
      # subcommand) and prepends a note containing "repeated from args"
      # to the result — counting the notes counts the model's mistakes,
      # zero round trips sacrificed (see internal/tools/git/git.go and
      # pitfalls "a refusal the model does not learn from").
      $JQ -s '[.[] | select(.type=="tool_end" and .name=="git" and (.result // "" | contains("repeated from args")))] | length' "$path"
      ;;
    bash_chains)
      # A bash call whose command chains multiple commands (';' or '&&').
      # ';' is not even a separator on cmd.exe (the Windows shell), so a
      # POSIX-style chain fails wholesale ("ambiguous argument 'echo'");
      # '&&' is legal on cmd but still the chaining anti-pattern — the
      # target shape is one command per call, or parallel dedicated-tool
      # calls. '|' is deliberately NOT counted: pipelines and grep
      # alternations (--grep="a|b") are legitimate. Matches the raw args
      # JSON of the tool call (pre-exec), so a failed chain still counts.
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="bash" and ((.args // "") | test("[;]|&&")))] | length' "$path"
      ;;
    bash_git_calls)
      # A bash call that invokes git — the dedicated git tool covers
      # every read-only query (log/diff/status/show/...), so reaching for
      # bash skips the subcommand whitelist and the auto-fix. Matches git
      # as a COMMAND TOKEN: preceded by start/whitespace/quote/;/&/| and
      # followed by whitespace or end — deliberately NOT \bgit\b, which
      # would false-positive on ".git" in paths (cd .git, ls .git) and
      # "github.com" style strings. Catches "cd x && git log" chains too.
      # Mutating git (commit/push) legitimately belongs in bash, but this
      # eval's prompts are read-only, so 0 is the correct target there.
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="bash" and ((.args // "") | test("(^|[;&|[:space:]\"])git([[:space:]]|$)")))] | length' "$path"
      ;;
    bash_before_dedicated)
      # Bash calls made BEFORE the first dedicated inspection call —
      # bash as FIRST resort. Ordering is the honest anti-pattern
      # signal when a case's task legitimately forces shell recovery
      # LATER (e.g. read elided an over-1024-byte line and verbatim
      # text needs awk/sed): a total bash_calls bound cannot tell
      # recovery from overreach, this can. `list_dir` joined the
      # dedicated set 2026-11-18: bash-overreach's legitimate answer
      # path is list_dir(depth=2), so excluding it scored
      # list_dir→bash recovery as overreach (3/5 rounds). Adding a
      # tool to the set can only LOWER this metric, so no existing
      # bound gets stricter. No dedicated call at all → every bash
      # call counts (sentinel index past the array end).
      $JQ -s '
        [ .[] | select(.type=="tool_start") ] as $ts
        | [ range(0; $ts|length) as $i | select($ts[$i].name=="grep" or $ts[$i].name=="read" or $ts[$i].name=="list_dir") | $i ] as $ded_idx
        | ( if ($ded_idx|length) > 0 then $ded_idx[0] else 999999999 end ) as $first_ded
        | [ range(0; $ts|length) as $i | select($ts[$i].name=="bash" and $i < $first_ded) ] | length
      ' "$path"
      ;;
    probe_reads)
      # A read that returned zero lines from a non-starting offset is a
      # probe past EOF — the model could not tell "exactly N lines" from
      # "more pages exist". (An empty-file read at offset=1 also emits
      # "0 lines" but lacks the "from line" fragment, so it is excluded.)
      $JQ -s '[.[] | select(.type=="tool_end" and .name=="read" and (.result // "" | contains("0 lines emitted")) and (.result // "" | contains("from line")))] | length' "$path"
      ;;
    write_refusals)
      # fsobserve guard refusals start with "write refused:" (see
      # internal/fsobserve/fsobserve.go Explain).
      $JQ -s '[.[] | select(.type=="tool_end" and .name=="write" and (.error // "" | contains("write refused")))] | length' "$path"
      ;;
    write_ok)
      # write tool calls that completed without error — the in-CWD
      # write default-allow path under print-mode PrefDeny (writes
      # inside the working directory are not "dangerous", so the deny
      # preference never sees them). Distinct from write_refusals,
      # which counts the fsobserve blind-overwrite guard.
      $JQ -s '[.[] | select(.type=="tool_end" and .name=="write" and ((.error // "") == ""))] | length' "$path"
      ;;
    denied_tool_ends)
      # Tool calls refused by the permission gate. internal/permission
      # surfaces refusals as tool RESULTS whose error is ErrDenied
      # ("permission denied", permission.go) — never a fatal error, so
      # the run keeps going and still exits 0.
      $JQ -s '[.[] | select(.type=="tool_end" and (.error // "" | contains("permission denied")))] | length' "$path"
      ;;
    agent_ends)
      # agent_end events in the stream — one per seek invocation. A
      # run that dies mid-flight (API failure, crash) emits none, so
      # runner-chained cases bind exact_agent_ends to catch that.
      $JQ -s '[.[] | select(.type=="agent_end")] | length' "$path"
      ;;
    session_id_missing)
      # agent_end events WITHOUT a session_id. cmd/seek/main.go sets
      # jsonLine.SessionID whenever a session is active (stable
      # contract, schema comment on the struct); --no-save runs
      # legitimately omit it, so only bind this metric in cases that
      # run WITHOUT -no-save.
      $JQ -s '[.[] | select(.type=="agent_end" and ((.session_id // "") == ""))] | length' "$path"
      ;;
    session_id_distinct)
      # Distinct session_id values across agent_end events. A fresh
      # run plus a -resume of that session must report the SAME id
      # (the session file is reused), so the contract holds at exactly 1.
      $JQ -s '[.[] | select(.type=="agent_end") | .session_id // empty] | unique | length' "$path"
      ;;
    nonzero_exits)
      # runner_fact lines reporting a non-zero seek exit code. Exit
      # codes are process-level, not part of the JSONL stream, so a
      # runner.sh appends {"type":"runner_fact","exit_code":N} after
      # each invocation; this metric is how that reaches expect.json.
      # Cases using the default invocation emit no facts → 0.
      $JQ -s '[.[] | select(.type=="runner_fact" and ((.exit_code // 0) != 0))] | length' "$path"
      ;;
    bg_started_events|bg_completed_events|bg_notified_events|bg_notified_transcript|bg_resumed_lineage|bg_artifacts_missing)
      # subagent-bg-resume's disk-side family (eval v2 E4, c_vollxv):
      # the case's runner.sh reads the ISOLATED SEEK_HOME's project
      # state after its invocations — projects/*/subagents.jsonl (the
      # lifecycle event source, internal/subagent/index.go) and the
      # parent session transcript — and reports counts as
      # {"type":"runner_fact","check":<metric name>,"value":N} lines,
      # the file-level siblings of nonzero_exits above. Each metric
      # takes the MAX value across matching facts, 0 when absent; bind
      # them only in cases whose runner ALWAYS emits the fact channel
      # (otherwise a crashed round reads as "unknown metric"). The
      # runner asserts event SHAPE and FIELDS (event kind, sub_sid
      # joins, the "[agent: background done sub-sid=<full>]" wire
      # prefix) — never prose wording. Per-metric meanings live in
      # eval/README.md's "Metric vocabulary".
      $JQ -s --arg check "$name" '[.[] | select(.type=="runner_fact" and .check==$check) | (.value // 0)] | max // 0' "$path"
      ;;
    turns)
      $JQ -s '[.[] | select(.type=="turn_end")] | length' "$path"
      ;;
    review_line_refs)
      # Content proxy for "number of findings": count code-location
      # callouts in the assistant's text (concatenated text_delta deltas,
      # reasoning excluded) — both the prose form ("line 24" / "L24") and
      # the file:line form ("sizecache.go:24" → the ":24"). A thorough
      # review cites more specific locations than a terse one.
      # Deliberately does NOT count severity words (critical/high/low/…) —
      # those collide with the effort vocabulary the prompt echoes, which
      # would bias the low-effort case upward. Rough by design (a fix
      # snippet that quotes a slice like a[1:2] over-counts); pair it with
      # completion_tokens, which is the robust signal.
      $JQ -rs '
        ( [ .[] | select(.type=="text_delta" and ((.reasoning // false) | not)) | (.delta // "") ] | join("") )
        | [ match("(?i)\\bline\\s*\\.?\\s*\\d+|\\bL\\d+\\b|:\\d+\\b"; "g") ]
        | length
      ' "$path"
      ;;
    sse_delta_frames)
      # serve-web-stream (E5): delta frames on the W1.4 SSE wire, as
      # captured+paired by the case's runner.sh into sse_frame rows.
      $JQ -s '[.[] | select(.type=="sse_frame" and .event=="delta")] | length' "$path"
      ;;
    sse_usage_frames)
      # usage frames (turn_end translated server-side, webfrontend_stream.go).
      $JQ -s '[.[] | select(.type=="sse_frame" and .event=="usage")] | length' "$path"
      ;;
    sse_frame_order_ok)
      # Wire-order invariant: every named (delta/usage/tool) frame of the
      # close-out's run PRECEDES the close-out default frame (the run
      # table's events…reply contract). 1/0; 0 when no close-out exists.
      $JQ -s '
        [ .[] | select(.type=="sse_frame") ] as $f
        | [ $f | to_entries[] | select(.value.event=="" and (.value.data.run // null) != null) ] as $co
        | if ($co|length) < 1 then 0
          else ($co|last) as $c
            | [ $f | to_entries[]
                | select((.value.event=="delta" or .value.event=="usage" or .value.event=="tool"))
                | select(.value.data.run == $c.value.data.run and .key >= $c.key) ] | length
            | if . == 0 then 1 else 0 end
          end
      ' "$path"
      ;;
    sse_delta_accum_ok)
      # Accumulation: the delta frames' text, concatenated in wire order,
      # equals the close-out frame's text (the authoritative replacement).
      $JQ -s '
        [ .[] | select(.type=="sse_frame") ] as $f
        | [ $f | to_entries[] | select(.value.event=="" and (.value.data.run // null) != null) ] as $co
        | if ($co|length) < 1 then 0
          else ($co|last.value.data) as $r
            | [ $f | to_entries[] | select(.value.event=="delta" and .value.data.run == $r.run) | .value.data.text ] | join("") as $acc
            | if $acc == $r.text then 1 else 0 end
          end
      ' "$path"
      ;;
    sse_reply_closeout_ok)
      # Close-out shape: the reply frame carries a non-empty run (equal to
      # every delta frame's run) and a non-empty session_id (the W1.4
      # close-out addition over the MVP envelope).
      $JQ -s '
        [ .[] | select(.type=="sse_frame") ] as $f
        | [ $f | to_entries[] | select(.value.event=="" and (.value.data.run // null) != null) ] as $co
        | if ($co|length) < 1 then 0
          else ($co|last.value.data) as $r
            | [ $f | to_entries[] | select(.value.event=="delta") | .value.data.run ] as $druns
            | if (($r.run // "") != "" and ($r.session_id // "") != "" and ($druns|length) > 0 and ($druns|all(. == $r.run))) then 1 else 0 end
          end
      ' "$path"
      ;;
    session_list_hits)
      # Persistence axis: /rpc session/list sessions[].sid entries matching
      # the close-out frame's session_id (child wrote it under the same
      # SEEK_HOME ladder the rpc plane reads back through).
      $JQ -s '
        [ .[] | select(.type=="sse_frame" and .event=="" and ((.data.session_id // "") != "")) ] as $co
        | if ($co|length) < 1 then 0
          else ($co|last.data.session_id) as $sid
            | [ .[] | select(.type=="session_list") | (.sids // [])[] | select(. == $sid) ] | length
          end
      ' "$path"
      ;;
    *)
      echo "0"
      ;;
  esac
}

# run_one CASE_DIR [ROUND_NO] → executes ONE round of one case: appends
# its JSON row to the results file, prints the per-round PASS/FAIL lines,
# and sets round_pass (1/0). Never touches the gate totals — judgement
# (single-round legacy vs N-round aggregate) belongs to run_case below.
# ROUND_NO is 1..rounds; the row's `round` field and the stdout round tag
# appear only when rounds > 1, keeping the rounds=1 path byte-identical
# to the pre-E1 script.
run_one() {
  local case_dir=$1
  local round_no=${2:-1}
  local case_name
  case_name=$(basename "$case_dir")
  round_pass=0

  local prompt_file="$case_dir/prompt.txt"
  local expect_file="$case_dir/expect.json"

  local max_turns
  max_turns=$($JQ -r '.max_turns // 10' "$expect_file")
  local prompt
  prompt=$(cat "$prompt_file")

  # Optional per-case hooks, run from repo root in the same cwd as the
  # seek invocation: setup.sh prepares fixtures (e.g. copies them into
  # eval/tmp/<case>/ so the model can mutate them), teardown.sh cleans
  # up afterwards. Teardown runs even when the seek invocation failed.
  local setup_file="$case_dir/setup.sh"
  local teardown_file="$case_dir/teardown.sh"
  if [[ -f "$setup_file" ]]; then
    ( cd "$repo_root" && bash "$setup_file" )
  fi

  local out_file
  out_file=$(mktemp -t "seek-eval-${case_name}.XXXXXX")
  # Same jq.exe/MSYS compatibility as the path block up top: mktemp
  # yields /tmp/..., which a Windows-native jq cannot reopen later.
  if command -v cygpath >/dev/null 2>&1; then
    out_file=$(cygpath -m "$out_file")
  fi
  local started_at
  started_at=$(date +%s)

  # Per-case runner hook: when runner.sh exists it REPLACES the default
  # invocation below. Needed for cases whose contract is about the CLI
  # shape itself rather than the model's tool-call stream — e.g. running
  # WITHOUT -yolo (print-mode permission defaults) or chaining multiple
  # seek invocations (fresh run + -resume must reuse the session id).
  # Contract: executed from repo root with SEEK_EVAL_BINARY,
  # SEEK_EVAL_REPO_ROOT, SEEK_EVAL_PROMPT_FILE and SEEK_EVAL_MAX_TURNS in
  # the environment; writes the JSONL event stream to stdout, optionally
  # followed by {"type":"runner_fact","exit_code":N} lines (see the
  # nonzero_exits metric) to surface process-level results. Runners that
  # want the provider lane forward ${SEEK_EVAL_PROVIDER_ARGS-} into their
  # own invocations (exported above; empty when the lane is off) and
  # inherit the lane's exported credential env vars.
  local runner_file="$case_dir/runner.sh"
  if [[ -f "$runner_file" ]]; then
    ( cd "$repo_root" && \
      SEEK_EVAL_BINARY="$binary" \
      SEEK_EVAL_REPO_ROOT="$repo_root" \
      SEEK_EVAL_PROMPT_FILE="$prompt_file" \
      SEEK_EVAL_MAX_TURNS="$max_turns" \
      SEEK_EVAL_EXTRA_ARGS="$seek_extra_args" \
      SEEK_EVAL_PROVIDER_ARGS="$provider_args" \
      bash "$runner_file" ) > "$out_file" 2>/dev/null
  else
  # -no-save: don't pollute ~/.seek/sessions/ with eval runs.
  # -yolo:    these prompts are read-only inspections; --yolo lets
  #           grep/read/list_dir run without inline approval prompts.
  # cwd:     repo root, so the agent has the actual seek codebase to
  #          inspect (most cases reference real files).
  # $seek_extra_args (unquoted by intent — a space-separated flag
  # list, e.g. the ablation lane's "-tools read,edit,write,bash,grep")
  # is empty for every non-ablation invocation. $provider_args likewise
  # (provider-lane flags; "-provider opencode" on the default lane,
  # empty only on the explicit SEEK_EVAL_PROVIDER=deepseek fallback).
  ( cd "$repo_root" && "$binary" -yolo -no-save -max-turns "$max_turns" -json $seek_extra_args $provider_args -p "$prompt" ) \
    > "$out_file" 2>/dev/null
  fi
  local elapsed=$(( $(date +%s) - started_at ))

  if [[ -f "$teardown_file" ]]; then
    ( cd "$repo_root" && bash "$teardown_file" )
  fi

  # agent_end carries cumulative token counts; pull them straight out
  # so we can plot cost-vs-quality trends AND bound them as metrics
  # (completion_tokens is a robust verbosity proxy — load-bearing for
  # the code-review-effort cases).
  local prompt_tokens completion_tokens cache_hit_tokens
  prompt_tokens=$($JQ -s 'map(select(.type=="agent_end")) | .[0].prompt_tokens // 0' "$out_file")
  completion_tokens=$($JQ -s 'map(select(.type=="agent_end")) | .[0].completion_tokens // 0' "$out_file")
  # cache_hit_tokens: MAX across agent_end events, not .[0] — in a
  # runner-chained fresh+resume case the hit lands on the RESUME run's
  # agent_end (the whole prefix is re-sent, so the provider's server
  # cache is warm); .[0] would read the cold fresh run and report 0.
  cache_hit_tokens=$($JQ -s '[.[] | select(.type=="agent_end") | .cache_hit_tokens // 0] | max // 0' "$out_file")

  # Final assistant text (all turns' text_delta joined, reasoning
  # excluded, capped) so offline gold-answer scoring works without
  # keeping the raw stream — e.g. "does the answer contain 45s".
  local final_text
  final_text=$($JQ -rs '
    ( [ .[] | select(.type=="text_delta" and ((.reasoning // false) | not)) | (.delta // "") ] | join("") )
    | .[0:2000]
  ' "$out_file")

  # Build a metrics object: extract one entry per metric in the
  # vocabulary. We compute the same fixed set every case so historical
  # diffs are comparable across cases. completion_tokens is folded in
  # here (not just recorded) so expect.json can bound it.
  #
  # The nine agent/bg keys (E4, c_vollxv) are DATA-GATED: merged only
  # when this round's stream carries an agent surface (an `agent`
  # tool_start or a check-bearing runner_fact). This is the same
  # discipline as the conditional `round` key — the fixed set above is
  # byte-frozen by scripts/eval-ablate-test.sh's no-env equivalence pin
  # (its pre-change blob predates every metric addition since), so an
  # unconditional growth would break that guard for every non-agent
  # case. Readers must treat an absent agent key as 0; cases binding
  # these bounds must guarantee the surface (subagent-bg-resume's
  # runner always emits its fact channel).
  local has_agent_surface
  has_agent_surface=$($JQ -s '
    ( [.[] | select(.type=="tool_start" and .name=="agent")] | length )
    + ( [.[] | select(.type=="runner_fact" and ((.check // "") != ""))] | length )
    > 0' "$out_file")
  local metrics
  metrics=$($JQ -n \
    --argjson unknown_field_errors "$(extract_metric unknown_field_errors "$out_file")" \
    --argjson think_calls          "$(extract_metric think_calls          "$out_file")" \
    --argjson total_tool_calls     "$(extract_metric total_tool_calls     "$out_file")" \
    --argjson read_calls           "$(extract_metric read_calls           "$out_file")" \
    --argjson grep_calls           "$(extract_metric grep_calls           "$out_file")" \
    --argjson mcp_calls            "$(extract_metric mcp_calls            "$out_file")" \
    --argjson list_dir_calls       "$(extract_metric list_dir_calls       "$out_file")" \
    --argjson agent_calls          "$(extract_metric agent_calls          "$out_file")" \
    --argjson agent_bg_calls       "$(extract_metric agent_bg_calls       "$out_file")" \
    --argjson agent_continue_calls "$(extract_metric agent_continue_calls "$out_file")" \
    --argjson bg_started_events       "$(extract_metric bg_started_events       "$out_file")" \
    --argjson bg_completed_events     "$(extract_metric bg_completed_events     "$out_file")" \
    --argjson bg_notified_events      "$(extract_metric bg_notified_events      "$out_file")" \
    --argjson bg_notified_transcript  "$(extract_metric bg_notified_transcript  "$out_file")" \
    --argjson bg_resumed_lineage      "$(extract_metric bg_resumed_lineage      "$out_file")" \
    --argjson bg_artifacts_missing    "$(extract_metric bg_artifacts_missing    "$out_file")" \
    --argjson bash_calls           "$(extract_metric bash_calls           "$out_file")" \
    --argjson bash_chains          "$(extract_metric bash_chains          "$out_file")" \
    --argjson bash_git_calls       "$(extract_metric bash_git_calls       "$out_file")" \
    --argjson bash_before_dedicated "$(extract_metric bash_before_dedicated "$out_file")" \
    --argjson edit_calls           "$(extract_metric edit_calls           "$out_file")" \
    --argjson git_calls            "$(extract_metric git_calls            "$out_file")" \
    --argjson git_subcommand_dupes "$(extract_metric git_subcommand_dupes "$out_file")" \
    --argjson probe_reads          "$(extract_metric probe_reads          "$out_file")" \
    --argjson write_refusals       "$(extract_metric write_refusals       "$out_file")" \
    --argjson write_ok             "$(extract_metric write_ok             "$out_file")" \
    --argjson denied_tool_ends     "$(extract_metric denied_tool_ends     "$out_file")" \
    --argjson agent_ends           "$(extract_metric agent_ends           "$out_file")" \
    --argjson session_id_missing   "$(extract_metric session_id_missing   "$out_file")" \
    --argjson session_id_distinct  "$(extract_metric session_id_distinct  "$out_file")" \
    --argjson nonzero_exits        "$(extract_metric nonzero_exits        "$out_file")" \
    --argjson turns                "$(extract_metric turns                "$out_file")" \
    --argjson review_line_refs     "$(extract_metric review_line_refs     "$out_file")" \
    --argjson completion_tokens    "$completion_tokens" \
    --argjson cache_hit_tokens     "$cache_hit_tokens" \
    --argjson has_agent_surface    "$has_agent_surface" \
    '{unknown_field_errors:$unknown_field_errors, think_calls:$think_calls, total_tool_calls:$total_tool_calls, read_calls:$read_calls, grep_calls:$grep_calls, mcp_calls:$mcp_calls, list_dir_calls:$list_dir_calls, bash_calls:$bash_calls, bash_chains:$bash_chains, bash_git_calls:$bash_git_calls, bash_before_dedicated:$bash_before_dedicated, edit_calls:$edit_calls, git_calls:$git_calls, git_subcommand_dupes:$git_subcommand_dupes, probe_reads:$probe_reads, write_refusals:$write_refusals, write_ok:$write_ok, denied_tool_ends:$denied_tool_ends, agent_ends:$agent_ends, session_id_missing:$session_id_missing, session_id_distinct:$session_id_distinct, nonzero_exits:$nonzero_exits, turns:$turns, review_line_refs:$review_line_refs, completion_tokens:$completion_tokens, cache_hit_tokens:$cache_hit_tokens}
     + (if $has_agent_surface then {agent_calls:$agent_calls, agent_bg_calls:$agent_bg_calls, agent_continue_calls:$agent_continue_calls, bg_started_events:$bg_started_events, bg_completed_events:$bg_completed_events, bg_notified_events:$bg_notified_events, bg_notified_transcript:$bg_notified_transcript, bg_resumed_lineage:$bg_resumed_lineage, bg_artifacts_missing:$bg_artifacts_missing} else {} end)')

  # Serve-plane metrics (E5 serve-web-stream, card c_lbtdqw): merged in
  # ONLY for streams that actually carry sse_frame rows — a
  # content-conditional extension, so every non-serve case's row keeps
  # its exact historical byte shape (the no-env equivalence pinned by
  # scripts/eval-ablate-test.sh) while the serve case's rows gain the
  # six-axis wire metrics. A serve case that crashed before producing
  # any frame skips this block and its expect bounds read "(unknown
  # metric …)" — red, with the failing axis named in the row.
  if grep -q '"type":"sse_frame"' "$out_file" 2>/dev/null; then
    # -cn, not -c: this is a pure filter over $m — without -n jq would
    # consume stdin (empty under run_one) and silently emit nothing,
    # clobbering metrics and turning the failures scan's --argjson m ''
    # into an "invalid JSON text" error instead of a judgement.
    metrics=$($JQ -cn \
      --argjson sse_delta_frames       "$(extract_metric sse_delta_frames       "$out_file")" \
      --argjson sse_usage_frames       "$(extract_metric sse_usage_frames       "$out_file")" \
      --argjson sse_frame_order_ok     "$(extract_metric sse_frame_order_ok     "$out_file")" \
      --argjson sse_delta_accum_ok     "$(extract_metric sse_delta_accum_ok     "$out_file")" \
      --argjson sse_reply_closeout_ok  "$(extract_metric sse_reply_closeout_ok  "$out_file")" \
      --argjson session_list_hits      "$(extract_metric session_list_hits      "$out_file")" \
      --argjson m "$metrics" \
      '$m + {sse_delta_frames:$sse_delta_frames, sse_usage_frames:$sse_usage_frames, sse_frame_order_ok:$sse_frame_order_ok, sse_delta_accum_ok:$sse_delta_accum_ok, sse_reply_closeout_ok:$sse_reply_closeout_ok, session_list_hits:$session_list_hits}')
  fi

  # Check each bound from expect.json against the metric of the same
  # name. failures is an array of "metric: expected ≤/≥/= bound, got
  # actual" lines, empty when everything passed. `min_pass_rate` (E1
  # gate config) is excluded from the scan — it rides a `min_` prefix
  # but is not a metric bound (first smoke run had every round fail
  # with "(unknown metric pass_rate)" before this exclusion).
  # NB `max_turns` IS scanned as a turns ceiling — pre-existing
  # behaviour, left untouched.
  local failures
  failures=$($JQ -r --argjson m "$metrics" '
    [ to_entries[]
      | select(.key | startswith("max_") or startswith("min_") or startswith("exact_"))
      | select(.key != "min_pass_rate")
      | . as $entry
      | ($entry.key | sub("^(max|min|exact)_"; "")) as $metric
      | ($entry.key | capture("^(?<op>max|min|exact)_") | .op) as $op
      | ($m[$metric] // null) as $actual
      | if $actual == null then "(unknown metric \($metric))"
        elif $op=="max"   and $actual <= $entry.value then empty
        elif $op=="min"   and $actual >= $entry.value then empty
        elif $op=="exact" and $actual == $entry.value then empty
        else "\($metric): want \($op)=\($entry.value), got \($actual)"
        end
    ]
  ' "$expect_file")

  local pass="true"
  if [[ "$failures" != "[]" ]]; then
    pass="false"
  fi

  local row
  # -c: jq 1.7+ pretty-prints -n object output by default; results must
  # stay strict single-line JSONL. The `round` key is appended AFTER
  # ran_at and only when rounds > 1 (`obj + {}` is the identity at
  # rounds=1, so the single-round row keeps its exact historical bytes —
  # pinned by scripts/eval-rounds-test.sh). The gate is the GLOBAL rounds
  # count ($emit_round), not the round number — round 1 of a 3-round run
  # must carry `round: 1` too.
  local emit_round=0
  if (( rounds > 1 )); then
    emit_round=1
  fi
  row=$($JQ -cn \
    --arg case "$case_name" \
    --argjson pass "$pass" \
    --argjson metrics "$metrics" \
    --argjson prompt_tokens "$prompt_tokens" \
    --argjson completion_tokens "$completion_tokens" \
    --argjson cache_hit_tokens "$cache_hit_tokens" \
    --argjson elapsed_s "$elapsed" \
    --argjson failures "$failures" \
    --arg version "$version_full" \
    --arg final_text "$final_text" \
    --arg ran_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson round "$round_no" \
    --argjson emit_round "$emit_round" \
    '{case:$case, pass:$pass, metrics:$metrics, prompt_tokens:$prompt_tokens, completion_tokens:$completion_tokens, cache_hit_tokens:$cache_hit_tokens, elapsed_s:$elapsed_s, failures:$failures, binary_version:$version, final_text:$final_text, ran_at:$ran_at}
     + (if $emit_round == 1 then {round:$round} else {} end)')

  echo "$row" >> "$results_file"

  local round_tag=""
  if (( rounds > 1 )); then
    round_tag="  r${round_no}/${rounds}"
  fi
  if [[ "$pass" == "true" ]]; then
    round_pass=1
    echo "PASS  $case_name${round_tag}  (${elapsed}s, $(echo "$metrics" | $JQ -c .))"
  else
    round_pass=0
    echo "FAIL  $case_name${round_tag}  (${elapsed}s)"
    echo "$failures" | $JQ -r '.[]' | sed 's/^/      /'
    echo "      metrics: $(echo "$metrics" | $JQ -c .)"
  fi

  rm -f "$out_file"
}

# read_gate_config EXPECT_FILE → sets record_only (true/false) and
# min_pass_rate (empty when unset, decimal string otherwise). Exits 2 on
# any malformed gate config — never silently coerce: a threshold read as
# garbage and defaulted to 1.0 would gate on the wrong side of every
# judgement (PRD §四: 不许静默当 1.0). An unparseable expect.json dies
# here too, honouring the "exit 2 on unparseable expect.json" contract
# in the header (previously it leaked empty max_turns into seek).
read_gate_config() {
  local expect_file=$1
  record_only=$($JQ -r '
    if .record_only == null then "false"
    elif (.record_only | type) == "boolean" then (.record_only | tostring)
    else "ERR" end' "$expect_file" 2>/dev/null) || record_only="ERR"
  if [[ "$record_only" == "ERR" ]]; then
    echo "eval: $expect_file: record_only must be a boolean (or absent)" >&2
    exit 2
  fi
  min_pass_rate=$($JQ -r '
    if .min_pass_rate == null then ""
    elif (.min_pass_rate | type) != "number" then "ERR"
    else (.min_pass_rate | tostring) end' "$expect_file" 2>/dev/null) || min_pass_rate="ERR"
  if [[ "$min_pass_rate" == "ERR" ]]; then
    echo "eval: $expect_file: min_pass_rate must be a number (or absent)" >&2
    exit 2
  fi
  if [[ -n "$min_pass_rate" ]]; then
    # Range rule: 0 < min_pass_rate <= 1. Zero is rejected as well: an
    # always-green gate is not a gate — that case wants record_only.
    local ok
    ok=$($JQ -n --arg m "$min_pass_rate" '($m|tonumber) > 0 and ($m|tonumber) <= 1')
    if [[ "$ok" != "true" ]]; then
      echo "eval: $expect_file: min_pass_rate must satisfy 0 < m <= 1 (got: $min_pass_rate)" >&2
      exit 2
    fi
  fi
}

# run_case CASE_DIR → owns the judgement for one case (E1): reads its
# gate config, runs `rounds` full case lifecycles via run_one, then
# aggregates — legacy single-round verdict when rounds == 1 (and not
# record_only), pass-rate gate or record-only marking otherwise.
run_case() {
  local case_dir=$1
  local case_name
  case_name=$(basename "$case_dir")

  local prompt_file="$case_dir/prompt.txt"
  local expect_file="$case_dir/expect.json"
  if [[ ! -f "$prompt_file" ]] || [[ ! -f "$expect_file" ]]; then
    echo "eval: skipping $case_name (missing prompt.txt or expect.json)" >&2
    return
  fi

  read_gate_config "$expect_file"

  local k passed_rounds=0
  for (( k=1; k <= rounds; k++ )); do
    run_one "$case_dir" "$k"
    passed_rounds=$(( passed_rounds + round_pass ))
  done

  local rate=""
  if (( rounds > 1 )); then
    rate=$($JQ -n --argjson p "$passed_rounds" --argjson n "$rounds" '$p / $n')
  fi
  local verdict=""

  if [[ "$record_only" == "true" ]]; then
    # Record-only: never red, never green — counted in its own column.
    record_total=$(( record_total + 1 ))
    verdict="record"
    echo "RECORD  $case_name  (${passed_rounds}/${rounds} rounds, record-only — not gated)"
  elif (( rounds == 1 )); then
    # Historical single-sample path: the round IS the verdict. run_one
    # already printed the classic PASS/FAIL line; totals only, so the
    # no-flag output stays byte-identical to the pre-E1 script.
    if (( round_pass )); then
      pass_total=$(( pass_total + 1 ))
    else
      fail_total=$(( fail_total + 1 ))
    fi
  else
    # Gate: pass rate over the round set must reach min_pass_rate
    # (default 1.0), INCLUSIVE — 3/5 rounds against 0.6 passes, since
    # IEEE division and decimal parsing both round to the same double.
    # E2 calibration guidance: pick thresholds that are exactly k/n
    # (0.6, 0.75, 0.8) — a written 0.67 is NOT 2/3 and will gate one
    # round stricter than intended (eval/README.md § Judgment).
    local min_num=1
    if [[ -n "$min_pass_rate" ]]; then
      min_num=$min_pass_rate
    fi
    local gate_ok
    gate_ok=$($JQ -n --argjson p "$passed_rounds" --argjson n "$rounds" --argjson m "$min_num" '$p / $n >= $m')
    if [[ "$gate_ok" == "true" ]]; then
      verdict="pass"
      pass_total=$(( pass_total + 1 ))
      echo "PASS  $case_name  aggregate (${passed_rounds}/${rounds} rounds >= min_pass_rate ${min_num})"
    else
      verdict="fail"
      fail_total=$(( fail_total + 1 ))
      echo "FAIL  $case_name  aggregate (${passed_rounds}/${rounds} rounds < min_pass_rate ${min_num})"
    fi
  fi

  # Aggregate row (rounds > 1 only): the judgement itself, recorded
  # alongside the per-round rows. Deliberately NO `pass` key —
  # scripts/eval-trend.sh selects rows on `pass` being boolean, so an
  # aggregate row can never double-count as a sample.
  if (( rounds > 1 )); then
    local min_json=null
    if [[ -n "$min_pass_rate" ]]; then
      min_json=$min_pass_rate
    fi
    local ro_json=false
    if [[ "$record_only" == "true" ]]; then
      ro_json=true
    fi
    local agg_row
    agg_row=$($JQ -cn \
      --arg case "$case_name" \
      --argjson rounds "$rounds" \
      --argjson passed "$passed_rounds" \
      --argjson rate "$rate" \
      --argjson min "$min_json" \
      --argjson ro "$ro_json" \
      --arg verdict "$verdict" \
      --arg ran_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      '{case:$case, type:"aggregate", rounds:$rounds, passed:$passed, pass_rate:$rate, min_pass_rate:$min, record_only:$ro, verdict:$verdict, ran_at:$ran_at}')
    echo "$agg_row" >> "$results_file"
  fi
}

pass_total=0
fail_total=0
record_total=0

if [[ -n "$case_filter" ]]; then
  case_dir="$cases_dir/$case_filter"
  if [[ ! -d "$case_dir" ]]; then
    echo "eval: no such case: $case_filter" >&2
    exit 2
  fi
  run_case "$case_dir"
elif [[ -n "$lane" ]]; then
  # Lane dispatch: the validated list, in file order (cheapest-first
  # curation reads better in the transcript than an alphabetical glob).
  for lane_name in "${lane_cases[@]}"; do
    run_case "$cases_dir/$lane_name"
  done
else
  shopt -s nullglob
  for case_dir in "$cases_dir"/*/; do
    run_case "${case_dir%/}"
  done
fi

echo
summary_recorded=""
if (( record_total > 0 )); then
  summary_recorded=", $record_total recorded (record-only)"
fi
echo "eval: $pass_total passed, $fail_total failed${summary_recorded}"
exit $(( fail_total > 0 ? 1 : 0 ))
