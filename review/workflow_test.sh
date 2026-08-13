#!/usr/bin/env bash
#
# review/workflow_test.sh — prove that the reusable review workflow says what it
# must, and that every one of those assertions can fail.
#
# `.github/workflows/pr-review.yml` here is the ONE home of the review job for
# every gophersys repository: each of the others holds a caller that carries no
# logic. So a defect in this file reaches all of them at once, and a check on
# this file that cannot fail protects none of them.
#
# The suite runs in the same 2 phases as review/review_test.sh, plus a control:
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
# Nothing here runs a workflow: GitHub runs workflows. What this reads is the
# file, the repository the file points into, and what a real workflow linter
# says about it. `actionlint` is required, never optional — neither yq nor a
# YAML library reports a duplicate mapping key, they silently keep the last one,
# and a duplicated `timeout-minutes` is the defect that produced startup
# failures with NO check attached to the pull request in 3 repositories.
#
# Usage: bash review/workflow_test.sh [test-name ...]   # no names = every test
set -Eeuo pipefail
IFS=$'\n\t'

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/.." && pwd)"
WORKFLOWS_REL=".github/workflows"
PR_REVIEW_REL=".github/workflows/pr-review.yml"
ACTIONLINT_CONFIG_REL=".github/actionlint.yaml"
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
# this workflow exists to remove, and it is how a lint job here once shipped
# green while its tool had never been installed. 127 is the code ctl.sh uses for
# the same refusal.
require_tools() {
  local missing=() cmd
  for cmd in "$@"; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [ "${#missing[@]}" -gt 0 ]; then
    printf '\033[0;31m[test]\033[0m missing required tool(s): %s\n' "${missing[*]}" >&2
    printf '       actionlint is the only linter that reports a duplicate workflow key.\n' >&2
    printf '       Install it pinned: go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n' >&2
    exit 127
  fi
}

require_tools actionlint awk diff find cp mv sort

# --------------------------------------------------------------------------
# the trees
# --------------------------------------------------------------------------

# new_tree prints the path of a fresh copy of the 2 directories these tests
# read. A test reads a tree, never the repository directly, so a counter-
# stimulus can rename a file without touching the working copy.
new_tree() {
  local root
  root="$(mktemp -d "$WORK/tree.XXXXXX")"
  cp -R "$REPO_ROOT/.github" "$root/.github"
  cp -R "$REPO_ROOT/review" "$root/review"
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

sut_pr_review() { printf '%s\n' "$SUT_ROOT/$PR_REVIEW_REL"; }

# --------------------------------------------------------------------------
# assertions
# --------------------------------------------------------------------------

assert_the_workflow_exists() {
  [ -f "$1" ] || fail "$PR_REVIEW_REL does not exist. It is the ONE home of the review job: every other repository is meant to hold a 14-line caller that points at it."
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

# --------------------------------------------------------------------------
# reading the workflow
# --------------------------------------------------------------------------
#
# These 3 read structure rather than text, because 2 assertions below are about
# what the workflow MEANS and a substring cannot tell that. yq is not used and
# cannot be: it answers 15 for a file that declares `timeout-minutes` twice, so
# the very defect this suite exists to catch is invisible to it.

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

# sha_env_name prints the environment key that carries github.job_workflow_sha:
# the name the workflow gives to the commit it ASKED for.
sha_env_name() {
  awk 'match($0, /^[ \t]*[A-Za-z_][A-Za-z0-9_]*:[ \t]*\$\{\{[^}]*job_workflow_sha[^}]*\}\}[ \t]*$/) {
    k = $0; sub(/^[ \t]*/, "", k); sub(/:.*$/, "", k); print k
  }' "$1"
}

# pin_comparison prints "<left>|<right>": the 2 operands of the test inside the
# guard:pin line, stripped of quoting. It reads the bracket and not the whole
# line on purpose — an error message that still names the right variable is how a
# guard that compares a value to ITSELF reads as correct.
pin_comparison() {
  awk '
    $0 ~ /# guard:pin$/ {
      line = $0
      if (match(line, /\[[^]]*\]/) == 0) next
      inner = substr(line, RSTART + 1, RLENGTH - 2)
      if (match(inner, /[ \t]+(!=|==|=)[ \t]+/) == 0) next
      l = substr(inner, 1, RSTART - 1)
      r = substr(inner, RSTART + RLENGTH)
      gsub(/^[ \t]+|[ \t]+$/, "", l); gsub(/^[ \t]+|[ \t]+$/, "", r)
      gsub(/["{}]/, "", l); gsub(/["{}]/, "", r)
      printf "%s|%s\n", l, r
    }
  ' "$1"
}

# --------------------------------------------------------------------------
# the tests
# --------------------------------------------------------------------------

# The pin is the whole safety of a shared workflow. `github.job_workflow_sha`
# resolves to the commit of THIS file at run time, so the caller's `uses:` line
# is the only pin and it cannot fall back to a default branch. A tag name can be
# moved after the fact; a commit cannot.
t_the_reviewer_is_pinned_to_this_workflows_own_commit() {
  local wf; wf="$(sut_pr_review)"
  assert_the_workflow_exists "$wf"

  if [ "$(count_matching "$wf" '^[ \t]*workflow_call:')" -eq 0 ]; then
    fail "pr-review.yml has no 'workflow_call:' trigger, so no other repository can call this home, and 'github.job_workflow_sha' is not even defined in it"
  fi
  if [ "$(count_matching "$wf" 'github[.]job_workflow_sha')" -eq 0 ]; then
    fail "pr-review.yml never names github.job_workflow_sha, so the reviewer source is not fetched at this workflow's own commit and the caller's pin does not reach the code that runs"
  fi
  # The string pin this replaces. A version string resolves to a tag NAME, and a
  # clone that could not find the tag fell back to the default branch: cictl
  # moved 23 commits in one evening and no review from that period can be
  # reproduced.
  if [ "$(count_matching "$wf" 'CICTL_VERSION')" -ne 0 ]; then
    fail "pr-review.yml still carries CICTL_VERSION. That is a second pin, and a tag name can be moved after the fact while a commit cannot"
  fi

  local guard
  guard="$(lines_matching "$wf" '# guard:pin$')"
  if [ -z "$guard" ]; then
    fail "no line in pr-review.yml ends with '# guard:pin'. Fetching at a commit is not enough: the workflow must assert that the checkout it got IS that commit, on 1 line, the way every guard in review/review.sh is written"
  fi
  if ! printf '%s\n' "$guard" | grep -qE 'exit[[:space:]]+[1-9]'; then
    fail "the pin guard never exits non-zero, so a checkout at the wrong commit would be reviewed anyway: $guard"
  fi

  # Everything above this line is SHAPE, and shape is not enough: a guard that
  # compares a value to itself ends in `# guard:pin`, contains `exit 1`, and
  # holds nothing at all. What follows reads the comparison.
  local requested comparison left right observed
  requested="$(sha_env_name "$wf")"
  if [ -z "$requested" ]; then
    fail "no environment key in pr-review.yml carries github.job_workflow_sha, so nothing in the job names the commit that was asked for and the guard has nothing to compare against"
  fi
  comparison="$(pin_comparison "$wf")"
  if [ -z "$comparison" ]; then
    fail "the pin guard is not a comparison; it cannot tell what arrived from what was asked for: $guard"
  fi
  left="${comparison%%|*}"
  right="${comparison#*|}"
  if [ "$left" = "$right" ]; then
    fail "the pin guard compares '$left' to itself, so it passes whatever arrived. Deleting it and defanging it are the same defect"
  fi
  if [ "$left" = "\$$requested" ]; then
    observed="$right"
  elif [ "$right" = "\$$requested" ]; then
    observed="$left"
  else
    fail "the pin guard compares '$left' with '$right', and neither is \$$requested — the commit the caller asked for. It is checking something else"
  fi

  # The other side must be what ARRIVED. A variable assigned from the requested
  # sha instead of from the checkout compares 2 different names holding 1 value,
  # which is a self-comparison wearing a disguise.
  if ! printf '%s\n' "$observed" | grep -qE '^\$[A-Za-z_][A-Za-z0-9_]*$'; then
    fail "the observed side of the pin guard is '$observed', which is not a plain shell variable this suite can trace back to the checkout"
  fi
  local observed_name="${observed#\$}"
  if [ "$(count_matching "$wf" "^[ \t]*${observed_name}=.*git")" -eq 0 ]; then
    fail "the pin guard reads \$$observed_name as the checkout it got, but no line assigns $observed_name from git. Nothing observes the commit that actually arrived"
  fi
}

# actionlint reports a DUPLICATE key and says nothing about an absent one, and a
# duplicate is only half of this defect. A job with no limit inherits GitHub's
# default of 360 minutes: 6 hours of 1 slot on a pool with 2, held by a job that
# is already hung. The 8 lines of comment above the key argue exactly that, and
# a comment is not a check.
t_the_review_job_has_a_time_limit() {
  local wf; wf="$(sut_pr_review)"
  assert_the_workflow_exists "$wf"

  local line job count value jobs=0
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    jobs=$((jobs + 1))
    job="${line%% *}"
    value="${line#* }"
    count="${value%% *}"
    value="${value#* }"
    if [ "$count" -eq 0 ]; then
      fail "the '$job' job declares no timeout-minutes, so it inherits GitHub's default of 360 minutes and one hung review holds a slot for 6 hours. actionlint is silent about an absent key, so this is the only check that would see it"
    fi
    if [ "$count" -ne 1 ]; then
      fail "the '$job' job declares timeout-minutes $count times. That is invalid workflow YAML: GitHub answers with a startup failure that attaches NO check to the pull request, so the job reads as absent rather than broken"
    fi
    if ! printf '%s\n' "$value" | grep -qE '^[1-9][0-9]*$'; then
      fail "the '$job' job sets timeout-minutes to '$value', which is not a positive whole number of minutes"
    fi
  done < <(job_timeouts "$wf")

  if [ "$jobs" -eq 0 ]; then
    fail "pr-review.yml declares no jobs at all, so there is nothing here to limit"
  fi
}

# The workflow fetches THIS repository and runs a path inside it. A path that
# does not exist here is a job that fails in 20 repositories at once, and the
# rename that breaks it happens in this repository, where the workflow is not
# even open.
t_the_one_home_points_at_something_that_exists() {
  local wf; wf="$(sut_pr_review)"
  assert_the_workflow_exists "$wf"

  local paths path scripts=0
  paths="$(tokens_matching "$wf" 'review/[A-Za-z0-9_.-]+')"
  if [ -z "$paths" ]; then
    fail "pr-review.yml names no path under review/, so nothing in it connects to the reviewer it is supposed to run"
  fi
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    if [ ! -e "$SUT_ROOT/$path" ]; then
      fail "pr-review.yml runs '$path', which does not exist in this repository"
    fi
    case "$path" in *.sh) scripts=$((scripts + 1)) ;; esac
  done <<< "$paths"
  if [ "$scripts" -eq 0 ]; then
    fail "pr-review.yml names no script under review/, so the job cannot be running the reviewer at all"
  fi

  # The instructions ship with the source and review.sh reads them beside
  # itself, so the workflow never names them. They must be here all the same:
  # without them the reviewer runs with no instructions.
  if [ ! -f "$SUT_ROOT/review/CLAUDE.md" ]; then
    fail "review/CLAUDE.md is missing, so the reviewer this workflow starts would have no instructions"
  fi
}

# The Claude credential is an environment variable on the arc-review pool, and
# deliberately so: that pool runs this workflow and nothing else. A `secrets.`
# reference would move the credential into repository scope and create a second
# home for it, in every repository that calls this workflow.
t_the_credential_stays_on_the_pool() {
  local wf; wf="$(sut_pr_review)"
  assert_the_workflow_exists "$wf"

  local names name
  names="$(tokens_matching "$wf" 'secrets[.][A-Za-z0-9_]+')"
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    if [ "$name" != "secrets.GITHUB_TOKEN" ]; then
      fail "pr-review.yml reads $name. The Claude credential lives on the arc-review pool as an environment variable; a secrets reference gives it a second home, in every calling repository"
    fi
  done <<< "$names"

  # The other way in: a `secrets:` input on workflow_call makes every caller
  # hold the credential in order to pass it.
  if [ "$(count_matching "$wf" '^[ \t]*secrets:')" -ne 0 ]; then
    fail "pr-review.yml declares a 'secrets:' block. A reusable workflow that takes a secret makes every caller store one"
  fi
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
  if [ "${#files[@]}" -eq 0 ]; then
    fail "no workflow files found under $WORKFLOWS_REL; the discovery is broken, not the tree"
  fi

  # actionlint finds its config from the project root, and it finds the project
  # root from .git — which a copied tree does not have. Naming the config makes
  # the real tree and every copy answer identically, which is what phase 1b
  # then checks.
  local config=()
  if [ -f "$SUT_ROOT/$ACTIONLINT_CONFIG_REL" ]; then
    config=(-config-file "$ACTIONLINT_CONFIG_REL")
  fi

  local out rc
  set +e
  out="$(cd "$SUT_ROOT" && actionlint -oneline -no-color ${config[@]+"${config[@]}"} "${files[@]}" 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ] || [ -n "$out" ]; then
    fail "actionlint rejected ${#files[@]} workflow file(s) (exit $rc):
$out"
  fi
}

TESTS=(
  t_the_reviewer_is_pinned_to_this_workflows_own_commit
  t_the_one_home_points_at_something_that_exists
  t_the_credential_stays_on_the_pool
  t_the_review_job_has_a_time_limit
  t_no_workflow_has_a_duplicate_key
)

# MARKER_COVERAGE maps every guard marker in pr-review.yml to the test that
# proves it. It is written by hand on purpose, the same way review_test.sh does
# it: a guard whose marker no test claims is a guard nobody has checked, and
# that is how a guard once shipped here with a marker, no test, and a suite
# reporting that every guard held.
MARKER_COVERAGE="
guard:pin   t_the_reviewer_is_pinned_to_this_workflows_own_commit
"

every_marker_is_covered() {
  local wf="$REPO_ROOT/$PR_REVIEW_REL"
  # A missing pr-review.yml is already a phase 1 failure, named there. Reporting
  # it a second time here would only hide which check found it.
  [ -f "$wf" ] || return 0
  local marker name test uncovered=() missing=()
  while IFS= read -r marker; do
    name="${marker##*# }"
    test="$(awk -v m="$name" '$1 == m { print $2 }' <<< "$MARKER_COVERAGE")"
    if [ -z "$test" ]; then
      uncovered+=("$name")
    elif ! grep -qxF -- "$test" <<< "${TESTS[*]}"; then
      missing+=("$name -> $test")
    fi
  done < <(tokens_matching "$wf" '# guard:[a-z-]+')
  [ "${#uncovered[@]}" -eq 0 ] || die "these markers in pr-review.yml have no test: ${uncovered[*]}"
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
    t_the_reviewer_is_pinned_to_this_workflows_own_commit)
      printf '%s\n' pin-to-main no-pin-guard defanged-pin pin-ignores-the-requested-sha pin-observes-nothing ;;
    t_the_one_home_points_at_something_that_exists)
      printf '%s\n' rename-the-reviewer ;;
    t_the_credential_stays_on_the_pool)
      printf '%s\n' oauth-secret-in-the-step secrets-input-on-the-call ;;
    t_the_review_job_has_a_time_limit)
      printf '%s\n' no-time-limit duplicate-time-limit time-limit-of-zero ;;
    t_no_workflow_has_a_duplicate_key)
      printf '%s\n' duplicate-timeout-key ;;
    *) die "no counter-stimulus is declared for $1; every test must name what would make it fail" ;;
  esac
}

# shellcheck disable=SC2016 # the awk text is data, not shell to expand
apply_stimulus() {
  local stimulus="$1" root="$2" wf="$2/$PR_REVIEW_REL"
  case "$stimulus" in
    pin-to-main|no-pin-guard|defanged-pin|pin-ignores-the-requested-sha|pin-observes-nothing|oauth-secret-in-the-step|secrets-input-on-the-call|no-time-limit|duplicate-time-limit|time-limit-of-zero)
      [ -f "$wf" ] || die "the counter-stimulus '$stimulus' needs $PR_REVIEW_REL, and this repository does not have it yet" ;;
  esac
  case "$stimulus" in
    # The ref becomes a branch name, which is what the pin exists to forbid.
    pin-to-main)
      edit_file "$wf" '{ gsub(/\$\{\{[^}]*job_workflow_sha[^}]*\}\}/, "main"); print }' ;;
    # The fetch still asks for the commit, but nothing checks what arrived.
    no-pin-guard)
      edit_file "$wf" '!/# guard:pin$/ { print }' ;;
    # The guard stays, on 1 line, ending in its marker, still exiting 1 — and
    # compares the checkout to ITSELF. Deleting a guard and neutering one are
    # the same defect, and only this stimulus tells them apart.
    defanged-pin)
      edit_file "$wf" '
        {
          line = $0
          if (line ~ /# guard:pin$/ && match(line, /\[[^]]*\]/)) {
            bs = RSTART; bl = RLENGTH
            inner = substr(line, bs + 1, bl - 2)
            if (match(inner, /[ \t]+(!=|==|=)[ \t]+/)) {
              left = substr(inner, 1, RSTART - 1)
              gsub(/^[ \t]+|[ \t]+$/, "", left)
              line = substr(line, 1, bs - 1) "[ " left " = " left " ]" substr(line, bs + bl)
            }
          }
          print line
        }' ;;
    # The guard compares a real pair, but not against the commit that was asked
    # for. github.sha is the CALLER's head, not this workflow's own commit.
    pin-ignores-the-requested-sha)
      edit_file "$wf" '
        {
          line = $0
          if (line ~ /# guard:pin$/ && match(line, /\[[^]]*\]/)) {
            bs = RSTART; bl = RLENGTH
            inner = substr(line, bs + 1, bl - 2)
            if (match(inner, /[ \t]+(!=|==|=)[ \t]+/)) {
              left = substr(inner, 1, RSTART - 1)
              gsub(/^[ \t]+|[ \t]+$/, "", left)
              line = substr(line, 1, bs - 1) "[ " left " = \"$GITHUB_SHA\" ]" substr(line, bs + bl)
            }
          }
          print line
        }' ;;
    # 2 different names holding 1 value. The comparison passes every time, and
    # nothing ever looks at what the checkout actually is.
    pin-observes-nothing)
      local requested
      requested="$(sha_env_name "$wf")"
      [ -n "$requested" ] || die "the counter-stimulus 'pin-observes-nothing' needs an environment key carrying github.job_workflow_sha"
      edit_file "$wf" '
        {
          line = $0
          if (!done && line ~ /^[ \t]*[A-Za-z_][A-Za-z0-9_]*=.*git/) {
            match(line, /^[ \t]*[A-Za-z_][A-Za-z0-9_]*=/)
            line = substr(line, 1, RSTART + RLENGTH - 1) "\"$" requested "\""
            done = 1
          }
          print line
        }' -v "requested=$requested" ;;
    # The limit disappears and the job inherits 360 minutes. actionlint is
    # silent about this, which is the whole reason the assertion exists.
    no-time-limit)
      edit_file "$wf" '!/^[ \t]*timeout-minutes:/ { print }' ;;
    # The same key twice: invalid workflow YAML, and a startup failure that
    # attaches no check at all to the pull request.
    duplicate-time-limit)
      edit_file "$wf" '{ print } !duplicated && /^[ \t]*timeout-minutes:/ { print; duplicated = 1 }' ;;
    # A limit that is not a limit.
    time-limit-of-zero)
      edit_file "$wf" '{ if ($0 ~ /^[ \t]*timeout-minutes:/) { sub(/:[ \t]*.*$/, ": 0") } print }' ;;
    # The reviewer moves in this repository, and the workflow is not touched.
    rename-the-reviewer)
      [ -f "$root/review/review.sh" ] || die "the counter-stimulus 'rename-the-reviewer' needs review/review.sh"
      mv "$root/review/review.sh" "$root/review/run.sh" ;;
    # The credential gets a second home, in repository scope.
    oauth-secret-in-the-step)
      edit_file "$wf" '
        { print }
        !inserted && /secrets\.GITHUB_TOKEN/ {
          match($0, /^[ ]*/)
          printf "%sCLAUDE_CODE_OAUTH_TOKEN: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}\n", substr($0, 1, RLENGTH)
          inserted = 1
        }' ;;
    # The same second home, arrived at from the caller side.
    secrets-input-on-the-call)
      edit_file "$wf" '
        { print }
        !inserted && /^[ ]*workflow_call:/ {
          match($0, /^[ ]*/)
          pad = substr($0, 1, RLENGTH) "  "
          printf "%ssecrets:\n%s  CLAUDE_CODE_OAUTH_TOKEN:\n%s    required: true\n", pad, pad, pad
          inserted = 1
        }' ;;
    # The real defect: 1 key declared twice, which no YAML reader reports.
    duplicate-timeout-key)
      local f target=""
      while IFS= read -r f; do
        if [ "$(count_matching "$f" '^[ \t]*timeout-minutes:')" -ne 0 ]; then target="$f"; break; fi
      done < <(find "$root/$WORKFLOWS_REL" -type f \( -name '*.yml' -o -name '*.yaml' \) | sort)
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
  if ( set -Eeuo pipefail; "$t" ); then
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
