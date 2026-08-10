package contract_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gophersys/cictl/internal/contract"
)

const sample = `apiVersion: eden.ci/v1
repo: libs
kind: libraries
runner:
  runsOn: ubuntu-latest
  container: true
image: ghcr.io/gophersys/base
languages: [go, typescript]
tiers:
  pr:
    verbs: [affected-gate-fast]
    timeoutMinutes: 15
  merge:
    verbs: [affected-gate-substrate]
    substrate: [docker, k3d]
    privileged: true
  nightly:
    verbs: [gate-all]
    substrate: [docker]
    privileged: true
    schedule: "0 6 * * *"
providers: [github]
toolMatrix:
  sources: ["**/go.mod"]
`

func TestDecode_RoundTrip(t *testing.T) {
	t.Parallel()
	c, err := contract.Decode([]byte(sample))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if c.APIVersion != contract.APIVersion {
		t.Errorf("apiVersion = %q", c.APIVersion)
	}
	if c.Kind != contract.KindLibraries {
		t.Errorf("kind = %q", c.Kind)
	}
	if c.Image != contract.ImageBase {
		t.Errorf("image = %q", c.Image)
	}
	if len(c.Tiers.Pr.Verbs) != 1 || c.Tiers.Pr.Verbs[0] != "affected-gate-fast" {
		t.Errorf("pr verbs = %v", c.Tiers.Pr.Verbs)
	}
	if len(c.Tiers.Merge.Substrate) != 2 {
		t.Errorf("merge substrate = %v", c.Tiers.Merge.Substrate)
	}
	if !c.Tiers.Merge.Privileged {
		t.Errorf("merge should be privileged")
	}
	if c.Tiers.Nightly.Schedule != "0 6 * * *" {
		t.Errorf("nightly schedule = %q", c.Tiers.Nightly.Schedule)
	}
}

func TestDecode_RejectsUnknownField(t *testing.T) {
	t.Parallel()
	if _, err := contract.Decode([]byte(sample + "mystery: 1\n")); err == nil {
		t.Fatal("strict decode should reject an unknown field")
	}
}

func TestLoad_FromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ci.contract.yaml")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, err := contract.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Repo != "libs" {
		t.Errorf("repo = %q", c.Repo)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	t.Parallel()
	if _, err := contract.Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("Load of a missing file should error")
	}
}
