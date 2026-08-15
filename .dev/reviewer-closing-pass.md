# reviewer-closing-pass

phase:    green
repo:     gophersys/cictl
branch:   fix/reviewer-closing-pass
worktree: ~/code/.worktrees/cictl-closing-pass
pr:       -
attempt:  0/2

## Goal (#80)
The pr-review agent's 2-round limit (review/review.sh: MAX_ROUNDS, the `round > MAX_ROUNDS`
skip at ~line 108) means a PR that ADDRESSES its round-2 findings gets a skip-notice on
round 3, never an APPROVE — so it can never satisfy merge-authority condition 3 (pr-review
APPROVE), forcing Mateo to merge every findings-addressed PR by hand. This forced his manual
merge of #175 today and would recur for every such PR.
When done: after the round limit, the reviewer runs ONE bounded CLOSING pass that may only
judge whether the ALREADY-RAISED findings are resolved — APPROVE if yes, REQUEST_CHANGES if
not — and may NOT open new findings. Preserves the bounded-rounds intent (no new fronts) while
letting a fixed PR clear itself, so a green findings-addressed PR self-approves and can merge
under the granted authority without Mateo.

## Plan
(dev-planner fills this — see Next.)

## Proven
- Phase 2 RED (bash 3.2, `bash ./ctl.sh test-review`): t_the_round_after_the_limit_is_a_closing_pass FAIL "claude was not invoked" (round 5 hit the old blanket skip); round-1/round-2 tests FAIL "of 2" not "of 4"; closing-pass-verdict-only FAIL "closing pass never ran".
- Phase 3 GREEN (`bash ./ctl.sh test-review`) rc=0: "all 32 guards hold, and all 32 are proven able to fail"; shellcheck -x -S style review/review.sh rc=0.
- Phase 4 verifier (adversarial): boundedness SOUND (broke guard:closing-pass-once by Edit -> round 6 ran a 2nd agent call, caught, reverted); closing pass APPROVEs + is counted (posts MARKER); no band-aid (the no-new-finding directive is in the piped prompt); all 32 mutations genuine. Found the round-3/4 fresh-review defect below.
- Round-3/4 fix (this addendum): elif round==2 -> round>1 so rounds 2-4 all inject the prior review + judge-only directive, making CLAUDE.md ## Limits true. Proven by a new round-3 content test (red->green).

## Blocked
Nothing.

## Next
dev-planner: read review/review.sh (MAX_ROUNDS, rounds_done/round counting, the round>MAX skip
path + SKIP_MARKER, the APPROVE/REQUEST_CHANGES verdict parsing), the reviewer prompt (CLAUDE.md),
and review_test.sh (how rounds + verdicts are tested). Produce the exact change to add a CLOSING
pass (verdict-only, no-new-findings, still hard-bounded so it cannot loop) + the red test recipe
(a fixture where round>MAX with prior findings addressed must now APPROVE, not skip; and a guard
that the closing pass cannot open a NEW finding). Owners: review_test.sh + fixtures -> dev-test-author;
review/review.sh + CLAUDE.md prompt -> dev-implementer.

## Plan — SELF-APPROVED (--auto), risk: shared reviewer, org-wide blast radius; edit is confined to the round>MAX_ROUNDS branch (rounds 1..MAX byte-identical) + a bounded, counted closing pass.
The fix (dev-planner, folding Mateo's MAX_ROUNDS 2->4):
- review/review.sh L108-124: replace the blanket skip with a 3-way split on `round`:
  `<= MAX_ROUNDS` normal (unchanged); `== MAX_ROUNDS+1` set closing_pass=yes and run the
  NORMAL path (agent call, verdict parse, post with MARKER so it is COUNTED -> exactly one
  closing pass); `> MAX_ROUNDS+1` the existing SKIP_MARKER hard-stop. Guards:
  `# guard:round-limit`, `# guard:closing-pass`, `# guard:closing-pass-once`.
- review/review.sh L31: MAX_ROUNDS default 2 -> 4 (`${REVIEW_MAX_ROUNDS:-4}`), still env-overridable.
- review/review.sh prompt block L131: closing-pass branch injects `previous` + a one-line directive
  (CLOSING PASS: judge ONLY whether the prior review's findings are resolved; open NO new finding;
  APPROVE if all resolved else REQUEST_CHANGES).
- review/CLAUDE.md: add `## The closing pass` after `## Your verdict` (verdict-only, no new finding, final).
Tests (dev-test-author owns review_test.sh + the sandbox `claude` stub): extend the stub to drain
stdin to $sb/prompt_seen. 3 new: (1) t_the_round_after_the_limit_is_a_closing_pass — round MAX+1
calls claude, posts APPROVE with MARKER (RED today: blanket skip, claude not called); (2)
t_the_closing_pass_is_verdict_only — captured prompt contains the no-new-finding directive; (3)
t_after_the_closing_pass_no_further_review — round MAX+2 skips (SKIP_MARKER), claude NOT called.
Retarget: delete t_a_third_run_refuses_before_it_costs_anything; move the two skip-notice tests to
round MAX+2. Add a test pinning the DEFAULT ceiling == 4. Make closing-pass boundary tests set
REVIEW_MAX_ROUNDS explicitly (deterministic regardless of the new default). Add the 2 new markers to
MARKER_COVERAGE + mutation_for. Gate: `bash ./ctl.sh test-review` (runs under bash 3.2, local) + shellcheck;
CI runs full `gate` (Go unaffected — only shell changed).

## Next
dev-test-author: write the tests + stub change, PROVE t_the_round_after_the_limit_is_a_closing_pass RED
on current code (round MAX+1 skips today). Then implementer.

## Plan FINALIZED (planner re-ran with MAX_ROUNDS=4 folded in)
Deltas from the note above: default ceiling is 4 so the closing pass is round 5 — tests use
`rounds_with 4` (closing pass) and `rounds_with 5` (round 6 skip) at the DEFAULT, so test 1 proves
BOTH the bump and the closing pass (RED today: default 2 -> round 5 hard-skips). ALSO fix review.sh
L127 `round %s of 2` literal -> dynamic `${MAX_ROUNDS}` (now wrong with default 4; # guard-free, one line).
Retarget "of 2"->"of 4" in t_the_first_run_is_round_1/..round_2; move the 2 skip-notice tests to
`rounds_with 5`; keep t_the_round_ceiling_is_configurable (override MAX=3). SPEND: <=5 paid runs vs 2
per PR across every repo — Mateo's explicit choice, flagged.
