// Package affected maps a git diff to the set of changed project roots. A project
// is a directory that contains both ctl.sh and project.json (e.g. libs/go/<x>,
// libs/typescript/<x>); a changed file is attributed to its nearest such ancestor.
// This is the nx-free, uniform "what changed" the CI tiers gate over.
package affected

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gophersys/eden/tools/cictl/internal/failure"
)

// Projects lists the project roots (repo-relative, slash-separated, sorted) whose
// directory tree contains at least one file changed in `git diff <base>...HEAD`.
// repoDir is the repository working tree; base is the diff base (e.g.
// origin/main). A file not under any project root is ignored — only project
// directories gate.
func Projects(repoDir, base string) ([]string, error) {
	files, err := changedFiles(repoDir, base)
	if err != nil {
		return nil, err
	}
	roots := map[string]struct{}{}
	for _, f := range files {
		root, ok := nearestProjectRoot(repoDir, f)
		if ok {
			roots[root] = struct{}{}
		}
	}
	out := make([]string, 0, len(roots))
	for r := range roots {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

// changedFiles returns the repo-relative paths changed between base and HEAD
// using the three-dot form (changes on HEAD since it diverged from base), which
// is the same set a pull-request gate considers.
func changedFiles(repoDir, base string) ([]string, error) {
	// Fixed git argv; base is a caller-supplied ref (e.g. origin/main), passed as a
	// distinct arg, never interpolated into a shell.
	cmd := exec.Command("git", "-C", repoDir, "diff", "--name-only", base+"...HEAD") //nolint:gosec // fixed argv, no shell.
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if asExit(err, &ee) {
			return nil, failure.Wrapf(err, "git diff %s...HEAD: %s", base, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, failure.Wrapf(err, "git diff %s...HEAD", base)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// nearestProjectRoot walks up from the changed file's directory toward the repo
// root, returning the first directory that is a project (has both ctl.sh and
// project.json). The returned root is repo-relative with forward slashes.
func nearestProjectRoot(repoDir, relFile string) (string, bool) {
	dir := filepath.Dir(filepath.Join(repoDir, filepath.FromSlash(relFile)))
	repoAbs, err := filepath.Abs(repoDir)
	if err != nil {
		repoAbs = repoDir
	}
	for {
		if isProjectDir(dir) {
			rel, err := filepath.Rel(repoAbs, dir)
			if err != nil {
				return "", false
			}
			return filepath.ToSlash(rel), true
		}
		parent := filepath.Dir(dir)
		if parent == dir || !within(repoAbs, dir) {
			return "", false
		}
		dir = parent
	}
}

// isProjectDir reports whether dir holds both ctl.sh and project.json.
func isProjectDir(dir string) bool {
	return fileExists(filepath.Join(dir, "ctl.sh")) && fileExists(filepath.Join(dir, "project.json"))
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// within reports whether dir is the repo root or below it (never above), so the
// upward walk stops at the repo boundary.
func within(repoAbs, dir string) bool {
	rel, err := filepath.Rel(repoAbs, dir)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// asExit is errors.As specialised to *exec.ExitError without importing errors in
// the hot path's signature; it keeps the stderr-capturing branch readable.
func asExit(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError) //nolint:errorlint // direct type, no wrapping at the call site.
	if ok {
		*target = ee
	}
	return ok
}
