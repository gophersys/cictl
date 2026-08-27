#!/usr/bin/env bash
# Run the pull request review agent against the current checkout.
#
# Usage (inside a GitHub Actions job on the arc-review pool):
#   bash review/review.sh <pr-number>
#
# It reads the diff, runs the selected harness headless with the reviewer instructions, and
# posts the result as a pull request review.
#
# WHAT THIS MIRRORS
# The flag set comes from Eden's libs/go/agentsession/claudeadapter, which is the
# canonical headless invocation in this organization. It never uses
# --dangerously-skip-permissions, and it authenticates with an OAuth token, never
# an API key: the adapter scrubs ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN
# because a stray API key silently takes precedence over the token.
#
# EVERY GUARD IS PROVEN
# Each guard is 1 line and ends with a `# guard:<name>` marker. review_test.sh
# deletes the marked line, 1 guard at a time, and requires the matching test to
# FAIL. A guard with no marker is a guard that nobody proves. Keep each guard on
# 1 line so that the removal is atomic.
set -Eeuo pipefail
IFS=$'\n\t'

PR="${1:-}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HARNESS="${REVIEW_HARNESS:-claude}" # capture:harness
# The default model is the CLI's FAMILY ALIAS, not a pinned id: `opus` tracks
# the newest Opus as the pinned CLI advances, so the reviewer never silently
# ages while the organization's CLI moves. The summary logs what the alias
# RESOLVED to, read from the stream. An explicit REVIEW_MODEL still wins, so a
# pinned review (a bisect, a cost cap, a regression hunt) stays possible.
MODEL="${REVIEW_MODEL:-opus}" # capture:model
if [ "$HARNESS" = "codex" ] && [ -z "${REVIEW_MODEL:-}" ]; then
  MODEL=default
fi
BUDGET_USD="${REVIEW_BUDGET_USD:-25}"
# THE BUDGET IS THE LIMITER, NOT THE TURN COUNT. At 40 the reviewer was cut off
# MID-TOOL-CALL on real pull requests and the run died with NO VERDICT: the
# budget was spent, nothing was posted as a review, and the only thing standing
# between that and a silent pass is the no-verdict guard below. A cap that is
# reached in normal operation is not a safety net, it is a defect generator, so
# the default is raised to a ceiling a healthy review never approaches and
# --max-budget-usd remains the real ceiling (Mateo's ruling, 2026-08-26).
# REVIEW_MAX_TURNS still wins, so a deliberately short run stays possible.
MAX_TURNS="${REVIEW_MAX_TURNS:-500}" # capture:max-turns
# The round ceiling Mateo asked for: bounded, and configurable in 1 place.
MAX_ROUNDS="${REVIEW_MAX_ROUNDS:-4}" # guard:round-limit
# The footer that marks a comment as this agent's. It is how a round is counted,
# so it must be posted with every review and must never be edited by hand.
MARKER="<!-- gophersys-review-agent -->"
# The footer for an UNcounted notice: the round-limit skip, and the no-verdict
# death. It is DISTINCT from MARKER on purpose: the round counter selects
# comments that contain MARKER, and `agent-skip -->` never holds the contiguous
# `agent -->`, so a notice is visible to a human but never counted as a review.
# If it carried MARKER, the next push would count the notice and the ceiling
# would cascade.
SKIP_MARKER="<!-- gophersys-review-agent-skip -->" # guard:skip-uncounted
# The full event stream. The workflow keeps it as an artifact, so it must outlive
# the run and therefore is NOT a temp file.
STREAM_FILE="${REVIEW_STREAM_FILE:-review-stream.jsonl}"
# Every tool the reviewer may use. Read-only by construction: nothing here can
# write to the tree, post a comment or merge anything.
READ_ONLY_TOOLS="${REVIEW_ALLOWED_TOOLS:-Read,Grep,Glob,Bash(gh pr view:*),Bash(gh pr diff:*),Bash(gh api:*),Bash(git log:*),Bash(git show:*),Bash(git diff:*),Bash(ls:*),Bash(cat:*),Bash(rg:*),Bash(find:*)}" # capture:allowed-tools

log() { printf '\033[0;36m[review]\033[0m %s\n' "$*"; }
die() { printf '\033[0;31m[review]\033[0m %s\n' "$*" >&2; exit 1; }

# Fail loudly on a missing tool or a missing credential. A review step that
# silently does nothing is worse than no review step, because it is believed.
require_tools() {
  local missing=()
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || missing+=("$c")
  done
  [ "${#missing[@]}" -eq 0 ] || die "missing required tool(s): ${missing[*]}"
}
require_env() { [ -n "${!1:-}" ] || die "$1 is empty. $2"; }
# An API key would silently win over the OAuth token. Refuse rather than review
# under a credential that nobody chose.
refuse_env() { [ -z "${!1:-}" ] || die "$1 is set; it would take precedence over the OAuth token"; }

[ -n "$PR" ] || die "usage: bash review/review.sh <pr-number>" # guard:usage

require_env GH_TOKEN "The review cannot be posted without it." # guard:gh-token
case "$HARNESS" in
  claude)
    require_tools claude gh git jq # guard:tools
    require_env CLAUDE_CODE_OAUTH_TOKEN "The review pool sets it from the claude-review-token Secret. This job must NOT pass silently." # guard:oauth
    refuse_env ANTHROPIC_API_KEY # guard:api-key
    refuse_env ANTHROPIC_AUTH_TOKEN # guard:auth-token
    ;;
  codex)
    require_tools codex gh git jq # guard:codex-tools
    require_env CODEX_HOME "The review pool mounts its private writable Codex profile here." # guard:codex-home
    [ -s "$CODEX_HOME/auth.json" ] || die "CODEX_HOME/auth.json is missing or empty. The review pool must seed it from codex-review-auth." # guard:codex-auth
    refuse_env OPENAI_API_KEY # guard:openai-api-key
    ;;
  *) die "unknown review harness '$HARNESS' (want claude or codex)" ;; # guard:harness
esac
# `$(cat ...)` on a missing file expands to the empty string and does not stop
# the script. Without this line a lost REVIEW.md buys a full-price review that
# had no instructions.
[ -f "$HERE/REVIEW.md" ] || die "the reviewer instructions are missing: $HERE/REVIEW.md" # guard:instructions

log "reviewing pull request #${PR} with ${HARNESS}/${MODEL}"

# Create every temp file BEFORE the trap. A trap that names a variable which is
# not yet set aborts on `set -u`, removes nothing, and leaks the file that holds
# the complete diff on the runner.
diff_file="$(mktemp)"; files_file="$(mktemp)"; prompt_file="$(mktemp)"; out_file="$(mktemp)"; err_file="$(mktemp)" # guard:temp-lifetime
trap 'rm -f "$diff_file" "$files_file" "$prompt_file" "$out_file" "$err_file"' EXIT

gh pr diff "$PR" > "$diff_file" || die "cannot read the diff for #${PR}"
[ -s "$diff_file" ] || die "the diff for #${PR} is empty" # guard:empty-diff
gh pr view "$PR" --json files > "$files_file" || die "cannot read the changed-file manifest for #${PR}"
log "diff: $(wc -l < "$diff_file") lines"

# Round 2 sees the previous review, so it can judge whether each finding is now
# resolved instead of repeating it.
#
# Count COMMENTS, not reviews. This script posts with `gh pr comment`, which
# creates an issue comment and never a formal review, so a version of this that
# read `--json reviews` found nothing every time: every run was round 1, the
# agent never saw its own earlier findings, and the round ceiling never engaged.
# Each push then bought a fresh full-price review. MARKER is what makes a comment
# ours, so a human quoting the agent cannot be counted as a round.
previous="$(gh pr view "$PR" --json comments \
  --jq "[.comments[] | select(.body | contains(\"$MARKER\")) | .body] | last // \"\"" 2>/dev/null || true)"
rounds_done="$(gh pr view "$PR" --json comments \
  --jq "[.comments[] | select(.body | contains(\"$MARKER\"))] | length" 2>/dev/null || echo 0)"
round=$(( rounds_done + 1 ))
# After the ceiling the loop does not go silent forever. There are 3 cases now,
# split on $round:
#   1..MAX_ROUNDS       an ordinary review, unchanged (the prompt block below).
#   MAX_ROUNDS + 1      ONE closing pass: the same agent path, with a verdict-only
#                       directive that judges the prior findings and opens none. It
#                       posts with MARKER, so it is counted and cannot repeat.
#   MAX_ROUNDS + 2 ...  the hard stop. Spend is over; leave a visible, UNcounted
#                       skip notice and exit green.
#
# Reaching the hard stop is the design working, not a failure. The first version
# exited 1 here, which turned every pull request that took another push red for
# the rest of its life — and a check that is permanently red for a correct reason
# is exactly what teaches people to ignore red.
if [ "$round" -gt "$(( MAX_ROUNDS + 1 ))" ]; then # guard:closing-pass-once
  log "${rounds_done} review(s) already posted on #${PR}; the ${MAX_ROUNDS}-round limit and its closing pass are spent, so this push is not reviewed"
  # A green pr-review check with nothing on the pull request reads as a review
  # that happened. Leave a visible trace instead of only a runner-log line: state
  # that the push was not reviewed, why, and the remedy. SKIP_MARKER, not MARKER,
  # keeps this out of the round count.
  {
    printf 'This push was **not reviewed**.\n\n'
    printf 'The pull request has reached the %s-round review limit, so the review\n' "$MAX_ROUNDS"
    printf 'agent did not run on this push.\n\n'
    # shellcheck disable=SC2016  # backticks are markdown for the PR, not a subshell
    printf 'Review this push manually, or raise `REVIEW_MAX_ROUNDS` to allow another round.\n'
    printf '\n%s\n' "$SKIP_MARKER"
  } > "$out_file"
  gh pr comment "$PR" --body-file "$out_file" || die "could not post the skip notice" # guard:skip-notice
  exit 0
fi

# Round MAX_ROUNDS + 1 is the single closing pass. It runs the ordinary agent path
# below; the only difference is the verdict-only directive added to the prompt.
closing_pass=no
if [ "$round" -eq "$(( MAX_ROUNDS + 1 ))" ]; then
  closing_pass=yes
  log "round ${round} is past the ${MAX_ROUNDS}-round limit; running a single closing pass to judge whether the prior findings are resolved"
fi

{
  printf 'Review pull request #%s in %s. This is round %s of %s.\n\n' \
    "$PR" "${GITHUB_REPOSITORY:-this repository}" "$round" "$MAX_ROUNDS"
  printf 'Title: %s\n\n' "$(gh pr view "$PR" --json title --jq .title)"
  printf 'Description:\n%s\n\n' "$(gh pr view "$PR" --json body --jq '.body // "(none)"')"
  if [ "$closing_pass" = "yes" ]; then
    printf 'YOUR PREVIOUS REVIEW:\n%s\n\n' "$previous"
    printf 'CLOSING PASS. Judge ONLY whether the prior review findings are resolved. Open no new finding under any circumstance. APPROVE if all are resolved, else REQUEST_CHANGES.\n\n' # guard:closing-pass
  elif [ "$round" -gt 1 ]; then
    printf 'YOUR PREVIOUS REVIEW:\n%s\n\n' "$previous"
    printf 'Judge only whether each earlier finding is now resolved. Do not open a\n'
    printf 'new front unless the fix introduced it.\n\n'
  fi
  printf 'THE DIFF:\n%s\n' "$(cat "$diff_file")"
} > "$prompt_file"

# Codex CI deliberately has no shell or network tools, so runner credentials are
# outside the model's reach. Supply bounded full-file context from the parent
# process instead. Paths come from the already-fetched diff, must remain relative,
# and are capped so one generated file cannot consume the entire context window.
if [ "$HARNESS" = "codex" ]; then
  context_bytes=0
  while IFS= read -r encoded_path; do
    context_path="$(jq -r . <<< "$encoded_path")"
    case "$context_path" in /*|*'..'*) continue ;; esac
    if [ ! -f "$context_path" ] || [ -L "$context_path" ]; then
      continue
    fi
    file_bytes="$(wc -c < "$context_path")"
    [ "$file_bytes" -le 262144 ] || continue
    [ "$(( context_bytes + file_bytes ))" -le 1048576 ] || continue
    grep -Iq . "$context_path" || continue
    {
      printf '\nFULL CHANGED FILE: %s\n```\n' "$context_path"
      cat "$context_path"
      printf '\n```\n'
    } >> "$prompt_file"
    context_bytes=$(( context_bytes + file_bytes ))
  done < <(jq -c '.files[].path' "$files_file")
  log "Codex context: ${context_bytes} bytes of full changed files (1 MiB total, 256 KiB/file caps)"
fi

# --max-budget-usd is the real ceiling, not a proxy. --permission-mode default
# keeps the agent from editing anything: it reads and reports.
#
# READ_ONLY_TOOLS states what the reviewer may do, rather than leaving it to the
# default to deny whatever it happens to deny. On the first run under the new
# capture, 4 Bash calls were denied and every one of them was the agent trying to
# read the cross-repository pull request that the description referenced. It was
# doing exactly the right thing and could not. Each entry is read-only: `gh pr
# view` cannot comment and cannot merge, and no entry can write to the tree.
#
# The whole event stream is kept, not only the final text. When a review came
# back wrong there was no record of which files the agent read, how many turns it
# took or what it cost against the ceiling, so the only way to learn anything was
# to pay for another run. The workflow keeps STREAM_FILE as an artifact.
#
# stderr goes to its own file. Merging it into stdout would corrupt the JSONL.
if [ "$HARNESS" = "claude" ]; then
  set +e
  claude -p \
    --model "$MODEL" \
    --max-budget-usd "$BUDGET_USD" \
    --max-turns "$MAX_TURNS" \
    --permission-mode default \
    --allowedTools "$READ_ONLY_TOOLS" \
    --output-format stream-json --verbose \
    --append-system-prompt "$(cat "$HERE/REVIEW.md")" \
    < "$prompt_file" > "$STREAM_FILE" 2> "$err_file"
  rc=$?
  set -e
else
  printf '\nHARNESS LIMIT: ChatGPT-managed Codex has no per-run dollar or turn ceiling. The CI job timeout is the hard wall; finish with a verdict before it.\n' >> "$prompt_file"
  printf '\nREVIEWER INSTRUCTIONS:\n%s\n' "$(cat "$HERE/REVIEW.md")" >> "$prompt_file"
  set +e
  bash "$HERE/run-codex.sh" "$prompt_file" "$STREAM_FILE" "$err_file"
  rc=$?
  set -e
fi

[ -s "$STREAM_FILE" ] || die "the reviewer produced no events (exit ${rc}): $(tail -5 "$err_file")" # guard:empty-stream

# The result event carries the final text and every number worth knowing. Without
# it the run did not finish, whatever the exit code says.
result="$(jq -c 'select(.type=="result")' "$STREAM_FILE" | tail -1)"
[ -n "$result" ] || die "the stream has no result event (exit ${rc}); the run did not finish: $(tail -5 "$err_file")" # guard:no-result-event

if [ "$HARNESS" = "claude" ]; then
  log "  cost      \$$(jq -r '(.total_cost_usd // 0) * 100 | round / 100' <<< "$result") of \$${BUDGET_USD}" # capture:summary
  log "  turns     $(jq -r '.num_turns // 0' <<< "$result") of ${MAX_TURNS}"
  log "  duration  $(jq -r '(.duration_ms // 0) / 1000 | round' <<< "$result")s"
  log "  stop      $(jq -r '.stop_reason // .terminal_reason // "n/a"' <<< "$result")"
else
  log "  usage     $(jq -r 'select(.type=="turn.completed") | .usage | "input=\(.input_tokens // 0), cached=\(.cached_input_tokens // 0), output=\(.output_tokens // 0)"' "$STREAM_FILE" | tail -1)"
  log "  limit     job timeout (Codex membership exposes no dollar or turn ceiling)"
fi
# What the model alias RESOLVED to. The init event is where the CLI states the
# actual model id; without it the log states the alias and says the stream
# artifact is the record — it never invents a resolution.
resolved="$(jq -r 'select(.type=="system" and .subtype=="init") | .model // empty' "$STREAM_FILE" | tail -1)"
if [ -n "$resolved" ]; then
  log "  model     ${MODEL} (resolved: ${resolved})"
else
  log "  model     ${MODEL} (no resolved model in the stream; the ${STREAM_FILE} artifact is the record)"
fi
# Which tools it used. A shallow review almost always means it read very little,
# and this is the line that shows it.
log "  tools     $(jq -rs '[.[] | select(.type=="assistant") | .message.content[]? | select(.type=="tool_use") | .name] | if length == 0 then "none" else (group_by(.) | map("\(.[0]) x\(length)") | join(", ")) end' "$STREAM_FILE")"

# `jq -r` prints a trailing newline even for an empty string, so `-s` on the file
# would call a 1-byte empty review "present". Test the text itself, whitespace
# stripped, and only then write it.
text="$(jq -r '.result // ""' <<< "$result")"

# THE DEATH WITH NO VERDICT LEAVES A TRACE. A run can end with no final text at
# all: at MAX_TURNS the agent is cut off mid-tool-call (stop: tool_use) and the
# CLI emits an ERROR-VARIANT result event — subtype error_max_turns, is_error
# true, and NO .result field, because no error variant carries one (verified
# against claude 2.1.229). That is why this stands BEFORE the error guard:
# behind it, the error guard would eat every turn-cap death and the notice
# could never be posted. A success-shaped result whose text is empty lands here
# too. Either way the budget is spent and there is no verdict. The first version
# died here with only a runner-log line: the pull request showed nothing and the
# red check never said why. So the death now posts a visible notice — with
# SKIP_MARKER, never MARKER, so it consumes no round — naming the spend, the
# turns against the cap and the stop reason, and the remedy: re-run the check,
# or review the push manually. Then the job still fails, because a spent budget
# with no verdict is a failure, never a pass.
if [ -z "${text//[[:space:]]/}" ]; then
  cost="$(jq -r '(.total_cost_usd // 0) * 100 | round / 100' <<< "$result")"
  turns="$(jq -r '.num_turns // 0' <<< "$result")"
  stop="$(jq -r '.stop_reason // .terminal_reason // "n/a"' <<< "$result")"
  {
    printf 'This push was **not reviewed**: the review died with **no verdict**.\n\n'
    if [ "$HARNESS" = "claude" ]; then
      printf 'The reviewer spent $%s of its $%s budget and stopped after %s of %s turns (stop: %s) without writing a review, so there is no review and no verdict to post.\n\n' "$cost" "$BUDGET_USD" "$turns" "$MAX_TURNS" "$stop"
    else
      printf 'The Codex reviewer stopped without writing a review. Its hard wall is the CI job timeout; ChatGPT membership exposes no dollar or turn ceiling.\n\n'
    fi
    printf 'Re-run the pr-review check, or review this push manually.\n'
    printf '\n%s\n' "$SKIP_MARKER"
  } > "$out_file"
  gh pr comment "$PR" --body-file "$out_file" || die "could not post the no-verdict death notice" # guard:no-verdict
  if [ "$HARNESS" = "claude" ]; then
    :
    death_message="the reviewer spent \$${cost} and died with no verdict after ${turns} of ${MAX_TURNS} turns (stop: ${stop}, exit ${rc}). The notice is on the pull request; re-run the review, or review the push manually." # guard:empty-output
  else
    death_message="the Codex reviewer died with no verdict (exit ${rc}). The notice is on the pull request; re-run the review, or review the push manually."
  fi
  die "$death_message"
fi

[ "$(jq -r '.is_error // false' <<< "$result")" != "true" ] || die "the reviewer reported an error (stop: $(jq -r '.stop_reason // .terminal_reason // "unknown"' <<< "$result")). Not posting." # guard:agent-error

printf '%s\n' "$text" > "$out_file"
log "review: $(wc -c < "$out_file") bytes"

# The verdict decides how the review is posted. An unparseable verdict is a
# failure: a review with no verdict cannot end the loop.
# Match the verdict on its own line, tolerating markdown. The first version
# required a bare line and rejected a real 3281-byte review because the model
# wrote the verdict with emphasis: a heading marker, a blockquote marker, a code
# span and trailing punctuation all appear in practice.
#
# The START anchor is the load-bearing part. The token must OPEN its line, which
# is what stops a verdict being read out of a sentence such as "I would APPROVE
# this if you fixed the test".
#
# It may carry its reason after a dash or a colon. Requiring the token to stand
# entirely alone refused 2 real reviews: 1 written with markdown emphasis, and 1
# written as "**REQUEST_CHANGES** — D44 should be marked resolved before this
# merges". Each refusal threw away a paid review over punctuation.
#
# Both properties have their own test, and each test's mutation narrows or widens
# the pattern below rather than deleting it, because a deleted pattern proves only
# that the branch exists.
# shellcheck disable=SC2016  # a regex, not a string to expand
RE_REQUEST_CHANGES='^[[:space:]]*[*_`#> ]*REQUEST_CHANGES[*_`]*([[:space:]]*[—:.-].*)?[[:space:]]*$' # verdict:request-changes
# shellcheck disable=SC2016  # a regex, not a string to expand
RE_APPROVE='^[[:space:]]*[*_`#> ]*APPROVE[*_`]*([[:space:]]*[—:.-].*)?[[:space:]]*$' # verdict:approve
#
# THE REVIEW IS THE PRODUCT. THE VERDICT IS METADATA.
#
# 2 real reviews were thrown away because their verdict line did not match: 1
# carried markdown emphasis, 1 carried a trailing reason. Each time a paid run
# was discarded, the pull request got nothing, and a human had to notice.
#
# That trade is always wrong. A review that names a real defect is worth having
# even when its last line is punctuated oddly. So an unparseable verdict NEVER
# discards the review now: the body is posted, the job records that the verdict
# could not be read, and it FAILS on that. The work is kept and the defect is
# still loud.
verdict_parsed=yes
if grep -qE "$RE_REQUEST_CHANGES" "$out_file"; then
  event=REQUEST_CHANGES
elif grep -qE "$RE_APPROVE" "$out_file"; then
  event=APPROVE
else
  verdict_parsed=no # guard:verdict
  # REQUEST_CHANGES is the safe reading of an unreadable verdict: it asks a human
  # to look, where APPROVE would wave the change through on a guess.
  event=REQUEST_CHANGES
  log "the verdict line could not be read, so this review is posted as ${event} and the job will fail"
  printf '\n\n---\n\n> The verdict line of this review could not be read automatically, so it\n> was recorded as REQUEST_CHANGES. The findings above are unchanged. The\n> reviewer should end with a line that OPENS with APPROVE or REQUEST_CHANGES.\n' >> "$out_file"
fi
log "verdict: ${event}"

# GITHUB_TOKEN cannot APPROVE its own repository's pull request, and an approval
# from it would not satisfy a required review anyway. Post the body as a comment
# and let the verdict line carry the meaning.
printf '\n\n%s\n' "$MARKER" >> "$out_file" # post:marker
gh pr comment "$PR" --body-file "$out_file" || die "could not post the review comment" # post:comment

log "posted round ${round} of ${MAX_ROUNDS} review on #${PR}"
log "full event stream: ${STREAM_FILE}"
[ "$event" = "APPROVE" ] || log "changes requested — fix the findings and push again"

# The review is posted by now, so failing here loses nothing. It stays loud
# because a verdict nobody can read means the loop cannot end on its own.
[ "$verdict_parsed" = "yes" ] || die "the review was posted, but its verdict line could not be read. Fix the reviewer instructions or the matcher." # guard:verdict-unparsed

# Not every denial is a defect, and the first version of this got it wrong.
#
# The reviewer runs under an explicit read-only allowlist. A denied Bash call is
# that policy WORKING: a piped or compound command can never match a prefix rule,
# so the agent reaching for `grep x f | head` is refused by design. Failing the
# job for that punishes the policy for being enforced.
#
# What must never pass quietly is a denial of Read, Grep or Glob. Those are the
# tools the reviewer needs in order to see the code at all, the allowlist grants
# all 3, and a denial of one means the policy is not being applied as written —
# so the review is genuinely shallower than it looks.
denied_reads="$(jq -r '[(.permission_denials // [])[] | select(.tool_name == "Read" or .tool_name == "Grep" or .tool_name == "Glob")] | length' <<< "$result")"
denied_other="$(jq -r '[(.permission_denials // [])[] | select(.tool_name != "Read" and .tool_name != "Grep" and .tool_name != "Glob") | .tool_name] | unique | join(", ")' <<< "$result")"
[ -z "$denied_other" ] || log "  denied    ${denied_other} — outside the read-only allowlist, which is the policy working"
[ "${denied_reads:-0}" -eq 0 ] || die "${denied_reads} read tool call(s) were denied, so the reviewer could not see the code: the allowlist grants Read, Grep and Glob and is not being applied" # guard:denials
exit 0
