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

	"github.com/gophersys/eden/tools/cictl/internal/failure"
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
	// Image is the container image every tier job runs inside. One of the known
	// Eden images.
	Image Image `json:"image" yaml:"image"`
	// Languages lists the ecosystems present in the repo (drives nothing structural
	// yet; it is contract metadata the updatability matrix and humans read).
	Languages []Language `json:"languages" yaml:"languages" jsonschema:"minItems=1,uniqueItems=true"`
	// Tiers maps the three CI tiers to their verb/substrate definitions.
	Tiers Tiers `json:"tiers" yaml:"tiers"`
	// Providers lists the CI systems the workflows are generated for.
	Providers []Provider `json:"providers" yaml:"providers" jsonschema:"minItems=1,uniqueItems=true"`
	// ToolMatrix declares where pinned tool versions are read from.
	ToolMatrix ToolMatrix `json:"toolMatrix" yaml:"toolMatrix"`
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
