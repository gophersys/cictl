package cirepo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gophersys/cictl/internal/cirepo"
	"github.com/gophersys/cictl/internal/contract"
)

func newContract() *contract.Contract {
	return &contract.Contract{
		APIVersion: contract.APIVersion,
		Repo:       "libs",
		Kind:       contract.KindLibraries,
		Image:      contract.ImageBase,
		Languages:  []contract.Language{contract.LanguageGo},
		Tiers: contract.Tiers{
			Pr:      contract.Tier{Verbs: []string{"affected-gate-fast"}, TimeoutMinutes: 15},
			Merge:   contract.Tier{Verbs: []string{"affected-gate-substrate"}, Substrate: []contract.Substrate{contract.SubstrateDocker}, Privileged: true},
			Nightly: contract.Tier{Verbs: []string{"gate-all"}, Substrate: []contract.Substrate{contract.SubstrateDocker}, Privileged: true, Schedule: "0 6 * * *"},
		},
		Providers:  []contract.Provider{contract.ProviderGithub},
		ToolMatrix: contract.ToolMatrix{Sources: []string{"**/go.mod"}},
	}
}

func TestWrite_ThenDriftClean(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	layout := cirepo.New(dir)
	c := newContract()

	written, err := layout.Write(c)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Two destinations per file × 3 files = 6 paths.
	if len(written) != 6 {
		t.Fatalf("expected 6 written paths, got %d: %v", len(written), written)
	}
	// Both the provider copy and the real workflow must exist for each file.
	for _, name := range []string{"on-pr.yml", "on-push.yml", "nightly.yml"} {
		for _, p := range []string{
			filepath.Join(dir, ".ci", "providers", "github", name),
			filepath.Join(dir, ".github", "workflows", name),
		} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("expected %s to exist: %v", p, err)
			}
		}
	}

	divs, err := layout.Drift(c)
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	if len(divs) != 0 {
		t.Fatalf("freshly-written repo should have no drift, got %d: %+v", len(divs), divs)
	}
}

func TestDrift_DetectsHandEdit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	layout := cirepo.New(dir)
	c := newContract()
	if _, err := layout.Write(c); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Hand-edit a generated workflow.
	target := filepath.Join(dir, ".github", "workflows", "on-pr.yml")
	body, err := os.ReadFile(target) //nolint:gosec // target is a test-built path under t.TempDir.
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(target, append(body, []byte("\n# sneaky hand-edit\n")...), 0o600); err != nil { //nolint:gosec // target is a test-built path under t.TempDir.
		t.Fatalf("hand-edit: %v", err)
	}

	divs, err := layout.Drift(c)
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	if len(divs) == 0 {
		t.Fatal("drift not detected after a hand-edit (vacuous gate)")
	}
	var sawTarget bool
	for _, d := range divs {
		if d.Path == ".github/workflows/on-pr.yml" {
			sawTarget = true
			if d.Reason != "content differs" {
				t.Errorf("reason = %q, want content differs", d.Reason)
			}
		}
	}
	if !sawTarget {
		t.Fatalf("drift did not flag the hand-edited file; got %+v", divs)
	}
}

func TestDrift_DetectsMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	layout := cirepo.New(dir)
	c := newContract()
	if _, err := layout.Write(c); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".github", "workflows", "nightly.yml")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	divs, err := layout.Drift(c)
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	var sawMissing bool
	for _, d := range divs {
		if d.Path == ".github/workflows/nightly.yml" && d.Reason == "missing" {
			sawMissing = true
		}
	}
	if !sawMissing {
		t.Fatalf("drift did not flag the deleted file as missing; got %+v", divs)
	}
}
