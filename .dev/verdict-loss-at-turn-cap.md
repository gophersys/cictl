# verdict-loss-at-turn-cap

phase:    pr
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
text, so it stands BEFORE guard:agent-error — the CLI's error-variant result
events (subtype error_max_turns among them) are is_error:true and carry NO
.result field at all (verified against claude 2.1.229 by the phase-4 verifier),
so behind the error guard the notice could never post. Consequence: an errored
run with NO text now posts the notice where it previously died silently; an
errored run WITH text keeps its existing no-post behavior
(t_an_agent_error_is_not_posted is unchanged and still passes).

1. review/review_test.sh (red first, proven failing):
   - t_a_no_verdict_death_posts_and_fails — stub stream ends turn-capped in
     the CLI's real shape (subtype error_max_turns, is_error true, num_turns
     40, stop tool_use, cost 18.73, no .result field). Asserts: rc nonzero; a
     comment is posted; it names the spend, "40 of 40 turns", tool_use, and
     the remedy. The is_error:false empty-text shape stays pinned by the
     repurposed empty-output test.
   - t_the_no_verdict_notice_consumes_no_round — same death. Asserts
     SKIP_MARKER present, MARKER absent, and functionally: feed the posted
     notice back as a PR comment beside 1 real MARKER review and prove the
     next run is round 2, not round 3.
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

3. Scope addition, approved by Mateo via the orchestrator (2026-08-16, same
   pull request): default MODEL to the CLI's `opus` family alias so the
   reviewer tracks the newest Opus as the pinned CLI advances
   (`# capture:model`); the run summary logs what the alias RESOLVED to, read
   from the stream's init event — and when the stream carries no resolved
   model it states the alias and names the stream artifact as the record,
   never inventing a resolution. Guard tests:
   t_the_default_model_is_the_opus_alias (default is the alias; alias is
   logged), a resolved-model assertion in
   t_the_run_summary_and_the_stream_are_kept (init event in the fixture), and
   t_a_model_override_wins (explicit REVIEW_MODEL still wins), each backed by
   its own MODEL-line mutation.

Ownership note: single-agent execution per the orchestrator's task instruction;
edits are serial, so no concurrent file ownership exists to split.

## Proven

- `bash review/review_test.sh` at e3d4cef (baseline, before any edit):
  "all 33 guards hold, and all 33 are proven able to fail", exit 0.
- RED — final test file against `git show e3d4cef:review/review.sh` in a
  scratch dir: exit 1; "phase 1 — behaviour: 35 tests", exactly 3 FAIL, each
  for the silent-death reason:
  - t_a_no_verdict_death_posts_and_fails: stderr never said "died with no
    verdict"; it said "the reviewer reported an error (stop: tool_use). Not
    posting." — the turn-cap death died with nothing posted.
  - t_an_empty_output_is_not_posted_as_a_review: stderr said "the reviewer
    produced no output (exit 2)" — the same silent death, success-shaped.
  - t_the_no_verdict_notice_consumes_no_round: "no review was posted".
- GREEN — `bash review/review_test.sh` in the worktree after the review.sh
  change: exit 0, "all 35 guards hold, and all 35 are proven able to fail".
- `shellcheck -S style review/review.sh review/review_test.sh`: exit 0, silent.
- actionlint was absent from this host AND from base-runner:e0c6bc5 (gate run
  in the container failed exit 127 "missing required tool(s): actionlint" —
  fail, not skip). Unblocked by installing the CI-pinned version on the host:
  `go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` (the same
  line ci.yml runs), then
  `PATH=/Users/mateo/go/bin:$PATH bash ./ctl.sh gate`: exit 0 — build OK,
  lint OK (gofumpt + shellcheck + actionlint + vet + golangci-lint),
  test-review OK, test-workflow OK, go test -race OK. A first gate run failed
  on golangci-lint findings pointing into a deleted sibling worktree
  (cictl-affected-relative-c) — stale host lint cache, surfaced not caused;
  `golangci-lint cache clean` and the rerun was green.
- RED (model scope) — `bash review/review_test.sh` with the model tests in and
  review.sh still on the pinned default: exit 1; "phase 1 — behaviour: 37
  tests", exactly 2 FAIL, both for the pinned-default reason:
  - t_the_default_model_is_the_opus_alias: "the agent did not run with the
    opus family alias: -p --model claude-opus-4-5 --max-budget-usd 25 ..."
  - t_the_run_summary_and_the_stream_are_kept: stdout never said
    "resolved: claude-opus-4-5-20251101".
  Phase 2 also proved the alias mutation was impossible pre-change: "the
  mutation changed nothing: s|^MODEL=.*|MODEL="${REVIEW_MODEL:-claude-opus-4-5}"|".
- GREEN (model scope) — `bash review/review_test.sh`: exit 0, "all 37 guards
  hold, and all 37 are proven able to fail";
  `shellcheck -S style review/review.sh review/review_test.sh` exit 0;
  `PATH=/Users/mateo/go/bin:$PATH bash ./ctl.sh gate` rerun after the model
  scope: exit 0 end to end.
- Phase-4 adversarial verifier (dev-verifier): DONE-CONFIRMED. It reproduced
  baseline, red, green, gate and shellcheck; proved both new mutations are
  real counter-stimuli; probed the functional round-count proof independently
  (stripping the marker assertion still goes red on "posted round 3 of 4");
  ran an in-tree break-revert through ctl.sh test-review (SKIP_MARKER→MARKER:
  2 failures, then restored, suite green). Its record findings are folded in
  here; pre-existing findings it deferred are listed in the PR body.

## Blocked

-

## Next

Open the pull request. Do NOT merge: the orchestrator owns the merge, and this
state file is deleted in the last commit before it.
