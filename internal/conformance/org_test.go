package conformance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gophersys/cictl/internal/conformance"
	"github.com/gophersys/cictl/internal/contract"
	"github.com/gophersys/cictl/internal/workflow"
)

// THE FAKE GITHUB.
//
// The org audit reads the live API, and a check that can only be exercised
// against the live API is a check nobody runs — so every rule here is exercised
// against an httptest server that speaks the same shapes. That is also what makes
// the rules break-testable: each test starts from a fleet that is IN contract,
// changes exactly one thing, and requires the gap to appear. A rule proven only
// on a passing fixture proves nothing.

const (
	pinnedSHA = "0123456789abcdef0123456789abcdef01234567"
	olderSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// fakeRepository is one repository as the fake API serves it.
type fakeRepository struct {
	archived  bool
	contract  string            // raw .ci/ci.contract.yaml, empty means absent
	workflows map[string]string // base name -> body
}

// compareStatus is what the fake answers for a commit comparison, keyed
// "<base>...<head>". An absent key answers "behind", so a test that forgets to
// declare an ancestry gets the strict answer rather than a free pass.
type fakeOrg struct {
	repositories map[string]fakeRepository
	compares     map[string]string
}

func (f fakeOrg) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			// The real API answers 401 without a credential. Reproducing that keeps
			// the "a missing token must fail, never fall through to anonymous" rule
			// honest rather than asserted.
			http.Error(w, "requires authentication", http.StatusUnauthorized)
			return
		}
		path := r.URL.Path
		switch {
		case path == "/orgs/gophersys/repos":
			f.serveRepositories(w, r)
		case strings.HasSuffix(path, "/contents/.ci/ci.contract.yaml"):
			f.serveContract(w, path)
		case strings.HasSuffix(path, "/contents/.github/workflows"):
			f.serveWorkflowList(w, path)
		case strings.Contains(path, "/contents/.github/workflows/"):
			f.serveWorkflowBody(w, path)
		case strings.Contains(path, "/compare/"):
			f.serveCompare(w, path)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f fakeOrg) serveRepositories(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("page") != "1" {
		writeJSON(w, []any{})
		return
	}
	type row struct {
		Name     string `json:"name"`
		Archived bool   `json:"archived"`
	}
	out := make([]row, 0, len(f.repositories))
	for name, entry := range f.repositories {
		out = append(out, row{Name: name, Archived: entry.archived})
	}
	writeJSON(w, out)
}

func (f fakeOrg) repositoryOf(path string) (fakeRepository, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/repos/gophersys/"), "/")
	if len(parts) == 0 {
		return fakeRepository{}, false
	}
	entry, ok := f.repositories[parts[0]]
	return entry, ok
}

func (f fakeOrg) serveContract(w http.ResponseWriter, path string) {
	entry, ok := f.repositoryOf(path)
	if !ok || entry.contract == "" {
		notFound(w)
		return
	}
	if _, err := w.Write([]byte(entry.contract)); err != nil {
		panic(err)
	}
}

func (f fakeOrg) serveWorkflowList(w http.ResponseWriter, path string) {
	found, ok := f.repositoryOf(path)
	if !ok || len(found.workflows) == 0 {
		notFound(w)
		return
	}
	type listing struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	out := make([]listing, 0, len(found.workflows))
	for name := range found.workflows {
		out = append(out, listing{Name: name, Type: "file"})
	}
	writeJSON(w, out)
}

func (f fakeOrg) serveWorkflowBody(w http.ResponseWriter, path string) {
	entry, ok := f.repositoryOf(path)
	if !ok {
		notFound(w)
		return
	}
	name := path[strings.LastIndex(path, "/")+1:]
	body, present := entry.workflows[name]
	if !present {
		notFound(w)
		return
	}
	if _, err := w.Write([]byte(body)); err != nil {
		panic(err)
	}
}

func (f fakeOrg) serveCompare(w http.ResponseWriter, path string) {
	spec := path[strings.LastIndex(path, "/compare/")+len("/compare/"):]
	status, ok := f.compares[spec]
	if !ok {
		status = "behind"
	}
	writeJSON(w, map[string]string{"status": status})
}

// notFound answers 404 without needing the request http.NotFound insists on and
// never reads. The fake serves absence a great deal — a repository with no
// contract, a repository with no workflow directory — and each of those is a
// FINDING the audit must report rather than an error it may abort on.
func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		panic(err)
	}
}

// The fixtures.

// conformantContract is a review-enabled contract that renders cleanly.
func conformantContract() *contract.Contract {
	return &contract.Contract{
		APIVersion: contract.APIVersion,
		Repo:       "libs",
		Kind:       contract.KindLibraries,
		Runner:     contract.Runner{RunsOn: "arc-org", Container: false},
		Languages:  []contract.Language{contract.LanguageGo},
		Tiers: contract.Tiers{
			Pr:      contract.Tier{Verbs: []string{"affected-gate-fast"}, TimeoutMinutes: 15},
			Merge:   contract.Tier{Verbs: []string{"affected-gate-substrate"}, Substrate: []contract.Substrate{contract.SubstrateDocker}, Privileged: true, TimeoutMinutes: 30},
			Nightly: contract.Tier{Verbs: []string{"gate-all"}, Substrate: []contract.Substrate{contract.SubstrateDocker}, Privileged: true, Schedule: "0 6 * * *", TimeoutMinutes: 90},
		},
		Review: contract.Review{
			Enabled: true, Ref: pinnedSHA, Release: "v0.6.0",
			Tier: contract.ReviewTierModule, RunsOn: "arc-review", TimeoutMinutes: 30,
			StreamStore: contract.ReviewStreamMinio, StreamEndpoint: "https://minio.example.invalid", StreamBucket: "review-streams",
		},
		Providers:  []contract.Provider{contract.ProviderGithub},
		ToolMatrix: contract.ToolMatrix{Sources: []string{"**/go.mod"}},
	}
}

// contractYAML is the on-the-wire form of a contract. It is written by hand
// rather than marshaled, because that is what a repository actually holds.
func contractYAML() string {
	return `apiVersion: eden.ci/v1
repo: libs
kind: libraries
runner:
  runsOn: arc-org
  container: false
languages: [go]
tiers:
  pr:      { verbs: [affected-gate-fast], timeoutMinutes: 15 }
  merge:   { verbs: [affected-gate-substrate], substrate: [docker], privileged: true, timeoutMinutes: 30 }
  nightly: { verbs: [gate-all], substrate: [docker], privileged: true, schedule: "0 6 * * *", timeoutMinutes: 90 }
review:
  enabled: true
  ref: "` + pinnedSHA + `"
  release: v0.6.0
  tier: module
  runsOn: arc-review
  timeoutMinutes: 30
  streamStore: minio
  streamEndpoint: https://minio.example.invalid
  streamBucket: review-streams
providers: [github]
toolMatrix:
  sources: ["**/go.mod"]
`
}

// renderedCaller is exactly what `cictl generate` produces for contractYAML.
func renderedCaller(t *testing.T) string {
	t.Helper()
	files, err := workflow.Render(conformantContract())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, f := range files {
		if f.Name == workflow.ReviewFileName {
			return string(f.Content)
		}
	}
	t.Fatal("the fixture contract rendered no review caller")
	return ""
}

// conformantFleet is one repository that is fully in contract. Every test starts
// here and changes exactly one thing.
func conformantFleet(t *testing.T) fakeOrg {
	t.Helper()
	return fakeOrg{
		repositories: map[string]fakeRepository{
			"libs": {
				contract:  contractYAML(),
				workflows: map[string]string{workflow.ReviewFileName: renderedCaller(t)},
			},
		},
		compares: map[string]string{olderSHA + "..." + pinnedSHA: "ahead"},
	}
}

func audit(t *testing.T, f fakeOrg, opts *conformance.OrgOptions) conformance.OrgReport {
	t.Helper()
	srv := f.server(t)
	opts.Org = "gophersys"
	opts.BaseURL = srv.URL
	if opts.Token == "" {
		opts.Token = "test-token"
	}
	rep, err := conformance.CheckOrg(context.Background(), opts)
	if err != nil {
		t.Fatalf("CheckOrg: %v", err)
	}
	return rep
}

// gapsOf returns one repository's gaps, joined for matching.
func gapsOf(t *testing.T, rep conformance.OrgReport, name string) string {
	t.Helper()
	for _, row := range rep.Rows {
		if row.Repo == name {
			return strings.Join(row.Gaps, "\n")
		}
	}
	t.Fatalf("no row for %q in the report", name)
	return ""
}

// The baseline: a fleet in contract reports no gap.

// TestCheckOrg_AConformantFleetHasNoGaps is the control. Without it every test
// below could be passing because the audit reports a gap for everything.
func TestCheckOrg_AConformantFleetHasNoGaps(t *testing.T) {
	t.Parallel()
	rep := audit(t, conformantFleet(t), &conformance.OrgOptions{Floor: olderSHA})
	if !rep.OK() {
		t.Fatalf("a conformant fleet reported %d gap(s):\n%s", rep.Gaps(), rep.Board())
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rep.Rows))
	}
	row := rep.Rows[0]
	if !row.HasContract || !row.ReviewEnabled || row.Tier != "module" || row.Ref != pinnedSHA {
		t.Errorf("the row does not describe the repository it read: %+v", row)
	}
}

// Rule 1: a review-enabled contract has a caller.

func TestCheckOrg_MissingCallerIsAGap(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	f.repositories["libs"] = fakeRepository{contract: contractYAML()}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	if !strings.Contains(gapsOf(t, rep, "libs"), "is absent") {
		t.Fatalf("a contract that enables the reviewer with no caller was not reported:\n%s", rep.Board())
	}
}

// A caller with no contract behind it is the OTHER direction, and it matters as
// much: nothing generates that file and no drift gate covers it.
func TestCheckOrg_AnUnmanagedCallerIsAGap(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	f.repositories["libs"] = fakeRepository{
		contract:  strings.Replace(contractYAML(), "enabled: true", "enabled: false", 1),
		workflows: map[string]string{workflow.ReviewFileName: renderedCaller(t)},
	}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	if !strings.Contains(gapsOf(t, rep, "libs"), "nothing generates or drift-checks it") {
		t.Fatalf("an unmanaged caller was not reported:\n%s", rep.Board())
	}
}

// Rule 2: the caller equals the generator's output, which is also what makes the
// tier correct.

func TestCheckOrg_AHandEditedCallerIsAGap(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	edited := strings.Replace(renderedCaller(t), "timeout-minutes: 30", "timeout-minutes: 45", 1)
	f.repositories["libs"] = fakeRepository{contract: contractYAML(), workflows: map[string]string{workflow.ReviewFileName: edited}}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	if !strings.Contains(gapsOf(t, rep, "libs"), "differs from `cictl generate` output") {
		t.Fatalf("a hand-edited caller was not reported:\n%s", rep.Board())
	}
}

// THE TIER IS CHECKED BY RULE 2, not by a rule of its own. The spend inputs are
// derived from the tier in one place, so a caller whose budget was raised by hand
// no longer equals the render — which is the same finding, reached without a
// second table of what each tier costs.
func TestCheckOrg_AHandRaisedBudgetIsAGap(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	raised := strings.Replace(renderedCaller(t), `budget-usd: "25"`, `budget-usd: "250"`, 1)
	if raised == renderedCaller(t) {
		t.Fatal("the fixture no longer carries the module tier's budget; this test is checking nothing")
	}
	f.repositories["libs"] = fakeRepository{contract: contractYAML(), workflows: map[string]string{workflow.ReviewFileName: raised}}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	if !strings.Contains(gapsOf(t, rep, "libs"), "differs from `cictl generate` output") {
		t.Fatalf("a caller whose spend was raised by hand was not reported:\n%s", rep.Board())
	}
}

// Rules 3 and 4: every action is a commit, and the reviewer is at or after the
// floor.

// gophersys/* is NOT exempt from the pin rule. The exemption that used to exist
// justified itself with a pin-guard mechanism that could never work.
func TestCheckOrg_AnUnpinnedActionIsAGap(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	f.repositories["libs"] = fakeRepository{
		contract: contractYAML(),
		workflows: map[string]string{
			workflow.ReviewFileName: renderedCaller(t),
			"validate.yml":          "name: v\non: [push]\njobs:\n  gate:\n    runs-on: arc-org\n    timeout-minutes: 10\n    steps:\n      - uses: actions/checkout@v4\n",
		},
	}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	if !strings.Contains(gapsOf(t, rep, "libs"), `pins actions/checkout to "v4"`) {
		t.Fatalf("a tag-pinned action was not reported:\n%s", rep.Board())
	}
}

func TestCheckOrg_AReviewerBehindTheFloorIsAGap(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	// The fake answers "behind" for any comparison it was not told about, so this
	// declares the strict answer explicitly rather than relying on the default.
	f.compares = map[string]string{olderSHA + "..." + pinnedSHA: "behind"}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	if !strings.Contains(gapsOf(t, rep, "libs"), "is behind the floor") {
		t.Fatalf("a reviewer pinned behind the floor was not reported:\n%s", rep.Board())
	}
}

// With no floor declared, ancestry is not asserted and no comparison is made.
// A rule that silently invented a floor would report gaps nobody chose.
func TestCheckOrg_NoFloorAssertsOnlyThatThePinIsACommit(t *testing.T) {
	t.Parallel()
	rep := audit(t, conformantFleet(t), &conformance.OrgOptions{})
	if !rep.OK() {
		t.Fatalf("with no floor the fleet must be in contract, got:\n%s", rep.Board())
	}
}

// Rules 5 and 6: a timeout on every job, an arc-* pool on every job.

// These are the rules that exist today in exactly ONE repository's private test
// suite. Applying them fleet-wide is the whole point of the bird's-eye verb.
func TestCheckOrg_WorkflowHygieneIsCheckedInEveryRepository(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "no timeout",
			body: "name: v\non: [push]\njobs:\n  gate:\n    runs-on: arc-org\n    steps:\n      - run: true\n",
			want: "declares no timeout-minutes",
		},
		{
			// actionlint reports a duplicate key; a YAML library does not, because it
			// silently keeps the last one. This is why the audit reads text.
			name: "duplicate timeout",
			body: "name: v\non: [push]\njobs:\n  gate:\n    runs-on: arc-org\n    timeout-minutes: 10\n    timeout-minutes: 20\n    steps:\n      - run: true\n",
			want: "declares timeout-minutes 2 times",
		},
		{
			name: "zero timeout",
			body: "name: v\non: [push]\njobs:\n  gate:\n    runs-on: arc-org\n    timeout-minutes: 0\n    steps:\n      - run: true\n",
			want: "which is not a positive whole number of minutes",
		},
		{
			name: "hosted runner",
			body: "name: v\non: [push]\njobs:\n  gate:\n    runs-on: ubuntu-latest\n    timeout-minutes: 10\n    steps:\n      - run: true\n",
			want: "which is not an arc-* pool",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := conformantFleet(t)
			f.repositories["libs"] = fakeRepository{
				contract: contractYAML(),
				workflows: map[string]string{
					workflow.ReviewFileName: renderedCaller(t),
					"validate.yml":          tc.body,
				},
			}
			rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
			if !strings.Contains(gapsOf(t, rep, "libs"), tc.want) {
				t.Fatalf("expected a gap containing %q, got:\n%s", tc.want, rep.Board())
			}
		})
	}
}

// A repository with no contract at all is still held to the hygiene rules. That
// is what makes them fleet rules rather than contract rules — eden and libs have
// workflows and no contract, and they are exactly the repositories a one-repo
// test suite could never reach.
func TestCheckOrg_AContractlessRepositoryIsStillAudited(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	f.repositories["eden"] = fakeRepository{
		workflows: map[string]string{"on-pr.yml": "name: v\non: [push]\njobs:\n  gate:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@v4\n"},
	}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	gaps := gapsOf(t, rep, "eden")
	for _, want := range []string{"not an arc-* pool", "declares no timeout-minutes", "not a 40-hex commit"} {
		if !strings.Contains(gaps, want) {
			t.Errorf("a contractless repository escaped the %q rule:\n%s", want, rep.Board())
		}
	}
}

// The shape of the answer.

// TestCheckOrg_AnArchivedRepositoryIsNotAudited. An archive is frozen by
// definition, so holding one to a contract it predates produces gaps nobody may
// act on — and repository governance forbids un-archiving to fix them.
func TestCheckOrg_AnArchivedRepositoryIsNotAudited(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	f.repositories["old-thing"] = fakeRepository{
		archived:  true,
		workflows: map[string]string{"v.yml": "name: v\non: [push]\njobs:\n  gate:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n"},
	}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	for _, row := range rep.Rows {
		if row.Repo == "old-thing" {
			t.Fatalf("an archived repository was audited:\n%s", rep.Board())
		}
	}
}

// TestCheckOrg_NoTokenIsAnOperationalFailure. A missing credential must fail and
// name itself. Anonymous, a private repository's contents answer 404, so every
// rule would report "absent" for files that are present: a report about the
// credential wearing the clothes of a report about the fleet.
func TestCheckOrg_NoTokenIsAnOperationalFailure(t *testing.T) {
	t.Parallel()
	srv := conformantFleet(t).server(t)
	_, err := conformance.CheckOrg(context.Background(), &conformance.OrgOptions{Org: "gophersys", BaseURL: srv.URL})
	if err == nil {
		t.Fatal("CheckOrg accepted an empty token; a missing credential must fail, never fall through to an anonymous read")
	}
	if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Errorf("the refusal does not name the variable to set: %v", err)
	}
}

// TestCheckOrg_AnAPIErrorIsAnOperationalFailure. The distinction the exit code
// rests on: a red from this verb means "the audit did not run", never "somebody
// else's repository drifted".
func TestCheckOrg_AnAPIErrorIsAnOperationalFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	_, err := conformance.CheckOrg(context.Background(), &conformance.OrgOptions{Org: "gophersys", Token: "t", BaseURL: srv.URL})
	if err == nil {
		t.Fatal("a 500 from the API was reported as a clean fleet")
	}
}

// TestCheckOrg_TheBoardNamesEveryGap. The row is the glance; a reader acts on the
// detail underneath it, so a board that counted gaps without naming them would
// be a number nobody can use.
func TestCheckOrg_TheBoardNamesEveryGap(t *testing.T) {
	t.Parallel()
	f := conformantFleet(t)
	f.repositories["libs"] = fakeRepository{contract: contractYAML()}
	rep := audit(t, f, &conformance.OrgOptions{Floor: olderSHA})
	board := rep.Board()
	for _, want := range []string{"ORG CONTRACT", "gophersys", "libs", "GAPS", "is absent", "cictl drift"} {
		if !strings.Contains(board, want) {
			t.Errorf("the board does not carry %q:\n%s", want, board)
		}
	}
	if strings.Contains(board, "The fleet is in contract.") {
		t.Errorf("the board claims the fleet is in contract while reporting a gap:\n%s", board)
	}
}
