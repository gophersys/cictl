package conformance

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gophersys/cictl/internal/contract"
	"github.com/gophersys/cictl/internal/validation"
	"github.com/gophersys/cictl/internal/workflow"
)

// The patterns the fleet rules read with. A workflow is read as text on purpose:
// a YAML library silently keeps the LAST of two duplicate keys, so a document
// that GitHub rejects with a startup failure parses cleanly here and the rule
// that exists to catch it would report nothing.
var (
	// reUses matches a `uses:` line and captures the action reference and its ref.
	reUses = regexp.MustCompile(`(?m)^[ \t]*-?[ \t]*uses:[ \t]*([A-Za-z0-9_./-]+)@([A-Za-z0-9_.-]+)`)
	// reRunsOn matches a `runs-on:` line and captures the pool label.
	reRunsOn = regexp.MustCompile(`(?m)^[ \t]*runs-on:[ \t]*([A-Za-z0-9_.-]+)`)
	// reJob matches a job key: under `jobs:`, a key at exactly 2 spaces is a job
	// name and nothing else is.
	reJob = regexp.MustCompile(`(?m)^ {2}([A-Za-z0-9_-]+):[ \t]*$`)
	// reTimeout matches a timeout-minutes declaration and captures its value.
	reTimeout = regexp.MustCompile(`(?m)^[ \t]*timeout-minutes:[ \t]*([^ \t#\n]+)`)
	// reSHA40 matches a full commit and nothing shorter.
	reSHA40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ContractPath and WorkflowDir are where the audit looks in every repository.
const (
	ContractPath = ".ci/ci.contract.yaml"
	WorkflowDir  = ".github/workflows"
)

// auditRepository applies every fleet rule to one repository and returns its row. An
// error here is operational — the API failed — and never a finding.
func (c *orgClient) auditRepository(ctx context.Context, repository string) (OrgRow, error) {
	row := OrgRow{Repo: repository}

	raw, ok, err := c.raw(ctx, repository, ContractPath)
	if err != nil {
		return row, err
	}
	var decoded *contract.Contract
	if ok {
		row.HasContract = true
		decoded, err = contract.Decode(raw)
		if err != nil {
			// A contract that will not parse is that repository's finding, not this
			// verb's failure: the fleet report should name it and carry on rather
			// than abort the whole audit over one bad document.
			row.Gaps = append(row.Gaps, fmt.Sprintf("contract does not parse: %v", err))
			decoded = nil
		}
	}

	files, err := c.workflowFiles(ctx, repository)
	if err != nil {
		return row, err
	}
	row.Workflows = len(files)

	if decoded != nil {
		row.ReviewEnabled = decoded.Review.Enabled
		row.Tier = string(decoded.Review.Tier)
		row.Gaps = append(row.Gaps, c.reviewGaps(ctx, decoded, files, &row)...)
	}
	row.Gaps = append(row.Gaps, hygieneGaps(files)...)
	sort.Strings(row.Gaps)
	return row, nil
}

// reviewGaps applies the rules that only mean anything for a repository whose
// contract asks for a reviewer: the caller is present, it is exactly what the
// generator produces (which is also what makes the tier correct — the model,
// budget and round ceiling are derived from the tier, so a caller that matches
// the render cannot carry the wrong spend), and its pin is a real commit at or
// after the floor.
func (c *orgClient) reviewGaps(ctx context.Context, decoded *contract.Contract, files map[string]string, row *OrgRow) []string {
	if !decoded.Review.Enabled {
		// A repository may sit outside the reviewer deliberately. What is NOT
		// allowed is a caller with no contract behind it: that file is unmanaged,
		// nothing regenerates it, and no drift gate covers it.
		if _, present := files[workflow.ReviewFileName]; present {
			return []string{fmt.Sprintf("%s/%s exists but the contract does not enable the reviewer, so nothing generates or drift-checks it", WorkflowDir, workflow.ReviewFileName)}
		}
		return nil
	}

	var gaps []string
	body, present := files[workflow.ReviewFileName]
	if !present {
		return append(gaps, fmt.Sprintf("the contract enables the reviewer, but %s/%s is absent: run `cictl generate`", WorkflowDir, workflow.ReviewFileName))
	}

	// The contract must itself be valid before its render means anything.
	if res := validation.Validate(decoded, nil); !res.OK() {
		return append(gaps, fmt.Sprintf("the contract is invalid, so the caller cannot be checked against it: %v", res.Error()))
	}

	rendered, err := workflow.Render(decoded)
	if err != nil {
		return append(gaps, fmt.Sprintf("the contract cannot be rendered: %v", err))
	}
	for _, f := range rendered {
		if f.Name != workflow.ReviewFileName {
			continue
		}
		if string(f.Content) != body {
			gaps = append(gaps, fmt.Sprintf("%s/%s differs from `cictl generate` output: it was hand-edited, or the contract moved and nobody regenerated", WorkflowDir, workflow.ReviewFileName))
		}
	}

	row.Ref = reviewRef(body)
	gaps = append(gaps, c.pinGapsForCaller(ctx, row.Ref)...)

	// The trailing release comment names the pin for a human. It must agree with
	// the commit, or it is a lie a reader will believe.
	if decoded.Review.Release != "" && row.Ref != "" && !strings.Contains(body, decoded.Review.Release) {
		gaps = append(gaps, fmt.Sprintf("the caller's pin does not carry the contract's release name %q, so the human-readable name and the commit disagree", decoded.Review.Release))
	}
	return gaps
}

// pinGapsForCaller checks the one ref that decides which reviewer actually runs.
// It is its own function because the three cases below — no pin, a pin that is
// not a commit, a commit older than the floor — are the whole safety of a shared
// reviewer, and folding them back into reviewGaps pushed that function past the
// complexity ceiling the shared linter enforces.
func (c *orgClient) pinGapsForCaller(ctx context.Context, ref string) []string {
	switch {
	case ref == "":
		return []string{"the caller does not use gophersys/cictl/review at all, so it runs some other reviewer"}
	case !reSHA40.MatchString(ref):
		return []string{fmt.Sprintf("the reviewer is pinned to %q, which is not a 40-hex commit: a tag is a pointer its owner can move", ref)}
	}
	atOrAfter, err := c.atOrAfterFloor(ctx, ref)
	switch {
	case err != nil:
		return []string{fmt.Sprintf("the reviewer pin %s could not be compared against the floor: %v", short(ref), err)}
	case !atOrAfter:
		return []string{fmt.Sprintf("the reviewer is pinned to %s, which is behind the floor %s", short(ref), short(c.opts.Floor))}
	}
	return nil
}

// hygieneGaps applies the three rules that exist today in exactly ONE
// repository's private test suite, to every workflow of every repository. That
// is the whole point of the bird's-eye verb: a rule that must be repeated in ten
// repositories is not a rule.
func hygieneGaps(files map[string]string) []string {
	var gaps []string
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		gaps = append(gaps, pinGaps(name, body)...)
		gaps = append(gaps, timeoutGaps(name, body)...)
		gaps = append(gaps, poolGaps(name, body)...)
	}
	return gaps
}

// pinGaps holds every action to a 40-hex commit. gophersys/* is NOT exempt.
//
// The exemption that used to exist justified itself with "a gophersys/ reusable
// workflow carries its own pin-guard (job_workflow_sha)". That justification was
// false: job_workflow_sha is EMPTY inside a called workflow, so the guard it
// named could never resolve. The exemption outlived its own reason, which is why
// the rule now covers everything.
func pinGaps(name, body string) []string {
	var gaps []string
	for _, m := range reUses.FindAllStringSubmatch(body, -1) {
		action, ref := m[1], m[2]
		if reSHA40.MatchString(ref) {
			continue
		}
		gaps = append(gaps, fmt.Sprintf("%s/%s pins %s to %q, which is not a 40-hex commit", WorkflowDir, name, action, ref))
	}
	return gaps
}

// timeoutGaps requires every job to declare timeout-minutes exactly once, with a
// positive value.
//
// Both halves matter and neither is redundant. A job with NO limit inherits
// GitHub's default of 360 minutes, and actionlint is silent about an absent key.
// A job with TWO is invalid workflow YAML: GitHub answers with a startup failure
// that attaches NO check to the pull request, so a dead workflow reads as an
// absent one — that defect ran for days in three repositories.
func timeoutGaps(name, body string) []string {
	jobs := splitJobs(body)
	var gaps []string
	for _, j := range jobs {
		values := reTimeout.FindAllStringSubmatch(j.body, -1)
		switch {
		case len(values) == 0:
			gaps = append(gaps, fmt.Sprintf("%s/%s job %q declares no timeout-minutes, so it inherits GitHub's default of 360 minutes", WorkflowDir, name, j.name))
		case len(values) > 1:
			gaps = append(gaps, fmt.Sprintf("%s/%s job %q declares timeout-minutes %d times, which is invalid workflow YAML: GitHub answers with a startup failure that attaches no check at all", WorkflowDir, name, j.name, len(values)))
		default:
			if n, err := strconv.Atoi(strings.TrimSpace(values[0][1])); err != nil || n <= 0 {
				gaps = append(gaps, fmt.Sprintf("%s/%s job %q sets timeout-minutes to %q, which is not a positive whole number of minutes", WorkflowDir, name, j.name, values[0][1]))
			}
		}
	}
	return gaps
}

// poolGaps requires every job to run on a self-hosted arc-* pool.
//
// The account had 195 of 2000 GitHub-hosted minutes left against a $0 budget, and
// at 0 a hosted job does not fail — it never starts. A job that never starts
// attaches no check, which reads as a check nobody configured.
func poolGaps(name, body string) []string {
	var gaps []string
	for _, m := range reRunsOn.FindAllStringSubmatch(body, -1) {
		pool := m[1]
		if strings.HasPrefix(pool, "arc-") {
			continue
		}
		gaps = append(gaps, fmt.Sprintf("%s/%s runs on %q, which is not an arc-* pool: against a $0 hosted budget a job does not fail, it never starts", WorkflowDir, name, pool))
	}
	return gaps
}

// jobBlock is one job of a workflow: its name and the text between its key and
// the next job key.
type jobBlock struct {
	name string
	body string
}

// splitJobs returns the jobs of a workflow. Under `jobs:` a key at exactly two
// spaces is a job name and nothing else is, so the nesting is readable without a
// YAML parser — which is required, because a parser cannot see a duplicate key.
//
// THE SCOPE IS THE LOAD-BEARING PART. A two-space key exists elsewhere in every
// workflow: `on:` carries `  pull_request:`, and reading that as a job reported
// "job pull_request declares no timeout-minutes" against a caller that is
// perfectly correct. So the scan is bounded to the `jobs:` mapping — from its key
// to the next line that begins in column 0 — and never the whole document.
func splitJobs(body string) []jobBlock {
	region, ok := jobsRegion(body)
	if !ok {
		return nil
	}
	locs := reJob.FindAllStringSubmatchIndex(region, -1)
	out := make([]jobBlock, 0, len(locs))
	for i, loc := range locs {
		end := len(region)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out = append(out, jobBlock{name: region[loc[2]:loc[3]], body: region[loc[0]:end]})
	}
	return out
}

// jobsRegion returns the text of the `jobs:` mapping. A top-level key is a line
// whose first character is neither a space nor a comment marker, so the mapping
// ends at the first such line after `jobs:` — or at the end of the document,
// which is where `jobs:` usually is.
func jobsRegion(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "jobs:") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			continue
		}
		end = i
		break
	}
	return strings.Join(lines[start:end], "\n"), true
}

// reviewRef extracts the ref the caller pins gophersys/cictl/review to.
func reviewRef(body string) string {
	for _, m := range reUses.FindAllStringSubmatch(body, -1) {
		if strings.HasSuffix(m[1], "/cictl/review") {
			return m[2]
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// Board renders the report as one terminal board: a per-repository row, the
// counts, and then every gap in full underneath. The row is the glance; the
// detail below it is what somebody acts on.
func (r OrgReport) Board() string {
	var b strings.Builder
	inContract, drifted, reviewing := 0, 0, 0
	for i := range r.Rows {
		row := &r.Rows[i]
		if row.OK() {
			inContract++
		} else {
			drifted++
		}
		if row.ReviewEnabled {
			reviewing++
		}
	}

	fmt.Fprintf(&b, "┌─ ORG CONTRACT · %s · %s ─────────────────────────────────────┐\n",
		r.Org, r.CheckedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "│ %-16s %-9s %-6s %-9s %-9s %s\n", "REPO", "CONTRACT", "REVIEW", "TIER", "PIN", "STATE")
	fmt.Fprintf(&b, "├%s\n", strings.Repeat("─", 74))
	for i := range r.Rows {
		row := &r.Rows[i]
		fmt.Fprintf(&b, "│ %-16s %-9s %-6s %-9s %-9s %s\n",
			truncate(row.Repo, 16), marker(row.HasContract), marker(row.ReviewEnabled),
			dash(row.Tier), dash(short(row.Ref)), state(row))
	}
	fmt.Fprintf(&b, "├%s\n", strings.Repeat("─", 74))
	fmt.Fprintf(&b, "│ %d repo(s) · %d in contract · %d with gaps · %d reviewing · %d gap(s) total\n",
		len(r.Rows), inContract, drifted, reviewing, r.Gaps())
	fmt.Fprintf(&b, "└%s\n", strings.Repeat("─", 74))

	if r.OK() {
		b.WriteString("\nThe fleet is in contract.\n")
		return b.String()
	}
	b.WriteString("\nGAPS\n")
	for i := range r.Rows {
		row := &r.Rows[i]
		if row.OK() {
			continue
		}
		fmt.Fprintf(&b, "\n  %s\n", row.Repo)
		for _, g := range row.Gaps {
			fmt.Fprintf(&b, "    · %s\n", g)
		}
	}
	// This verb REPORTS. Each repository's own `cictl drift` is what blocks that
	// repository's pull requests, and it names the exact file.
	b.WriteString("\nThis is a report. Fix each gap in the repository that owns it: its own\n")
	b.WriteString("`cictl drift` gate is what blocks its pull requests.\n")
	return b.String()
}

func marker(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func state(row *OrgRow) string {
	if row.OK() {
		return "OK"
	}
	return fmt.Sprintf("%d gap(s)", len(row.Gaps))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
