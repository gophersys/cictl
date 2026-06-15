package affected_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gophersys/eden/tools/cictl/internal/affected"
)

// gitRepo builds a throwaway git repository in t.TempDir with two project dirs
// (each ctl.sh + project.json) plus a non-project dir, makes a base commit, then
// returns the repo root. The caller mutates and commits to create a diff.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // fixed git argv in a test fixture.
		cmd.Dir = dir
		cmd.Env = append(
			os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "main")

	mkProject := func(rel string) {
		writeFile(t, dir, rel+"/ctl.sh", "#!/usr/bin/env bash\n")
		writeFile(t, dir, rel+"/project.json", "{}\n")
		writeFile(t, dir, rel+"/code.go", "package x\n")
	}
	mkProject("libs/go/alpha")
	mkProject("libs/go/beta")
	// a non-project dir (no ctl.sh/project.json)
	writeFile(t, dir, "docs/note.md", "hi\n")

	run("add", "-A")
	run("commit", "-q", "-m", "base")
	return dir
}

// writeFile creates dir/<rel> (rel is slash-separated) with content, making
// parent directories as needed.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitChange(t *testing.T, dir, rel, content string) {
	t.Helper()
	writeFile(t, dir, rel, content)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "change"}} {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed git argv in a test fixture.
		cmd.Dir = dir
		cmd.Env = append(
			os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestProjects_MapsChangeToNearestRoot(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	commitChange(t, dir, "libs/go/alpha/code.go", "package x // touched\n")

	got, err := affected.Projects(dir, "HEAD~1")
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(got) != 1 || got[0] != "libs/go/alpha" {
		t.Fatalf("got %v, want [libs/go/alpha]", got)
	}
}

func TestProjects_MultipleAndSorted(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	// Touch both projects + a non-project file in one commit.
	writeFile(t, dir, "libs/go/beta/code.go", "package x // b\n")
	writeFile(t, dir, "libs/go/alpha/code.go", "package x // a\n")
	writeFile(t, dir, "docs/note.md", "changed\n")
	commitChange(t, dir, "libs/go/alpha/extra.go", "package x\n")

	got, err := affected.Projects(dir, "HEAD~1")
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	want := []string{"libs/go/alpha", "libs/go/beta"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted)", got, want)
		}
	}
}

func TestProjects_NonProjectChangeIgnored(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	commitChange(t, dir, "docs/another.md", "doc only\n")
	got, err := affected.Projects(dir, "HEAD~1")
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a change outside any project should yield no affected roots; got %v", got)
	}
}

func TestProjects_EmptyDiff(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	got, err := affected.Projects(dir, "HEAD")
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("HEAD...HEAD diff should be empty; got %v", got)
	}
}
