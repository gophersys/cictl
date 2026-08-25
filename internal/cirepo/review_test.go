package cirepo_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gophersys/cictl/internal/cirepo"
	"github.com/gophersys/cictl/internal/contract"
	"github.com/gophersys/cictl/internal/workflow"
)

// reviewContract is newContract with the reviewer turned on.
func reviewContract() *contract.Contract {
	c := newContract()
	c.Review = contract.Review{
		Enabled: true, Ref: "0123456789abcdef0123456789abcdef01234567", Release: "v0.6.0",
		Tier: contract.ReviewTierModule, RunsOn: "arc-review", TimeoutMinutes: 30,
		StreamStore: contract.ReviewStreamMinio, StreamEndpoint: "https://minio.example.invalid", StreamBucket: "review-streams",
	}
	return c
}

// TestWrite_ReviewCallerReachesBothHomes. The caller is written to the provider
// source copy AND to .github/workflows — the file GitHub actually reads. A
// generator that wrote only one of them would drift on the very next run.
func TestWrite_ReviewCallerReachesBothHomes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	layout := cirepo.New(dir)

	written, err := layout.Write(reviewContract())
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Two destinations per file × 4 files = 8 paths.
	if len(written) != 8 {
		t.Fatalf("expected 8 written paths with the reviewer enabled, got %d: %v", len(written), written)
	}
	for _, p := range []string{
		filepath.Join(dir, ".ci", "providers", "github", workflow.ReviewFileName),
		filepath.Join(dir, ".github", "workflows", workflow.ReviewFileName),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the caller is missing from %s: %v", p, err)
		}
	}

	divs, err := layout.Drift(reviewContract())
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	if len(divs) != 0 {
		t.Fatalf("a freshly generated repository reported drift: %+v", divs)
	}
}

// TestDrift_CatchesAHandEditedCaller. THIS IS THE POINT OF GENERATING THE CALLER.
// Three hand-kept copies of the review job diverged because a human maintained
// them, and a fix to one reached some repositories and not others. After this, a
// copy that diverges fails that repository's own pull request.
func TestDrift_CatchesAHandEditedCaller(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	layout := cirepo.New(dir)
	c := reviewContract()
	if _, err := layout.Write(c); err != nil {
		t.Fatalf("Write: %v", err)
	}

	target := filepath.Join(dir, ".github", "workflows", workflow.ReviewFileName)
	body, err := os.ReadFile(target) //nolint:gosec // a path this test just wrote.
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Raising the budget by hand is the edit that actually costs money, so it is
	// the one worth pinning.
	edited := strings.Replace(string(body), `budget-usd: "25"`, `budget-usd: "250"`, 1)
	if edited == string(body) {
		t.Fatal("the generated caller no longer carries the module tier's budget; this test is checking nothing")
	}
	if err := os.WriteFile(target, []byte(edited), 0o600); err != nil { //nolint:gosec // a path this test just wrote inside its own t.TempDir().
		t.Fatalf("write: %v", err)
	}

	divs, err := layout.Drift(c)
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	found := false
	for _, d := range divs {
		if d.Path == ".github/workflows/"+workflow.ReviewFileName && d.Reason == "content differs" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a hand-raised review budget did not register as drift: %+v", divs)
	}
}

// TestDrift_CatchesADeletedCaller. Deleting the file is the other way to escape
// the reviewer, and it must be as loud as editing it.
func TestDrift_CatchesADeletedCaller(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	layout := cirepo.New(dir)
	c := reviewContract()
	if _, err := layout.Write(c); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".github", "workflows", workflow.ReviewFileName)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	divs, err := layout.Drift(c)
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	for _, d := range divs {
		if d.Path == ".github/workflows/"+workflow.ReviewFileName && d.Reason == "missing" {
			return
		}
	}
	t.Fatalf("a deleted review caller did not register as drift: %+v", divs)
}
