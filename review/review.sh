#!/usr/bin/env bash
# Run the pull request review agent against the current checkout.
#
# Usage (inside a GitHub Actions job on the arc-review pool):
#   bash review/review.sh <pr-number>
#
# It reads the diff, runs Claude headless with the reviewer instructions, and
# posts the result as a pull request review.
#
# WHAT THIS MIRRORS
# The flag set comes from Eden's libs/go/agentsession/claudeadapter, which is the
# canonical headless invocation in this organization. It never uses
# --dangerously-skip-permissions, and it authenticates with an OAuth token, never
# an API key: the adapter scrubs ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN
# because a stray API key silently takes precedence over the token.
set -Eeuo pipefail
IFS=$'\n\t'

PR="${1:-}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODEL="${REVIEW_MODEL:-claude-opus-4-5}"
BUDGET_USD="${REVIEW_BUDGET_USD:-25}"
MAX_TURNS="${REVIEW_MAX_TURNS:-40}"

log()  { printf '\033[0;36m[review]\033[0m %s\n' "$*"; }
die()  { printf '\033[0;31m[review]\033[0m %s\n' "$*" >&2; exit 1; }

[ -n "$PR" ] || die "usage: bash review/review.sh <pr-number>"

# Fail loudly on a missing tool or a missing credential. A review step that
# silently does nothing is worse than no review step, because it is believed.
for c in claude gh git jq; do
  command -v "$c" >/dev/null 2>&1 || die "missing required tool: $c"
done
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] || die \
  "CLAUDE_CODE_OAUTH_TOKEN is empty. The review pool sets it from the
   claude-review-token Secret. This job must NOT pass silently."
[ -n "${GH_TOKEN:-}" ] || die "GH_TOKEN is empty; the review cannot be posted"

# An API key would silently win over the OAuth token. Refuse rather than review
# under a credential nobody chose.
for k in ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN; do
  [ -z "${!k:-}" ] || die "$k is set; it would take precedence over the OAuth token"
done

log "reviewing pull request #${PR} with ${MODEL}, budget \$${BUDGET_USD}"

diff_file="$(mktemp)"; trap 'rm -f "$diff_file" "$out_file" "$prompt_file"' EXIT
gh pr diff "$PR" > "$diff_file" || die "cannot read the diff for #${PR}"
[ -s "$diff_file" ] || die "the diff for #${PR} is empty"
log "diff: $(wc -l < "$diff_file") lines"

# Round 2 sees the previous review, so it can judge whether each finding is now
# resolved instead of repeating it.
previous="$(gh pr view "$PR" --json reviews \
  --jq '[.reviews[] | select(.author.login == "github-actions") | .body] | last // ""' 2>/dev/null || true)"
round=1
[ -n "$previous" ] && round=2

prompt_file="$(mktemp)"
{
  printf 'Review pull request #%s in %s. This is round %s of 2.\n\n' \
    "$PR" "${GITHUB_REPOSITORY:-this repository}" "$round"
  printf 'Title: %s\n\n' "$(gh pr view "$PR" --json title --jq .title)"
  printf 'Description:\n%s\n\n' "$(gh pr view "$PR" --json body --jq '.body // "(none)"')"
  if [ "$round" = "2" ]; then
    printf 'YOUR PREVIOUS REVIEW:\n%s\n\n' "$previous"
    printf 'Judge only whether each earlier finding is now resolved. Do not open a\n'
    printf 'new front unless the fix introduced it.\n\n'
  fi
  printf 'THE DIFF:\n%s\n' "$(cat "$diff_file")"
} > "$prompt_file"

out_file="$(mktemp)"
# --max-budget-usd is the real ceiling, not a proxy. --permission-mode default
# keeps the agent from editing anything: it reads and reports.
set +e
claude -p \
  --model "$MODEL" \
  --max-budget-usd "$BUDGET_USD" \
  --max-turns "$MAX_TURNS" \
  --permission-mode default \
  --append-system-prompt "$(cat "$HERE/CLAUDE.md")" \
  < "$prompt_file" > "$out_file" 2>&1
rc=$?
set -e

if [ ! -s "$out_file" ]; then
  die "the reviewer produced no output (exit ${rc}). Failing rather than posting an empty review."
fi
log "reviewer exited ${rc}, $(wc -c < "$out_file") bytes"

# The verdict decides how the review is posted. An unparseable verdict is a
# failure: a review with no verdict cannot end the loop.
# Match the verdict on its own line, tolerating markdown. The first version
# required a bare line and rejected a real 3281-byte review because the model
# wrote the verdict with emphasis. The token must still stand alone on the line:
# that is what stops a verdict being matched from inside a sentence such as
# "I would APPROVE this if ...".
# shellcheck disable=SC2016  # a regex, not a string to expand
verdict_re='^[[:space:]]*[*_`#> ]*(APPROVE|REQUEST_CHANGES)[*_`.: ]*[[:space:]]*$'
if grep -qE "$verdict_re" "$out_file"; then
  if grep -qE "^[[:space:]]*[*_\`#> ]*REQUEST_CHANGES" "$out_file"; then
    event=REQUEST_CHANGES
  else
    event=APPROVE
  fi
else
  # Print the tail before failing. The first version discarded the output, so a
  # rejected review could only be diagnosed by paying for another one.
  echo "--- last 20 lines of the reviewer output ---" >&2
  tail -20 "$out_file" >&2
  echo "--- end ---" >&2
  die "the reviewer returned no verdict line (APPROVE or REQUEST_CHANGES). Not posting."
fi
log "verdict: ${event}"

# GITHUB_TOKEN cannot APPROVE its own repository's pull request, and an approval
# from it would not satisfy a required review anyway. Post the body as a comment
# and let the verdict line carry the meaning.
gh pr comment "$PR" --body-file "$out_file" \
  || die "could not post the review comment"

log "posted round ${round} review on #${PR}"
[ "$event" = "APPROVE" ] || log "changes requested — fix the findings and push again"
exit 0
