package workflow_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gophersys/cictl/internal/contract"
	"github.com/gophersys/cictl/internal/workflow"
)

func sampleContract() *contract.Contract {
	return &contract.Contract{
		APIVersion: contract.APIVersion,
		Repo:       "libs",
		Kind:       contract.KindLibraries,
		// The GitHub-hosted + container mode. Stated explicitly rather than left to
		// the zero value, so a test that asserts container behavior cannot silently
		// start asserting self-hosted behavior. selfHostedContract() is the other mode.
		Runner:    contract.Runner{RunsOn: "ubuntu-latest", Container: true},
		Image:     contract.ImageBase,
		Languages: []contract.Language{contract.LanguageGo, contract.LanguageTypeScript},
		Tiers: contract.Tiers{
			Pr:    contract.Tier{Verbs: []string{"affected-gate-fast"}, TimeoutMinutes: 15},
			Merge: contract.Tier{Verbs: []string{"affected-gate-substrate"}, Substrate: []contract.Substrate{contract.SubstrateDocker, contract.SubstrateK3d}, Privileged: true, TimeoutMinutes: 30},
			Nightly: contract.Tier{
				Verbs:          []string{"gate-all", "updatability"},
				Substrate:      []contract.Substrate{contract.SubstrateDocker, contract.SubstrateNats},
				Privileged:     true,
				Schedule:       "0 6 * * *",
				TimeoutMinutes: 90,
			},
		},
		Providers:  []contract.Provider{contract.ProviderGithub},
		ToolMatrix: contract.ToolMatrix{Sources: []string{"**/go.mod"}},
	}
}

// TestRender_Deterministic asserts the same contract renders byte-identical YAML
// across two calls (the property drift relies on).
func TestRender_Deterministic(t *testing.T) {
	t.Parallel()
	c := sampleContract()
	a, err := workflow.Render(c)
	if err != nil {
		t.Fatalf("Render a: %v", err)
	}
	b, err := workflow.Render(c)
	if err != nil {
		t.Fatalf("Render b: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("file count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			t.Fatalf("file %d name differs: %q vs %q", i, a[i].Name, b[i].Name)
		}
		if !bytes.Equal(a[i].Content, b[i].Content) {
			t.Fatalf("file %q is not deterministic", a[i].Name)
		}
	}
}

// TestRender_FileSet asserts exactly the three expected workflow files are produced.
func TestRender_FileSet(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(sampleContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Name] = true
	}
	for _, want := range []string{"on-pr.yml", "on-push.yml", "nightly.yml"} {
		if !got[want] {
			t.Errorf("missing rendered file %q", want)
		}
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
}

// TestRender_SubstrateJobIsPrivileged asserts a tier with a substrate gets the
// privileged container + dockerd-start step, and a tier without gets neither.
func TestRender_SubstrateJobIsPrivileged(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(sampleContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	onpr := contentOf(t, files, "on-pr.yml")

	// The pr job (no substrate) must NOT be privileged.
	prSection, mergeSection := splitJobs(t, onpr)
	if strings.Contains(prSection, "--privileged") {
		t.Errorf("pr job (no substrate) should not be privileged:\n%s", prSection)
	}
	if strings.Contains(prSection, "Start docker host") {
		t.Errorf("pr job (no substrate) should not start dockerd")
	}
	// The merge job (substrate) MUST be privileged and start dockerd.
	if !strings.Contains(mergeSection, "--privileged -v /var/run/docker.sock:/var/run/docker.sock") {
		t.Errorf("merge job (substrate) must be privileged with the docker socket:\n%s", mergeSection)
	}
	if !strings.Contains(mergeSection, "Start docker host") {
		t.Errorf("merge job (substrate) must start dockerd")
	}
}

// TestRender_VerbStepsInOrder asserts each tier verb becomes its own step, in
// declared order, invoking `bash .ci/ctl.sh <verb>`.
func TestRender_VerbStepsInOrder(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(sampleContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	nightly := contentOf(t, files, "nightly.yml")
	gateIdx := strings.Index(nightly, "run: bash .ci/ctl.sh gate-all")
	updIdx := strings.Index(nightly, "run: bash .ci/ctl.sh updatability")
	if gateIdx < 0 || updIdx < 0 {
		t.Fatalf("nightly missing verb steps:\n%s", nightly)
	}
	if gateIdx > updIdx {
		t.Errorf("nightly verbs out of order: gate-all should precede updatability")
	}
}

// TestRender_NightlyHasSchedule asserts the cron schedule lands in nightly only.
func TestRender_NightlyHasSchedule(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(sampleContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	nightly := contentOf(t, files, "nightly.yml")
	if !strings.Contains(nightly, `cron: "0 6 * * *"`) {
		t.Errorf("nightly missing the cron schedule:\n%s", nightly)
	}
	onpr := contentOf(t, files, "on-pr.yml")
	if strings.Contains(onpr, "cron:") {
		t.Errorf("on-pr must not carry a cron schedule")
	}
}

// TestRender_RejectsNoGithubProvider asserts a contract without github cannot
// render github workflows (the renderer refuses rather than emit empty files).
func TestRender_RejectsNoGithubProvider(t *testing.T) {
	t.Parallel()
	c := sampleContract()
	c.Providers = nil
	if _, err := workflow.Render(c); err == nil {
		t.Fatal("Render should fail when github is not a declared provider")
	}
}

// TestRender_GeneratedBanner asserts every file carries the do-not-edit banner.
func TestRender_GeneratedBanner(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(sampleContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, f := range files {
		if !strings.HasPrefix(string(f.Content), "# GENERATED by cictl") {
			t.Errorf("%s missing generated banner", f.Name)
		}
	}
}

func contentOf(t *testing.T, files []workflow.File, name string) string {
	t.Helper()
	for _, f := range files {
		if f.Name == name {
			return string(f.Content)
		}
	}
	t.Fatalf("no rendered file %q", name)
	return ""
}

// splitJobs returns the on-pr content split at the "merge:" job boundary so a
// test can assert on each job independently.
func splitJobs(t *testing.T, onpr string) (pr, merge string) {
	t.Helper()
	idx := strings.Index(onpr, "\n  merge:\n")
	if idx < 0 {
		t.Fatalf("on-pr has no merge job:\n%s", onpr)
	}
	return onpr[:idx], onpr[idx:]
}

// selfHostedContract is sampleContract moved onto a self-hosted pool whose runner
// image already carries the toolchain.
func selfHostedContract() *contract.Contract {
	c := sampleContract()
	c.Runner = contract.Runner{RunsOn: "arc-org"}
	c.Image = ""
	return c
}

// TestRender_RunsOnFromContract asserts the runner is contract-driven rather than
// hardcoded. Hardcoding ubuntu-latest is what made the only adopting repo fail
// 100 consecutive runs: its jobs never reached the self-hosted fleet.
func TestRender_RunsOnFromContract(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(selfHostedContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, f := range files {
		s := string(f.Content)
		if !strings.Contains(s, "runs-on: arc-org") {
			t.Errorf("%s: want `runs-on: arc-org`, got:\n%s", f.Name, s)
		}
		if strings.Contains(s, "ubuntu-latest") {
			t.Errorf("%s: still emits the hardcoded ubuntu-latest", f.Name)
		}
	}
}

// TestRender_SelfHostedOmitsContainer asserts no container block is emitted for a
// self-hosted pool. The pool's runner image IS the toolchain, so a container
// block would re-pay a cold image pull on every job — measured at 5m17s — and
// would authenticate to a private package with GITHUB_TOKEN, which returns 403.
func TestRender_SelfHostedOmitsContainer(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(selfHostedContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, f := range files {
		s := string(f.Content)
		for _, forbidden := range []string{"container:", "credentials:", "secrets.GITHUB_TOKEN"} {
			if strings.Contains(s, forbidden) {
				t.Errorf("%s: self-hosted job must not emit %q:\n%s", f.Name, forbidden, s)
			}
		}
	}
}

// TestRender_ContainerWhenRequested asserts the container path still works, so a
// GitHub-hosted runner can keep using an image.
func TestRender_ContainerWhenRequested(t *testing.T) {
	t.Parallel()
	c := sampleContract()
	c.Runner = contract.Runner{RunsOn: "ubuntu-latest", Container: true}
	files, err := workflow.Render(c)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, f := range files {
		s := string(f.Content)
		if !strings.Contains(s, "container:") {
			t.Errorf("%s: want a container block when Container is true:\n%s", f.Name, s)
		}
		if !strings.Contains(s, "runs-on: ubuntu-latest") {
			t.Errorf("%s: want runs-on: ubuntu-latest", f.Name)
		}
	}
}

// TestRender_SubstrateWithoutContainerStillStartsDocker asserts a substrate tier
// on a self-hosted pool keeps its dockerd bootstrap. The pool provides docker via
// a dind sidecar, but the step must survive the container block being dropped.
func TestRender_SubstrateWithoutContainerStillStartsDocker(t *testing.T) {
	t.Parallel()
	files, err := workflow.Render(selfHostedContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var body string
	for _, f := range files {
		if f.Name == "on-pr.yml" {
			body = string(f.Content)
		}
	}
	if !strings.Contains(body, "Start docker host") {
		t.Errorf("on-pr.yml: substrate tier lost its docker bootstrap:\n%s", body)
	}
}
