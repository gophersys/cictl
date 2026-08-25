package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gophersys/cictl/internal/failure"
)

// THE BIRD'S-EYE VIEW.
//
// `conformance` (no --org) answers "is THIS repository's .ci/ canonical", from a
// local directory. `conformance --org` answers a different question — "is the
// FLEET in contract" — and it is the only thing in the organization that can.
//
// Three of its rules exist today in exactly one repository's private test suite
// (SHA-pinned actions, a timeout on every job, an arc-* pool on every job). A
// rule that has to be repeated in ten repositories is not a rule; this verb is
// where they stop being repeated.
//
// IT REPORTS. IT DOES NOT BLOCK ANOTHER REPOSITORY'S MERGE. An org-wide required
// check reddens a pull request that cannot fix the cause: repository A goes red
// because repository B drifted, and the author of A has no way to act on it. Each
// repository's own `cictl drift` already blocks its own pull requests and names
// the exact file. So a fleet gap lands on the board and the process exits 0 by
// default; only an OPERATIONAL failure — no token, an API error, a document that
// will not parse — exits non-zero, because that is this verb failing rather than
// reporting. The scheduled job that runs it can turn gaps into its own red with
// --fail-on-gap, which is the one loud owner the design asks for.

// OrgOptions configures an organization-wide audit.
type OrgOptions struct {
	// Org is the GitHub organization to audit.
	Org string
	// Token authenticates the API reads. Every repository here is private, so an
	// anonymous read answers 404 for content that exists — a report about the
	// credential wearing the clothes of a report about the fleet.
	Token string
	// BaseURL is the GitHub API root. It exists so the tests can point the whole
	// audit at an httptest server: a check that can only be exercised against the
	// live API is a check nobody runs.
	BaseURL string
	// Client performs the requests. Nil means a client with a bounded timeout.
	Client *http.Client
	// Floor, when set, is the oldest cictl commit a repository may review with.
	// An empty floor asserts only that the ref is a commit.
	Floor string
	// ReviewRepo is the repository the review action lives in (this one).
	ReviewRepo string
}

// OrgRow is one repository's line on the board.
type OrgRow struct {
	// Repo is the short repository name.
	Repo string
	// HasContract reports whether .ci/ci.contract.yaml was found.
	HasContract bool
	// ReviewEnabled reports whether the contract asks for a reviewer.
	ReviewEnabled bool
	// Tier is the declared review tier, empty when there is no review block.
	Tier string
	// Ref is the cictl commit the repository's caller pins, empty when absent.
	Ref string
	// Workflows is how many workflow files were read.
	Workflows int
	// Gaps are this repository's contract violations, in rule order.
	Gaps []string
}

// OK reports whether this repository is in contract.
func (r *OrgRow) OK() bool { return len(r.Gaps) == 0 }

// OrgReport is the outcome of an organization-wide audit.
type OrgReport struct {
	// Org is the audited organization.
	Org string
	// Rows is one entry per repository, sorted by name.
	Rows []OrgRow
	// CheckedAt is when the audit ran; the board stamps it, because a board with
	// no timestamp is a claim about an unknown moment.
	CheckedAt time.Time
}

// Gaps counts the contract violations across the fleet.
func (r OrgReport) Gaps() int {
	n := 0
	for i := range r.Rows {
		n += len(r.Rows[i].Gaps)
	}
	return n
}

// OK reports whether the whole fleet is in contract.
func (r OrgReport) OK() bool { return r.Gaps() == 0 }

// CheckOrg audits every repository of an organization against the contract. The
// returned error is reserved for this verb's OWN failures; a fleet that is out of
// contract is a full report and a nil error.
func CheckOrg(ctx context.Context, opts *OrgOptions) (OrgReport, error) {
	c, err := newOrgClient(opts)
	if err != nil {
		return OrgReport{}, err
	}
	names, err := c.repositoryNames(ctx)
	if err != nil {
		return OrgReport{}, err
	}
	rep := OrgReport{Org: opts.Org, CheckedAt: time.Now().UTC()}
	for _, name := range names {
		row, err := c.auditRepository(ctx, name)
		if err != nil {
			return OrgReport{}, failure.Wrapf(err, "audit %s/%s", opts.Org, name)
		}
		rep.Rows = append(rep.Rows, row)
	}
	sort.Slice(rep.Rows, func(i, j int) bool { return rep.Rows[i].Repo < rep.Rows[j].Repo })
	return rep, nil
}

// orgClient is the audit's view of the GitHub API.
type orgClient struct {
	opts *OrgOptions
	http *http.Client
}

func newOrgClient(opts *OrgOptions) (*orgClient, error) {
	if strings.TrimSpace(opts.Org) == "" {
		return nil, errNoOrg
	}
	// A missing credential FAILS, and never falls through to an anonymous read.
	// Anonymous, a private repository's contents answer 404, so every rule would
	// report "absent" for files that are present — a green-looking report that
	// checked nothing, or a red about the wrong thing.
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errNoToken
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		opts.BaseURL = "https://api.github.com"
	}
	if strings.TrimSpace(opts.ReviewRepo) == "" {
		opts.ReviewRepo = "cictl"
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &orgClient{opts: opts, http: client}, nil
}

// errNoOrg and errNoToken are the two operational refusals that happen before a
// single request is made.
var (
	errNoOrg            = errors.New("no organization named: pass --org <name>")
	errNoToken          = errors.New("no GitHub token: set GITHUB_TOKEN or GH_TOKEN. Every gophersys repository is private, so an anonymous read reports absent for files that exist — a report about the credential wearing the clothes of a report about the fleet")
	errNoRepositories   = errors.New("the organization has no repositories; the listing is broken, not the fleet")
	errNotFound         = errors.New("not found")
	errUnexpectedStatus = errors.New("unexpected API status")
)

// get performs one authenticated API read. A 404 is returned as errNotFound so a
// caller can tell "this file is absent" (a finding) from "the API failed" (an
// operational failure). Everything else non-2xx is operational.
func (c *orgClient) get(ctx context.Context, path, accept string) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.opts.BaseURL, "/")+path, http.NoBody)
	if err != nil {
		return nil, failure.Wrapf(err, "build request %s", path)
	}
	req.Header.Set("Authorization", "Bearer "+c.opts.Token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, failure.Wrapf(err, "GET %s", path)
	}
	// A close failure after a full read cannot change the answer, but it is still
	// a failure and it is reported rather than blanked: a swallowed error here is
	// the same class of defect the audit itself exists to find.
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			body, err = nil, failure.Wrapf(cerr, "close response body for %s", path)
		}
	}()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, failure.Wrapf(err, "read %s", path)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, failure.Wrapf(errUnexpectedStatus, "GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// repoNames lists every non-archived repository of the organization. Archived
// repositories are excluded on purpose: an archive is frozen by definition, so
// holding one to a contract it predates produces gaps nobody may act on.
func (c *orgClient) repositoryNames(ctx context.Context) ([]string, error) {
	var names []string
	for page := 1; page <= 10; page++ {
		body, err := c.get(ctx, fmt.Sprintf("/orgs/%s/repos?per_page=100&page=%d", url.PathEscape(c.opts.Org), page), "application/vnd.github+json")
		if err != nil {
			return nil, err
		}
		var batch []struct {
			Name     string `json:"name"`
			Archived bool   `json:"archived"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, failure.Wrap(err, "parse repository listing")
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			if !r.Archived {
				names = append(names, r.Name)
			}
		}
		if len(batch) < 100 {
			break
		}
	}
	if len(names) == 0 {
		return nil, errNoRepositories
	}
	sort.Strings(names)
	return names, nil
}

// raw reads one file's bytes. It returns (nil, false, nil) when the file is
// simply absent, which is a finding and not an error.
func (c *orgClient) raw(ctx context.Context, repository, path string) (body []byte, present bool, err error) {
	raw, err := c.get(ctx,
		fmt.Sprintf("/repos/%s/%s/contents/%s", url.PathEscape(c.opts.Org), url.PathEscape(repository), path),
		"application/vnd.github.raw")
	switch {
	case err == nil:
		return raw, true, nil
	case errors.Is(err, errNotFound):
		return nil, false, nil
	default:
		return nil, false, err
	}
}

// workflowFiles reads every workflow of a repository, keyed by base name.
func (c *orgClient) workflowFiles(ctx context.Context, repository string) (map[string]string, error) {
	body, err := c.get(ctx,
		fmt.Sprintf("/repos/%s/%s/contents/.github/workflows", url.PathEscape(c.opts.Org), url.PathEscape(repository)),
		"application/vnd.github+json")
	switch {
	case errors.Is(err, errNotFound):
		return map[string]string{}, nil
	case err != nil:
		return nil, err
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, failure.Wrapf(err, "parse workflow listing for %s", repository)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.Type != "file" || (!strings.HasSuffix(e.Name, ".yml") && !strings.HasSuffix(e.Name, ".yaml")) {
			continue
		}
		raw, ok, err := c.raw(ctx, repository, ".github/workflows/"+e.Name)
		if err != nil {
			return nil, err
		}
		if ok {
			out[e.Name] = string(raw)
		}
	}
	return out, nil
}

// atOrAfterFloor reports whether ref is the floor commit or a descendant of it.
// A descendant is what "this repository is not reviewing with a retired reviewer"
// actually means; string comparison of two shas means nothing at all.
func (c *orgClient) atOrAfterFloor(ctx context.Context, ref string) (bool, error) {
	if c.opts.Floor == "" || c.opts.Floor == ref {
		return true, nil
	}
	body, err := c.get(ctx,
		fmt.Sprintf("/repos/%s/%s/compare/%s...%s", url.PathEscape(c.opts.Org), url.PathEscape(c.opts.ReviewRepo), url.PathEscape(c.opts.Floor), url.PathEscape(ref)),
		"application/vnd.github+json")
	if err != nil {
		return false, err
	}
	var cmp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &cmp); err != nil {
		return false, failure.Wrap(err, "parse commit comparison")
	}
	return cmp.Status == "identical" || cmp.Status == "ahead", nil
}
