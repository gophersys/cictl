# review-round-limit-visible

phase:    plan
repo:     gophersys/cictl
branch:   fix/review-round-limit-visible
worktree: ~/code/.worktrees/cictl-review-limit-visible
pr:       -
attempt:  0/2

## Goal
The pr-review agent goes GREEN without reviewing when the round limit is hit, and nothing on the
PR says so — a merger sees a green `pr-review` check and assumes the push was reviewed when it was
not. This makes the skip VISIBLE on the PR (an honest green), without turning the check permanently
red (the author's deliberate, correct choice). When done, a round-limit skip leaves a comment on
the PR stating the push was not reviewed and why.

## Plan
(dev-planner to produce, then approved here)

## Proven
- CONFIRMED present (2026-08-14) at `review/review.sh:102-105`:
  `if [ "$round" -gt "$MAX_ROUNDS" ]; then log "…not reviewed"; exit 0; fi`
  It logs ONLY to the runner log (unread on a green check) and posts NOTHING on the PR. The check
  goes green (exit 0) with no review and no PR-visible trace.
- The author's intent is explicit (comment L100-101): NOT permanently red, because "a check that
  is permanently red for a correct reason is exactly what teaches people to ignore red." The fix
  must PRESERVE that (stay green) and only close the silent-skip gap.

## The subtlety the planner MUST handle (round-counting)
Rounds are counted by MARKER comments: `MARKER="<!-- gophersys-review-agent -->"` (L34), and a
round is the count of comments carrying that marker. If the skip-notice comment carries the SAME
marker, it will INFLATE the round count on the next push (a self-cascading miscount). So the skip
notice must be posted so it is VISIBLE to a human but NOT counted as a review round — e.g. a
comment with a DISTINCT sentinel the round-counter excludes, or a mechanism that the counting
query does not match. The planner must trace how rounds are counted (the `gh` search for the
marker) and choose a form that does not corrupt it. Idempotency: re-running the skip path must not
post duplicate notices every push (guard on an existing skip-notice for this HEAD sha, or accept
one-per-push — planner to decide, stated).

## Fix direction (not the plan)
On the round-limit branch, before `exit 0`, post a PR comment stating the push was not reviewed,
the round limit reached, and the remedy (review manually or bump `REVIEW_MAX_ROUNDS`). Keep exit 0.
The comment must be visible on the PR and must NOT be counted as a review round.

## Blocked
Nothing. cictl is quiet (0 open PRs). Non-Mateo.

## Next
dev-planner: trace the exact round-counting query (how MARKER comments are counted), design the
visible-but-uncounted skip notice, name the files (review/review.sh + review_test.sh), and give the
red/green test recipe (assert: on the limit path, a skip comment IS posted AND the round count is
NOT incremented by it). Ownership: review_test.sh → dev-test-author; review.sh → dev-implementer.
