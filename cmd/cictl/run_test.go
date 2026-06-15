package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func runArgs(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRun_NoArgsUsage(t *testing.T) {
	t.Parallel()
	code, _, _ := runArgs(t)
	if code != 2 {
		t.Fatalf("no args should be a usage error (2), got %d", code)
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	t.Parallel()
	code, _, errOut := runArgs(t, "frobnicate")
	if code != 2 {
		t.Fatalf("unknown command should be 2, got %d", code)
	}
	if !bytes.Contains([]byte(errOut), []byte("unknown command")) {
		t.Errorf("expected 'unknown command' message, got %q", errOut)
	}
}

func TestRun_SchemaToStdout(t *testing.T) {
	t.Parallel()
	code, out, _ := runArgs(t, "schema", "-o", "-")
	if code != 0 {
		t.Fatalf("schema -o - exit = %d", code)
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(out), &root); err != nil {
		t.Fatalf("schema stdout is not JSON: %v", err)
	}
}

func TestRun_SchemaToFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	code, _, _ := runArgs(t, "schema", "-o", path)
	if code != 0 {
		t.Fatalf("schema exit = %d", code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("schema did not write file: %v", err)
	}
}

const goodContract = `apiVersion: eden.ci/v1
repo: demo
kind: libraries
image: ghcr.io/gophersys/base
languages: [go]
tiers:
  pr: {verbs: [fast], timeoutMinutes: 15}
  merge: {verbs: [substrate], substrate: [docker], privileged: true}
  nightly: {verbs: [fast], substrate: [docker], privileged: true, schedule: "0 6 * * *"}
providers: [github]
toolMatrix: {sources: ["**/go.mod"]}
`

func TestRun_ValidatePassAndFail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte(goodContract), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runArgs(t, "validate", "-f", good); code != 0 {
		t.Fatalf("validate good exit = %d", code)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("apiVersion: eden.ci/WRONG\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runArgs(t, "validate", "-f", bad); code != 1 {
		t.Fatalf("validate bad exit = %d, want 1", code)
	}
}

// TestRun_GenerateThenDrift exercises the full generate→drift loop and the
// hand-edit detection through the CLI surface.
func TestRun_GenerateThenDrift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ciDir := filepath.Join(dir, ".ci")
	if err := os.MkdirAll(filepath.Join(ciDir, "providers", "github"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ciDir, "ci.contract.yaml"), []byte(goodContract), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, _, errOut := runArgs(t, "generate", "-C", dir); code != 0 {
		t.Fatalf("generate exit = %d: %s", code, errOut)
	}
	if code, _, _ := runArgs(t, "drift", "-C", dir); code != 0 {
		t.Fatalf("drift after generate should be clean (0), got %d", code)
	}

	// Hand-edit a generated workflow → drift must fail.
	wf := filepath.Join(dir, ".github", "workflows", "on-pr.yml")
	body, err := os.ReadFile(wf) //nolint:gosec // wf is a test-built path under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wf, append(body, []byte("# edit\n")...), 0o600); err != nil { //nolint:gosec // wf is a test-built path under t.TempDir.
		t.Fatal(err)
	}
	if code, _, _ := runArgs(t, "drift", "-C", dir); code != 1 {
		t.Fatalf("drift after hand-edit should fail (1), got %d", code)
	}
}

func TestRun_GenerateRefusesInvalidContract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ciDir := filepath.Join(dir, ".ci")
	if err := os.MkdirAll(ciDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// nightly without a schedule → invalid.
	bad := `apiVersion: eden.ci/v1
repo: demo
kind: libraries
image: ghcr.io/gophersys/base
languages: [go]
tiers:
  pr: {verbs: [fast]}
  merge: {verbs: [substrate]}
  nightly: {verbs: [fast]}
providers: [github]
toolMatrix: {sources: []}
`
	if err := os.WriteFile(filepath.Join(ciDir, "ci.contract.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runArgs(t, "generate", "-C", dir); code != 1 {
		t.Fatalf("generate on invalid contract should fail (1), got %d", code)
	}
}
