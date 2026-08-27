# codex-legacy-image-config

phase: wait
repo: gophersys/cictl
branch: fix/codex-legacy-image-config
worktree: ~/code/.worktrees/cictl-codex-legacy-image-config
pr: 28
attempt: 1/2

## Goal

Run the tool-less Codex reviewer on a cictl-owned pinned CLI even when the
devcontainer runner image is temporarily behind that version.

## Plan

plan: SELF-APPROVED — installing a pinned CLI in each ephemeral review job adds a
small network/startup cost, but it is narrower and faster than publishing all six
devcontainer images merely to unblock review. The devcontainer pin remains a
recorded alignment follow-up.

1. Add a real 0.146.0 parser probe that requires the legacy image-disable config
   to load without making an authenticated request.
2. Prove the current strict invocation fails that test and matches the live CI
   failure.
3. Remove strict mode, retain ignored user config and every explicit deny, and
   prove targeted/full gates.
4. Submit, independently review, clean up, and merge; repin Eden and rerun review.

## Proven

- Eden run 33052846745 authenticated, then failed with `unknown configuration
  field tools.view_image in -c/--config override` under `--strict-config`.
- A no-auth real 0.146.0 probe without strict mode accepted
  `tools.view_image=false` and progressed to request handling; this is parser
  evidence only and exposed no credential.
- RED — the real 0.146.0 `run-codex_test.sh` exited 1 and printed
  `deployed Codex rejects the legacy image-disable key in strict mode`, including
  the current strict adapter invocation.
- GREEN — the same real-version test exits 0 after removing strict mode; its
  no-auth parser probe reaches `No prompt provided via stdin`, proving the legacy
  image-disable key loaded without inference or credentials.
- ShellCheck, `git diff --check`, and the full `bash ./ctl.sh gate` exit 0; all 45
  reviewer guards and eight action assertions retain their mutation proof.
- Independent review proved the non-strict parser check vacuous: 0.146.0 accepts
  arbitrary unknown keys the same way, so it did not prove `view_image` was
  disabled. The finding is valid. The fix restores strict config, removes the
  unsupported key, and documents the pinned CLI's read-only image handler as an
  explicit capability difference; every executable/network/agent feature remains
  denied.
- A second rereview proved that merely documenting the handler was insufficient:
  attacker-controlled changed-file text can name a guessed absolute image path.
  The final fix instead installs pinned Codex 0.150.1 in the ephemeral Codex job
  and restores the explicit `view_image` deny; strict config remains enabled.
- Real 0.150.1 adapter conformance, ShellCheck, action discrimination, and
  `git diff --check` pass. The full gate passed immediately before this narrow
  installer revision; the remote gate will rerun the complete suite.

## Blocked

-

## Next

Push the pinned-installer fix, receive rereview, and inspect the remote gate.
