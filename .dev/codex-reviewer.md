# codex-reviewer

phase: pr
repo: gophersys/cictl
branch: feat/codex-reviewer
worktree: ~/code/.worktrees/cictl-codex-reviewer
pr: -
attempt: 0/2

## Goal

Allow the existing `cictl` pull-request reviewer boundary to select Claude or
Codex while preserving one review-round, verdict, comment, and archive contract.
Eden can temporarily select Codex while its Claude subscription is unavailable.

## Plan

plan: SELF-APPROVED — the risk is disguising real harness differences behind a
false common interface. Keep round/comment/verdict behavior canonical in
`review.sh`, branch only at agent invocation and stream interpretation, and fail
loudly when a harness-specific safety control has no equivalent.

- Add a `harness` action input with `claude` as the compatibility default.
- Keep the existing Claude invocation and guards unchanged.
- Add Codex membership-auth preflight via `CODEX_HOME/auth.json`, read-only
  sandbox execution, ephemeral sessions, JSONL capture, and last-message output.
- Use one harness-neutral reviewer instruction document.
- Extend behavior and mutation tests before using the adapter remotely.
- Update the generated workflow contract only as required to express harness.

Capability difference: Claude's `--max-budget-usd` is an enforceable per-run
ceiling. ChatGPT-managed Codex has no equivalent CLI flag. Codex mode must reject
API-key auth, use the mounted membership credential, and state that the existing
budget input does not constrain it.

## Proven

- `codex exec --help` on CLI 0.150.1 exposes `--ephemeral`, `--ignore-user-config`,
  `--sandbox read-only`, `--json`, `--output-last-message`, and optional model.
- A prior real read-only Codex probe emitted `item.completed` agent messages and
  `turn.completed` usage, and wrote a complete `REQUEST_CHANGES` verdict.
- RED — `bash review/run-codex_test.sh` failed with `authentication was not
  checked first`, proving the adapter initially lacked the required membership
  preflight and fixed approval boundary.
- GREEN — `bash review/run-codex_test.sh` exits 0 with `run-codex-test: OK`.
- `bash review/review_test.sh`: all 45 reviewer guards hold and every guard is
  proven able to fail, including Codex harness, tool, home, auth, and competing
  API-key guards.
- `bash review/action_test.sh`: all 8 composite-action assertions hold and all
  are proven able to fail.
- `shellcheck -S style` on every changed shell file: exit 0.
- `bash ./ctl.sh gate`: exit 0; build, gofumpt, shellcheck, actionlint, vet,
  golangci-lint, Go race tests, 45 reviewer mutation guards, the Codex adapter
  test, and 8 composite-action discrimination assertions all ran green.

## Blocked

-

## Next

Run the repository gate, then submit the cictl pull request.
