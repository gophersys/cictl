# review-round-limit-visible

phase:    verify
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

## Plan — APPROVED (dev-planner, orchestrator-approved 2026-08-14)
EXACT STRINGS both agents must use verbatim (coordination-critical):
- `SKIP_MARKER="<!-- gophersys-review-agent-skip -->"` — distinct from the review
  `MARKER="<!-- gophersys-review-agent -->"`. The counter (review.sh:93-97) selects comments whose
  body `contains($MARKER)`; `agent-skip -->` never contains the contiguous `agent -->`, so the skip
  notice is UNCOUNTED. LANDMINE: the human-facing skip text must NEVER quote the literal MARKER string.
- Two guard lines (review.sh guard doctrine — each ends `# guard:<name>`, and review_test.sh mutates
  each by deleting/altering the line and asserts the matching test fails): `# guard:skip-notice`
  (the `gh pr comment … --body-file` post line) and `# guard:skip-uncounted` (the `SKIP_MARKER=` line).

FIX (review.sh, dev-implementer): define `SKIP_MARKER` near MARKER (L34); in the round-limit branch
(L102-105), BEFORE `exit 0`, write a skip notice (not reviewed; limit N reached; remedy = review
manually or raise REVIEW_MAX_ROUNDS) carrying SKIP_MARKER, and post it via `gh pr comment "$PR"
--body-file "$out_file"`. Keep exit 0. shellcheck-clean (quote every expansion).

TESTS (review_test.sh, dev-test-author): 2 new + relax 1.
1. t_a_round_limit_skip_posts_a_visible_notice — rounds_with 2 → exit 0, claude NOT called, a comment
   IS posted stating not-reviewed + limit + remedy. RED now (current code posts nothing). Mutation:
   delete `# guard:skip-notice` → no comment → fails.
2. t_the_skip_notice_is_not_counted_as_a_round — posted body contains SKIP_MARKER and does NOT contain
   the review MARKER. Mutation: swap SKIP_MARKER to the review marker (`# guard:skip-uncounted`) →
   body contains MARKER → fails. RED now: assert_comment_posted fails first (right reason).
3. RELAX t_a_third_run_refuses_before_it_costs_anything (L509-520): remove `assert_no_comment` (L519)
   — it encodes the OLD silent behaviour. Keep rc 0 + "limit is reached" + assert_not_called claude.
   Its own mutation MAX_ROUNDS=99 still reddens it, so proof stays intact.

Idempotency: ONE notice per skipped push, no dedup (pr-review.yml runs once/push; concurrency cancels
in-flight; each un-reviewed push is a distinct honest trace). Out of scope: MAX_ROUNDS, the counting
query, exit-0 semantics, pr-review.yml, ci.yml.

GATE NOTE: `bash ./ctl.sh gate` needs actionlint v1.7.12 — MISSING on this host (127, FAIL-NOT-SKIP).
Inner loop `bash ./ctl.sh test-review` runs with present tools. Full gate at phase 4: install
actionlint (brew) or run in base-runner; the cictl PR ci.yml is the authoritative full gate.

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

## Proven (green)
- dev-implementer (commit f7b8c72, only review.sh, +19): SKIP_MARKER at col 0 + skip-notice posted
  on the round-limit branch (contains "not reviewed", "limit", "REVIEW_MAX_ROUNDS"; carries
  SKIP_MARKER; never the review marker), exit 0 preserved. `bash ./ctl.sh test-review` rc=0 — all 30
  behaviour tests ok, all 30 mutations redden their own test ("all 30 guards hold, and all 30 are
  proven able to fail"). Both new tests green + proven-able-to-fail; relaxed test 3 green + still
  reddened by MAX_ROUNDS=99. shellcheck review/review.sh clean (rc=0).

## Gate strategy (shell-only change)
The change is entirely in review.sh (a shell script — NOT Go, not built, not go-tested). cictl's
`ctl.sh gate` = build + lint(shellcheck+golangci+actionlint) + test -race. The ONLY components a
review.sh change touches are shellcheck (clean) + the review test suite (test-review, 30/30 both
phases). Go build/test/golangci and actionlint (workflows) are unaffected — confirmed authoritatively
by the cictl PR ci.yml on the real runner (which has actionlint; absent locally + in base-runner).
No emulated local full-gate — it would only re-confirm Go components this change cannot alter.

## Blocked
Nothing. cictl is quiet (0 open PRs). Non-Mateo.

## Next
dev-planner: trace the exact round-counting query (how MARKER comments are counted), design the
visible-but-uncounted skip notice, name the files (review/review.sh + review_test.sh), and give the
red/green test recipe (assert: on the limit path, a skip comment IS posted AND the round count is
NOT incremented by it). Ownership: review_test.sh → dev-test-author; review.sh → dev-implementer.
