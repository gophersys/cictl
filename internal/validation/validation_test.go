package validation_test

import (
	"strings"
	"testing"

	"github.com/gophersys/cictl/internal/validation"
)

// goodContract is the canonical valid instance the table mutates per-case.
const goodContract = `apiVersion: eden.ci/v1
repo: libs
kind: libraries
runner:
  runsOn: arc-org
  container: false
languages: [go, typescript]
tiers:
  pr:
    verbs: [affected-gate-fast]
    substrate: []
    timeoutMinutes: 15
  merge:
    verbs: [affected-gate-substrate]
    substrate: [docker, k3d, kind]
    privileged: true
    timeoutMinutes: 30
  nightly:
    verbs: [gate-all, updatability]
    substrate: [docker, k3d, kind, postgres, minio, nats]
    privileged: true
    schedule: "0 6 * * *"
    timeoutMinutes: 90
providers: [github]
toolMatrix:
  sources: ["**/go.mod"]
`

func TestValidate_AcceptsGood(t *testing.T) {
	t.Parallel()
	res := validation.ValidateBytes([]byte(goodContract))
	if !res.OK() {
		t.Fatalf("known-good contract rejected: %v", res.Error())
	}
}

func TestValidate_RejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		yaml    string
		wantSub string // a substring expected somewhere in the problem set
	}{
		{
			name: "missing apiVersion",
			yaml: strings.Replace(goodContract, "apiVersion: eden.ci/v1\n", "", 1),
			// a strict decoder still decodes; the empty apiVersion fails const + semantic.
			wantSub: "eden.ci/v1",
		},
		{
			name:    "wrong apiVersion",
			yaml:    strings.Replace(goodContract, "eden.ci/v1", "eden.ci/v9", 1),
			wantSub: "eden.ci/v1",
		},
		{
			name:    "unknown substrate",
			yaml:    strings.Replace(goodContract, "substrate: [docker, k3d, kind]", "substrate: [bogus]", 1),
			wantSub: "substrate",
		},
		{
			// Container mode still demands a known image.
			name: "unknown image in container mode",
			yaml: strings.Replace(goodContract,
				"runner:\n  runsOn: arc-org\n  container: false\n",
				"runner:\n  runsOn: ubuntu-latest\n  container: true\nimage: ghcr.io/evil/x\n", 1),
			wantSub: "unknown image",
		},
		{
			// Container mode without an image has nothing to run in.
			name: "container mode without an image",
			yaml: strings.Replace(goodContract,
				"runner:\n  runsOn: arc-org\n  container: false\n",
				"runner:\n  runsOn: ubuntu-latest\n  container: true\n", 1),
			wantSub: "required when runner.container is true",
		},
		{
			// An image on a self-hosted pool is dead configuration that reads as
			// intent: the pool's runner image already carries the toolchain.
			name:    "image declared on a self-hosted pool",
			yaml:    strings.Replace(goodContract, "  container: false\n", "  container: false\nimage: ghcr.io/gophersys/base\n", 1),
			wantSub: "must be empty",
		},
		{
			name:    "empty runsOn",
			yaml:    strings.Replace(goodContract, "  runsOn: arc-org\n", "  runsOn: \"\"\n", 1),
			wantSub: "runsOn",
		},
		{
			name:    "empty pr verbs",
			yaml:    strings.Replace(goodContract, "verbs: [affected-gate-fast]", "verbs: []", 1),
			wantSub: "at least one verb",
		},
		{
			name:    "nightly missing schedule",
			yaml:    strings.Replace(goodContract, "    schedule: \"0 6 * * *\"\n", "", 1),
			wantSub: "schedule",
		},
		{
			name:    "unknown kind",
			yaml:    strings.Replace(goodContract, "kind: libraries", "kind: nonsense", 1),
			wantSub: "kind",
		},
		{
			name:    "unknown provider",
			yaml:    strings.Replace(goodContract, "providers: [github]", "providers: [gitlab]", 1),
			wantSub: "provider",
		},
		{
			name:    "substrate without privileged",
			yaml:    strings.Replace(goodContract, "    substrate: [docker, k3d, kind]\n    privileged: true\n", "    substrate: [docker]\n", 1),
			wantSub: "privileged",
		},
		{
			name:    "privileged without substrate",
			yaml:    strings.Replace(goodContract, "    verbs: [affected-gate-fast]\n    substrate: []\n", "    verbs: [affected-gate-fast]\n    privileged: true\n", 1),
			wantSub: "substrate",
		},
		{
			name:    "schedule on pr tier",
			yaml:    strings.Replace(goodContract, "    verbs: [affected-gate-fast]\n", "    verbs: [affected-gate-fast]\n    schedule: \"0 0 * * *\"\n", 1),
			wantSub: "only the nightly tier",
		},
		{
			name:    "empty languages",
			yaml:    strings.Replace(goodContract, "languages: [go, typescript]", "languages: []", 1),
			wantSub: "language",
		},
		{
			name:    "unknown top-level field",
			yaml:    goodContract + "bogusField: 1\n",
			wantSub: "bogusField",
		},
		{
			name:    "not yaml",
			yaml:    "::: not : valid : yaml :::",
			wantSub: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := validation.ValidateBytes([]byte(tc.yaml))
			if res.OK() {
				t.Fatalf("malformed contract %q was ACCEPTED (vacuous validation)", tc.name)
			}
			if tc.wantSub == "" {
				return
			}
			joined := res.Error().Error()
			if !strings.Contains(joined, tc.wantSub) {
				t.Fatalf("problems for %q do not mention %q:\n%s", tc.name, tc.wantSub, joined)
			}
		})
	}
}

// TestValidate_StructuralCatchesEnum proves the structural (schema) pass alone is
// non-vacuous: a bad enum value must produce a schema-located problem (the
// "at '/...'" form the jsonschema validator emits), not only a semantic one.
func TestValidate_StructuralCatchesEnum(t *testing.T) {
	t.Parallel()
	bad := strings.Replace(goodContract, "kind: libraries", "kind: nonsense", 1)
	res := validation.ValidateBytes([]byte(bad))
	if res.OK() {
		t.Fatal("bad kind accepted")
	}
	var sawSchema bool
	for _, p := range res.Problems {
		if strings.Contains(p.Message, "at '/kind'") {
			sawSchema = true
		}
	}
	if !sawSchema {
		t.Fatalf("structural schema pass did not flag /kind; problems:\n%v", res.Error())
	}
}
