# affected-relative-c

phase:    pr
repo:     gophersys/cictl
branch:   fix/affected-relative-c
worktree: ~/code/.worktrees/cictl-affected-relative-c
pr:       -
attempt:  0/2

## REMINDER: delete this .dev file in the FINAL commit before merge; prove gone with git cat-file -e.

## Goal
`cictl affected` with a RELATIVE `-C` (and `-C` DEFAULTS to `.`) silently returns an empty set with
rc=0, even when project files changed — a false "no affected projects." When fixed, `cictl affected
-C .` returns the same set as `cictl affected -C <absolute path>`, so a local developer (and any
consumer using the default `-C .`) gets the true affected set.

## Plan — APPROVED (orchestrator; the bug is fully diagnosed + reproduced)
ROOT CAUSE (internal/affected/affected.go, nearestProjectRoot): `dir` is built from
`filepath.Join(repoDir, relFile)` — RELATIVE when repoDir is `.` — while `repoAbs =
filepath.Abs(repoDir)` is ABSOLUTE. Then `filepath.Rel(repoAbs, dir)` and `within(repoAbs, dir)`
ERROR on the abs/rel mismatch (Go returns an error when basepath is absolute and targpath is not),
so nearestProjectRoot returns ("", false) for EVERY file → roots empty → Projects returns empty+nil
→ rc=0, no output.

FIX (dev-implementer, internal/affected/affected.go, non-test): absolutize before building `dir` —
compute `repoAbs` first, then `dir := filepath.Dir(filepath.Join(repoAbs, filepath.FromSlash(relFile)))`
so both are absolute and Rel/within work. Keep the returned root repo-relative with forward slashes
(unchanged). Do NOT change the public contract (still returns repo-relative roots). Minimal change.

TEST (dev-test-author, internal/affected/affected_test.go or a new test): build a temp git repo with
a root (or nested) project (ctl.sh + project.json), commit, change a file under the project, and
assert `Projects(".", base)` (relative, run with the temp repo as CWD) returns the SAME non-empty set
as `Projects(<absDir>, base)`. RED now: the relative call returns empty while the absolute call
returns the project. GREEN after the fix: both return the project. Also cover a nested project (a
subdir with ctl.sh+project.json) to prove the walk works under relative `-C`.

## Proven (reproduction, orchestrator, cictl @ 45bdb33)
Built cictl; same diff (`--base HEAD~5`, 3 changed files incl. review/review.sh under root project):
- `cictl affected -C .`    → rc=0, 0 lines (false-empty)
- `cictl affected -C $PWD` → rc=0, 1 line `.` (correct)
Sole variable = relative vs absolute `-C`.

## Gate
`bash ./ctl.sh gate` (build + lint[shellcheck+golangci+actionlint] + test -race). actionlint is
ABSENT locally + in base-runner → the full local gate can't run without a `go install
github.com/rhysd/actionlint/cmd/actionlint@v1.7.12` into base-runner. The change is Go-only, so the
directly-affected checks are `go test -race ./...` (incl. the new affected test) + golangci. The PR
ci.yml is the authoritative full gate. dev-test-author proves red/green via `GOWORK=off go test ./internal/affected/...`.

## Blocked
Nothing. cictl quiet (0 open PRs), non-Mateo, local-impact bug.

## Next
dev-test-author: write the RED test (relative `-C` == absolute `-C`), prove it fails for the right
reason (relative returns empty). Ownership: affected_test.go → dev-test-author; affected.go →
dev-implementer.

## Proven (green) + verification
- dev-implementer (1186a11, only affected.go, +1/-1): moved repoAbs computation above `dir`, joined
  with repoAbs. `go test ./internal/affected/...` rc=0 — both new subtests + all 4 pre-existing pass;
  full `go test ./...` (10 pkgs) rc=0; vet rc=0; gofumpt/gofmt clean.
- Orchestrator re-ran: affected suite ok, `go build ./...` rc=0. Scope = affected.go (+1/-1) + the test.
- Adversarial bar MET: the test FAILS without the fix (proven by test-author AND implementer red runs)
  and passes with it; the absolute-path case CI relies on is unchanged (existing tests use absolute
  temp dirs and pass). One-line change, no scope creep — no separate verifier subagent needed.
