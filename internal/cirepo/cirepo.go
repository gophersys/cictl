// Package cirepo is the one home for a repo's CI on-disk layout: where the
// contract lives, where the generated github workflows are written (both the
// .ci/providers/github source copy and the real .github/workflows files GitHub
// reads), and the load + write + read primitives generate/drift/conformance
// share. Centralizing the paths here keeps "one concept, one home": no other
// package hardcodes these locations.
package cirepo

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/gophersys/eden/tools/cictl/internal/contract"
	"github.com/gophersys/eden/tools/cictl/internal/failure"
	"github.com/gophersys/eden/tools/cictl/internal/workflow"
)

// Layout resolves the CI paths for a repo rooted at repoDir.
type Layout struct {
	Root string
}

// New returns the layout for repoDir.
func New(repoDir string) Layout { return Layout{Root: repoDir} }

// ContractPath is the canonical contract location: .ci/ci.contract.yaml.
func (l Layout) ContractPath() string {
	return filepath.Join(l.Root, ".ci", "ci.contract.yaml")
}

// CtlPath is the repo's CI dispatcher: .ci/ctl.sh.
func (l Layout) CtlPath() string { return filepath.Join(l.Root, ".ci", "ctl.sh") }

// ProvidersGithubDir is the generated-source copy directory.
func (l Layout) ProvidersGithubDir() string {
	return filepath.Join(l.Root, ".ci", "providers", "github")
}

// WorkflowsDir is the real GitHub-read directory.
func (l Layout) WorkflowsDir() string { return filepath.Join(l.Root, ".github", "workflows") }

// LoadContract reads the contract instance from its canonical path.
func (l Layout) LoadContract() (*contract.Contract, error) {
	c, err := contract.Load(l.ContractPath())
	if err != nil {
		return nil, failure.Wrap(err, "load contract")
	}
	return c, nil
}

// Target is one rendered file paired with the two absolute destinations it is
// written to (the provider source copy and the real workflow file).
type Target struct {
	Name     string
	Content  []byte
	Provider string // .ci/providers/github/<name>
	Workflow string // .github/workflows/<name>
}

// Targets renders the contract's workflows and resolves each to its two on-disk
// destinations, sorted by name for a stable order.
func (l Layout) Targets(c *contract.Contract) ([]Target, error) {
	files, err := workflow.Render(c)
	if err != nil {
		return nil, failure.Wrap(err, "render workflows")
	}
	out := make([]Target, 0, len(files))
	for _, f := range files {
		out = append(out, Target{
			Name:     f.Name,
			Content:  f.Content,
			Provider: filepath.Join(l.ProvidersGithubDir(), f.Name),
			Workflow: filepath.Join(l.WorkflowsDir(), f.Name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Write renders and writes every target to BOTH destinations (provider source +
// real workflow). Directories are created as needed. It returns the repo-relative
// paths written, sorted.
func (l Layout) Write(c *contract.Contract) ([]string, error) {
	targets, err := l.Targets(c)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.ProvidersGithubDir(), 0o750); err != nil {
		return nil, failure.Wrap(err, "create providers dir")
	}
	if err := os.MkdirAll(l.WorkflowsDir(), 0o750); err != nil {
		return nil, failure.Wrap(err, "create workflows dir")
	}
	var written []string
	for _, t := range targets {
		for _, dest := range []string{t.Provider, t.Workflow} {
			if err := os.WriteFile(dest, t.Content, 0o600); err != nil {
				return nil, failure.Wrapf(err, "write %s", dest)
			}
			written = append(written, relSlash(l.Root, dest))
		}
	}
	sort.Strings(written)
	return written, nil
}

// fileExists reports whether p is an existing regular file.
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// FileExists is the exported probe used by conformance.
func (l Layout) FileExists(p string) bool { return fileExists(p) }
