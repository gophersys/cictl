# cictl

The CI contract tool for `gophersys`. `cictl` uses one contract file for each
repository: `.ci/ci.contract.yaml`. The file describes the CI of that
repository. `cictl` generates the provider workflows from the file. `cictl`
fails the build if a person edits a generated workflow by hand.

## Why it exists

The CI logic was in YAML. There were 386 lines of inline shell in 22 workflow
files. There were also 9 workflow files that were hand-made copies, identical
byte for byte. You cannot run shell that is in YAML on your machine. You cannot
check it with shellcheck. You cannot test it.

The contract changes this. The repository declares *what* it wants. The
generator controls *how* a provider expresses it. The verbs are in
`.ci/ctl.sh`, where they are usual shell commands that a person can run.

## History

This tool was at `eden/tools/cictl`. Only 1 repository adopted it. That
repository then failed **100 CI runs in sequence**, which was every run after
the adoption. There were 3 causes. All 3 causes are now corrected or recorded.

1. The renderer wrote `runs-on: ubuntu-latest` as a fixed value. Thus the jobs
   never went to the self-hosted pool. The contract now has a `runner` field.
2. The renderer wrote a `container:` block that used `${{ secrets.GITHUB_TOKEN }}`
   to authenticate against a **private** package. This returns 403. For a
   self-hosted pool, `cictl` now writes no container block at all.
3. The generated workflows call the `cictl` binary. That binary was in no image
   and in no PATH. The binary is now installed into `ghcr.io/gophersys/base`.

`git subtree split` extracted this repository, thus the original authorship and
the original dates stay correct. The module does not depend on the monorepo that
contained it. The proof is that the module path was the only necessary rename.

## The contract

```yaml
apiVersion: eden.ci/v1
repo: libs
kind: libraries
runner:
  runsOn: arc-org        # emitted verbatim as `runs-on:`
  container: false       # the pool's runner image already carries the toolchain
languages: [go, typescript]
tiers:
  pr:      { verbs: [affected-gate-fast], substrate: [], timeoutMinutes: 15 }
  merge:   { verbs: [affected-gate-substrate], substrate: [docker, k3d], privileged: true, timeoutMinutes: 30 }
  nightly: { verbs: [gate-all, updatability], substrate: [docker], privileged: true, schedule: "0 6 * * *", timeoutMinutes: 90 }
review:
  enabled: true
  ref: 0123456789abcdef0123456789abcdef01234567   # the cictl commit this repo reviews with
  release: v0.6.0                                 # names that commit for a human reader
  tier: module                                    # platform | module | research | tooling
  runsOn: arc-review
  timeoutMinutes: 30
  streamStore: minio
  streamEndpoint: https://minio.example.invalid
  streamBucket: review-streams
providers: [github]
toolMatrix:
  sources: ["**/go.mod"]
```

### The `runner` field is the critical field

`runsOn` is a free string. It is deliberately **not** an enum. You add a pool
for a physical capability: an architecture, a USB-attached board or a different
kernel. When you add a pool, you must never have to change this code.

`container` selects if the jobs run in `image`:

| `container` | What is emitted | When to use it |
| --- | --- | --- |
| `false` | no container block | a self-hosted pool whose runner image **is** the toolchain |
| `true` | `container:` and the registry credentials | a GitHub-hosted runner that needs an image |

`image` is required when `container` is `true`. `image` **must be absent** when
`container` is `false`. If a self-hosted pool also gives an image, that image
has no effect, but a reader can think that it has an effect. Thus the validation
rejects it.

The `false` case is important for this reason. The runner pod pulls a
`container:` image **inside the pod**. The pod is ephemeral, thus no cache keeps
the image. The measured time is more than 5 minutes per job for these images. If
the image is the image of the pod, the kubelet pulls it one time per node.

## Commands

| Command | Function |
| --- | --- |
| `schema` | Emits the JSON Schema of the contract. |
| `validate` | Does a structural check and a semantic check of `.ci/ci.contract.yaml`. |
| `conformance` | Asserts that `.ci/ctl.sh` really implements every verb that the contract names. |
| `conformance --org` | The bird's-eye audit: reads every repository of a GitHub organization and reports the fleet against the contract. |
| `generate` | Writes the provider workflows. |
| `drift` | Renders the workflows again in memory and fails on any hand edit. **This command is the gate.** |
| `affected` | Lists the project roots that changed after a base ref. |
| `updatability` | Shows an aligned table of the pinned tool versions. It can also probe upstream. |

## The CI of this repository is hand-written, on purpose

`cictl` does not generate its own workflow. If it did, the tool that gates the
build would be a product of the build that it gates. This exception is
deliberate and it is the only exception.

## The review agent

`review/` is the pull request review agent, and it is the ONE home of it for
every gophersys repository. It has 3 parts:

| File | What it is |
| --- | --- |
| `review/action.yml` | the composite action every repository calls |
| `review/action.sh` | the preflight: it validates every input, then runs the reviewer |
| `review/review.sh` | the reviewer itself: it reads the diff, runs the agent, posts the result |
| `review/archive-stream.sh` | it writes the full event stream to an object store |
| `review/CLAUDE.md` | the reviewer's instructions, read beside `review.sh` |

### Why a composite action, and not a reusable workflow

`.github/workflows/pr-review.yml` used to be a reusable workflow that other
repositories called. **It is retired, and it is deleted.** Its safety rested on
`github.job_workflow_sha` — the context a called workflow uses to learn its own
commit, so it can fetch the reviewer source at that same commit. That context is
**empty** inside a called workflow. It was measured on
`gophersys/infrastructure#171`: every populated context named the CALLER. So the
pin could never resolve, the pin guard correctly failed, and no repository could
adopt it. That is ledger #46, and it is not fixable — no context a called
workflow can read names the called workflow.

A composite action has no such problem by construction. **Before the first step
runs, the runner checks this repository out at the exact ref on the caller's
`uses:` line, into `${{ github.action_path }}`.** The source on disk IS the
pinned commit, so there is no `git fetch`, no context variable and no pin guard —
there is nothing left to verify. `review/action_test.sh` asserts the property
that replaces the old pin test: **nothing under `review/` fetches its own
source**, because the moment it does, the caller's ref stops being the pin.

Both mechanisms are never present at once. A test fails if
`.github/workflows/pr-review.yml` comes back, or if any workflow here declares
`workflow_call`.

### The caller is generated, not copied

A repository does not hand-write its caller. It declares a `review:` block, and
`cictl generate` emits `.github/workflows/pr-review.yml` beside `on-pr.yml`,
`on-push.yml` and `nightly.yml`. `cictl drift` fails any hand edit.

This is the part that ends drift. Three hand-kept copies of the review job had
already diverged, because a human maintained them: a fix to one reached some
repositories and not others. After this, a copy that diverges fails that
repository's own pull request.

### The tier decides the spend

The contract declares a **tier**, never a budget. The tier is the repository's
governance type, and the spend preset is derived from it in one place:

| Tier | Model | Budget | Rounds | Worst case per pull request |
| --- | --- | --- | --- | --- |
| `platform`, `module` | `opus` | $25 | 4 | $125 |
| `research`, `tooling` | `sonnet` | $10 | 2 | $30 |

Worst case is `(rounds + 1) x budget`, because the closing pass is a full-price
round. Changing what `platform` costs is one edit to `TierPreset` and a
regeneration — never an edit of four contracts.

`model` is the CLI's **family alias**, deliberately not a pinned model id.
`opus` and `sonnet` track the newest model of each family as the pinned CLI
advances (today `claude-opus-5` and `claude-sonnet-5`), which is the standing
policy: always the latest Sonnet and Opus. A pinned id would be a second version
home that ages silently — the exact defect the SHA pin above exists to remove —
and `review.sh` logs the id the alias RESOLVED to, read from the agent's own
init event, so the run record still names the model that answered.

### The event stream does not go to GitHub artifact storage

`actions/upload-artifact` writes into one quota for the whole organization. When
it filled, `Failed to CreateArtifact: Artifact storage quota has been hit` failed
the keep-the-run step on runs whose Review step had **succeeded** — measured on
`.devcontainer` run `32868094840` and `infrastructure` run `32763725563`, five
times across one weekend, once on a review that had APPROVED.

A shared quota must never decide one repository's review verdict. The stream is
written to the homelab MinIO over S3 instead (`streamStore: minio`), with the
credentials supplied by the pool exactly as the Claude token is. `streamStore:
none` is the explicit, visible opt-out; an unknown value is refused rather than
treated as `none`, so a typo cannot silently turn archiving off.

The archive step may fail the job, and that is deliberate. The review comment is
already posted by then, so the pull request carries its verdict either way; what
is at stake is the RECORD, and without the stream, diagnosing a wrong review
means paying for another run. The message says plainly that the review itself
completed, so nobody reads that red as a verdict.

### The bird's-eye view

`cictl conformance --org gophersys` reads every repository of the organization
through the GitHub API and reports the fleet against the contract: the caller is
present, it is exactly what the generator produces (which is what makes the tier
correct), every action is pinned to a 40-hex commit, every job declares a
timeout exactly once, and every job runs on an `arc-*` pool.

Three of those rules exist today in exactly one repository's private test suite.
A rule that has to be repeated in ten repositories is not a rule; this verb is
where they stop being repeated.

**It reports. It does not block another repository's merge.** An org-wide
required check reddens a pull request that cannot fix the cause. So a fleet gap
lands on the board and the process exits 0; only an OPERATIONAL failure — no
token, an API error — exits non-zero, because that is the verb failing rather
than reporting. The one scheduled job that owns the fleet passes
`--fail-on-gap` to turn gaps into its own red, in one loud place.

### Every guard is proven

`review/review_test.sh` and `review/action_test.sh` prove the refusals. Each
suite runs in phases:

1. **Behaviour.** Each test runs against the real tree. All must pass.
2. **Control** (`action_test.sh`). The same tests against an unmutated copy must
   reach the same verdicts. A test that answers differently on a copy was decided
   by the copy, and the next phase would prove nothing.
3. **Mutation / discrimination.** For each test, a counter-stimulus is applied to
   a copy — a guard deleted, a guard defanged, a fetch reintroduced, the retired
   workflow resurrected — and the same test must then FAIL. A test that still
   passes proves nothing, so the suite exits non-zero.

Each guard is 1 line and ends with a `# guard:<name>` marker. Keep each guard on
1 line, because the mutation phase removes the whole line. A marker no test
claims stops the suite with an error. It does not skip.

`review_test.sh` makes no network call and spends no money: it replaces `PATH`
with a sandbox holding a stub `claude`, a stub `gh` and the small set of
coreutils that `review.sh` needs, and runs under `env -i`, so a real token on the
machine cannot rescue a test or break one. `action_test.sh` runs no action —
GitHub runs actions — but it does run the REAL generator and lints its output
with `actionlint`, which is the only linter that reports a duplicate workflow
key.

## Development

```sh
bash ./ctl.sh test           # go test -race ./... and both review suites
bash ./ctl.sh test-review    # only the review/review.sh guard suite
bash ./ctl.sh test-action    # only the review composite-action suite
bash ./ctl.sh gate           # build + lint + test
```

Each verb runs with `GOWORK=off`. The module must build on its own, because CI
uses the module in that way.
