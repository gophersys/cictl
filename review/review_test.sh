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
# jq is REAL, not a stub: review.sh parses the agent's event stream with it, so a
# stub would test nothing. git is still a stub, because it is only gated on.
COREUTILS=(bash dirname rm wc cat grep tail jq)

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
# Drain the prompt (stdin) into prompt_seen so a test can read what the reviewer
# was told. The closing pass sends a verdict-only directive, and this is how it
# is inspected without paying for a real agent.
cat > "$sb/prompt_seen"
if [ -f "$sb/fx/stream" ]; then
  cat "$sb/fx/stream"
else
  jq -n --rawfile r "$sb/fx/out" \
    '{type:"result",subtype:"success",is_error:false,result:\$r,total_cost_usd:1.234,num_turns:7,duration_ms:9000,permission_denials:[]}'
fi
exit "\$(cat "$sb/fx/rc")"
EOF

  cat > "$sb/bin/gh" <<EOF
#!/usr/bin/env bash
set -eu
printf '%s\n' "\$*" >> "$sb/calls/gh.log"
case "\$1 \$2" in
  'pr diff') cat "$sb/fx/diff" ;;
  'pr view')
    # Apply --jq with real jq, as gh does. A stub that returned the raw fixture
    # would teach the test that gh does not filter, which is a lie the script
    # then depends on.
    filter=""; prev=""
    for a in "\$@"; do [ "\$prev" = "--jq" ] && filter="\$a"; prev="\$a"; done
    case "\$*" in
      *comments*)
        if [ -n "\$filter" ]; then jq -r "\$filter" "$sb/fx/comments"; else cat "$sb/fx/comments"; fi ;;
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

  # git is gated on but never called. A stub that records is enough, and it also
  # proves that it is never called.
  cat > "$sb/bin/git" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$sb/calls/git.log"
EOF

  chmod +x "$sb/bin/mktemp" "$sb/bin/claude" "$sb/bin/gh" "$sb/bin/git"

  # Default fixtures: a real-looking diff, no earlier review, an approving
  # reviewer that exits 0. Each test overrides what it is about.
  printf 'diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n+var x = 1\n' > "$sb/fx/diff"
  # No earlier review by default. gh returns a JSON object, so the fixture is one.
  printf '{"comments":[]}\n' > "$sb/fx/comments"
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
  env -i PATH="$SB/bin" HOME="$SB/home" REVIEW_STREAM_FILE="$SB/stream.jsonl" \
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
# Case-insensitive variant, for a log phrase whose exact capitalization is the
# implementer's choice, not the contract.
assert_stdout_has_i() {
  grep -qiF -- "$1" "$SB/stdout" || fail "stdout never said (any case) \"$1\"; it said: $(cat "$SB/stdout")"
}
assert_not_called() {
  [ ! -f "$SB/calls/$1.log" ] || fail "$1 ran, and must not have: $(cat "$SB/calls/$1.log")"
}
assert_called() {
  [ -f "$SB/calls/$1.log" ] || fail "$1 was not invoked, and must have been"
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

# An unreadable verdict must NEVER discard the review. The body is posted, it is
# recorded as REQUEST_CHANGES because that asks a human to look, and the job still
# fails so the problem stays loud.
t_an_unreadable_verdict_is_posted_and_then_fails() {
  local sb; sb="$(new_sandbox)"
  printf 'Where: x.go:1\nWhat: a real defect worth keeping.\n\nI think you should probably change this.\n' > "$sb/fx/out"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_comment_posted
  assert_rc_nonzero
  assert_stderr_has "verdict line could not be read"
  grep -qF -- 'a real defect worth keeping' "$SB/posted_body" \
    || fail "the paid review was discarded instead of posted"
  assert_stdout_has "verdict: REQUEST_CHANGES"
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

# A verdict may carry its reason. Requiring the token to stand entirely alone
# refused 2 real reviews over punctuation, each one a paid run thrown away.
t_a_verdict_may_carry_its_reason() {
  local sb; sb="$(new_sandbox)"
  printf 'Where: x.go:1\nWhat: a defect.\n\n**REQUEST_CHANGES** — D44 should be marked resolved before this merges.\n' > "$sb/fx/out"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  assert_stdout_has "verdict: REQUEST_CHANGES"
  assert_comment_posted
}

t_a_verdict_inside_a_sentence_is_not_a_verdict() {
  local sb; sb="$(new_sandbox)"
  printf 'I would APPROVE this if you fixed the test.\nThis does not REQUEST_CHANGES anything.\n' > "$sb/fx/out"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  # The property under test is that a verdict word inside a sentence is NOT read
  # as a verdict. It must never come out as APPROVE, which would wave a change
  # through on a guess. The review is still posted, because the review is the
  # product, and the job still fails because the verdict was unreadable.
  assert_rc_nonzero
  assert_stdout_has "verdict: REQUEST_CHANGES"
  assert_stderr_has "verdict line could not be read"
}

# ---- the event stream: capture, and the failures it makes visible ----

# A result event, built from named parts so each test states only what it is about.
stream_result() { # stream_result <text> <is_error> <denials-json>
  jq -n --arg r "$1" --argjson e "$2" --argjson d "$3" \
    '{type:"result",subtype:"success",is_error:$e,result:$r,total_cost_usd:2.5,num_turns:11,duration_ms:42000,permission_denials:$d}'
}

t_the_run_summary_and_the_stream_are_kept() {
  local sb; sb="$(new_sandbox)"
  printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}\n' > "$sb/fx/stream"
  printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}\n' >> "$sb/fx/stream"
  stream_result 'Where: x.go:1
What: a defect.

REQUEST_CHANGES' false '[]' >> "$sb/fx/stream"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  # The numbers that make a bad review diagnosable without paying for another.
  assert_stdout_has "cost      \$2.5"
  assert_stdout_has "turns     11"
  assert_stdout_has "tools     Read x2"
  assert_comment_posted
  # The stream itself must survive the run: the workflow keeps it as an artifact.
  [ -s "$SB/stream.jsonl" ] || fail "the event stream was not kept"
}

t_a_stream_with_no_result_event_is_refused() {
  local sb; sb="$(new_sandbox)"
  printf '{"type":"system","subtype":"init"}\n{"type":"assistant","message":{"content":[]}}\n' > "$sb/fx/stream"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "no result event"
  assert_no_comment
}

t_an_agent_error_is_not_posted() {
  local sb; sb="$(new_sandbox)"
  stream_result 'I ran out of budget half way.

APPROVE' true '[]' > "$sb/fx/stream"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "reported an error"
  assert_no_comment
}

# A denied READ means the reviewer could not see the code. The review is posted
# anyway — it is paid for — and then the job fails, loudly.
t_a_denied_read_is_posted_and_then_fails_the_job() {
  local sb; sb="$(new_sandbox)"
  stream_result 'Where: x.go:1
What: a defect.

REQUEST_CHANGES' false '[{"tool_name":"Read"}]' > "$sb/fx/stream"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_comment_posted
  assert_rc_nonzero
  assert_stderr_has "could not see the code"
}

# A denied Bash is the read-only allowlist doing its job: a piped or compound
# command cannot match a prefix rule. It must be reported and must NOT fail.
t_a_denied_bash_is_reported_but_does_not_fail_the_job() {
  local sb; sb="$(new_sandbox)"
  stream_result 'Where: x.go:1
What: a defect.

REQUEST_CHANGES' false '[{"tool_name":"Bash"},{"tool_name":"Bash"}]' > "$sb/fx/stream"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_comment_posted
  assert_rc_zero
  assert_stdout_has "which is the policy working"
}

# The reviewer's tool set is an allowlist, and it must stay read-only. A write
# tool reaching this list would let a reviewer edit the branch it is judging.
t_the_agent_is_given_a_read_only_tool_set() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  grep -q -- '--allowedTools' "$SB/calls/claude.log" || fail "the agent ran with no allowlist at all"
  # It must be able to read the cross-repository context it was denied before.
  grep -q -- 'Bash(gh pr view:\*)' "$SB/calls/claude.log" || fail "the agent cannot read a referenced pull request"
  local t
  for t in Write Edit NotebookEdit 'Bash(gh pr comment' 'Bash(gh pr merge' 'Bash(rm' 'Bash(git push'; do
    ! grep -qF -- "$t" "$SB/calls/claude.log" || fail "a write tool reached the allowlist: $t"
  done
}

t_an_empty_stream_is_refused() {
  local sb; sb="$(new_sandbox)"
  : > "$sb/fx/stream"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_nonzero
  assert_stderr_has "produced no events"
  assert_no_comment
}

# rounds_with <n> writes a comments fixture holding n reviews by this agent, plus
# a human comment that quotes the marker text without being a review.
rounds_with() {
  jq -n --argjson n "$1" \
    '{comments: ([range($n) | {body: "a finding\n\n<!-- gophersys-review-agent -->"}] + [{body: "I disagree with the agent"}])}'
}

t_the_first_run_is_round_1() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  assert_stdout_has "posted round 1 of 4"
}

t_a_second_run_is_round_2_and_sees_the_first() {
  local sb; sb="$(new_sandbox)"
  rounds_with 1 > "$sb/fx/comments"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  assert_stdout_has "posted round 2 of 4"
}

t_the_round_ceiling_is_configurable() {
  local sb; sb="$(new_sandbox)"
  rounds_with 2 > "$sb/fx/comments"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} REVIEW_MAX_ROUNDS=3 -- 1
  assert_rc_zero
  assert_stdout_has "posted round 3 of 3"
}

# The marker is what makes a comment ours. Without it on the posted body, the
# next run cannot count this round and the ceiling never engages.
t_the_posted_review_carries_the_marker() {
  local sb; sb="$(new_sandbox)"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  grep -qF -- '<!-- gophersys-review-agent -->' "$SB/posted_body" \
    || fail "the posted review carries no marker, so the next round cannot count it"
}

# When the round ceiling is reached the push is NOT reviewed — that is the design
# working. But a green pr-review check with nothing on the pull request reads as a
# review that happened. The skip must leave a visible trace on the pull request: a
# comment stating the push was not reviewed, that the limit was hit, and the remedy.
t_a_round_limit_skip_posts_a_visible_notice() {
  local sb; sb="$(new_sandbox)"
  rounds_with 5 > "$sb/fx/comments"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  # Exit 0: the ceiling stays green, as the author chose.
  assert_rc_zero
  # Spend is the whole point of the ceiling: the agent is never reached.
  assert_not_called claude
  # The skip is now visible on the pull request, not only in the runner log.
  assert_comment_posted
  grep -qF -- 'not reviewed' "$SB/posted_body" \
    || fail "the skip notice does not say the push was not reviewed: $(cat "$SB/posted_body")"
  grep -qF -- 'limit' "$SB/posted_body" \
    || fail "the skip notice does not say the round limit was reached: $(cat "$SB/posted_body")"
  grep -qF -- 'REVIEW_MAX_ROUNDS' "$SB/posted_body" \
    || fail "the skip notice does not state the remedy (raise REVIEW_MAX_ROUNDS): $(cat "$SB/posted_body")"
}

# The skip notice must be visible to a human but MUST NOT be counted as a review
# round. Rounds are counted by MARKER comments, so the notice carries a DISTINCT
# skip marker and never the review marker: if it carried the review marker the next
# push would count this skip as a round and the ceiling would cascade.
t_the_skip_notice_is_not_counted_as_a_round() {
  local sb; sb="$(new_sandbox)"
  rounds_with 5 > "$sb/fx/comments"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  assert_comment_posted
  grep -qF -- '<!-- gophersys-review-agent-skip -->' "$SB/posted_body" \
    || fail "the skip notice carries no skip marker, so it cannot be told apart from a review"
  # `agent-skip -->` never contains the contiguous `agent -->`, so this is the
  # property that keeps the notice uncounted by the marker query.
  ! grep -qF -- '<!-- gophersys-review-agent -->' "$SB/posted_body" \
    || fail "the skip notice carries the review marker, so the next push counts it as a round"
}

# After the ceiling, the reviewer does NOT go silent forever. Round MAX+1 is a
# single CLOSING pass: it runs the agent once more with a verdict-only directive
# and posts the verdict WITH the review marker, so a pull request that fixed its
# round-2 findings can clear itself instead of collecting a skip notice on every
# push. At the default ceiling of 4 this is round 5, so rounds_with 4 lands on it.
t_the_round_after_the_limit_is_a_closing_pass() {
  local sb; sb="$(new_sandbox)"
  rounds_with 4 > "$sb/fx/comments"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  # The whole point: the agent IS reached on the closing pass. On today's code
  # round 5 is past the ceiling of 2 and hard-skips, so this is the red the
  # feature turns green.
  assert_called claude
  # The run announces, in its log, that this is the closing pass. This is what
  # tells a closing pass apart from an ordinary round, so raising MAX_ROUNDS (the
  # mutation) turns round 5 back into an ordinary review and drops this line.
  assert_stdout_has_i "closing pass"
  # The verdict is posted WITH the review marker, so it is counted like any round
  # and the loop is bounded to exactly one closing pass.
  assert_comment_posted
  grep -qF -- 'APPROVE' "$SB/posted_body" \
    || fail "the closing pass did not post the reviewer's APPROVE verdict: $(cat "$SB/posted_body")"
  grep -qF -- '<!-- gophersys-review-agent -->' "$SB/posted_body" \
    || fail "the closing-pass review carries no marker, so the next push cannot count it"
}

# The closing pass is verdict-only by construction: its prompt tells the agent to
# judge only whether the prior findings are resolved and to open NO new finding.
# The stub drains the prompt into prompt_seen so that directive can be read back.
t_the_closing_pass_is_verdict_only() {
  local sb; sb="$(new_sandbox)"
  rounds_with 4 > "$sb/fx/comments"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  [ -f "$SB/prompt_seen" ] \
    || fail "the closing pass never ran: the agent was sent no prompt to inspect"
  grep -qiF -- 'no new finding' "$SB/prompt_seen" \
    || fail "the closing-pass prompt did not forbid new findings: $(cat "$SB/prompt_seen")"
}

# There is exactly ONE closing pass. Round MAX+2 and beyond are the hard-stop
# again: the agent is NOT reached, and a skip notice (not counted as a round) is
# left on the pull request. At the default ceiling of 4 this is round 6, so
# rounds_with 5 lands one past the closing pass.
t_after_the_closing_pass_no_further_review() {
  local sb; sb="$(new_sandbox)"
  rounds_with 5 > "$sb/fx/comments"
  run_review "$sb" ${CREDS[@]+"${CREDS[@]}"} -- 1
  assert_rc_zero
  # Past the single closing pass, spend stops: the agent is never reached again.
  assert_not_called claude
  assert_comment_posted
  grep -qF -- 'not reviewed' "$SB/posted_body" \
    || fail "the skip notice does not say the push was not reviewed: $(cat "$SB/posted_body")"
  grep -qF -- '<!-- gophersys-review-agent-skip -->' "$SB/posted_body" \
    || fail "the skip notice carries no skip marker: $(cat "$SB/posted_body")"
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
  t_an_unreadable_verdict_is_posted_and_then_fails
  t_request_changes_is_reported_as_request_changes
  t_approve_is_posted_with_the_guarded_flag_set
  t_a_markdown_verdict_is_understood
  t_a_verdict_inside_a_sentence_is_not_a_verdict
  t_a_verdict_may_carry_its_reason
  t_the_run_summary_and_the_stream_are_kept
  t_a_stream_with_no_result_event_is_refused
  t_an_agent_error_is_not_posted
  t_a_denied_read_is_posted_and_then_fails_the_job
  t_a_denied_bash_is_reported_but_does_not_fail_the_job
  t_the_agent_is_given_a_read_only_tool_set
  t_an_empty_stream_is_refused
  t_the_first_run_is_round_1
  t_a_second_run_is_round_2_and_sees_the_first
  t_the_round_ceiling_is_configurable
  t_the_posted_review_carries_the_marker
  t_a_round_limit_skip_posts_a_visible_notice
  t_the_skip_notice_is_not_counted_as_a_round
  t_the_round_after_the_limit_is_a_closing_pass
  t_the_closing_pass_is_verdict_only
  t_after_the_closing_pass_no_further_review
)

# MARKER_COVERAGE maps every marker in review.sh to the test that proves it.
# It is written out by hand on purpose: a mutation can narrow a pattern instead of
# deleting a line, so the marker name does not always appear in the sed
# expression, and matching on the expression text silently missed every
# substitution mutation.
MARKER_COVERAGE="
guard:usage                 t_usage_requires_pr_number
guard:tools                 t_missing_tool_is_named
guard:oauth                 t_missing_oauth_token
guard:gh-token              t_missing_gh_token
guard:api-key               t_api_key_outranks_the_token
guard:auth-token            t_auth_token_outranks_the_token
guard:instructions          t_missing_instructions
guard:temp-lifetime         t_temp_files_are_removed_when_the_run_fails
guard:empty-diff            t_empty_diff_is_refused
guard:empty-stream          t_an_empty_stream_is_refused
guard:no-result-event       t_a_stream_with_no_result_event_is_refused
guard:agent-error           t_an_agent_error_is_not_posted
guard:empty-output          t_empty_reviewer_output_is_not_posted
guard:verdict               t_an_unreadable_verdict_is_posted_and_then_fails
guard:verdict-unparsed      t_an_unreadable_verdict_is_posted_and_then_fails
guard:denials               t_a_denied_read_is_posted_and_then_fails_the_job
guard:round-limit           t_the_round_after_the_limit_is_a_closing_pass
guard:closing-pass          t_the_closing_pass_is_verdict_only
guard:closing-pass-once     t_after_the_closing_pass_no_further_review
guard:skip-notice           t_a_round_limit_skip_posts_a_visible_notice
guard:skip-uncounted        t_the_skip_notice_is_not_counted_as_a_round
verdict:request-changes     t_a_verdict_may_carry_its_reason
capture:summary             t_the_run_summary_and_the_stream_are_kept
capture:allowed-tools       t_the_agent_is_given_a_read_only_tool_set
verdict:approve             t_a_verdict_inside_a_sentence_is_not_a_verdict
  t_a_verdict_may_carry_its_reason
post:marker                 t_the_posted_review_carries_the_marker
post:comment                t_approve_is_posted_with_the_guarded_flag_set
"

# every_marker_is_covered fails when review.sh carries a marker that no test
# claims, or when a claimed test does not exist. The suite already proved that
# every test states how it is proven; this is the other direction, and that
# asymmetry is how guard:empty-stream once shipped with a marker, no test, and a
# suite reporting that all guards held.
every_marker_is_covered() {
  local marker name test uncovered=() missing=()
  while IFS= read -r marker; do
    name="${marker##*# }"
    test="$(awk -v m="$name" '$1 == m {print $2}' <<< "$MARKER_COVERAGE")"
    if [ -z "$test" ]; then
      uncovered+=("$name")
    elif ! grep -qF -- "$test" <<< "${TESTS[*]}"; then
      missing+=("$name -> $test")
    fi
  done < <(grep -oE '# (guard|capture|verdict|post):[a-z-]+' "$REVIEW_SH" | sort -u)
  [ "${#uncovered[@]}" -eq 0 ] || die "these markers in review.sh have no test: ${uncovered[*]}"
  [ "${#missing[@]}" -eq 0 ] || die "these markers name a test that does not exist: ${missing[*]}"
}

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
    t_the_run_summary_and_the_stream_are_kept)   printf '/# capture:summary$/d' ;;
    t_a_stream_with_no_result_event_is_refused)  printf '/# guard:no-result-event$/d' ;;
    t_an_agent_error_is_not_posted)              printf '/# guard:agent-error$/d' ;;
    t_a_denied_read_is_posted_and_then_fails_the_job) printf '/# guard:denials$/d' ;;
    # Widen the read set to everything, so a denied Bash also fails the job.
    t_a_denied_bash_is_reported_but_does_not_fail_the_job) printf 's|select(.tool_name == \"Read\" or .tool_name == \"Grep\" or .tool_name == \"Glob\")|select(true)|' ;;
    t_the_agent_is_given_a_read_only_tool_set)   printf '/# capture:allowed-tools$/d' ;;
    t_an_empty_stream_is_refused)                printf '/# guard:empty-stream$/d' ;;
    # Narrow the pattern back to a token that must stand entirely alone.
    t_a_verdict_may_carry_its_reason)            printf 's|^RE_REQUEST_CHANGES=.*|RE_REQUEST_CHANGES="^REQUEST_CHANGES[[:space:]]*$"|' ;;
    t_the_first_run_is_round_1)                  printf 's|^round=.*|round=99|' ;;
    t_a_second_run_is_round_2_and_sees_the_first) printf 's|^round=.*|round=1|' ;;
    # Raise the ceiling out of the way, so round MAX+1 is an ordinary review and
    # drops the "closing pass" log line the test reads.
    t_the_round_after_the_limit_is_a_closing_pass) printf 's|^MAX_ROUNDS=.*|MAX_ROUNDS=99|' ;;
    # Delete the line that injects the verdict-only directive into the prompt.
    t_the_closing_pass_is_verdict_only)          printf '/# guard:closing-pass$/d' ;;
    # Widen the hard-stop threshold so round MAX+2 runs a SECOND closing pass
    # instead of skipping; the guarded line is the `round > MAX_ROUNDS+1` test.
    t_after_the_closing_pass_no_further_review)  printf 's|^.*# guard:closing-pass-once$|if [ "$round" -gt 9999 ]; then|' ;;
    # Delete the post line, so the round-limit skip leaves nothing on the pull request.
    t_a_round_limit_skip_posts_a_visible_notice) printf '/# guard:skip-notice$/d' ;;
    # Swap the skip marker to the review marker, so the notice would be counted as a round.
    t_the_skip_notice_is_not_counted_as_a_round) printf 's|^SKIP_MARKER=.*|SKIP_MARKER="<!-- gophersys-review-agent -->"|' ;;
    t_the_round_ceiling_is_configurable)         printf 's|^MAX_ROUNDS=.*|MAX_ROUNDS=2|' ;;
    t_the_posted_review_carries_the_marker)      printf '/# post:marker$/d' ;;
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
    # Remove the line that FAILS the job, so an unreadable verdict passes silently.
    t_an_unreadable_verdict_is_posted_and_then_fails) printf '/# guard:verdict-unparsed$/d' ;;
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

every_marker_is_covered

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
