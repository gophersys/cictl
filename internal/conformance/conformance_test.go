package conformance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gophersys/cictl/internal/conformance"
)

const canonicalContract = `apiVersion: eden.ci/v1
repo: demo
kind: libraries
runner:
  runsOn: arc-org
  container: false
languages: [go]
tiers:
  pr:
    verbs: [fast]
    timeoutMinutes: 15
  merge:
    verbs: [substrate]
    substrate: [docker]
    privileged: true
  nightly:
    verbs: [fast, substrate]
    substrate: [docker]
    privileged: true
    schedule: "0 6 * * *"
providers: [github]
toolMatrix:
  sources: ["**/go.mod"]
`

// ctlWithVerbs builds a minimal .ci/ctl.sh whose __verbs lister prints the given
// verbs (one per line).
func ctlWithVerbs(verbs ...string) string {
	body := "#!/usr/bin/env bash\ncase \"$1\" in\n  __verbs)\n"
	for _, v := range verbs {
		body += "    echo " + v + "\n"
	}
	body += "    ;;\nesac\n"
	return body
}

// materialize writes a .ci/ directory with the given contract + ctl.sh and a
// github provider dir, returning the repo root.
func materialize(t *testing.T, contractYAML, ctl string) string {
	t.Helper()
	dir := t.TempDir()
	ciDir := filepath.Join(dir, ".ci")
	if err := os.MkdirAll(filepath.Join(ciDir, "providers", "github"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ciDir, "ci.contract.yaml"), []byte(contractYAML), 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ciDir, "ctl.sh"), []byte(ctl), 0o750); err != nil { //nolint:gosec // the ctl.sh fixture must be executable to run __verbs
		t.Fatalf("write ctl.sh: %v", err)
	}
	return dir
}

func TestCheck_Canonical(t *testing.T) {
	t.Parallel()
	dir := materialize(t, canonicalContract, ctlWithVerbs("fast", "substrate"))
	rep := conformance.Check(dir)
	if !rep.OK() {
		t.Fatalf("canonical repo flagged: %v", rep.Error())
	}
}

func TestCheck_MissingVerb(t *testing.T) {
	t.Parallel()
	// ctl.sh defines "fast" but the contract references "substrate" too.
	dir := materialize(t, canonicalContract, ctlWithVerbs("fast"))
	rep := conformance.Check(dir)
	if rep.OK() {
		t.Fatal("missing verb not caught (vacuous conformance)")
	}
	if !containsSub(rep.Gaps, "substrate") {
		t.Fatalf("gap should name the missing verb; got %v", rep.Gaps)
	}
}

func TestCheck_MissingContract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rep := conformance.Check(dir)
	if rep.OK() {
		t.Fatal("a repo with no contract should not be conformant")
	}
	if !containsSub(rep.Gaps, "missing contract") {
		t.Fatalf("gap should mention missing contract; got %v", rep.Gaps)
	}
}

func TestCheck_NoVerbsLister(t *testing.T) {
	t.Parallel()
	// ctl.sh has no __verbs arm — conformance must fail loudly, not pass.
	dir := materialize(t, canonicalContract, "#!/usr/bin/env bash\nexit 0\n")
	rep := conformance.Check(dir)
	if rep.OK() {
		t.Fatal("a ctl.sh without __verbs should fail conformance")
	}
}

func TestCheck_InvalidContract(t *testing.T) {
	t.Parallel()
	bad := canonicalContract + "extra: 1\n" // strict decode rejects this
	dir := materialize(t, bad, ctlWithVerbs("fast", "substrate"))
	rep := conformance.Check(dir)
	if rep.OK() {
		t.Fatal("an invalid contract should fail conformance")
	}
}

func containsSub(gaps []string, sub string) bool {
	for _, g := range gaps {
		if len(g) >= len(sub) && stringContains(g, sub) {
			return true
		}
	}
	return false
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
