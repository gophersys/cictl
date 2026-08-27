# codex-runner-flags

phase: wait
repo: gophersys/cictl
branch: fix/codex-runner-flags
worktree: ~/code/.worktrees/cictl-codex-runner-flags
pr: 27
attempt: 1/2

## Goal

Make the Codex reviewer run against the exact Codex CLI version installed in the
private review-runner image, while preserving the read-only/no-tools boundary.

## Plan

plan: SELF-APPROVED — the risk is silently enabling a capability when removing
an unsupported disable flag. The removed `view_image` flag does not grant image
access because the review prompt contains text only and the runner exposes no
image tool or image input; all six capabilities available in 0.146.0 remain
explicitly disabled.

1. Strengthen installed-CLI conformance to require every disabled feature and
   pin CI to the deployed runner's Codex 0.146.0.
2. Prove the existing command fails against that exact CLI because `view_image`
   is unknown.
3. Remove only the unsupported flag; prove the stub boundary and real CLI
   conformance green.
4. Run cictl's repository gate, submit, review, clean up, and merge.
5. Repin Eden's review workflow to the immutable corrected cictl commit.

Affected target: cictl's Codex reviewer adapter and its conformance test. Fastest
proof: `bash review/run-codex_test.sh`.

Explicit exclusions: no runner image rebuild, no authentication changes, no
review prompt or verdict changes, no Claude adapter changes.

## Proven

- Eden run 33047602597 authenticated with `Logged in using ChatGPT`, then failed
  before inference with `Unknown feature flag: view_image`.
- The deployed cloud image pins Codex 0.146.0. A real 0.146.0 `features list`
  exposes `shell_tool`, `unified_exec`, `image_generation`, `browser_use`, `apps`,
  and `multi_agent`, but not `view_image`.
- RED — `npm exec --yes --package=@openai/codex@0.146.0 -- bash
  review/run-codex_test.sh`: exit 1 with
  `installed Codex has no feature named view_image` after strengthening the test
  to validate every disabled feature against the real deployed CLI version.
- GREEN — the same real 0.146.0 command exits 0 with `run-codex-test: OK` after
  removing only the unsupported `view_image` disable flag.
- `shellcheck -S style review/run-codex.sh review/run-codex_test.sh` and
  `git diff --check`: exit 0, silent.
- `bash ./ctl.sh gate`: exit 0 after build, strict shell/action/Go lint, race tests,
  all 45 reviewer guards with mutation proof, real Codex conformance, and all
  eight composite-action assertions with discrimination proof.
- Independent review requested changes: the new conformance loop derived its
  expected deny-list from `run-codex.sh`, so deleting a deny flag also deleted the
  expectation. It also verified that Codex 0.146.0 contains a `view_image` tool
  handler even though no feature flag of that name exists. The finding is valid.
- FIX — the test now owns the literal six-feature deny-list and compares the
  adapter against it before checking every name in the real CLI. The adapter
  disables the non-feature image reader through the supported
  `tools.view_image=false` config seam, and the stub asserts that config reaches
  Codex.
- `npm exec --yes --package=@openai/codex@0.146.0 -- bash
  review/run-codex_test.sh`: exit 0 with `run-codex-test: OK` after the fix.
- `shellcheck -S style review/run-codex.sh review/run-codex_test.sh`,
  `git diff --check`, and the full `bash ./ctl.sh gate`: exit 0 after the fix.

## Blocked

-

## Next

Push the fix, receive the independent rereview, and inspect the remote gate.
