// Package updatability reads a contract's toolMatrix.sources, parses the pinned
// versions out of each matched file, and reports a matrix of (tool · pinned ·
// source). It generalises the harness-upgrade-check (ADR-0021) from the three
// harness pins to EVERY pinned dependency in the repo, so "what are we pinned to,
// and is it current" is one uniform query.
//
// Parsing is per-file-kind:
//   - Dockerfile          → `ARG NAME_VERSION=x.y.z` lines
//   - *.env (versions.env) → `KEY=value` lines
//   - go.mod              → the require() block's module + version lines
//   - package.json        → dependencies / devDependencies version specs
package updatability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gophersys/eden/tools/cictl/internal/failure"
)

// Pin is one parsed version pin.
type Pin struct {
	Tool   string // logical name (e.g. GO_VERSION, github.com/foo/bar, react)
	Pinned string // the pinned version string
	Source string // repo-relative file it came from
	Kind   string // dockerfile | env | go.mod | package.json
}

// Matrix is the collected set of pins, deduplicated and sorted.
type Matrix struct {
	Pins []Pin
}

// Collect resolves each glob in sources against repoDir, parses the matched files
// per kind, and returns the deduplicated, sorted matrix. A glob matching nothing
// is not an error (a source may be conditional, e.g. "versions.env if present").
func Collect(repoDir string, sources []string) (Matrix, error) {
	seen := map[string]Pin{}
	for _, src := range sources {
		matches, err := globRepo(repoDir, src)
		if err != nil {
			return Matrix{}, failure.Wrapf(err, "glob %q", src)
		}
		for _, abs := range matches {
			pins, perr := parseFile(repoDir, abs)
			if perr != nil {
				return Matrix{}, perr
			}
			for _, p := range pins {
				key := p.Source + "\x00" + p.Tool
				seen[key] = p
			}
		}
	}
	out := make([]Pin, 0, len(seen))
	for _, p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Tool < out[j].Tool
	})
	return Matrix{Pins: out}, nil
}

// globRepo expands a (possibly `**`-containing) glob relative to repoDir into a
// sorted list of absolute file paths, skipping vendor/build noise directories.
func globRepo(repoDir, pattern string) ([]string, error) {
	if strings.Contains(pattern, "**") {
		return globDoubleStar(repoDir, pattern)
	}
	matches, err := filepath.Glob(filepath.Join(repoDir, filepath.FromSlash(pattern)))
	if err != nil {
		return nil, failure.Wrapf(err, "glob %s", pattern)
	}
	matches = filterNoise(matches)
	sort.Strings(matches)
	return matches, nil
}

// globDoubleStar handles `**` by walking the tree and matching the basename of
// the pattern (e.g. "**/go.mod" → every go.mod), which is the only `**` shape the
// contract's sources use.
func globDoubleStar(repoDir, pattern string) ([]string, error) {
	idx := strings.Index(pattern, "**")
	prefix := strings.TrimSuffix(pattern[:idx], "/")
	suffix := strings.TrimPrefix(pattern[idx+2:], "/")
	base := filepath.Join(repoDir, filepath.FromSlash(prefix))
	var out []string
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // a missing prefix dir is "matched nothing", not an error.
			}
			return err
		}
		if d.IsDir() {
			if isNoiseDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return failure.Wrapf(rerr, "relativise %s", path)
		}
		if matchSuffix(filepath.ToSlash(rel), suffix) {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, failure.Wrapf(err, "walk %s", base)
	}
	sort.Strings(out)
	return out, nil
}

// matchSuffix reports whether relPath satisfies the post-`**` suffix glob. The
// suffix is matched against the basename when it has no slash (e.g. "go.mod"),
// else against the trailing path segments (e.g. "package.json").
func matchSuffix(relPath, suffix string) bool {
	if suffix == "" {
		return true
	}
	ok, err := filepath.Match(suffix, filepath.Base(relPath))
	if err == nil && ok {
		return true
	}
	// also allow a multi-segment suffix to match the path tail
	if strings.HasSuffix(relPath, suffix) {
		return true
	}
	return false
}

func filterNoise(paths []string) []string {
	var out []string
	for _, p := range paths {
		if pathHasNoiseDir(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func pathHasNoiseDir(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if isNoiseDir(seg) {
			return true
		}
	}
	return false
}

// relSlash renders abs relative to repoDir with forward slashes, falling back to
// abs when it is not under repoDir.
func relSlash(repoDir, abs string) string {
	r, err := filepath.Rel(repoDir, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(r)
}

func isNoiseDir(name string) bool {
	switch name {
	case "node_modules", ".git", "vendor", "target", "dist", "build", ".venv":
		return true
	default:
		return false
	}
}

// parseFile dispatches to the right parser based on the file's name.
func parseFile(repoDir, abs string) ([]Pin, error) {
	rel := relSlash(repoDir, abs)
	raw, err := os.ReadFile(abs) //nolint:gosec // abs comes from a contract-declared source glob.
	if err != nil {
		return nil, failure.Wrapf(err, "read %s", rel)
	}
	base := filepath.Base(abs)
	switch {
	case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile"):
		return parseDockerfile(rel, raw), nil
	case strings.HasSuffix(base, ".env"):
		return parseEnv(rel, raw), nil
	case base == "go.mod":
		return parseGoMod(rel, raw), nil
	case base == "package.json":
		return parsePackageJSON(rel, raw)
	default:
		// Unknown file kind: not fatal — it simply contributes no pins.
		return nil, nil
	}
}

var dockerArgRe = regexp.MustCompile(`^\s*ARG\s+([A-Z0-9_]+_VERSION)=([^\s#]+)`)

func parseDockerfile(source string, raw []byte) []Pin {
	var pins []Pin
	for _, line := range strings.Split(string(raw), "\n") {
		if m := dockerArgRe.FindStringSubmatch(line); m != nil {
			pins = append(pins, Pin{Tool: m[1], Pinned: m[2], Source: source, Kind: "dockerfile"})
		}
	}
	return pins
}

var envRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=([^\s#]+)`)

func parseEnv(source string, raw []byte) []Pin {
	var pins []Pin
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := envRe.FindStringSubmatch(line); m != nil {
			pins = append(pins, Pin{Tool: m[1], Pinned: strings.Trim(m[2], `"'`), Source: source, Kind: "env"})
		}
	}
	return pins
}

var goRequireRe = regexp.MustCompile(`^\s*(\S+/\S+|\S+\.\S+)\s+(v\d\S*)`)

func parseGoMod(source string, raw []byte) []Pin {
	var pins []Pin
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "require ("):
			inBlock = true
			continue
		case inBlock && t == ")":
			inBlock = false
			continue
		case strings.HasPrefix(t, "require ") && !strings.Contains(t, "("):
			// single-line require: `require module v1.2.3`
			rest := strings.TrimSpace(strings.TrimPrefix(t, "require"))
			if m := goRequireRe.FindStringSubmatch(rest); m != nil {
				pins = append(pins, goModPin(source, m[1], m[2], rest))
			}
			continue
		}
		if !inBlock {
			continue
		}
		if m := goRequireRe.FindStringSubmatch(t); m != nil {
			pins = append(pins, goModPin(source, m[1], m[2], t))
		}
	}
	return pins
}

func goModPin(source, module, version, line string) Pin {
	tool := module
	if strings.Contains(line, "// indirect") {
		tool = module + " (indirect)"
	}
	return Pin{Tool: tool, Pinned: version, Source: source, Kind: "go.mod"}
}

func parsePackageJSON(source string, raw []byte) ([]Pin, error) {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, failure.Wrapf(err, "parse %s", source)
	}
	var pins []Pin
	add := func(entries map[string]string, suffix string) {
		for name, spec := range entries {
			tool := name
			if suffix != "" {
				tool = name + suffix
			}
			pins = append(pins, Pin{Tool: tool, Pinned: spec, Source: source, Kind: "package.json"})
		}
	}
	add(pkg.Dependencies, "")
	add(pkg.DevDependencies, " (dev)")
	return pins, nil
}

// Render formats the matrix as an aligned text table.
func (m Matrix) Render() string {
	if len(m.Pins) == 0 {
		return "no pins found in the declared toolMatrix sources\n"
	}
	toolW, pinW, srcW := len("TOOL"), len("PINNED"), len("SOURCE")
	for _, p := range m.Pins {
		toolW = max(toolW, len(p.Tool))
		pinW = max(pinW, len(p.Pinned))
		srcW = max(srcW, len(p.Source))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %s\n", toolW, "TOOL", pinW, "PINNED", srcW, "SOURCE", "KIND")
	for _, p := range m.Pins {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %s\n", toolW, p.Tool, pinW, p.Pinned, srcW, p.Source, p.Kind)
	}
	return b.String()
}
