# cictl

The CI contract tool for `gophersys`. One file per repo — `.ci/ci.contract.yaml` —
describes what that repo's CI is; `cictl` generates the provider workflows from it
and fails the build if anyone hand-edits the result.

## Why it exists

CI logic had been living in YAML: 386 lines of inline shell across 22 workflow
files, plus nine workflow files maintained as byte-identical hand-copied twins.
Shell in YAML cannot be run locally, cannot be shellchecked, and cannot be tested.

The contract inverts that. The repo declares *what* it wants; the generator owns
*how* it is expressed for a provider; and the verbs themselves live in
`.ci/ctl.sh`, where they are ordinary shell that a human can run.

## History

This tool lived at `eden/tools/cictl` and was adopted by exactly one repo, which
then failed **100 consecutive CI runs** — every run since adoption. Three causes,
all now fixed or recorded:

1. The renderer hardcoded `runs-on: ubuntu-latest`, so jobs never reached the
   self-hosted fleet. The contract now carries a `runner`.
2. It emitted a `container:` block authenticated with `${{ secrets.GITHUB_TOKEN }}`
   against a **private** package, which returns 403. A self-hosted pool now emits
   no container block at all.
3. The `cictl` binary the generated workflows invoke was installed in no image and
   no PATH. It is now installed into `ghcr.io/gophersys/base`.

Extracted here with `git subtree split`, so the original authorship and dates
survive. The module has no dependency on the monorepo it came from — verified by
the fact that the only rename needed was the module path.

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

### `runner` is the load-bearing field

`runsOn` is a free string, deliberately **not** an enum. Pools are added for
physical capability — an architecture, a USB-attached board, a different kernel —
and adding one must never require changing this code.

`container` decides whether jobs run inside `image`:

| `container` | What is emitted | When to use it |
| --- | --- | --- |
| `false` | no container block | a self-hosted pool whose runner image **is** the toolchain |
| `true` | `container:` + registry credentials | a GitHub-hosted runner that needs an image |

`image` is required when `container` is true and **must be absent** when it is
false. A self-hosted pool that also names an image is dead configuration that
reads as intent, so validation rejects it.

The reason the false case matters: a `container:` image is pulled **inside the
runner pod**, and the pod is ephemeral, so nothing caches. Measured at over five
minutes per job for these images. As the pod's own image, the kubelet pulls it
once per node.

## Commands

| Command | Does |
| --- | --- |
| `schema` | emit the JSON Schema for the contract |
| `validate` | structural + semantic check of `.ci/ci.contract.yaml` |
| `conformance` | assert `.ci/ctl.sh` actually implements every verb the contract names |
| `generate` | write the provider workflows |
| `drift` | re-render in memory and fail on any hand-edit — **this is the gate** |
| `affected` | project roots changed since a base ref |
| `updatability` | aligned table of pinned tool versions, optionally probed upstream |

## This repo's own CI is hand-written, on purpose

`cictl` does not generate its own workflow. Doing so would make the tool that
gates the build a product of the build it gates. The exception is deliberate and
it is the only one.

## Development

```sh
bash ./ctl.sh test      # go test ./...
bash ./ctl.sh gate      # fmt + vet + lint + test
```

Every verb runs with `GOWORK=off`: the module must build standalone, because that
is how CI consumes it.
