#!/usr/bin/env bash
#
# review/action_test.sh — prove that the review composite action says what it
# must, and that every one of those assertions can fail.
#
# `review/action.yml` is the ONE home of the review job for every gophersys
# repository: each of the others holds a ~25-line GENERATED caller that carries
# no logic. So a defect here reaches all of them at once, and a check on this
# action that cannot fail protects none of them.
#
# This file replaces the suite that guarded `.github/workflows/pr-review.yml`,
# the reusable workflow that is now RETIRED. That workflow's whole safety rested
# on `github.job_workflow_sha`, which is EMPTY inside a called workflow, so its
# pin could never resolve (ledger #46). A composite action needs no pin guard at
# all: the runner checks this repository out at the caller's `uses:` ref, so the
# source on disk IS the pinned commit. What replaces the pin test is therefore a
# test that nothing here FETCHES — the moment the action reaches for code that is
# not beside it, the ref stops being the pin.
#
# The suite runs in the same 3 phases as review/review_test.sh:
#
#   phase 1   behaviour      — every test must PASS against the real tree.
#   phase 1b  control        — the same tests against an unmutated COPY of the
#                              tree must reach the same verdicts. A test that
#                              answers differently on a copy was decided by the
#                              copy and not by the tree, and phase 2 would then
#                              prove nothing.
#   phase 2   discrimination — for each test, apply each counter-stimulus to a
#                              copy of the tree and require that same test to
#                              FAIL. A test that still passes proves nothing,
#                              and this suite exits non-zero when that happens.
#
# Nothing here runs an action: GitHub runs actions. What this reads is the action
# definition, the scripts it dispatches to, the repository those scripts point
# into, the caller the generator actually emits, and what a real workflow linter
# says about it. `actionlint` is required, never optional — neither yq nor a YAML
# library reports a duplicate mapping key, they silently keep the last one, and a
# duplicated `timeout-minutes` is the defect that produced startup failures with
# NO check attached to the pull request in 3 repositories.
#
# Usage: bash review/action_test.sh [test-name ...]   # no names = every test
set -Eeuo pipefail
IFS=$'\n\t'

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
WORKFLOWS_REL=".github/workflows"
ACTION_REL="review/action.yml"
PREFLIGHT_REL="review/action.sh"
ARCHIVE_REL="review/archive-stream.sh"
# The caller the generator emits, rendered once at start-up and copied into every
# tree. It is NOT committed: it is this suite's view of what `cictl generate`
# produces for a review-enabled contract.
CALLER_REL="review/.generated-caller.yml"
RETIRED_WORKFLOW_REL=".github/workflows/pr-review.yml"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# SUT_ROOT is the tree that the current phase reads. Phase 1b and phase 2
# repoint it at a copy.
SUT_ROOT="$REPO_ROOT"

info() { printf '\033[0;36m[test]\033[0m %s\n' "$*"; }
ok()   { printf '\033[0;32m  ok  \033[0m %s\n' "$*"; }
bad()  { printf '\033[0;31m FAIL \033[0m %s\n' "$*" >&2; }
die()  { printf '\033[0;31m[test]\033[0m %s\n' "$*" >&2; exit 1; }
# fail ends the test it is called from. Each test runs in its own subshell.
fail() { printf '       %s\n' "$*" >&2; exit 1; }

# --------------------------------------------------------------------------
# the tool gate
# --------------------------------------------------------------------------

# require_tools names what is missing and stops. It never skips. A suite that
# passes quietly because its linter is absent is the exact shape of defect that
# this action exists to remove, and it is how a lint job here once shipped green
# while its tool had never been installed. 127 is the code ctl.sh uses for the
# same refusal.
require_tools() {
  local missing=() cmd
  for cmd in "$@"; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    printf '\033[0;31m[test]\033[0m missing required tool(s): %s\n' "${missing[*]}" >&2
    printf '       actionlint is the only linter that reports a duplicate workflow key.\n' >&2
    printf '       Install it pinned: go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n' >&2
    printf '       go is required too: this suite runs the real generator and lints its output.\n' >&2
    exit 127
  fi
}

require_tools actionlint go awk diff find cp mv sort

# --------------------------------------------------------------------------
# the generated caller
# --------------------------------------------------------------------------

# render_caller runs the REAL generator over a review-enabled contract and prints
# the path of the caller it emitted. Phase 1 then asserts against that output, so
# a generator that stops emitting a timeout, an arc pool or a SHA-pinned action
# turns this suite red — which is the point. Phase 2's counter-stimuli mutate the
# rendered COPY, which proves the assertions discriminate; they are not claims
# about what the generator can be made to do.
render_caller() {
  local repo="$WORK/caller-repo"
  mkdir -p "$repo/.ci"
  cat > "$repo/.ci/ci.contract.yaml" <<'YAML'
apiVersion: eden.ci/v1
repo: sample
kind: libraries
runner:
  runsOn: arc-org
  container: false
languages: [go]
tiers:
  pr:      { verbs: [affected-gate-fast], substrate: [], timeoutMinutes: 15 }
  merge:   { verbs: [affected-gate-substrate], substrate: [docker], privileged: true, timeoutMinutes: 30 }
  nightly: { verbs: [gate-all], substrate: [docker], privileged: true, schedule: "0 6 * * *", timeoutMinutes: 90 }
review:
  enabled: true
  ref: "0123456789abcdef0123456789abcdef01234567"
  release: v0.6.0
  tier: module
  runsOn: arc-review
  timeoutMinutes: 30
  streamStore: minio
  streamEndpoint: https://minio.example.invalid
  streamBucket: review-streams
providers: [github]
toolMatrix:
  sources: ["**/go.mod"]
YAML
  (cd "$REPO_ROOT" && GOWORK=off go run ./cmd/cictl generate -C "$repo") >/dev/null \
    || die "the generator refused the suite's own sample contract; the contract or the generator is broken"
  local out="$repo/.github/workflows/pr-review.yml"
  [ -f "$out" ] || die "the generator emitted no pr-review.yml for a review-enabled contract"
  printf '%s\n' "$out"
}

GENERATED_CALLER="$(render_caller)"

# --------------------------------------------------------------------------
# the trees
# --------------------------------------------------------------------------

# new_tree prints the path of a fresh copy of the directories these tests read,
# plus the rendered caller. A test reads a tree, never the repository directly,
# so a counter-stimulus can rename a file without touching the working copy.
new_tree() {
  local root
  root="$(mktemp -d "$WORK/tree.XXXXXX")"
  cp -R "$REPO_ROOT/.github" "$root/.github"
  cp -R "$REPO_ROOT/review" "$root/review"
  cp "$GENERATED_CALLER" "$root/$CALLER_REL"
  printf '%s\n' "$root"
}

# edit_file rewrites a file through an awk program. awk, not sed: BSD sed and
# GNU sed disagree about a newline in a replacement, and this suite must give
# the same answer on a workstation and in the runner image.
# edit_file <path> <awk program> [awk assignments...]
edit_file() {
  local path="$1" program="$2" tmp
  shift 2
  [ -f "$path" ] || die "cannot edit $path: it does not exist"
  tmp="$(mktemp "$WORK/edit.XXXXXX")"
  awk "$@" "$program" "$path" > "$tmp"
  mv "$tmp" "$path"
}

sut() { printf '%s\n' "$SUT_ROOT/$1"; }

# --------------------------------------------------------------------------
# assertions
# --------------------------------------------------------------------------

assert_exists() {
  [ -f "$1" ] || fail "$2 does not exist. $3"
}

# lines_matching prints every line of a file that matches an extended regular
# expression. awk, so that no match is an empty answer and not an exit code —
# `grep` returning 1 on no match would end the test where it stands.
lines_matching() { awk -v re="$2" '$0 ~ re { print }' "$1"; }

# count_matching prints how many lines of a file match.
count_matching() { awk -v re="$2" '$0 ~ re { n++ } END { print n + 0 }' "$1"; }

# tokens_matching prints every distinct substring of a file that matches.
tokens_matching() {
  awk -v re="$2" '{ s = $0; while (match(s, re)) { print substr(s, RSTART, RLENGTH); s = substr(s, RSTART + RLENGTH) } }' "$1" | sort -u
}

# action_sources prints every file of the tree that is EFFECTIVE action code: the
# action definition and the scripts it dispatches to. It is discovered rather than
# listed, so a file added to review/ tomorrow is scanned the day it lands.
#
# Three kinds are excluded, and each exclusion is a statement rather than a
# convenience. A `*_test.sh` is this suite and its sibling — they quote the very
# tokens they forbid, in awk programs, and a scan that read them would report
# every counter-stimulus as the defect it exists to detect. A `*.md` is prose for
# the agent, not code the runner executes. The generated caller is the CALLER's
# file: it checks the repository UNDER REVIEW out, which is correct and is the
# opposite of the action fetching its own source.
action_sources() {
  local f
  while IFS= read -r f; do
    case "${f##*/}" in
      *_test.sh | *.md | .generated-caller.yml) continue ;;
    esac
    printf '%s\n' "$f"
  done < <(find "$SUT_ROOT/review" -type f | sort)
}

# action_source_matching prints "<relative path>:<line>" for every EFFECTIVE line
# of the action that matches, so a test can say "nowhere in the action does this
# token do anything". Comment lines are stripped: this file argues at length about
# `git fetch` and `actions/upload-artifact` in order to explain why neither is
# here, and a scan that could not tell an argument from an instruction would make
# the explanation illegal.
action_source_matching() {
  local re="$1" f line body
  while IFS= read -r f; do
    while IFS= read -r line; do
      body="${line#"${line%%[![:space:]]*}"}"
      case "$body" in '#'*) continue ;; esac
      printf '%s:%s\n' "${f#"$SUT_ROOT"/}" "$line"
    done < <(lines_matching "$f" "$re")
  done < <(action_sources)
}

# guard_line prints the single line carrying a `# guard:<name>` marker.
guard_line() { lines_matching "$1" "# guard:$2\$"; }

# ends_the_run reports whether a line of shell can stop the script where it
# stands: it dies, or it exits non-zero.
ends_the_run() { printf '%s\n' "$1" | grep -qE '(die|exit[[:space:]]+[1-9])'; }

# helper_body prints the body of a refusal helper defined in the file, whether it
# is written on one line or many. It is a real resolution and not a widened
# pattern: a guard that delegates is only a guard if the thing it delegates to
# can end the run, and nothing but reading that function can say so.
helper_body() {
  awk -v h="$2" '
    index($0, h "() {") == 1 { inside = 1 }
    inside { print }
    inside && (/^\}/ || /; *\}[ \t]*$/) { exit }
  ' "$1"
}

# assert_guard requires a marked guard that exists and can end the run. A guard
# that never fails is a comment, and deleting a guard and defanging one are the
# same defect — this is what tells them apart.
assert_guard() {
  local file="$1" name="$2" why="$3" line helper body
  line="$(guard_line "$file" "$name")"
  [ -n "$line" ] || fail "no line of ${file#"$SUT_ROOT"/} ends with '# guard:$name'. $why"
  ends_the_run "$line" && return 0

  # A guard may delegate to a refusal helper — `require_env FOO "why"` reads
  # better than the test it expands to. Resolve it rather than trusting the name.
  helper="$(printf '%s\n' "$line" | awk '{ for (i = 1; i <= NF; i++) if ($i ~ /^(require|refuse)_[a-z_]+$/) { print $i; exit } }')"
  [ -n "$helper" ] || fail "the '$name' guard never fails the run, so the input it checks is accepted anyway: $line"
  body="$(helper_body "$file" "$helper")"
  [ -n "$body" ] || fail "the '$name' guard delegates to '$helper', which ${file#"$SUT_ROOT"/} does not define, so nothing checks the input at all: $line"
  ends_the_run "$body" \
    || fail "the '$name' guard delegates to '$helper', and '$helper' never fails the run, so the input it checks is accepted anyway: $line"
}

# job_timeouts prints "<job> <count> <value>" for every job in a workflow, where
# count is how many times that job declares timeout-minutes. Under `jobs:` a key
# at 2 spaces is a job name and nothing else is, so the nesting is readable
# without a YAML parser.
job_timeouts() {
  awk '
    /^jobs:[ \t]*$/ { in_jobs = 1; next }
    in_jobs && /^[^ \t#]/ { in_jobs = 0 }
    in_jobs && /^  [A-Za-z0-9_-]+:[ \t]*$/ {
      job = $0; sub(/^[ \t]*/, "", job); sub(/:.*$/, "", job)
      n++; order[n] = job; count[job] = count[job] + 0; value[job] = ""
      next
    }
    in_jobs && job != "" && /^[ \t]*timeout-minutes:/ {
      count[job]++
      v = $0; sub(/^[ \t]*timeout-minutes:[ \t]*/, "", v); sub(/[ \t]*(#.*)?$/, "", v)
      value[job] = v
    }
    END { for (i = 1; i <= n; i++) printf "%s %d %s\n", order[i], count[order[i]], value[order[i]] }
  ' "$1"
}

# --------------------------------------------------------------------------
# the tests
# --------------------------------------------------------------------------

# THE REF IS THE PIN, AND IT IS THE PIN ONLY WHILE NOTHING IS FETCHED.
#
# The runner checks this repository out at the caller's `uses:` ref, into
# $GITHUB_ACTION_PATH, before the first step runs. So the reviewer beside the
# action IS the commit the caller asked for — there is nothing to fetch and
# nothing to verify. The moment the action clones, curls or checks out the
# reviewer instead, the ref stops being the pin and the old defect is back: the
# reusable workflow fetched at `github.job_workflow_sha`, that context is EMPTY
# in a called workflow, and every run failed on its own pin guard.
t_the_reviewer_runs_at_the_pinned_commit() {
  local action preflight; action="$(sut "$ACTION_REL")"; preflight="$(sut "$PREFLIGHT_REL")"
  assert_exists "$action" "$ACTION_REL" "It is the ONE home of the review job for every gophersys repository."
  assert_exists "$preflight" "$PREFLIGHT_REL" "The action dispatches to it, and it holds the input guards."

  if [ "$(count_matching "$action" '^[ \t]*using:[ \t]*[\"'\'']?composite')" -eq 0 ]; then
    fail "action.yml does not declare 'using: composite'. Only a composite action is checked out at the caller's uses: ref, which is the whole pin"
  fi

  # Every `run:` must address the action's own directory. A run that names a
  # workspace-relative path executes the CALLER's copy of whatever is at that
  # path, not the pinned commit.
  local runs
  runs="$(lines_matching "$action" '^[ \t]*run:')"
  [ -n "$runs" ] || fail "action.yml has no run: step, so it dispatches to nothing"
  local line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    printf '%s\n' "$line" | grep -q 'GITHUB_ACTION_PATH' \
      || fail "a run: step does not address \$GITHUB_ACTION_PATH, so it would execute the caller's copy of that path and not this action's pinned commit: $line"
  done <<< "$runs"

  # Nothing under review/ may fetch the reviewer. This is the assertion that
  # replaces the old pin guard.
  local fetches
  fetches="$(action_source_matching '(git[[:space:]]+(clone|fetch|checkout)|actions/checkout@)')"
  [ -z "$fetches" ] || fail "the action fetches its own source, which makes the caller's uses: ref stop being the pin:
$fetches"

  # The context that could never work must not come back.
  local resurrected
  resurrected="$(action_source_matching 'job_workflow_sha')"
  [ -z "$resurrected" ] || fail "'job_workflow_sha' is back in the action. It is EMPTY inside a called workflow, and a composite action needs no such context at all:
$resurrected"

  assert_guard "$preflight" source "Running from \$GITHUB_ACTION_PATH is what makes the pinned source the source that runs; an absent reviewer means the checkout is not the pinned commit"
}

# The action runs paths inside this repository. A path that does not exist here
# is a job that fails in every repository at once, and the rename that breaks it
# happens in THIS repository, where no caller is even open.
t_the_action_points_at_something_that_exists() {
  local action; action="$(sut "$ACTION_REL")"
  assert_exists "$action" "$ACTION_REL" "It is the ONE home of the review job."

  local paths path scripts=0
  paths="$(tokens_matching "$action" 'GITHUB_ACTION_PATH\}?/[A-Za-z0-9_.-]+')"
  [ -n "$paths" ] || fail "action.yml names no path under its own directory, so nothing in it connects to the reviewer it is supposed to run"
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    path="review/${path##*/}"
    if [ ! -e "$SUT_ROOT/$path" ]; then
      fail "action.yml runs '$path', which does not exist in this repository"
    fi
    case "$path" in *.sh) scripts=$((scripts + 1)) ;; esac
  done <<< "$paths"
  [ "$scripts" -gt 0 ] || fail "action.yml names no script under review/, so the action cannot be running anything at all"

  # The preflight execs the reviewer beside itself; the reviewer reads its
  # instructions beside itself. The action names neither, so only this test can
  # see that they are here.
  local f
  for f in review/review.sh review/CLAUDE.md; do
    [ -f "$SUT_ROOT/$f" ] || fail "$f is missing, so the reviewer this action starts would have no ${f##*/} to run with"
  done
}

# The Claude credential is an environment variable on the arc-review pool, and
# deliberately so: that pool runs this action and nothing else. A `secrets.`
# reference or a secret-shaped input would move the credential into repository
# scope and give it a second home, in EVERY repository that reviews.
t_the_credential_stays_on_the_pool() {
  local action; action="$(sut "$ACTION_REL")"
  assert_exists "$action" "$ACTION_REL" "It is the ONE home of the review job."

  local names name
  names="$(tokens_matching "$action" 'secrets[.][A-Za-z0-9_]+')"
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    fail "action.yml reads $name. A composite action cannot even read repository secrets unless a caller passes them in, and the Claude credential lives on the arc-review pool; a secrets reference gives it a second home in every calling repository"
  done <<< "$names"

  # The other way in: an input that a caller has to fill with a credential.
  local inputs
  inputs="$(awk '
    /^inputs:[ \t]*$/ { in_inputs = 1; next }
    in_inputs && /^[^ \t#]/ { in_inputs = 0 }
    in_inputs && /^  [A-Za-z0-9_-]+:[ \t]*$/ {
      k = $0; sub(/^[ \t]*/, "", k); sub(/:.*$/, "", k); print k
    }' "$action")"
  [ -n "$inputs" ] || fail "action.yml declares no inputs at all, so no caller can even name a pull request"
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    case "$name" in
      *token* | *secret* | *password* | *credential*)
        fail "action.yml declares the input '$name'. An action that takes a credential makes every caller store one" ;;
    esac
  done <<< "$inputs"
}

# EVERY SPEND INPUT IS CHECKED BEFORE THE FIRST DOLLAR. review.sh spawns a paid
# agent, so an unknown model or a malformed budget must cost nothing. It must
# also never fall back to a default: a default chosen inside the action picks
# somebody's spend for them, silently.
t_every_spend_input_is_guarded_before_a_dollar_is_spent() {
  local action preflight; action="$(sut "$ACTION_REL")"; preflight="$(sut "$PREFLIGHT_REL")"
  assert_exists "$action" "$ACTION_REL" "It is the ONE home of the review job."
  assert_exists "$preflight" "$PREFLIGHT_REL" "It holds the input guards."

  # The tiered-spend inputs the contract's presets drive. Without all four, a
  # caller cannot express a tier at all and every repository reviews at whatever
  # review.sh happens to default to.
  local key
  for key in pr harness model budget-usd max-rounds; do
    if [ "$(count_matching "$action" "^  ${key}:[ \t]*\$")" -eq 0 ]; then
      fail "action.yml declares no '$key' input, so no caller can set it and the tier presets cannot reach the reviewer"
    fi
  done

  local guard
  for guard in pr harness model codex-model budget rounds; do
    assert_guard "$preflight" "$guard" "An unchecked input reaches a paid agent"
  done

  # Order is the load-bearing part: a guard that runs after the reviewer starts
  # has already let the money go.
  local exec_line
  exec_line="$(awk '/exec[[:space:]]+bash/ { print NR; exit }' "$preflight")"
  [ -n "$exec_line" ] || fail "action.sh never execs the reviewer, so the action dispatches to nothing"
  local last_guard
  last_guard="$(awk '/# guard:(pr|harness|model|codex-model|budget|rounds)$/ { n = NR } END { print n + 0 }' "$preflight")"
  if [ "$last_guard" -ge "$exec_line" ]; then
    fail "a spend guard sits at line $last_guard, at or below the exec of the reviewer at line $exec_line, so it runs after the money is already committed"
  fi
}

# THE STREAM DOES NOT GO TO GITHUB ARTIFACT STORAGE. That storage is one quota
# for the whole organization, and when it filled it failed the keep-the-run step
# on runs whose Review step had SUCCEEDED — 5 times across one weekend, once on
# a review that had APPROVED. A shared quota must never decide one repository's
# review verdict.
t_the_stream_never_touches_github_artifact_storage() {
  local action archive; action="$(sut "$ACTION_REL")"; archive="$(sut "$ARCHIVE_REL")"
  assert_exists "$action" "$ACTION_REL" "It is the ONE home of the review job."
  assert_exists "$archive" "$ARCHIVE_REL" "It is where the reviewer's event stream is archived."

  local artifacts
  artifacts="$(action_source_matching 'actions/(upload|download)-artifact')"
  [ -z "$artifacts" ] || fail "the review stream is back on GitHub artifact storage, whose org-wide quota already failed 5 review checks over one weekend:
$artifacts"

  # A review that FAILED is exactly the one whose stream you need.
  if [ "$(count_matching "$action" '^[ \t]*if:[ \t]*always\(\)')" -eq 0 ]; then
    fail "no step of action.yml runs under 'if: always()', so the stream of a failed review — the only one worth reading — is never archived"
  fi

  # A missing credential must be loud. A skip here is a green run that archived
  # nothing while reporting that it did.
  local guard
  for guard in credential upload; do
    assert_guard "$archive" "$guard" "A lost record means diagnosing a wrong review costs another paid run"
  done
}

# THE RETIRED MECHANISM MUST NOT COME BACK. Two mechanisms is the state this
# change exists to end: `.github/workflows/pr-review.yml` was a reusable workflow
# whose pin could never resolve, and leaving it in place would let a repository
# adopt the broken one by accident. A comment cannot stop that; this test can.
t_the_reusable_workflow_is_retired() {
  if [ -e "$SUT_ROOT/$RETIRED_WORKFLOW_REL" ]; then
    fail "$RETIRED_WORKFLOW_REL is back. It is the reusable workflow the composite action replaces: its pin read github.job_workflow_sha, which is EMPTY inside a called workflow, so every run failed its own pin guard. Two mechanisms is what this change removed"
  fi

  # A reusable workflow of any name is the same defect wearing a different file.
  local dir="$SUT_ROOT/$WORKFLOWS_REL"
  [ -d "$dir" ] || return 0
  local f callable=()
  while IFS= read -r f; do
    if [ "$(count_matching "$f" '^[ \t]*workflow_call:')" -ne 0 ]; then callable+=("${f#"$SUT_ROOT"/}"); fi
  done < <(find "$dir" -type f \( -name '*.yml' -o -name '*.yaml' \) | sort)
  if [ "${#callable[@]}" -ne 0 ]; then
    fail "these workflows declare 'workflow_call', so another repository can still call this repository's review as a workflow: ${callable[*]}. The composite action is the one mechanism"
  fi
}

# THE GENERATED CALLER IS WHAT ACTUALLY SHIPS. Every repository's pr-review.yml
# is rendered by `cictl generate` from its contract, so a generator that emits a
# caller with no timeout, a hosted runner or an unpinned action ships that defect
# to every repository at once. Phase 1 asserts against the REAL generator output;
# phase 2 mutates the rendered copy, which proves these assertions discriminate.
t_the_generated_caller_is_a_correct_workflow() {
  local caller; caller="$(sut "$CALLER_REL")"
  assert_exists "$caller" "$CALLER_REL" "It is the output of the real generator, rendered by this suite at start-up."

  # A generated file must say so. A hand edit of one is what `cictl drift`
  # catches, and it can only catch it if the reader knows not to edit.
  if [ "$(count_matching "$caller" 'GENERATED by cictl')" -eq 0 ]; then
    fail "the generated caller carries no generated-file banner, so a reader has no reason not to hand-edit it"
  fi

  # A job with no limit inherits GitHub's default of 360 minutes. arc-review runs
  # minRunners 0 and maxRunners 2, so one hung review holds half the pool for 6
  # hours while every other repository waits. actionlint is silent about an
  # ABSENT key, so this is the only check that would see it.
  local line job count value jobs=0
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    jobs=$((jobs + 1))
    job="${line%% *}"; value="${line#* }"; count="${value%% *}"; value="${value#* }"
    [ "$count" -ne 0 ] || fail "the generated '$job' job declares no timeout-minutes, so it inherits GitHub's default of 360 minutes and one hung review holds an arc-review slot for 6 hours"
    [ "$count" -eq 1 ] || fail "the generated '$job' job declares timeout-minutes $count times. That is invalid workflow YAML: GitHub answers with a startup failure that attaches NO check to the pull request, so the job reads as absent rather than broken"
    printf '%s\n' "$value" | grep -qE '^[1-9][0-9]*$' \
      || fail "the generated '$job' job sets timeout-minutes to '$value', which is not a positive whole number of minutes"
  done < <(job_timeouts "$caller")
  [ "$jobs" -ne 0 ] || fail "the generated caller declares no jobs at all, so there is nothing in it to run or to limit"

  # Every runner must be a self-hosted arc-* pool. The account had 195 of 2000
  # hosted minutes left against a $0 budget, and at 0 a hosted job does not fail
  # — it never starts.
  local runners runner
  runners="$(tokens_matching "$caller" 'runs-on:[ ]*[A-Za-z0-9_.-]+')"
  [ -n "$runners" ] || fail "the generated caller declares no runs-on at all"
  while IFS= read -r runner; do
    [ -n "$runner" ] || continue
    runner="${runner##*[ ]}"
    case "$runner" in arc-*) : ;; *) fail "the generated caller runs on '$runner', which is not an arc-* pool. A hosted runner against a \$0 budget does not fail — it never starts" ;; esac
  done <<< "$runners"

  # Every `uses:` must be pinned to a 40-hex commit, this repository's own action
  # included. A tag is a pointer its owner can move; cictl moved 23 commits in
  # one evening, and no review from that period can be reproduced from a tag.
  local uses u ref
  uses="$(tokens_matching "$caller" 'uses:[ ]*[A-Za-z0-9_./-]+@[A-Za-z0-9_.-]+')"
  [ -n "$uses" ] || fail "the generated caller uses no action at all, so it runs no reviewer"
  while IFS= read -r u; do
    [ -n "$u" ] || continue
    ref="${u##*@}"
    printf '%s\n' "$ref" | grep -qE '^[0-9a-f]{40}$' \
      || fail "the generated caller pins '${u#uses:}' to '$ref', which is not a 40-hex commit. A tag can be moved after the fact; a commit cannot"
  done <<< "$uses"

  # A generated caller that carried a secret would put the Claude credential in
  # every repository.
  local secrets
  secrets="$(tokens_matching "$caller" 'secrets[.][A-Za-z0-9_]+')"
  [ -z "$secrets" ] || fail "the generated caller reads $(printf '%s' "$secrets" | tr '\n' ' '). No caller passes a credential: the pool supplies it"
}

# The defect this is here for is not theoretical. A job that declared
# `timeout-minutes` twice made the file invalid workflow YAML; GitHub answered
# with a startup failure that attached NO check to the pull request, so a dead
# workflow read as an absent one for days in 3 repositories. Neither yq nor a
# YAML library reports it — both keep the last key silently — so only a real
# workflow linter can catch it.
t_no_workflow_has_a_duplicate_key() {
  local dir="$SUT_ROOT/$WORKFLOWS_REL"
  [ -d "$dir" ] || fail "$WORKFLOWS_REL does not exist, so there is nothing to lint"

  local files=() f
  while IFS= read -r f; do files+=("$f"); done < <(cd "$SUT_ROOT" && find "$WORKFLOWS_REL" -type f \( -name '*.yml' -o -name '*.yaml' \) | sort)
  # The generated caller is linted as a workflow too: it IS one, in every
  # consuming repository, and this is the only place a linter ever sees it.
  files+=("$CALLER_REL")
  if [ "${#files[@]}" -eq 0 ]; then
    fail "no workflow files found under $WORKFLOWS_REL; the discovery is broken, not the tree"
  fi

  local out rc
  set +e
  out="$(cd "$SUT_ROOT" && actionlint -oneline -no-color "${files[@]}" 2>&1)"
  rc=$?
  set -e
  # arc-* pools are self-hosted and in no table of GitHub-hosted labels, so
  # actionlint reports the label it cannot know. That one message is filtered by
  # name rather than by a config file, so any OTHER finding is still an error.
  out="$(printf '%s\n' "$out" | awk '!/label "arc-[a-z]+" is unknown/ && NF')"
  if [ "$rc" -ne 0 ] && [ -z "$out" ]; then rc=0; fi
  if [ "$rc" -ne 0 ] || [ -n "$out" ]; then
    fail "actionlint rejected ${#files[@]} workflow file(s) (exit $rc):
$out"
  fi
}

TESTS=(
  t_the_reviewer_runs_at_the_pinned_commit
  t_the_action_points_at_something_that_exists
  t_the_credential_stays_on_the_pool
  t_every_spend_input_is_guarded_before_a_dollar_is_spent
  t_the_stream_never_touches_github_artifact_storage
  t_the_reusable_workflow_is_retired
  t_the_generated_caller_is_a_correct_workflow
  t_no_workflow_has_a_duplicate_key
)

# MARKER_COVERAGE maps every guard marker in the action's scripts to the test
# that proves it. It is written by hand on purpose, the same way review_test.sh
# does it: a guard whose marker no test claims is a guard nobody has checked, and
# that is how a guard once shipped here with a marker, no test, and a suite
# reporting that every guard held.
MARKER_COVERAGE="
guard:source            t_the_reviewer_runs_at_the_pinned_commit
guard:pr                t_every_spend_input_is_guarded_before_a_dollar_is_spent
guard:harness           t_every_spend_input_is_guarded_before_a_dollar_is_spent
guard:model             t_every_spend_input_is_guarded_before_a_dollar_is_spent
guard:codex-model       t_every_spend_input_is_guarded_before_a_dollar_is_spent
guard:budget            t_every_spend_input_is_guarded_before_a_dollar_is_spent
guard:rounds            t_every_spend_input_is_guarded_before_a_dollar_is_spent
guard:stream-store      t_every_spend_input_is_guarded_before_a_dollar_is_spent
guard:store             t_the_stream_never_touches_github_artifact_storage
guard:tools             t_the_stream_never_touches_github_artifact_storage
guard:endpoint          t_the_stream_never_touches_github_artifact_storage
guard:bucket            t_the_stream_never_touches_github_artifact_storage
guard:key               t_the_stream_never_touches_github_artifact_storage
guard:credential        t_the_stream_never_touches_github_artifact_storage
guard:credential-secret t_the_stream_never_touches_github_artifact_storage
guard:stream            t_the_stream_never_touches_github_artifact_storage
guard:upload            t_the_stream_never_touches_github_artifact_storage
"

every_marker_is_covered() {
  local marker name test uncovered=() missing=() f
  for f in "$PREFLIGHT_REL" "$ARCHIVE_REL"; do
    [ -f "$REPO_ROOT/$f" ] || continue
    while IFS= read -r marker; do
      name="${marker##*# }"
      test="$(awk -v m="$name" '$1 == m { print $2 }' <<< "$MARKER_COVERAGE")"
      if [ -z "$test" ]; then
        uncovered+=("$f:$name")
      elif ! grep -qxF -- "$test" <<< "${TESTS[*]}"; then
        missing+=("$name -> $test")
      fi
    done < <(tokens_matching "$REPO_ROOT/$f" '# guard:[a-z-]+')
  done
  [ "${#uncovered[@]}" -eq 0 ] || die "these markers have no test: ${uncovered[*]}"
  [ "${#missing[@]}" -eq 0 ] || die "these markers name a test that does not exist: ${missing[*]}"
}

# --------------------------------------------------------------------------
# the counter-stimuli
# --------------------------------------------------------------------------

# stimuli_for prints every counter-stimulus that must make the named test fail.
# A test with no entry is a hard error, so nobody can add a test without stating
# what would make it red.
stimuli_for() {
  case "$1" in
    t_the_reviewer_runs_at_the_pinned_commit)
      printf '%s\n' fetch-the-reviewer run-from-the-workspace no-source-guard defanged-source-guard resurrect-job-workflow-sha drop-composite ;;
    t_the_action_points_at_something_that_exists)
      printf '%s\n' rename-the-reviewer rename-the-instructions ;;
    t_the_credential_stays_on_the_pool)
      printf '%s\n' oauth-secret-in-the-action token-input-on-the-action ;;
    t_every_spend_input_is_guarded_before_a_dollar_is_spent)
      printf '%s\n' no-budget-guard defanged-model-guard drop-the-max-rounds-input guard-after-the-spend ;;
    t_the_stream_never_touches_github_artifact_storage)
      printf '%s\n' back-to-upload-artifact archive-only-on-success no-credential-guard defanged-upload-guard ;;
    t_the_reusable_workflow_is_retired)
      printf '%s\n' resurrect-the-reusable-workflow a-second-reusable-workflow ;;
    t_the_generated_caller_is_a_correct_workflow)
      printf '%s\n' caller-without-a-time-limit caller-on-a-hosted-runner caller-pinned-to-a-tag caller-carrying-a-secret caller-without-the-banner ;;
    t_no_workflow_has_a_duplicate_key)
      printf '%s\n' duplicate-timeout-key ;;
    *) die "no counter-stimulus is declared for $1; every test must name what would make it fail" ;;
  esac
}

# shellcheck disable=SC2016 # the awk text is data, not shell to expand
apply_stimulus() {
  local stimulus="$1" root="$2"
  local action="$2/$ACTION_REL" preflight="$2/$PREFLIGHT_REL"
  local archive="$2/$ARCHIVE_REL" caller="$2/$CALLER_REL"
  case "$stimulus" in
    # The action reaches for its own source instead of using the one beside it.
    # That is the reusable workflow's defect, rebuilt inside an action.
    fetch-the-reviewer)
      edit_file "$preflight" '{ print } /^HERE=/ && !done { print "git clone --depth 1 https://github.com/gophersys/cictl /tmp/cictl"; done = 1 }' ;;
    # The step runs the CALLER's copy of the path, not the pinned commit.
    run-from-the-workspace)
      edit_file "$action" '{ gsub(/\$\{GITHUB_ACTION_PATH\}/, "."); print }' ;;
    no-source-guard)
      edit_file "$preflight" '!/# guard:source$/ { print }' ;;
    # The guard stays, on 1 line, ending in its marker — and cannot fail.
    # Deleting a guard and neutering one are the same defect, and only this
    # stimulus tells them apart.
    defanged-source-guard)
      edit_file "$preflight" '{ if ($0 ~ /# guard:source$/) { print "true # guard:source" } else { print } }' ;;
    resurrect-job-workflow-sha)
      edit_file "$action" '{ print } /^[ \t]*GH_TOKEN:/ && !done { match($0, /^[ ]*/); printf "%sREVIEWER_SHA: ${{ github.job_workflow_sha }}\n", substr($0, 1, RLENGTH); done = 1 }' ;;
    drop-composite)
      edit_file "$action" '{ if ($0 ~ /^[ \t]*using:[ \t]*composite/) { print "  using: node20" } else { print } }' ;;
    rename-the-reviewer)
      [ -f "$root/review/review.sh" ] || die "the counter-stimulus 'rename-the-reviewer' needs review/review.sh"
      mv "$root/review/review.sh" "$root/review/run.sh" ;;
    rename-the-instructions)
      [ -f "$root/review/CLAUDE.md" ] || die "the counter-stimulus 'rename-the-instructions' needs review/CLAUDE.md"
      mv "$root/review/CLAUDE.md" "$root/review/INSTRUCTIONS.md" ;;
    # The credential gets a second home, in repository scope.
    oauth-secret-in-the-action)
      edit_file "$action" '
        { print }
        !inserted && /github\.token/ {
          match($0, /^[ ]*/)
          printf "%sCLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}\n", substr($0, 1, RLENGTH)
          inserted = 1
        }' ;;
    # The same second home, arrived at from the caller side.
    token-input-on-the-action)
      edit_file "$action" '
        { print }
        !inserted && /^inputs:/ {
          printf "  claude-token:\n    description: The Claude credential.\n    required: true\n"
          inserted = 1
        }' ;;
    no-budget-guard)
      edit_file "$preflight" '!/# guard:budget$/ { print }' ;;
    defanged-model-guard)
      edit_file "$preflight" '{ if ($0 ~ /# guard:model$/) { print "  *) : ;; # guard:model" } else { print } }' ;;
    drop-the-max-rounds-input)
      edit_file "$action" '{ if ($0 ~ /^  max-rounds:[ \t]*$/) { next } print }' ;;
    # The guards survive, in full, and run after the reviewer has been started.
    guard-after-the-spend)
      edit_file "$preflight" '
        /# guard:(pr|model|budget|rounds)$/ { held[++n] = $0; next }
        /^exec[[:space:]]+bash/ { print; for (i = 1; i <= n; i++) print held[i]; done = 1; next }
        { print }
        END { if (!done) for (i = 1; i <= n; i++) print held[i] }' ;;
    # Back onto the org-wide quota that failed 5 review checks in a weekend.
    back-to-upload-artifact)
      edit_file "$action" '
        { print }
        !inserted && /if: always\(\)/ {
          match($0, /^[ ]*/)
          printf "%suses: actions/upload-artifact@v4\n", substr($0, 1, RLENGTH)
          inserted = 1
        }' ;;
    # The stream of a FAILED review — the only one worth reading — is lost.
    archive-only-on-success)
      edit_file "$action" '{ if ($0 ~ /^[ \t]*if:[ \t]*always\(\)/) { next } print }' ;;
    no-credential-guard)
      edit_file "$archive" '!/# guard:credential$/ { print }' ;;
    defanged-upload-guard)
      edit_file "$archive" '{ if ($0 ~ /# guard:upload$/) { sub(/\|\|.*# guard:upload$/, "|| true # guard:upload") } print }' ;;
    # The retired mechanism comes back, and a repository can adopt the broken one.
    resurrect-the-reusable-workflow)
      mkdir -p "$root/$WORKFLOWS_REL"
      printf 'name: pr-review\non:\n  workflow_call:\njobs:\n  review:\n    runs-on: arc-review\n    timeout-minutes: 30\n    steps:\n      - run: echo hi\n' \
        > "$root/$RETIRED_WORKFLOW_REL" ;;
    # The same defect wearing a different file name.
    a-second-reusable-workflow)
      mkdir -p "$root/$WORKFLOWS_REL"
      printf 'name: shared-review\non:\n  workflow_call:\njobs:\n  review:\n    runs-on: arc-review\n    timeout-minutes: 30\n    steps:\n      - run: echo hi\n' \
        > "$root/$WORKFLOWS_REL/shared-review.yml" ;;
    caller-without-a-time-limit)
      edit_file "$caller" '!/^[ \t]*timeout-minutes:/ { print }' ;;
    caller-on-a-hosted-runner)
      edit_file "$caller" '{ if ($0 ~ /^[ \t]*runs-on:/) { sub(/:[ \t]*.*$/, ": ubuntu-latest") } print }' ;;
    caller-pinned-to-a-tag)
      edit_file "$caller" '{ if ($0 ~ /uses: gophersys\/cictl\/review@/) { sub(/@[0-9a-f]+/, "@main") } print }' ;;
    caller-carrying-a-secret)
      edit_file "$caller" '
        { print }
        !inserted && /^[ \t]*with:/ {
          match($0, /^[ ]*/)
          printf "%s  claude-token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}\n", substr($0, 1, RLENGTH)
          inserted = 1
        }' ;;
    caller-without-the-banner)
      edit_file "$caller" '!/GENERATED by cictl/ { print }' ;;
    # The real defect: 1 key declared twice, which no YAML reader reports.
    duplicate-timeout-key)
      local f target=""
      while IFS= read -r f; do
        if [ "$(count_matching "$f" '^[ \t]*timeout-minutes:')" -ne 0 ]; then target="$f"; break; fi
      done < <(find "$root/$WORKFLOWS_REL" "$root/review" -type f \( -name '*.yml' -o -name '*.yaml' \) | sort)
      [ -n "$target" ] || die "no workflow declares timeout-minutes, so 'duplicate-timeout-key' has nothing to duplicate"
      edit_file "$target" '{ print } !duplicated && /^[ \t]*timeout-minutes:/ { print; duplicated = 1 }' ;;
    *) die "unknown counter-stimulus: $stimulus" ;;
  esac
}

# make_mutant prints the path of a copy of the tree with 1 counter-stimulus
# applied. It refuses to hand back a copy that is unchanged: a stimulus that
# changed nothing would make the discrimination phase report a pass that means
# nothing.
make_mutant() {
  local stimulus="$1" root drc
  root="$(new_tree)"
  apply_stimulus "$stimulus" "$root"
  set +e
  diff -r "$PRISTINE" "$root" > "$WORK/stimulus.diff" 2>&1
  drc=$?
  set -e
  case "$drc" in
    0) die "the counter-stimulus '$stimulus' changed nothing, so it cannot prove that anything fails" ;;
    1) ;; # the tree differs, which is what a counter-stimulus is for
    *) die "diff failed (exit $drc) while checking '$stimulus': $(cat "$WORK/stimulus.diff")" ;;
  esac
  printf '%s\n' "$root"
}

# --------------------------------------------------------------------------
# the driver
# --------------------------------------------------------------------------

SELECTED=()
if [ "$#" -gt 0 ]; then
  for a in "$@"; do
    grep -qxF -- "$a" <<< "${TESTS[*]}" || die "no such test: $a"
    SELECTED+=("$a")
  done
else
  SELECTED=("${TESTS[@]}")
fi

PRISTINE="$(new_tree)"
failures=0
verdicts=()
t=""
s=""

info "phase 1 — behaviour: ${#SELECTED[@]} test(s) against the real tree"
for t in "${SELECTED[@]}"; do
  if ( set -Eeuo pipefail; SUT_ROOT="$PRISTINE"; "$t" ); then
    ok "$t"
    verdicts+=(pass)
  else
    bad "$t"
    verdicts+=(fail)
    failures=$((failures + 1))
  fi
done

every_marker_is_covered

info "phase 1b — control: the same test(s) against an unmutated copy of the tree"
i=0
for t in "${SELECTED[@]}"; do
  SUT_ROOT="$PRISTINE"
  if ( set -Eeuo pipefail; "$t" ); then control=pass; else control=fail; fi
  SUT_ROOT="$REPO_ROOT"
  if [ "$control" = "${verdicts[$i]}" ]; then
    ok "$t answers the same on a copy ($control)"
  else
    bad "$t answers '$control' on a copy and '${verdicts[$i]}' on the tree; the copy decided the result, so phase 2 would prove nothing"
    failures=$((failures + 1))
  fi
  i=$((i + 1))
done

info "phase 2 — discrimination: each counter-stimulus must make its own test fail"
i=0
for t in "${SELECTED[@]}"; do
  stimuli=()
  while IFS= read -r s; do
    [ -n "$s" ] || continue
    stimuli+=("$s")
  done < <(stimuli_for "$t")
  # A test that is already red proves nothing by going red again: the counter-
  # stimulus would take the credit for the failure that was there before it.
  # This is a failure and never a skip, so the count below rises either way.
  if [ "${verdicts[$i]}" != "pass" ]; then
    bad "$t already fails with nothing applied, so none of its counter-stimuli can prove that it discriminates: ${stimuli[*]}"
    failures=$((failures + 1))
    i=$((i + 1))
    continue
  fi
  i=$((i + 1))
  for s in "${stimuli[@]}"; do
    SUT_ROOT="$(make_mutant "$s")"
    if ( set -Eeuo pipefail; "$t" ); then
      bad "$t still passed under '$s'; it proves nothing"
      failures=$((failures + 1))
    else
      ok "$t fails under '$s'"
    fi
    SUT_ROOT="$REPO_ROOT"
  done
done

if [ "$failures" -ne 0 ]; then
  die "$failures failure(s) across the 3 phases"
fi
info "all ${#SELECTED[@]} assertion(s) hold, and all ${#SELECTED[@]} are proven able to fail"
