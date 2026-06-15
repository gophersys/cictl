package cirepo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gophersys/eden/tools/cictl/internal/contract"
	"github.com/gophersys/eden/tools/cictl/internal/failure"
)

// Divergence is one drift finding: a destination whose on-disk content does not
// match the freshly-rendered content (or is missing), with a unified-ish diff.
type Divergence struct {
	Path   string // repo-relative
	Reason string // "missing" | "content differs"
	Diff   string // line-level diff, present when content differs
}

// Drift re-renders the contract in memory and compares every destination (both
// the provider source copies and the real workflow files) against the rendered
// bytes. It returns the divergences, sorted by path; an empty slice means the
// committed workflows are exactly what the contract produces.
func (l Layout) Drift(c *contract.Contract) ([]Divergence, error) {
	targets, err := l.Targets(c)
	if err != nil {
		return nil, err
	}
	var out []Divergence
	for _, t := range targets {
		for _, dest := range []string{t.Provider, t.Workflow} {
			d, err := compare(l.Root, dest, t.Content)
			if err != nil {
				return nil, err
			}
			if d != nil {
				out = append(out, *d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// compare diffs one destination against the expected bytes. A missing file is a
// divergence; differing content yields a line diff. A nil *Divergence with a nil
// error means "no divergence" — the in-band, allocation-free signal the caller
// already branches on.
func compare(root, dest string, want []byte) (*Divergence, error) {
	rel := relSlash(root, dest)
	got, err := os.ReadFile(dest) //nolint:gosec // dest is a resolved layout path.
	if err != nil {
		if os.IsNotExist(err) {
			return &Divergence{Path: rel, Reason: "missing"}, nil
		}
		return nil, failure.Wrapf(err, "read %s", dest)
	}
	if bytes.Equal(got, want) {
		return nil, nil //nolint:nilnil // (nil, nil) is the documented "no divergence" result.
	}
	return &Divergence{Path: rel, Reason: "content differs", Diff: lineDiff(string(got), string(want))}, nil
}

// relSlash renders dest relative to root with forward slashes, falling back to
// dest when it is not under root.
func relSlash(root, dest string) string {
	r, err := filepath.Rel(root, dest)
	if err != nil {
		return filepath.ToSlash(dest)
	}
	return filepath.ToSlash(r)
}

// lineDiff renders a compact, deterministic line-level diff: lines present on
// disk but not expected are prefixed "-", expected-but-absent lines "+". It is a
// set-difference view (not an alignment) — enough to point a reviewer at the
// hand-edit without pulling a diff library.
func lineDiff(got, want string) string {
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	wantSet := map[string]int{}
	for _, l := range wantLines {
		wantSet[l]++
	}
	gotSet := map[string]int{}
	for _, l := range gotLines {
		gotSet[l]++
	}
	var b strings.Builder
	for _, l := range gotLines {
		if wantSet[l] == 0 {
			fmt.Fprintf(&b, "- %s\n", l)
		}
	}
	for _, l := range wantLines {
		if gotSet[l] == 0 {
			fmt.Fprintf(&b, "+ %s\n", l)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
