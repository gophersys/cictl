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
| `generate` | Writes the provider workflows. |
| `drift` | Renders the workflows again in memory and fails on any hand edit. **This command is the gate.** |
| `affected` | Lists the project roots that changed after a base ref. |
| `updatability` | Shows an aligned table of the pinned tool versions. It can also probe upstream. |

## The CI of this repository is hand-written, on purpose

`cictl` does not generate its own workflow. If it did, the tool that gates the
build would be a product of the build that it gates. This exception is
deliberate and it is the only exception.

## The review agent

`review/review.sh` runs the pull request review agent. It starts a paid agent
and it writes to a pull request. Its value is in what it refuses to do.

`review/review_test.sh` proves the refusals. The suite runs in 2 phases.

1. **Behaviour.** Each test runs against the real `review.sh`. All must pass.
2. **Mutation.** For each test, the suite deletes the 1 line that holds the guard
   which that test covers. The same test must then fail. A test that still
   passes proves nothing, thus the suite exits non-zero.

Each guard in `review.sh` is 1 line and ends with a `# guard:<name>` marker.
Keep each guard on 1 line, because the mutation phase removes the whole line. If
you delete a marker, the mutation phase stops with an error. It does not skip.

The suite makes no network call and it spends no money. It replaces `PATH` with
a sandbox directory. The sandbox holds a stub `claude`, a stub `gh` and the
small set of coreutils that `review.sh` needs. The real `claude` and the real
`gh` are unreachable. The runs also use `env -i`, thus a real token or a real API
key on the machine cannot rescue a test and cannot break one.

## The review workflow has one home

`.github/workflows/pr-review.yml` in **this** repository is the one review job
that a `gophersys` repository calls instead of holding a copy of. It was a
106-line file copied byte for byte into 2 of them, so a fix to the reviewer
landed in some and not in others. It is now a reusable workflow, and a
repository calls it:

```yaml
# .github/workflows/pr-review.yml — copy this, and nothing else.
name: pr-review
on:
  pull_request:
    types: [opened, synchronize, reopened]

jobs:
  review:
    # The token the review is posted with. A called workflow cannot raise what
    # the caller granted, so this grant has to be here.
    permissions:
      contents: read
      pull-requests: write
    uses: gophersys/cictl/.github/workflows/pr-review.yml@v0.4.0
```

> **DO NOT ADOPT THIS YET. The pin does not resolve, and it is measured.**
>
> The intent below is correct and the mechanism is not. On the first real run
> (`gophersys/infrastructure#171`) `${{ github.job_workflow_sha }}` was **empty**,
> so the job could not learn its own commit. A probe then read the whole
> candidate set inside a called workflow:
>
> ```
> job_workflow_sha = []                          EMPTY
> job_workflow_ref = []                          EMPTY
> workflow_sha     = [86d6f8f...]                the CALLER's commit
> workflow_ref     = [<caller>/.github/workflows/pr-review.yml@refs/pull/171/merge]
> sha              = [86d6f8f...]                the CALLER's commit
> repository       = [<caller>]                  the CALLER's repo
> ```
>
> **Inside a called workflow there is no context variable that names the reusable
> workflow's own commit.** Every one that is populated names the caller.
>
> The `# guard:pin` line did its job: an empty value made `git fetch origin ""`
> take the default branch, and the guard failed the run loudly instead of
> reviewing against a commit nobody chose.
>
> The replacement mechanism is an open decision. Until it lands, a repository
> keeps the reviewer it has.

The tag on the `uses:` line is meant to be the **only** pin: it resolves to a
commit, the review job fetches its own source at that same commit, and it asserts
that the checkout it got is that commit — so the workflow that runs and the
reviewer that runs are one commit, with no second version string to keep in step.

That property is the reason for this design, and it is worth keeping. What the
measurement above rules out is only the WAY the job learns the commit.

**`v0.4.0` is the first tag that carries this workflow, and it is the earliest one
a caller may name.** Every tag before it fails:

```
git cat-file -e v0.4.0:.github/workflows/pr-review.yml    rc=0   carries it
git cat-file -e v0.3.0:.github/workflows/pr-review.yml    rc=1   absent
... and the same for v0.2.1, v0.2.0 and v0.1.0
```

A caller that names an earlier tag fails at run time with
`workflow was not found`, and **no local check sees it first**: actionlint accepts
any ref, because it does not resolve one. So verify the tag yourself before you
pin it:

```
git ls-remote --tags https://github.com/gophersys/cictl
```

A repository may sit on an older tag on purpose. The pin is per repository and
nothing forces them to move together — that is the point of putting the version
on the `uses:` line rather than in a second version string inside the job.

A repository may then sit on an older tag deliberately. Nothing here updates a
caller, and a caller that is 3 tags behind keeps reviewing with the reviewer it
names. Raise the tag when you want the newer one.

`arc-review` is a self-hosted pool in the Default runner group, which sets
`allows_public_repositories: false`. A **public** repository that calls this
workflow queues forever with no error and no message, so a caller belongs only in
a private one.

## Development

```sh
bash ./ctl.sh test           # go test -race ./... and both review suites
bash ./ctl.sh test-review    # only the review/review.sh guard suite
bash ./ctl.sh test-workflow  # only the pr-review workflow suite
bash ./ctl.sh gate           # build + lint + test
```

Each verb runs with `GOWORK=off`. The module must build on its own, because CI
uses the module in that way.
