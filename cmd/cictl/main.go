// Command cictl is the Eden CI contract tool. The CI contract (.ci/ci.contract.yaml)
// is expressed once as Go structs (go-first-emit); cictl emits the JSON Schema from
// them, validates an instance, renders the provider workflows deterministically,
// drift-checks the committed workflows against the contract, asserts a repo's .ci/
// is canonical, lists the affected project roots, and reports the pinned-version
// matrix. The workflows are GENERATED, never hand-edited — `cictl drift` is the
// CI-on-CI gate that enforces it.
//
// Usage:
//
//	cictl schema       [-o FILE]
//	cictl validate     [-f .ci/ci.contract.yaml]
//	cictl conformance  [-C repo]
//	cictl affected     [--base origin/main]
//	cictl generate     [-C repo]
//	cictl drift        [-C repo]
//	cictl updatability [-C repo] [--check-latest]
package main

import (
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
