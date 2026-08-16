# verdict-loss-at-turn-cap

phase:    red
repo:     gophersys/cictl
branch:   fix/verdict-loss-at-turn-cap
worktree: ~/code/.worktrees/cictl-79
pr:       -
attempt:  0/2

## Goal

Ledger #79. When the review agent run ends with no verdict line — the 40-turn
cap (stop reason tool_use), or any end whose result event carries no final
text — review/review.sh dies with only a runner-log line: the budget is spent,
the pull request shows nothing, and the red check does not say why. After this
fix the death (a) posts a visible, UNcounted pull request comment (SKIP_MARKER,
never MARKER, so it consumes no round) naming the spend, the turns against the
cap and the stop reason, and telling the reader to re-run or review manually;
and (b) still exits nonzero so the check is red. Every existing behavior —
rounds, closing pass, markers, budget, the unreadable-verdict salvage path —
is preserved; the no-verdict path is additive.

## Plan

plan: SELF-APPROVED (--auto) — subagent inside the approved program, ledger #95
(Mateo, 2026-08-16). Risk weighed: the new guard must fire on ANY empty final
text, so it stands BEFORE guard:agent-error — the CLI marks a turn-capped run
is_error true on some versions and false on others. Consequence: an errored run
with NO text now posts the notice where it previously died silently; an errored
run WITH text keeps its existing no-post behavior (t_an_agent_error_is_not_posted
is unchanged and still passes).

1. review/review_test.sh (red first, proven failing):
   - t_a_no_verdict_death_posts_and_fails — stub stream ends turn-capped
     (subtype error_max_turns, is_error false, num_turns 40, stop tool_use,
     cost 18.73, no .result). Asserts: rc nonzero; a comment is posted; it names
     the spend, "40 of 40 turns", tool_use, and the remedy.
   - t_the_no_verdict_notice_consumes_no_round — same death with is_error true
     (the other CLI shape). Asserts SKIP_MARKER present, MARKER absent, and
     functionally: feed the posted notice back as a PR comment beside 1 real
     MARKER review and prove the next run is round 2, not round 3.
   - t_empty_reviewer_output_is_not_posted → renamed
     t_an_empty_output_is_not_posted_as_a_review: the empty-output end is one
     shape of the no-verdict end; it now asserts the notice is posted, nothing
     carries MARKER, and the job fails.
   - Delete dead t_output_without_a_verdict_is_not_posted (pre-#12 residue:
     it is in no TESTS entry and asserts behavior PR #12 removed).
   - mutation_for + MARKER_COVERAGE rows for both new tests.
2. review/review.sh (green, minimal):
   - Extract the result text BEFORE guard:agent-error. If whitespace-empty:
     build the notice (cost, turns of MAX_TURNS, stop reason, remedy) into
     out_file with SKIP_MARKER; post it — `# guard:no-verdict` one-liner; then
     die naming spend/turns/stop/exit — the existing `# guard:empty-output`
     marker moves onto this die.
   - Generalize the SKIP_MARKER comment: it now footers both uncounted notices.

Ownership note: single-agent execution per the orchestrator's task instruction;
edits are serial, so no concurrent file ownership exists to split.

## Proven

- `bash review/review_test.sh` at e3d4cef (baseline, before any edit):
  "all 33 guards hold, and all 33 are proven able to fail", exit 0.

## Blocked

-

## Next

Write the red tests and prove each fails for the silent-death reason.
