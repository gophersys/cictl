#!/usr/bin/env bash
# The preflight of the gophersys review composite action.
#
# It validates every input, then runs the reviewer FROM THIS DIRECTORY. That last
# part is the whole pin: the runner checked this repository out at the caller's
# `uses:` ref into $GITHUB_ACTION_PATH, so the script beside this one is the
# commit the caller asked for. There is nothing to fetch and nothing to verify.
#
# WHY VALIDATE HERE. review.sh spawns a paid agent. An unknown model or a
# malformed budget must cost nothing, so every input is checked before the first
# dollar. A bad input is a failure and never a fallback to a default: a default
# chosen here would pick somebody's spend for them, silently.
#
# EVERY GUARD IS PROVEN. Each guard is 1 line and ends with a `# guard:<name>`
# marker. review/action_test.sh deletes the marked line, 1 guard at a time, and
# requires the matching test to FAIL. A guard with no marker is a guard nobody
# proves. Keep each guard on 1 line so the removal is atomic.
set -Eeuo pipefail
IFS=$'\n\t'

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log() { printf '\033[0;36m[review-action]\033[0m %s\n' "$*"; }
die() { printf '\033[0;31m[review-action]\033[0m %s\n' "$*" >&2; exit 1; }

# The action's own source must be beside this file. This is the composite-action
# replacement for the old pin guard: the ref is the pin, and running the script
# from $GITHUB_ACTION_PATH is what makes the pinned source the source that runs.
# A missing review.sh means the checkout is not what it claims to be.
[ -f "$HERE/review.sh" ] || die "the reviewer is not beside this action: $HERE/review.sh is missing. The runner checks this repository out at the caller's uses: ref, so an absent reviewer means the checkout is not the pinned commit." # guard:source

# A pull request number, and nothing that could be a flag or a path.
[[ "${REVIEW_PR:-}" =~ ^[1-9][0-9]*$ ]] || die "pr must be a positive whole number, got '${REVIEW_PR:-}'" # guard:pr

case "${REVIEW_HARNESS:-}" in
  claude | codex) : ;; # capture:harness
  *) die "harness must be 'claude' or 'codex', got '${REVIEW_HARNESS:-}'" ;; # guard:harness
esac

# The model. `opus` and `sonnet` are the CLI FAMILY ALIASES: each tracks the
# newest model of its family as the pinned CLI advances, which is why the presets
# use them and not a pinned id — a pinned id is a second version home that ages
# silently, and today's would be claude-opus-5 / claude-sonnet-5. An explicit
# `claude-*` id is still accepted, for a deliberately pinned review. Anything
# else is refused rather than handed to a paid agent.
if [[ "$REVIEW_HARNESS" == "claude" ]]; then
  case "${REVIEW_MODEL:-}" in
    default) REVIEW_MODEL=opus; export REVIEW_MODEL ;;
    opus | sonnet | claude-*) : ;; # capture:model
    *) die "Claude model must be 'default', 'opus', 'sonnet', or an explicit claude-* id; got '${REVIEW_MODEL:-}'" ;; # guard:model
  esac
elif [[ ! "${REVIEW_MODEL:-}" =~ ^(default|[A-Za-z0-9._-]+)$ ]]; then
  die "Codex model must be 'default' or a model identifier; got '${REVIEW_MODEL:-}'" # guard:codex-model
fi

# The spend ceiling, in whole dollars. Zero is refused: a review that cannot
# spend anything produces no verdict and still occupies a runner slot.
[[ "${REVIEW_BUDGET_USD:-}" =~ ^[1-9][0-9]*$ ]] || die "budget-usd must be a positive whole number of dollars, got '${REVIEW_BUDGET_USD:-}'" # guard:budget

# The round ceiling. Zero rounds would send every push straight to the hard stop.
[[ "${REVIEW_MAX_ROUNDS:-}" =~ ^[1-9][0-9]*$ ]] || die "max-rounds must be a positive whole number, got '${REVIEW_MAX_ROUNDS:-}'" # guard:rounds

# The stream destination is validated HERE, before the review runs, and not only
# in the archive step. The archive step runs after a full-price review; refusing
# a typo'd destination then would waste the round it was supposed to record.
case "${REVIEW_STREAM_STORE:-minio}" in
  minio | none) : ;;
  *) die "stream-store must be 'minio' or 'none', got '${REVIEW_STREAM_STORE:-}'" ;; # guard:stream-store
esac

log "reviewer: ${HERE}/review.sh"
log "harness ${REVIEW_HARNESS}, model ${REVIEW_MODEL}, budget \$${REVIEW_BUDGET_USD}, ${REVIEW_MAX_ROUNDS} round(s)"

exec bash "$HERE/review.sh" "$REVIEW_PR"
