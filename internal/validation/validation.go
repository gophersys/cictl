// Package validation validates a contract instance two ways: STRUCTURALLY against
// the JSON Schema emitted from the contract structs, and SEMANTICALLY against the
// cross-field rules a schema cannot express (every tier verb non-empty, nightly
// has a schedule, privileged implies substrate, and the closed-set membership the
// schema also guards — re-checked here so a semantic-only caller is complete).
package validation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gophersys/cictl/internal/contract"
	"github.com/gophersys/cictl/internal/failure"
	"github.com/gophersys/cictl/internal/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Problem is one validation failure: a JSON-pointer-ish location and a message.
type Problem struct {
	Where   string
	Message string
}

func (p Problem) String() string {
	if p.Where == "" {
		return p.Message
	}
	return p.Where + ": " + p.Message
}

// Result is the outcome of validating one instance: the (possibly empty) set of
// problems. OK reports whether the instance is valid.
type Result struct {
	Problems []Problem
}

// OK reports whether the instance passed every check.
func (r Result) OK() bool { return len(r.Problems) == 0 }

// InvalidError is the typed aggregate returned by Result.Error: the set of
// problems that made an instance invalid. Callers can inspect Problems or branch
// on the error via errors.As rather than scraping the message string.
type InvalidError struct {
	Problems []Problem
}

func (e *InvalidError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d validation problem(s):", len(e.Problems))
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  - %s", p.String())
	}
	return b.String()
}

// Error renders the problems as a single typed error, or nil when valid.
func (r Result) Error() error {
	if r.OK() {
		return nil
	}
	return &InvalidError{Problems: r.Problems}
}

// ValidateBytes decodes contract YAML bytes and runs the full structural +
// semantic validation, returning a Result. A decode failure is itself a problem
// (so malformed YAML is reported, not panicked on).
func ValidateBytes(raw []byte) Result {
	c, err := contract.Decode(raw)
	if err != nil {
		return Result{Problems: []Problem{{Where: "", Message: err.Error()}}}
	}
	return Validate(c, raw)
}

// Validate runs structural validation (the instance bytes against the emitted
// schema) followed by semantic validation (cross-field rules over the decoded
// contract). rawYAML is the original instance bytes used for the structural pass;
// when nil, the contract is re-encoded to JSON for the structural check so a
// programmatically-built Contract can still be validated.
func Validate(c *contract.Contract, rawYAML []byte) Result {
	var problems []Problem
	problems = append(problems, structural(rawYAML, c)...)
	problems = append(problems, semantic(c)...)
	sort.SliceStable(problems, func(i, j int) bool {
		if problems[i].Where != problems[j].Where {
			return problems[i].Where < problems[j].Where
		}
		return problems[i].Message < problems[j].Message
	})
	return Result{Problems: problems}
}

// structural compiles the emitted JSON Schema and validates the instance against
// it. The instance is taken from rawYAML when provided (decoded to a generic JSON
// value), else re-marshaled from the decoded contract.
func structural(rawYAML []byte, c *contract.Contract) []Problem {
	schemaBytes, err := schema.Emit()
	if err != nil {
		return []Problem{{Message: fmt.Sprintf("emit schema: %v", err)}}
	}
	compiler := jsonschema.NewCompiler()
	var schemaDoc any
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		return []Problem{{Message: fmt.Sprintf("parse emitted schema: %v", err)}}
	}
	const schemaURL = "eden://ci/contract.schema.json"
	if err := compiler.AddResource(schemaURL, schemaDoc); err != nil {
		return []Problem{{Message: fmt.Sprintf("register schema: %v", err)}}
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return []Problem{{Message: fmt.Sprintf("compile schema: %v", err)}}
	}

	instance, err := instanceValue(rawYAML, c)
	if err != nil {
		return []Problem{{Message: err.Error()}}
	}
	if err := compiled.Validate(instance); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return collectSchemaProblems(ve)
		}
		return []Problem{{Message: err.Error()}}
	}
	return nil
}

// instanceValue produces the generic JSON value the schema validator consumes:
// from the original YAML when available (so the user's literal document is what
// is checked), otherwise from the decoded contract re-encoded as JSON.
func instanceValue(rawYAML []byte, c *contract.Contract) (any, error) {
	var src []byte
	if len(rawYAML) > 0 {
		j, err := yamlToJSON(rawYAML)
		if err != nil {
			return nil, failure.Wrap(err, "normalize instance")
		}
		src = j
	} else {
		j, err := json.Marshal(c)
		if err != nil {
			return nil, failure.Wrap(err, "marshal instance")
		}
		src = j
	}
	var v any
	if err := json.Unmarshal(src, &v); err != nil {
		return nil, failure.Wrap(err, "decode instance")
	}
	return v, nil
}

// collectSchemaProblems flattens a jsonschema ValidationError tree into stable
// leaf problems keyed by instance location. Only leaf errors (no further causes)
// are recorded so the output is the concrete failing keywords, not the
// intermediate allOf/$ref wrappers.
func collectSchemaProblems(ve *jsonschema.ValidationError) []Problem {
	var out []Problem
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			out = append(out, Problem{
				Where:   instanceLocation(e.InstanceLocation),
				Message: e.Error(),
			})
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(out) == 0 {
		out = append(out, Problem{Where: "", Message: ve.Error()})
	}
	return out
}

// instanceLocation renders a JSON-pointer location from the validator's location
// segments (e.g. ["tiers","pr","verbs"] -> "/tiers/pr/verbs").
func instanceLocation(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	return "/" + strings.Join(loc, "/")
}

// semantic enforces the cross-field rules a JSON Schema cannot express, plus a
// defensive re-check of the closed-set memberships so a semantic-only caller is
// complete.
// runnerProblems checks the runner and the image it does or does not pair with.
// It is its own function because the rule is self-contained and because folding
// it into semantic() pushed that function past the complexity ceiling.
//
// A container job must name a known image. A self-hosted pool must not name one
// at all: its runner image already carries the toolchain, so a second image would
// be silently ignored — dead configuration that reads as intent.
func runnerProblems(c *contract.Contract) []Problem {
	var p []Problem
	if c.Runner.RunsOn == "" {
		p = append(p, Problem{Where: "/runner/runsOn", Message: "must be non-empty"})
	}
	switch {
	case c.Runner.Container && c.Image == "":
		p = append(p, Problem{Where: "/image", Message: "required when runner.container is true"})
	case c.Runner.Container && !inSet(c.Image, contract.Images):
		p = append(p, Problem{Where: "/image", Message: fmt.Sprintf("unknown image %q (want one of %s)", c.Image, joinImages(contract.Images))})
	case !c.Runner.Container && c.Image != "":
		p = append(p, Problem{Where: "/image", Message: "must be empty unless runner.container is true; a self-hosted pool's runner image already carries the toolchain"})
	}
	return p
}

func semantic(c *contract.Contract) []Problem {
	var p []Problem

	if c.APIVersion != contract.APIVersion {
		p = append(p, Problem{Where: "/apiVersion", Message: fmt.Sprintf("must be %q, got %q", contract.APIVersion, c.APIVersion)})
	}
	if strings.TrimSpace(c.Repo) == "" {
		p = append(p, Problem{Where: "/repo", Message: "must be non-empty"})
	}
	if !inSet(c.Kind, contract.Kinds) {
		p = append(p, Problem{Where: "/kind", Message: fmt.Sprintf("unknown kind %q (want one of %s)", c.Kind, joinKinds(contract.Kinds))})
	}
	p = append(p, runnerProblems(c)...)
	if len(c.Languages) == 0 {
		p = append(p, Problem{Where: "/languages", Message: "must list at least one language"})
	}
	for i, l := range c.Languages {
		if !inSet(l, contract.LanguagesAll) {
			p = append(p, Problem{Where: fmt.Sprintf("/languages/%d", i), Message: fmt.Sprintf("unknown language %q", l)})
		}
	}
	if len(c.Providers) == 0 {
		p = append(p, Problem{Where: "/providers", Message: "must list at least one provider"})
	}
	for i, pr := range c.Providers {
		if !inSet(pr, contract.ProvidersAll) {
			p = append(p, Problem{Where: fmt.Sprintf("/providers/%d", i), Message: fmt.Sprintf("unknown provider %q", pr)})
		}
	}

	p = append(p, tierProblems("/tiers/pr", &c.Tiers.Pr, false)...)
	p = append(p, tierProblems("/tiers/merge", &c.Tiers.Merge, false)...)
	p = append(p, tierProblems("/tiers/nightly", &c.Tiers.Nightly, true)...)
	p = append(p, reviewProblems(&c.Review)...)

	return p
}

// sha40 matches a full git commit: 40 lowercase hex characters and nothing else.
// A tag, a branch, an abbreviated sha and an uppercase sha are all rejected. The
// pin is the whole safety of a shared reviewer, and every one of those four is a
// pointer that can resolve to different code on two different days.
var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// reviewProblems validates the review block. A disabled block is checked for
// nothing: it renders no workflow, so a half-filled one cannot mislead anybody.
// An ENABLED block is checked completely, because every field of it is emitted
// into a workflow that spends money.
func reviewProblems(r *contract.Review) []Problem {
	if !r.Enabled {
		return nil
	}
	var p []Problem
	if r.Harness != "" && r.Harness != contract.ReviewHarnessClaude && r.Harness != contract.ReviewHarnessCodex {
		p = append(p, Problem{Where: "/review/harness", Message: fmt.Sprintf("unknown review harness %q (want claude or codex)", r.Harness)})
	}
	if !sha40.MatchString(r.Ref) {
		p = append(p, Problem{Where: "/review/ref", Message: fmt.Sprintf("must be a 40-hex commit, got %q; a tag or a branch is a pointer its owner can move", r.Ref)})
	}
	if _, ok := contract.TierPreset(r.Tier); !ok {
		p = append(p, Problem{Where: "/review/tier", Message: fmt.Sprintf("unknown tier %q (want one of %s)", r.Tier, joinTiers(contract.ReviewTiers))})
	}
	if strings.TrimSpace(r.RunsOn) == "" {
		p = append(p, Problem{Where: "/review/runsOn", Message: "must be non-empty"})
	}
	if r.TimeoutMinutes <= 0 {
		p = append(p, Problem{Where: "/review/timeoutMinutes", Message: "must be a positive number of minutes; a job with no limit inherits GitHub's default of 360"})
	}
	p = append(p, reviewStreamProblems(r)...)
	return p
}

// reviewStreamProblems checks the archive destination. It is its own function
// because folding it into reviewProblems pushed that function past the
// complexity ceiling the shared linter enforces.
func reviewStreamProblems(r *contract.Review) []Problem {
	var p []Problem
	switch r.StreamStore {
	case contract.ReviewStreamMinio:
		// There is deliberately no default endpoint. A URL nobody verified reads
		// as configuration and archives nothing, which is worse than an absent one.
		if strings.TrimSpace(r.StreamEndpoint) == "" {
			p = append(p, Problem{Where: "/review/streamEndpoint", Message: "required when streamStore is minio"})
		}
		if strings.TrimSpace(r.StreamBucket) == "" {
			p = append(p, Problem{Where: "/review/streamBucket", Message: "required when streamStore is minio"})
		}
	case contract.ReviewStreamNone:
		// An explicit opt-out must not carry the settings it opts out of: dead
		// configuration reads as intent.
		if strings.TrimSpace(r.StreamEndpoint) != "" || strings.TrimSpace(r.StreamBucket) != "" {
			p = append(p, Problem{Where: "/review/streamStore", Message: "streamEndpoint and streamBucket must be empty when streamStore is none"})
		}
	default:
		p = append(p, Problem{Where: "/review/streamStore", Message: fmt.Sprintf("unknown stream store %q (want one of %s)", r.StreamStore, joinStreamStores(contract.ReviewStreamStores))})
	}
	return p
}

// tierProblems validates one tier: non-empty verbs, in-enum substrates, the
// privileged⇔substrate coupling, and the nightly-only schedule requirement.
func tierProblems(where string, t *contract.Tier, nightly bool) []Problem {
	var p []Problem
	if len(t.Verbs) == 0 {
		p = append(p, Problem{Where: where + "/verbs", Message: "tier must declare at least one verb"})
	}
	for i, v := range t.Verbs {
		if strings.TrimSpace(v) == "" {
			p = append(p, Problem{Where: fmt.Sprintf("%s/verbs/%d", where, i), Message: "verb must be non-empty"})
		}
	}
	for i, s := range t.Substrate {
		if !inSet(s, contract.Substrates) {
			p = append(p, Problem{Where: fmt.Sprintf("%s/substrate/%d", where, i), Message: fmt.Sprintf("unknown substrate %q", s)})
		}
	}
	// A privileged tier exists to run real substrate; a substrate tier needs
	// privilege. Either without the other is a contradiction the generator cannot
	// honor coherently.
	if t.Privileged && len(t.Substrate) == 0 {
		p = append(p, Problem{Where: where + "/privileged", Message: "privileged tier must declare a non-empty substrate"})
	}
	if len(t.Substrate) > 0 && !t.Privileged {
		p = append(p, Problem{Where: where + "/privileged", Message: "tier with a substrate must be privileged"})
	}
	if t.TimeoutMinutes < 0 {
		p = append(p, Problem{Where: where + "/timeoutMinutes", Message: "must not be negative"})
	}
	if nightly {
		if strings.TrimSpace(t.Schedule) == "" {
			p = append(p, Problem{Where: where + "/schedule", Message: "nightly tier must declare a cron schedule"})
		}
	} else if strings.TrimSpace(t.Schedule) != "" {
		p = append(p, Problem{Where: where + "/schedule", Message: "only the nightly tier may declare a schedule"})
	}
	return p
}

func inSet[T comparable](v T, set []T) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

func joinKinds(ks []contract.Kind) string {
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}

func joinImages(is []contract.Image) string {
	parts := make([]string, len(is))
	for i, im := range is {
		parts[i] = string(im)
	}
	return strings.Join(parts, ", ")
}

func joinTiers(tiers []contract.ReviewTier) string {
	parts := make([]string, len(tiers))
	for i, t := range tiers {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}

func joinStreamStores(ss []contract.ReviewStreamStore) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}

// yamlToJSON converts a YAML document to canonical JSON bytes so the JSON-Schema
// validator (which speaks JSON value trees) can consume a YAML instance.
func yamlToJSON(raw []byte) ([]byte, error) {
	c, err := contract.Decode(raw)
	if err != nil {
		return nil, failure.Wrap(err, "decode yaml")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(c); err != nil {
		return nil, failure.Wrap(err, "encode json")
	}
	return buf.Bytes(), nil
}
