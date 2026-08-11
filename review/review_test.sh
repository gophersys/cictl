#!/usr/bin/env bash
#
# review/review_test.sh — prove that every guard in review/review.sh can fail.
#
# review.sh spawns a paid agent and writes to a pull request. Almost all of its
# value is in what it REFUSES to do, so this suite runs in 2 phases:
#
#   phase 1  behaviour — every test must PASS against the real review.sh.
#   phase 2  mutation  — for each test, delete the 1 line that holds the guard
#                        which that test covers, then require the same test to
#                        FAIL. A test that still passes proves nothing, and this
#                        suite exits non-zero when that happens.
#
# Nothing here touches the network and nothing spends money. PATH is replaced by
# a sandbox that holds stub `claude` and `gh` commands plus the small set of
# coreutils that review.sh needs. The real `claude` and `gh` are unreachable by
# construction, not by convention.
#
# Usage: bash review/review_test.sh
set -Eeuo pipefail
IFS=$'\n\t'

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REVIEW_SH="$HERE/review.sh"
INSTRUCTIONS="$HERE/CLAUDE.md"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# SUT is the script that the current phase runs. Phase 2 repoints it at a mutant.
SUT="$REVIEW_SH"
# SB and RC are the sandbox and the exit code of the last run_review call.
SB=""
RC=0

info() { printf '\033[0;36m[test]\033[0m %s\n' "$*"; }
ok()   { printf '\033[0;32m  ok  \033[0m %s\n' "$*"; }
bad()  { printf '\033[0;31m FAIL \033[0m %s\n' "$*" >&2; }
die()  { printf '\033[0;31m[test]\033[0m %s\n' "$*" >&2; exit 1; }
# fail ends the test it is called from. Each test runs in its own subshell.
fail() { printf '       %s\n' "$*" >&2; exit 1; }

# --------------------------------------------------------------------------
# the sandbox
# --------------------------------------------------------------------------

# Every external command that review.sh can run, apart from the 4 tools that it
# gates on. The sandbox PATH holds these and nothing else.
# The sandbox PATH is exactly what review.sh needs and nothing else, so adding a
# tool here is a deliberate act. `tail` is needed by the branch that prints the
# reviewer output before refusing a verdict-less review.
COREUTILS=(bash dirname rm wc cat grep tail)

# new_sandbox prints the path of a fresh sandbox directory.
new_sandbox() {
  local sb
  sb="$(mktemp -d "$WORK/sb.XXXXXX")"
  mkdir -p "$sb/bin" "$sb/calls" "$sb/fx" "$sb/home" "$sb/review"

  local c real
  for c in "${COREUTILS[@]}"; do
    real="$(command -v "$c")" || die "this host has no $c; the sandbox cannot be built"
    ln -s "$real" "$sb/bin/$c"
  done

  # mktemp is wrapped, not stubbed: it records each path it hands out so that a
  # test can prove the temp files were removed again.
  real="$(command -v mktemp)" || die "this host has no mktemp"
  cat > "$sb/bin/mktemp" <<EOF
#!/usr/bin/env bash
set -eu
p="\$("$real" "\$@")"
printf '%s\n' "\$p" >> "$sb/calls/mktemp.log"
printf '%s\n' "\$p"
EOF

  # The gh stub needs cp. It is addressed absolutely rather than added to the
  # sandbox PATH, so the PATH stays exactly the set that review.sh needs.
  local real_cp
  real_cp="$(command -v cp)" || die "this host has no cp"

  # The stubs record every invocation. An argv shape that they do not model is a
  # hard error: a stub that answers "fine" to a call it does not understand
  # teaches the test a lie.
  cat > "$sb/bin/claude" <<EOF
#!/usr/bin/env bash
set -eu
printf '%s\n' "\$*" >> "$sb/calls/claude.log"
cat "$sb/fx/out"
exit "\$(cat "$sb/fx/rc")"
EOF

  cat > "$sb/bin/gh" <<EOF
#!/usr/bin/env bash
set -eu
printf '%s\n' "\$*" >> "$sb/calls/gh.log"
case "\$1 \$2" in
  'pr diff') cat "$sb/fx/diff" ;;
  'pr view')
    case "\$*" in
      *reviews*) cat "$sb/fx/previous" ;;
      *title*)   printf 'a stub pull request title\n' ;;
      *body*)    printf 'a stub pull request body\n' ;;
      *) printf 'gh stub: unmodelled pr view: %s\n' "\$*" >&2; exit 64 ;;
    esac ;;
  'pr comment')
    prev=""
    for a in "\$@"; do
      if [ "\$prev" = "--body-file" ]; then "$real_cp" "\$a" "$sb/posted_body"; fi
      prev="\$a"
    done
    [ -f "$sb/posted_body" ] || { printf 'gh stub: pr comment without --body-file\n' >&2; exit 64; }
    printf 'https://example.invalid/pr/comment\n' ;;
  *) printf 'gh stub: unmodelled argv: %s\n' "\$*" >&2; exit 64 ;;
esac
EOF

  # git and jq are gated on but never called. A stub that records is enough, and
  # it also proves that they are never called.
  for c in git jq; do
    cat > "$sb/bin/$c" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$sb/calls/$c.log"
EOF
  done

  chmod +x "$sb/bin/mktemp" "$sb/bin/claude" "$sb/bin/gh" "$sb/bin/git" "$sb/bin/jq"

  # Default fixtures: a real-looking diff, no earlier review, an approving
  # reviewer that exits 0. Each test overrides what it is about.
  printf 'diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n+var x = 1\n' > "$sb/fx/diff"
  : > "$sb/fx/previous"
  printf 'Where: x.go:1\nWhat: nothing that blocks the merge.\n\nAPPROVE\n' > "$sb/fx/out"
  printf '0\n' > "$sb/fx/rc"

  cp "$SUT" "$sb/review/review.sh"
  cp "$INSTRUCTIONS" "$sb/review/CLAUDE.md"
  printf '%s\n' "$sb"
}

# run_review <sandbox> [VAR=VALUE ...] -- [args for review.sh]
# It runs under `env -i`, so the caller's real CLAUDE_CODE_OAUTH_TOKEN or
# ANTHROPIC_API_KEY can neither rescue a test nor break one.
run_review() {
  SB="$1"; shift
  local envs=() args=() seen=0 a
  for a in "$@"; do
    if [ "$a" = "--" ]; then seen=1; continue; fi
    if [ "$seen" -eq 1 ]; then args+=("$a"); else envs+=("$a"); fi
  done
  set +e
  env -i PATH="$SB/bin" HOME="$SB/home" \
    ${envs[@]+"${envs[@]}"} \
    "$SB/bin/bash" "$SB/review/review.sh" ${args[@]+"${args[@]}"} \
    > "$SB/stdout" 2> "$SB/stderr"
  RC=$?
  set -e
}

# CREDS is the environment of a run that should reach the agent.
CREDS=(CLAUDE_CODE_OAUTH_TOKEN=oauth-token-stub GH_TOKEN=gh-token-stub)

# --------------------------------------------------------------------------
# assertions
# --------------------------------------------------------------------------

assert_rc_nonzero() { [ "$RC" -ne 0 ] || fail "expected a non-zero exit, got 0"; }
assert_rc_zero()    { [ "$RC" -eq 0 ] || fail "expected exit 0, got $RC; stderr: $(cat "$SB/stderr")"; }
assert_stderr_has() {
  grep -qF -- "$1" "$SB/stderr" || fail "stderr never said \"$1\"; it said: $(cat "$SB/stderr")"
}
assert_stdout_has() {
  grep -qF -- "$1" "$SB/stdout" || fail "stdout never said \"$1\"; it said: $(cat "$SB/stdout")"
}
assert_not_called() {
  [ ! -f "$SB/calls/$1.log" ] || fail "$1 ran, and must not have: $(cat "$SB/calls/$1.log")"
}
assert_no_comment() {
  ! grep -q 'pr comment' "$SB/calls/gh.log" 2>/dev/null || fail "a review was posted, and must not have been"
}
assert_comment_posted() {
  grep -q 'pr comment' "$SB/calls/gh.log" 2>/dev/null || fail "no review was posted"
}
# assert_no_temp_leak checks every path that mktemp handed out during the run.
assert_no_temp_leak() {
  [ -f "$SB/calls/mktemp.log" ] || fail "no temp file was made; this test is not on the path it claims"
  local f
  while IFS= read -r f; do
    [ ! -e "$f" ] || fail "temp file leaked: $f"
  done < "$SB/calls/mktemp.log"
}

# --------------------------------------------------------------------------
# the tests
# --------------------------------------------------------------------------

t_usage_requires_pr_number() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} --
  assert_rc_nonzero
  assert_stderr_has "usage: bash review/review.sh"
  assert_not_called gh
  assert_not_called claude
}

t_missing_tool_is_named() {
  local sb; sb="$(new_sandbox)"
  rm -f "$sb/bin/claude"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "missing required tool(s): claude"
  # With the guard in place nothing at all happens, so gh is untouched.
  assert_not_called gh
}

t_missing_oauth_token() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" GH_TOKEN=gh-token-stub -- 1
  assert_rc_nonzero
  assert_stderr_has "CLAUDE_CODE_OAUTH_TOKEN"
  assert_not_called claude
  assert_not_called gh
}

t_missing_gh_token() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" CLAUDE_CODE_OAUTH_TOKEN=oauth-token-stub -- 1
  assert_rc_nonzero
  assert_stderr_has "GH_TOKEN"
  assert_not_called claude
  assert_not_called gh
}

t_api_key_outranks_the_token() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} ANTHROPIC_API_KEY=sk-ant-not-a-real-key -- 1
  assert_rc_nonzero
  assert_stderr_has "ANTHROPIC_API_KEY is set"
  assert_not_called claude
}

t_auth_token_outranks_the_token() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} ANTHROPIC_AUTH_TOKEN=not-a-real-token -- 1
  assert_rc_nonzero
  assert_stderr_has "ANTHROPIC_AUTH_TOKEN is set"
  assert_not_called claude
}

t_missing_instructions() {
  local sb; sb="$(new_sandbox)"
  rm -f "$sb/review/CLAUDE.md"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "the reviewer instructions are missing"
  assert_not_called claude
}

t_empty_diff_is_refused() {
  local sb; sb="$(new_sandbox)"
  : > "$sb/fx/diff"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "the diff for #1 is empty"
  assert_not_called claude
}

t_temp_files_are_removed_when_the_run_fails() {
  local sb; sb="$(new_sandbox)"
  : > "$sb/fx/diff" # fail right after the temp files exist
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_no_temp_leak
  ! grep -q 'unbound variable' "$SB/stderr" \
    || fail "the cleanup trap broke: $(cat "$SB/stderr")"
}

t_empty_reviewer_output_is_not_posted() {
  local sb; sb="$(new_sandbox)"
  : > "$sb/fx/out"
  printf '2\n' > "$sb/fx/rc"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "the reviewer produced no output (exit 2)"
  assert_no_comment
}

t_output_without_a_verdict_is_not_posted() {
  local sb; sb="$(new_sandbox)"
  printf 'I read the diff. It looks good to me.\nAPPROVED maybe.\n' > "$sb/fx/out"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "no verdict line"
  assert_no_comment
}

t_request_changes_is_reported_as_request_changes() {
  local sb; sb="$(new_sandbox)"
  printf 'Where: x.go:1\nWhat: a defect.\n\nREQUEST_CHANGES\n' > "$sb/fx/out"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  assert_stdout_has "verdict: REQUEST_CHANGES"
  assert_comment_posted
}

# The positive control. Without it every refusal test above could pass because
# review.sh is broken in some other way and never reaches the agent at all.
t_approve_is_posted_with_the_guarded_flag_set() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  assert_comment_posted
  assert_no_temp_leak
  grep -q 'APPROVE' "$SB/posted_body" || fail "the posted body is not the reviewer output"
  grep -q -- '--permission-mode default' "$SB/calls/claude.log" \
    || fail "the agent ran without --permission-mode default"
  grep -q -- '--max-budget-usd 25' "$SB/calls/claude.log" \
    || fail "the agent ran without the budget ceiling"
  ! grep -q -- '--dangerously-skip-permissions' "$SB/calls/claude.log" \
    || fail "the agent ran with --dangerously-skip-permissions"
}

# The verdict the model actually writes is rarely a bare token. A real 3281-byte
# review was refused because it carried markdown emphasis, so these 2 tests hold
# the widened matcher from both sides: it must read the emphasised form, and it
# must still refuse a verdict word that only appears inside a sentence.
t_a_markdown_verdict_is_understood() {
  local sb; sb="$(new_sandbox)"
  printf 'Where: x.go:1\nWhat: a defect.\n\n**REQUEST_CHANGES**\n' > "$sb/fx/out"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  assert_stdout_has "verdict: REQUEST_CHANGES"
  assert_comment_posted
}

t_a_verdict_inside_a_sentence_is_not_a_verdict() {
  local sb; sb="$(new_sandbox)"
  printf 'I would APPROVE this if you fixed the test.\nThis does not REQUEST_CHANGES anything.\n' > "$sb/fx/out"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "no verdict line"
  assert_no_comment
}

TESTS=(
  t_usage_requires_pr_number
  t_missing_tool_is_named
  t_missing_oauth_token
  t_missing_gh_token
  t_api_key_outranks_the_token
  t_auth_token_outranks_the_token
  t_missing_instructions
  t_empty_diff_is_refused
  t_temp_files_are_removed_when_the_run_fails
  t_empty_reviewer_output_is_not_posted
  t_output_without_a_verdict_is_not_posted
  t_request_changes_is_reported_as_request_changes
  t_approve_is_posted_with_the_guarded_flag_set
  t_a_markdown_verdict_is_understood
  t_a_verdict_inside_a_sentence_is_not_a_verdict
)

# --------------------------------------------------------------------------
# the mutations
# --------------------------------------------------------------------------

# mutation_for <test> prints the sed expression that removes the 1 line which
# holds the guard that the named test covers. A test with no entry is a hard
# error, so nobody can add a test without stating how it is proven.
# shellcheck disable=SC2016 # the sed text is data, not shell to expand
mutation_for() {
  case "$1" in
    # Narrow the pattern back to the bare token this once rejected a real review for.
    t_a_markdown_verdict_is_understood)          printf 's|^RE_REQUEST_CHANGES=.*|RE_REQUEST_CHANGES="^REQUEST_CHANGES[[:space:]]*$"|' ;;
    # Drop the anchors so the token matches inside a sentence.
    t_a_verdict_inside_a_sentence_is_not_a_verdict) printf 's|^RE_APPROVE=.*|RE_APPROVE="APPROVE"|' ;;
    t_usage_requires_pr_number)                  printf '/# guard:usage$/d' ;;
    t_missing_tool_is_named)                     printf '/# guard:tools$/d' ;;
    t_missing_oauth_token)                       printf '/# guard:oauth$/d' ;;
    t_missing_gh_token)                          printf '/# guard:gh-token$/d' ;;
    t_api_key_outranks_the_token)                printf '/# guard:api-key$/d' ;;
    t_auth_token_outranks_the_token)             printf '/# guard:auth-token$/d' ;;
    t_missing_instructions)                      printf '/# guard:instructions$/d' ;;
    t_empty_diff_is_refused)                     printf '/# guard:empty-diff$/d' ;;
    t_empty_reviewer_output_is_not_posted)       printf '/# guard:empty-output$/d' ;;
    # Deleting a line inside an if/else would be a syntax error, so these 3
    # replace the line instead.
    t_temp_files_are_removed_when_the_run_fails) printf 's|^.*# guard:temp-lifetime$|diff_file="$(mktemp)"|' ;;
    t_output_without_a_verdict_is_not_posted)    printf 's|^.*# guard:verdict$|  event=APPROVE|' ;;
    t_request_changes_is_reported_as_request_changes) printf 's|^.*# verdict:request-changes$|  event=APPROVE|' ;;
    t_approve_is_posted_with_the_guarded_flag_set)    printf 's|^.*# post:comment$|:|' ;;
    *) die "no mutation is declared for $1; every test must name the guard that it proves" ;;
  esac
}

# make_mutant prints the path of a copy of review.sh with 1 guard removed. It
# refuses to hand back a copy that is unchanged or that no longer parses: either
# would make the mutation phase report a pass that means nothing.
make_mutant() {
  local expr="$1" dir
  dir="$(mktemp -d "$WORK/mut.XXXXXX")"
  sed "$expr" "$REVIEW_SH" > "$dir/review.sh"
  if cmp -s "$REVIEW_SH" "$dir/review.sh"; then
    die "the mutation changed nothing: $expr (the guard marker is gone from review.sh)"
  fi
  bash -n "$dir/review.sh" || die "the mutation broke the syntax: $expr"
  printf '%s\n' "$dir/review.sh"
}

# --------------------------------------------------------------------------
# the driver
# --------------------------------------------------------------------------

failures=0
t=""

info "phase 1 — behaviour: ${#TESTS[@]} tests against the real review.sh"
for t in "${TESTS[@]}"; do
  if ( set -Eeuo pipefail; "$t" ); then
    ok "$t"
  else
    bad "$t"
    failures=$((failures + 1))
  fi
done

info "phase 2 — mutation: each guard removed must make its own test fail"
for t in "${TESTS[@]}"; do
  SUT="$(make_mutant "$(mutation_for "$t")")"
  if ( set -Eeuo pipefail; "$t" ); then
    bad "$t still passed with its guard removed; it proves nothing"
    failures=$((failures + 1))
  else
    ok "$t fails without its guard"
  fi
  SUT="$REVIEW_SH"
done

if [ "$failures" -ne 0 ]; then
  die "$failures failure(s) across both phases"
fi
info "all ${#TESTS[@]} guards hold, and all ${#TESTS[@]} are proven able to fail"
