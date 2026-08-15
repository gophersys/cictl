# reviewer-closing-pass

phase:    plan
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
(empty)

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
