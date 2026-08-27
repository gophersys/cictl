# codex-legacy-image-config

phase: wait
repo: gophersys/cictl
branch: fix/codex-legacy-image-config
worktree: ~/code/.worktrees/cictl-codex-legacy-image-config
pr: 28
attempt: 0/2

## Goal

Run the tool-less Codex reviewer on the deployed 0.146.0 CLI while disabling its
legacy `view_image` tool through the configuration mode that version actually
supports.

## Plan

plan: SELF-APPROVED — removing strict config validation is safe only because the
adapter already ignores all user config, passes one literal tested legacy key,
and independently denies every feature-backed tool.

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

## Blocked

-

## Next

Poll pull request 28, inspect the remote gate, and receive independent review.
