# cictl

The CI contract tool for `gophersys`. Each repository has one contract file:
`.ci/ci.contract.yaml`. The file describes the CI of that repository. `cictl`
generates the provider workflows from the file. `cictl` fails the build if a
person edits a generated workflow by hand.

## Why it exists

The CI logic was in YAML. There were 386 lines of inline shell in 22 workflow
files. There were also 9 workflow files that were hand-made copies, identical
byte for byte. You cannot run shell that is in YAML on your machine. You cannot
check it with shellcheck. You cannot test it.

The contract changes this. The repository declares what it wants. The generator
controls how a provider expresses it. The verbs are in `.ci/ctl.sh`, where they
are usual shell commands that a person can run.

## History

This tool was at `eden/tools/cictl`. Only 1 repository adopted it. That
repository then failed 100 CI runs in sequence, which was every run after the
adoption. There were 3 causes. All 3 causes are now corrected or recorded.

1. The renderer wrote `runs-on: ubuntu-latest` as a fixed value. Thus the jobs
   never went to the self-hosted pool. The contract now has a `runner` field.
2. The renderer wrote a `container:` block that used `${{ secrets.GITHUB_TOKEN }}`
   to authenticate against a **private** package. This returns 403. For a
   self-hosted pool, `cictl` now writes no container block.
3. The generated workflows call the `cictl` binary. That binary was in no image
   and in no PATH. The binary is now installed into `ghcr.io/gophersys/base`.

`git subtree split` extracted this repository, thus the initial authorship and
the initial dates stay correct. The module does not depend on the monorepo that
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
kernel. The addition of a pool must never make a change to this code necessary.

`container` selects if the jobs run in `image`:

| `container` | What is emitted | When to use it |
| --- | --- | --- |
| `false` | no container block | a self-hosted pool whose runner image **is** the toolchain |
| `true` | `container:` and the registry credentials | a GitHub-hosted runner that needs an image |

`image` is necessary when `container` is `true`. `image` **must be absent** when
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
| `conformance` | Makes sure that `.ci/ctl.sh` implements each verb that the contract names. |
| `generate` | Writes the provider workflows. |
| `drift` | Renders the workflows again in memory and fails on each hand edit. **This command is the gate.** |
| `affected` | Lists the project roots that changed after a base ref. |
| `updatability` | Shows an aligned table of the pinned tool versions. It can also probe upstream. |

## The CI of this repository is hand-written, on purpose

`cictl` does not generate its own workflow. If it did, the tool that gates the
build would be a product of the build that it gates. This exception is
deliberate and it is the only exception.

## Development

```sh
bash ./ctl.sh test      # go test ./...
bash ./ctl.sh gate      # fmt + vet + lint + test
```

Each verb runs with `GOWORK=off`. The module must build alone, because CI uses
the module in that way.
