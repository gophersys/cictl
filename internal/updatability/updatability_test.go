package updatability_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gophersys/eden/tools/cictl/internal/updatability"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil { //nolint:gosec // test fixture.
		t.Fatal(err)
	}
}

func TestCollect_Dockerfile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "Dockerfile", "FROM x\nARG GO_VERSION=1.26.4   # comment\nARG BUN_VERSION=1.3.14\nRUN echo hi\n")
	m, err := updatability.Collect(dir, []string{"Dockerfile"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := pinMap(m)
	if got["GO_VERSION"] != "1.26.4" {
		t.Errorf("GO_VERSION = %q", got["GO_VERSION"])
	}
	if got["BUN_VERSION"] != "1.3.14" {
		t.Errorf("BUN_VERSION = %q", got["BUN_VERSION"])
	}
}

func TestCollect_Env(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "harnesses/versions.env", "# header\nCLAUDE_CODE_VERSION=2.1.177\nOMP_VERSION=15.12.4\n\n# c\nCODEX_VERSION=0.139.0\n")
	m, err := updatability.Collect(dir, []string{"harnesses/versions.env"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := pinMap(m)
	if got["CLAUDE_CODE_VERSION"] != "2.1.177" {
		t.Errorf("CLAUDE_CODE_VERSION = %q", got["CLAUDE_CODE_VERSION"])
	}
	if got["OMP_VERSION"] != "15.12.4" || got["CODEX_VERSION"] != "0.139.0" {
		t.Errorf("env pins = %v", got)
	}
}

func TestCollect_GoMod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go/alpha/go.mod", "module x\n\ngo 1.26\n\nrequire (\n\tgithub.com/foo/bar v1.2.3\n\tgolang.org/x/sys v0.45.0 // indirect\n)\n\nrequire github.com/baz/qux v0.1.0\n")
	m, err := updatability.Collect(dir, []string{"**/go.mod"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := pinMap(m)
	if got["github.com/foo/bar"] != "v1.2.3" {
		t.Errorf("foo/bar = %q", got["github.com/foo/bar"])
	}
	if got["golang.org/x/sys (indirect)"] != "v0.45.0" {
		t.Errorf("x/sys = %q (want indirect-tagged)", got["golang.org/x/sys (indirect)"])
	}
	if got["github.com/baz/qux"] != "v0.1.0" {
		t.Errorf("single-line require not parsed: %q", got["github.com/baz/qux"])
	}
}

func TestCollect_PackageJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "typescript/app/package.json", `{"dependencies":{"react":"^19.0.0"},"devDependencies":{"vitest":"3.0.0"}}`)
	m, err := updatability.Collect(dir, []string{"typescript/**/package.json"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := pinMap(m)
	if got["react"] != "^19.0.0" {
		t.Errorf("react = %q", got["react"])
	}
	if got["vitest (dev)"] != "3.0.0" {
		t.Errorf("vitest = %q (want dev-tagged)", got["vitest (dev)"])
	}
}

func TestCollect_SkipsNoiseDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, "go/alpha/go.mod", "module x\n\ngo 1.26\n\nrequire github.com/real/dep v1.0.0\n")
	write(t, dir, "node_modules/pkg/go.mod", "module y\n\ngo 1.26\n\nrequire github.com/vendored/dep v9.9.9\n")
	m, err := updatability.Collect(dir, []string{"**/go.mod"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := pinMap(m)
	if _, ok := got["github.com/vendored/dep"]; ok {
		t.Errorf("a node_modules go.mod should be skipped; got %v", got)
	}
	if got["github.com/real/dep"] != "v1.0.0" {
		t.Errorf("real dep missing: %v", got)
	}
}

func TestCollect_MissingGlobIsNotError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, err := updatability.Collect(dir, []string{"harnesses/versions.env", "**/go.mod"})
	if err != nil {
		t.Fatalf("a glob matching nothing must not error: %v", err)
	}
	if len(m.Pins) != 0 {
		t.Fatalf("expected no pins, got %v", m.Pins)
	}
}

// fakeResolver returns canned latest versions so CheckLatest is tested offline.
type fakeResolver map[string]string

func (f fakeResolver) Latest(p updatability.Pin) (string, bool) {
	v, ok := f[p.Tool]
	return v, ok
}

func TestCheckLatest_StatusVerdicts(t *testing.T) {
	t.Parallel()
	m := updatability.Matrix{Pins: []updatability.Pin{
		{Tool: "github.com/foo/bar", Pinned: "v1.2.3", Source: "go.mod", Kind: "go.mod"},
		{Tool: "github.com/foo/old", Pinned: "v1.0.0", Source: "go.mod", Kind: "go.mod"},
		{Tool: "GO_VERSION", Pinned: "1.26.4", Source: "Dockerfile", Kind: "dockerfile"},
	}}
	r := fakeResolver{
		"github.com/foo/bar": "v1.2.3", // current
		"github.com/foo/old": "v2.0.0", // outdated
		// GO_VERSION absent → unknown
	}
	rows := updatability.CheckLatest(m, r)
	byTool := map[string]updatability.Row{}
	for _, row := range rows {
		byTool[row.Pin.Tool] = row
	}
	if byTool["github.com/foo/bar"].Status != "current" {
		t.Errorf("bar status = %q, want current", byTool["github.com/foo/bar"].Status)
	}
	if byTool["github.com/foo/old"].Status != "outdated" {
		t.Errorf("old status = %q, want outdated", byTool["github.com/foo/old"].Status)
	}
	if byTool["GO_VERSION"].Status != "unknown" {
		t.Errorf("GO_VERSION status = %q, want unknown", byTool["GO_VERSION"].Status)
	}
}

func TestCheckLatest_NilResolverIsAllUnknown(t *testing.T) {
	t.Parallel()
	m := updatability.Matrix{Pins: []updatability.Pin{{Tool: "x", Pinned: "1", Source: "s", Kind: "env"}}}
	rows := updatability.CheckLatest(m, nil)
	if len(rows) != 1 || rows[0].Status != "unknown" {
		t.Fatalf("nil resolver should yield unknown rows; got %+v", rows)
	}
}

func TestRender_NonEmpty(t *testing.T) {
	t.Parallel()
	m := updatability.Matrix{Pins: []updatability.Pin{{Tool: "x", Pinned: "1.0", Source: "go.mod", Kind: "go.mod"}}}
	out := m.Render()
	if out == "" || !contains(out, "TOOL") || !contains(out, "x") {
		t.Fatalf("Render output unexpected:\n%s", out)
	}
}

func pinMap(m updatability.Matrix) map[string]string {
	out := map[string]string{}
	for _, p := range m.Pins {
		out[p.Tool] = p.Pinned
	}
	return out
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
