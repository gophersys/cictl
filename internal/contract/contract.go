// Package contract defines the Eden CI contract as Go structs. These structs ARE
// the schema source (the go-first-emit / RD-17 pattern): the JSON Schema, the
// validator, the workflow generator, and the conformance checker all derive from
// this one declaration, so there is exactly one home for the CI shape.
//
// A contract instance lives at a repo's .ci/ci.contract.yaml. It declares what
// gates ("verbs") run, on which substrate, under which providers, grouped into
// three tiers (pr / merge / nightly). The same verbs run identically locally and
// on the remote provider — CI is "run the gates I can run locally, on a remote
// host".
package contract

import (
	"os"

	"github.com/gophersys/cictl/internal/failure"
	"gopkg.in/yaml.v3"
)

// APIVersion is the constant apiVersion every contract instance must carry. A
// future schema revision bumps this string and the validator rejects the old one.
const APIVersion = "eden.ci/v1"

// Contract is the root of an Eden CI contract instance. Field order here is the
// canonical declaration order; the emitted JSON Schema and generated workflows
// derive from it deterministically.
type Contract struct {
	// APIVersion pins the contract schema revision. Must equal APIVersion.
	APIVersion string `json:"apiVersion" yaml:"apiVersion" jsonschema:"const=eden.ci/v1,description=Contract schema revision; must be eden.ci/v1"`
	// Repo is the short repository identifier (e.g. "libs", "eden").
	Repo string `json:"repo" yaml:"repo" jsonschema:"minLength=1,description=Short repository identifier"`
	// Kind classifies the repository so a provider can pick sensible defaults.
	Kind Kind `json:"kind" yaml:"kind"`
	// Runner declares where a tier's jobs execute.
	Runner Runner `json:"runner" yaml:"runner"`
	// Image is the container image every tier job runs inside. Required only when
	// Runner.Container is set; a self-hosted pool's runner image already carries
	// the toolchain, so there is nothing to put in a container.
	Image Image `json:"image,omitempty" yaml:"image,omitempty"`
	// Languages lists the ecosystems present in the repo (drives nothing structural
	// yet; it is contract metadata the updatability matrix and humans read).
	Languages []Language `json:"languages" yaml:"languages" jsonschema:"minItems=1,uniqueItems=true"`
	// Tiers maps the three CI tiers to their verb/substrate definitions.
	Tiers Tiers `json:"tiers" yaml:"tiers"`
	// Review declares whether this repo runs the pull request review agent, and
	// at which cictl commit. An absent block means the repo has no reviewer, which
	// is the state every repo was in before this field existed.
	Review Review `json:"review,omitempty" yaml:"review,omitempty"`
	// Providers lists the CI systems the workflows are generated for.
	Providers []Provider `json:"providers" yaml:"providers" jsonschema:"minItems=1,uniqueItems=true"`
	// ToolMatrix declares where pinned tool versions are read from.
	ToolMatrix ToolMatrix `json:"toolMatrix" yaml:"toolMatrix"`
}

// Runner declares where a tier's jobs execute. It exists because the runner was
// once hardcoded to ubuntu-latest, which is why the only repo that adopted this
// contract failed 100 consecutive runs: its jobs never reached the self-hosted
// fleet, and the container image they wanted was a private package that
// GITHUB_TOKEN cannot pull.
//
// RunsOn is a free string, deliberately not an enum. Pools are added for physical
// capability — an architecture, a USB-attached board, a different kernel — and
// adding one must never require changing this code.
type Runner struct {
	// RunsOn is emitted verbatim as the job's `runs-on:` value.
	RunsOn string `json:"runsOn" yaml:"runsOn" jsonschema:"minLength=1,description=The runs-on value; a GitHub-hosted label or a self-hosted pool"`
	// Container runs each job inside Image. Leave false for a self-hosted pool
	// whose runner image already carries the toolchain: a container block there
	// re-pays a cold image pull on every job (measured at over five minutes for
	// these images) because the pull happens inside the ephemeral pod.
	Container bool `json:"container" yaml:"container" jsonschema:"description=Run jobs inside Image; false for a self-hosted pool that already carries the toolchain"`
}

// Kind is the repository classification enum.
type Kind string

// The repository classifications.
const (
	KindMonorepo       Kind = "monorepo"
	KindLibraries      Kind = "libraries"
	KindTemplates      Kind = "templates"
	KindDevcontainer   Kind = "devcontainer"
	KindInfrastructure Kind = "infrastructure"
)

// Kinds is the closed set of repository classifications, in declaration order.
var Kinds = []Kind{KindMonorepo, KindLibraries, KindTemplates, KindDevcontainer, KindInfrastructure}

// JSONSchema contributes the kind enum to the emitted schema.
func (Kind) JSONSchema() *SchemaEnum { return enumSchema("Repository classification", Kinds) }

// Image is the known-image enum: every tier job runs inside one of these.
type Image string

// The known Eden container images.
const (
	ImageBase    Image = "ghcr.io/gophersys/base"
	ImageBaseUI  Image = "ghcr.io/gophersys/base-ui"
	ImageFlutter Image = "ghcr.io/gophersys/flutter"
	ImageZephyr  Image = "ghcr.io/gophersys/zephyr"
)

// Images is the closed set of known container images, in declaration order.
var Images = []Image{ImageBase, ImageBaseUI, ImageFlutter, ImageZephyr}

// JSONSchema contributes the image enum to the emitted schema.
func (Image) JSONSchema() *SchemaEnum { return enumSchema("Known Eden container image", Images) }

// Language is the ecosystem enum.
type Language string

// The source ecosystems a repository may contain.
const (
	LanguageGo         Language = "go"
	LanguageTypeScript Language = "typescript"
	LanguagePython     Language = "python"
	LanguageRust       Language = "rust"
	LanguageZephyr     Language = "zephyr"
)

// LanguagesAll is the closed set of ecosystems, in declaration order.
var LanguagesAll = []Language{LanguageGo, LanguageTypeScript, LanguagePython, LanguageRust, LanguageZephyr}

// JSONSchema contributes the language enum to the emitted schema.
func (Language) JSONSchema() *SchemaEnum { return enumSchema("Source ecosystem", LanguagesAll) }

// Substrate is the real-dependency enum a tier may need on the host (docker /
// k3d / kind clusters, databases, object store, message bus). A non-empty
// substrate list marks a tier as privileged-capable: its generated job starts a
// docker daemon.
type Substrate string

// The real host dependencies a tier may need.
const (
	SubstrateDocker   Substrate = "docker"
	SubstrateK3d      Substrate = "k3d"
	SubstrateKind     Substrate = "kind"
	SubstratePostgres Substrate = "postgres"
	SubstrateMinio    Substrate = "minio"
	SubstrateNats     Substrate = "nats"
)

// Substrates is the closed set of substrates, in declaration order.
var Substrates = []Substrate{SubstrateDocker, SubstrateK3d, SubstrateKind, SubstratePostgres, SubstrateMinio, SubstrateNats}

// JSONSchema contributes the substrate enum to the emitted schema.
func (Substrate) JSONSchema() *SchemaEnum { return enumSchema("Real host dependency", Substrates) }

// Provider is the CI-system enum. Only github is wired today.
type Provider string

// ProviderGithub is the GitHub Actions provider.
const ProviderGithub Provider = "github"

// ProvidersAll is the closed set of providers, in declaration order.
var ProvidersAll = []Provider{ProviderGithub}

// JSONSchema contributes the provider enum to the emitted schema.
func (Provider) JSONSchema() *SchemaEnum { return enumSchema("CI provider", ProvidersAll) }

// Tiers groups the three CI tiers. Each is event-driven: pr on every push to a
// branch, merge on a pull_request into the default branch, nightly on a schedule.
type Tiers struct {
	// Pr is the fast lane: cheap gates on every push, no real substrate.
	Pr Tier `json:"pr" yaml:"pr"`
	// Merge is the careful-orchestration lane: real-substrate gates before merge.
	Merge Tier `json:"merge" yaml:"merge"`
	// Nightly is the exhaustive lane on a cron schedule.
	Nightly Tier `json:"nightly" yaml:"nightly"`
}

// Tier is one CI tier: the verbs to run, the substrate they need, and the job
// shape (privileged, schedule, timeout).
type Tier struct {
	// Verbs are the .ci/ctl.sh verbs this tier invokes, in order. Each must be a
	// verb the repo's .ci/ctl.sh actually defines (conformance enforces this).
	Verbs []string `json:"verbs" yaml:"verbs" jsonschema:"description=.ci/ctl.sh verbs this tier runs, in order"`
	// Substrate is the real host dependencies this tier needs. Non-empty implies a
	// privileged, docker-daemon-backed job.
	Substrate []Substrate `json:"substrate,omitempty" yaml:"substrate,omitempty" jsonschema:"uniqueItems=true,description=Real host dependencies the tier needs"`
	// Privileged requests a privileged container with the docker socket mounted.
	// It is implied by (and validated against) a non-empty substrate.
	Privileged bool `json:"privileged,omitempty" yaml:"privileged,omitempty"`
	// Schedule is the cron expression for the nightly tier (required there, absent
	// elsewhere).
	Schedule string `json:"schedule,omitempty" yaml:"schedule,omitempty" jsonschema:"description=Cron expression (nightly tier only)"`
	// TimeoutMinutes caps the job runtime.
	TimeoutMinutes int `json:"timeoutMinutes,omitempty" yaml:"timeoutMinutes,omitempty" jsonschema:"minimum=1,description=Job timeout in minutes"`
}

// Review declares the pull request review agent for one repository. It is the
// contract half of the composite action at gophersys/cictl/review: the generator
// turns this block into .github/workflows/pr-review.yml, and `cictl drift` fails
// any hand edit of the result.
//
// WHY A COMMIT AND NOT A VERSION. The reusable workflow this replaces read
// `github.job_workflow_sha` to learn its own commit, and that context is EMPTY
// inside a called workflow (measured on gophersys/infrastructure#171), so the pin
// could not resolve and the guard correctly failed every run. A composite action
// has no such problem by construction: the runner checks the action repository
// out at the exact `uses:` ref before the first step runs, so the source on disk
// IS the pinned commit. There is no fetch, no context variable and no pin guard,
// because there is nothing left to verify.
//
// Ref is a 40-hex commit and never a tag. A tag is a pointer its owner can move;
// cictl moved 23 commits in one evening, and no review from that period can be
// reproduced from a tag. Release names that commit for a human reader, and
// `cictl conformance --org` asserts the two agree, so the comment cannot lie.
type Review struct {
	// Enabled turns the reviewer on for this repository. False (the default)
	// renders no pr-review.yml at all.
	Enabled bool `json:"enabled" yaml:"enabled" jsonschema:"description=Run the pull request review agent in this repository"`
	// Ref is the cictl commit this repository reviews with: a 40-hex SHA, emitted
	// verbatim on the action's `uses:` line. A repository may sit on an older ref
	// deliberately; nothing bumps a caller for it.
	Ref string `json:"ref,omitempty" yaml:"ref,omitempty" jsonschema:"description=The 40-hex cictl commit the reviewer runs at"`
	// Release is the human-readable name of Ref (e.g. "v0.6.0"), emitted as a
	// trailing comment. It is decoration for a reader and is never the pin.
	Release string `json:"release,omitempty" yaml:"release,omitempty" jsonschema:"description=Release name of Ref, emitted as a trailing comment"`
	// Tier selects the spend preset. It is the repository's governance type, not a
	// free choice: the model, budget and round ceiling are derived from it in one
	// place (TierPreset) so a change to a preset reaches every repository of that
	// type without editing a single contract.
	Tier ReviewTier `json:"tier,omitempty" yaml:"tier,omitempty"`
	// RunsOn is the pool the review job runs on. It is a free string for the same
	// reason Runner.RunsOn is.
	RunsOn string `json:"runsOn,omitempty" yaml:"runsOn,omitempty" jsonschema:"description=The runs-on pool for the review job"`
	// TimeoutMinutes caps the review job. It counts EXECUTION only: the worst wait
	// measured on arc-review was 4353s of queue followed by 64s of execution, and
	// that run reported success. No value of this key would have shown it.
	TimeoutMinutes int `json:"timeoutMinutes,omitempty" yaml:"timeoutMinutes,omitempty" jsonschema:"minimum=1,description=Review job timeout in minutes"`
	// StreamStore is where the reviewer's full event stream is archived.
	StreamStore ReviewStreamStore `json:"streamStore,omitempty" yaml:"streamStore,omitempty"`
	// StreamEndpoint is the S3-compatible endpoint of the object store. Required
	// when StreamStore is minio; there is deliberately no default, because a
	// default nobody verified is a URL that reads as configuration and archives
	// nothing.
	StreamEndpoint string `json:"streamEndpoint,omitempty" yaml:"streamEndpoint,omitempty" jsonschema:"description=S3-compatible endpoint for the review stream archive"`
	// StreamBucket is the bucket the streams land in.
	StreamBucket string `json:"streamBucket,omitempty" yaml:"streamBucket,omitempty" jsonschema:"description=Bucket the review streams are written to"`
}

// ReviewTier is the repository's governance type, which selects the spend preset.
type ReviewTier string

// The repository governance types, as assigned in the gophersys repo-governance
// rule. They are the tier axis on purpose: a repository already has exactly one
// type, so no second classification has to be invented or kept in step.
const (
	ReviewTierPlatform ReviewTier = "platform"
	ReviewTierModule   ReviewTier = "module"
	ReviewTierResearch ReviewTier = "research"
	ReviewTierTooling  ReviewTier = "tooling"
)

// ReviewTiers is the closed set of review tiers, in declaration order.
var ReviewTiers = []ReviewTier{ReviewTierPlatform, ReviewTierModule, ReviewTierResearch, ReviewTierTooling}

// JSONSchema contributes the review-tier enum to the emitted schema.
func (ReviewTier) JSONSchema() *SchemaEnum {
	return enumSchema("Repository governance type; selects the review spend preset", ReviewTiers)
}

// ReviewStreamStore is where the reviewer's event stream is archived.
type ReviewStreamStore string

// The stream destinations.
//
// GitHub artifact storage is deliberately NOT one of them. It is an org-wide
// quota, and when it filled, the `Keep the run` step failed on runs whose Review
// step had already succeeded — measured on .devcontainer run 32868094840 and
// infrastructure run 32763725563, both 2026-08-24/25. A shared quota must never
// decide one repository's review verdict.
const (
	// ReviewStreamMinio writes the stream to the homelab MinIO over S3.
	ReviewStreamMinio ReviewStreamStore = "minio"
	// ReviewStreamNone keeps no stream. It is an explicit, visible opt-out in the
	// generated caller, never a silent skip.
	ReviewStreamNone ReviewStreamStore = "none"
)

// ReviewStreamStores is the closed set of stream destinations, in declaration order.
var ReviewStreamStores = []ReviewStreamStore{ReviewStreamMinio, ReviewStreamNone}

// JSONSchema contributes the stream-store enum to the emitted schema.
func (ReviewStreamStore) JSONSchema() *SchemaEnum {
	return enumSchema("Where the reviewer's event stream is archived", ReviewStreamStores)
}

// ReviewPreset is the resolved spend envelope for one tier: what the generator
// writes onto the action's `with:` block.
type ReviewPreset struct {
	// Model is the CLI's family alias, never a pinned model id. `opus` and
	// `sonnet` track the newest model of each family as the pinned CLI advances,
	// which is the standing policy (always latest Sonnet/Opus, 2026-08-25). A
	// pinned id would be a second version home that ages silently — the exact
	// defect the SHA pin above exists to remove — and review.sh already logs the
	// id the alias RESOLVED to, read from the agent's own init event, so the run
	// record still names the model that answered.
	Model string
	// BudgetUsd is the per-round ceiling handed to `claude --max-budget-usd`.
	BudgetUsd int
	// MaxRounds is the review round ceiling before the single closing pass.
	MaxRounds int
}

// The two presets. Worst case per pull request is (MaxRounds + 1) * BudgetUsd,
// because the closing pass is a full-price round: $125 deep, $30 light.
var (
	// reviewPresetDeep serves the repositories that RUN things, where the defect a
	// test cannot see is most expensive.
	reviewPresetDeep = ReviewPreset{Model: "opus", BudgetUsd: 25, MaxRounds: 4}
	// reviewPresetLight serves exploration and tooling.
	reviewPresetLight = ReviewPreset{Model: "sonnet", BudgetUsd: 10, MaxRounds: 2}
)

// TierPreset returns the spend envelope for a tier, and reports whether the tier
// is known. It is the ONE home of the preset table: changing what `platform`
// costs is one edit here and a regeneration, never an edit of four contracts.
func TierPreset(t ReviewTier) (ReviewPreset, bool) {
	switch t {
	case ReviewTierPlatform, ReviewTierModule:
		return reviewPresetDeep, true
	case ReviewTierResearch, ReviewTierTooling:
		return reviewPresetLight, true
	default:
		return ReviewPreset{}, false
	}
}

// ToolMatrix declares where pinned tool versions are read from. The updatability
// verb parses each source for pins and reports a matrix; this generalises the
// harness-upgrade-check (ADR-0021) to all pinned dependencies.
type ToolMatrix struct {
	// Sources are glob paths, relative to the repo root, to files carrying pinned
	// versions: versions.env, **/go.mod, **/package.json, Dockerfile.
	Sources []string `json:"sources" yaml:"sources" jsonschema:"description=Glob paths to files carrying pinned versions"`
}

// Load reads and YAML-decodes a contract instance from path. It does NOT validate;
// callers run Validate (or the validation package) for structural and semantic
// checks. A strict decoder rejects unknown fields so a typo'd key fails loudly
// rather than being silently ignored.
func Load(path string) (*Contract, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is an operator-chosen contract file.
	if err != nil {
		return nil, failure.Wrapf(err, "read contract %s", path)
	}
	c, err := Decode(raw)
	if err != nil {
		return nil, failure.Wrapf(err, "parse contract %s", path)
	}
	return c, nil
}

// Decode YAML-decodes contract bytes with strict (KnownFields) decoding.
func Decode(raw []byte) (*Contract, error) {
	dec := yaml.NewDecoder(bytesReader(raw))
	dec.KnownFields(true)
	var c Contract
	if err := dec.Decode(&c); err != nil {
		return nil, failure.Wrap(err, "decode yaml")
	}
	return &c, nil
}
