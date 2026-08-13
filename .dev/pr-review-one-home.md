# pr-review-one-home

phase:    red
repo:     gophersys/cictl
branch:   feat/pr-review-one-home
worktree: ~/code/.worktrees/cictl-review
pr:       -
attempt:  0/2

## Goal

The pull request review agent runs in every gophersys repository, from ONE home,
so a fix to it cannot land in some copies and not others. Today it is a 106-line
file copied byte for byte into 2 repositories, and 2 more have no reviewer at all.

## Plan

APPROVED 2026-08-13. The planner REFUTED my proposal and it was right.

**My proposal, rejected:** embed the canonical file in cictl and add a drift verb.
That DETECTS the duplication instead of removing it. It would leave 6 copies that
each need editing to change anything, add a 7th home for the same bytes, and
require cictl on PATH in 5 gates where it is absent or pinned at v0.1.0.

**Accepted: a REUSABLE WORKFLOW.** GitHub can execute one home. cictl publishes
`.github/workflows/pr-review.yml` with `on: workflow_call`; each repository keeps
a ~14-line caller that holds no logic. The copies stop existing rather than being
measured.

**The pin moves to the caller's `uses:` line and becomes the ONLY pin.** The
reviewer source is fetched at `${{ github.job_workflow_sha }}`, not at a
`CICTL_VERSION` string, so it resolves to a commit SHA at runtime and cannot fall
back to a default branch. Nothing is excluded from any comparison, so no exception
exists to rot. Repositories may sit on different tags deliberately.

**4 things in my brief were wrong, and the planner measured each:**
1. No cictl release was needed to fix production. v0.3.0 already carried both
   reviewer fixes. That became step 0 and is DONE.
2. My "24x timeout headroom" used the median. Successful reviews are 89-270s; one
   run reached 1267s. I later checked: that run FAILED, so the tail is a hang,
   which is what the limit is for.
3. cictl CANNOT run pr-review today. It is public, the Default runner group sets
   `allows_public_repositories: false`, so `arc-review` jobs from it queue forever
   with no error. Opening the pool would put the Claude OAuth token in reach of a
   fork pull request.
4. Only 2 repositories gain the reviewer, not 3.

**Deliberately out of scope:** contract adoption anywhere (task #16), the mapfile
swallow (task #36), any change to `arc-review` or its `maxRunners`, and whether
cictl reviews its own pull requests — that one needs Mateo.

## Proven

- `pr-review.yml` is BYTE-IDENTICAL between infrastructure and .devcontainer.
  `diff` on both origin/main copies: no output, rc=0. 106 lines, and every
  reference in it is a GitHub context variable.
- eden, libs and cictl have no reviewer. Confirmed by
  `git ls-tree -r --name-only origin/main | grep .github/workflows/`.
- The exclusion was never a decision: no ADR, no debt entry.
- STEP 0 IS DONE AND MERGED. infrastructure#170 and .devcontainer#32 moved
  `CICTL_VERSION` v0.2.0 -> v0.3.0 together. Both pins on main read v0.3.0, so
  there is no skew, and debt-register D43 line 791 agrees.
- D43's "not urgent" conclusion SURVIVES that bump, re-measured not assumed:
  `git diff --stat v0.2.0..v0.3.0 -- '*.go'` is EMPTY.
- The review agent found the stale register itself and returned REQUEST_CHANGES.
  Round 2 returned APPROVE. The reviewer is doing real work, which is the whole
  argument for giving it to eden and libs.

## Blocked

Nothing blocks phase 2.

2 questions are DEFERRED BY NAME for Mateo, not by silence:
- Does cictl review its own pull requests? It cannot use `arc-review` (public
  repository). The alternative is `ubuntu-latest` plus a repository secret, which
  creates a second home for the credential.
- `arc-review` capacity: 2 slots, minRunners 0, and 2 slow reviews can hold the
  pool. Measure after, unless a queue appears.

## Next

Phase 2 — the 4 red tests the planner named, each proven to fail for the reason
the feature is about. `actionlint` is NOT installed on this machine; it is a new
gate tool, and an absent tool is a FAILURE that names it, never a skip.
