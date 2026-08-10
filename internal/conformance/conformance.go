// Package conformance asserts that a repo's .ci/ directory is canonical: the
// contract is present and valid; .ci/ctl.sh defines every verb the tiers
// reference (discovered via the hidden `.ci/ctl.sh __verbs` lister); the github
// provider directory exists; and the declared image is known. Any gap is a
// precise, actionable failure — this is what makes "the contract and the repo
// agree" a mechanical gate rather than a hope.
package conformance

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gophersys/cictl/internal/cirepo"
	"github.com/gophersys/cictl/internal/contract"
	"github.com/gophersys/cictl/internal/failure"
	"github.com/gophersys/cictl/internal/validation"
)

// Report is the conformance outcome: the gaps found (empty means conformant).
type Report struct {
	Gaps []string
}

// OK reports whether the repo is conformant.
func (r Report) OK() bool { return len(r.Gaps) == 0 }

// NonConformantError is the typed aggregate returned by Report.Error: the set of
// gaps that made a repo non-conformant.
type NonConformantError struct {
	Gaps []string
}

func (e *NonConformantError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d conformance gap(s):", len(e.Gaps))
	for _, g := range e.Gaps {
		fmt.Fprintf(&b, "\n  - %s", g)
	}
	return b.String()
}

// Error renders the gaps as one typed error, or nil when conformant.
func (r Report) Error() error {
	if r.OK() {
		return nil
	}
	return &NonConformantError{Gaps: r.Gaps}
}

// Check runs every conformance assertion for the repo at repoDir.
func Check(repoDir string) Report {
	layout := cirepo.New(repoDir)
	var gaps []string

	// 1. contract present.
	if !layout.FileExists(layout.ContractPath()) {
		return Report{Gaps: []string{fmt.Sprintf("missing contract: %s", rel(repoDir, layout.ContractPath()))}}
	}
	c, err := layout.LoadContract()
	if err != nil {
		return Report{Gaps: []string{fmt.Sprintf("contract unreadable: %v", err)}}
	}

	// 2. contract valid (structural + semantic).
	if res := validation.Validate(c, nil); !res.OK() {
		for _, p := range res.Problems {
			gaps = append(gaps, "invalid contract: "+p.String())
		}
	}

	// 3. .ci/ctl.sh present and lists every referenced verb.
	if !layout.FileExists(layout.CtlPath()) {
		gaps = append(gaps, fmt.Sprintf("missing dispatcher: %s", rel(repoDir, layout.CtlPath())))
	} else {
		gaps = append(gaps, verbGaps(repoDir, layout.CtlPath(), c)...)
	}

	// 4. github provider directory present (only when github is a declared provider).
	for _, p := range c.Providers {
		if p == contract.ProviderGithub {
			if !dirExists(layout.ProvidersGithubDir()) {
				gaps = append(gaps, fmt.Sprintf("github provider declared but %s is absent", rel(repoDir, layout.ProvidersGithubDir())))
			}
		}
	}

	// 5. declared image is known, but only when the runner actually uses one.
	// A self-hosted pool names no image: its runner image is the toolchain.
	if c.Runner.Container && !knownImage(c.Image) {
		gaps = append(gaps, fmt.Sprintf("unknown image %q", c.Image))
	}

	sort.Strings(gaps)
	return Report{Gaps: gaps}
}

// verbGaps invokes the repo's `.ci/ctl.sh __verbs` hidden lister and reports any
// tier-referenced verb the dispatcher does not define. Requiring the lister (vs
// parsing the case arms) keeps the source of truth in the script itself.
func verbGaps(repoDir, ctlPath string, c *contract.Contract) []string {
	defined, err := listVerbs(repoDir, ctlPath)
	if err != nil {
		return []string{fmt.Sprintf("could not enumerate ctl.sh verbs (needs a `__verbs` hidden lister): %v", err)}
	}
	have := map[string]struct{}{}
	for _, v := range defined {
		have[v] = struct{}{}
	}
	var gaps []string
	seen := map[string]struct{}{}
	for tierName, tier := range map[string]contract.Tier{
		"pr":      c.Tiers.Pr,
		"merge":   c.Tiers.Merge,
		"nightly": c.Tiers.Nightly,
	} {
		for _, v := range tier.Verbs {
			key := tierName + "/" + v
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if _, ok := have[v]; !ok {
				gaps = append(gaps, fmt.Sprintf("tier %q references verb %q which .ci/ctl.sh does not define", tierName, v))
			}
		}
	}
	return gaps
}

// errNoVerbs is returned when a repo's .ci/ctl.sh __verbs lister prints nothing —
// the dispatcher must enumerate its verbs for the gate to check tier references.
var errNoVerbs = errors.New("`.ci/ctl.sh __verbs` produced no output (the hidden verb lister must print one verb per line)")

// listVerbs runs `bash <ctlPath> __verbs` and returns the whitespace-separated
// verb names it prints (one per line or space-separated, both tolerated). The
// command is fixed (`bash <ctl> __verbs`); ctlPath is a layout-resolved path, not
// attacker-controlled input.
func listVerbs(repoDir, ctlPath string) ([]string, error) {
	cmd := exec.Command("bash", ctlPath, "__verbs") //nolint:gosec // fixed argv; ctlPath is a resolved .ci/ctl.sh path.
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if asExit(err, &ee) {
			return nil, failure.Wrapf(err, "run __verbs: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, failure.Wrap(err, "run __verbs")
	}
	var verbs []string
	for _, f := range strings.Fields(string(out)) {
		verbs = append(verbs, strings.TrimSpace(f))
	}
	if len(verbs) == 0 {
		return nil, errNoVerbs
	}
	return verbs, nil
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func knownImage(img contract.Image) bool {
	for _, k := range contract.Images {
		if k == img {
			return true
		}
	}
	return false
}

func asExit(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError) //nolint:errorlint // direct type at the call site.
	if ok {
		*target = ee
	}
	return ok
}
