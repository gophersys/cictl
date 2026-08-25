package validation_test

import (
	"strings"
	"testing"

	"github.com/gophersys/cictl/internal/contract"
	"github.com/gophersys/cictl/internal/validation"
)

// reviewBase is a contract whose review block is correct in every way. Each test
// below changes exactly ONE field and requires the matching problem — a validator
// proven only against a passing instance proves nothing.
func reviewBase() *contract.Contract {
	return &contract.Contract{
		APIVersion: contract.APIVersion,
		Repo:       "libs",
		Kind:       contract.KindLibraries,
		Runner:     contract.Runner{RunsOn: "arc-org"},
		Languages:  []contract.Language{contract.LanguageGo},
		Tiers: contract.Tiers{
			Pr:      contract.Tier{Verbs: []string{"gate"}, TimeoutMinutes: 15},
			Merge:   contract.Tier{Verbs: []string{"gate"}, TimeoutMinutes: 30},
			Nightly: contract.Tier{Verbs: []string{"gate"}, Schedule: "0 6 * * *", TimeoutMinutes: 90},
		},
		Review: contract.Review{
			Enabled: true, Ref: "0123456789abcdef0123456789abcdef01234567",
			Tier: contract.ReviewTierPlatform, RunsOn: "arc-review", TimeoutMinutes: 30,
			StreamStore: contract.ReviewStreamMinio, StreamEndpoint: "https://minio.example.invalid", StreamBucket: "review-streams",
		},
		Providers:  []contract.Provider{contract.ProviderGithub},
		ToolMatrix: contract.ToolMatrix{Sources: []string{"**/go.mod"}},
	}
}

// TestValidate_AcceptsAGoodReviewBlock is the control for every case below.
func TestValidate_AcceptsAGoodReviewBlock(t *testing.T) {
	t.Parallel()
	if res := validation.Validate(reviewBase(), nil); !res.OK() {
		t.Fatalf("a correct review block was rejected: %v", res.Error())
	}
}

// TestValidate_ADisabledReviewBlockSkipsTheSemanticRules. A half-filled block
// that renders nothing cannot mislead anybody, and holding it to the enabled
// rules would make "turn the reviewer off" a change that fails validation.
func TestValidate_ADisabledReviewBlockSkipsTheSemanticRules(t *testing.T) {
	t.Parallel()
	c := reviewBase()
	c.Review = contract.Review{Enabled: false, Ref: "not-a-sha"}
	if res := validation.Validate(c, nil); !res.OK() {
		t.Fatalf("a disabled review block was held to the enabled-only rules: %v", res.Error())
	}
}

// TestValidate_AnUnknownTierIsStructurallyInvalidEvenWhenDisabled. The two layers
// answer different questions and this pins the boundary between them. The
// SEMANTIC rules ask "does this block make sense as a reviewer", and a disabled
// block is exempt. The STRUCTURAL layer asks "is this a legal document", and a
// tier outside the closed set is a typo whether the reviewer runs or not —
// catching it while the block is off is what stops it from surfacing months
// later, on the day somebody flips `enabled` to true.
func TestValidate_AnUnknownTierIsStructurallyInvalidEvenWhenDisabled(t *testing.T) {
	t.Parallel()
	c := reviewBase()
	c.Review = contract.Review{Enabled: false, Tier: "nonsense"}
	res := validation.Validate(c, nil)
	if res.OK() {
		t.Fatal("a tier outside the closed set was accepted; the emitted schema's enum did not hold")
	}
}

// TestValidate_AnAbsentReviewBlockIsValid. Every repository that adopted the
// contract before the reviewer existed has no `review:` block at all, and none of
// them may be made invalid by this field arriving.
func TestValidate_AnAbsentReviewBlockIsValid(t *testing.T) {
	t.Parallel()
	c := reviewBase()
	c.Review = contract.Review{}
	if res := validation.Validate(c, nil); !res.OK() {
		t.Fatalf("a contract with no review block became invalid: %v", res.Error())
	}
}

// TestValidate_ReviewRules walks each field. The pin cases are the important
// ones: a tag, a branch, an abbreviated sha and an uppercase sha all LOOK like
// pins and are all pointers that can resolve to different code on two days.
func TestValidate_ReviewRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		mut   func(*contract.Contract)
		where string
	}{
		{"a tag is not a pin", func(c *contract.Contract) { c.Review.Ref = "v0.6.0" }, "/review/ref"},
		{"a branch is not a pin", func(c *contract.Contract) { c.Review.Ref = "main" }, "/review/ref"},
		{"an abbreviated sha is not a pin", func(c *contract.Contract) { c.Review.Ref = "0123456" }, "/review/ref"},
		{"an uppercase sha is not a pin", func(c *contract.Contract) { c.Review.Ref = strings.ToUpper("0123456789abcdef0123456789abcdef01234567") }, "/review/ref"},
		{"an empty ref is not a pin", func(c *contract.Contract) { c.Review.Ref = "" }, "/review/ref"},
		{"an unknown tier has no preset", func(c *contract.Contract) { c.Review.Tier = "archive" }, "/review/tier"},
		{"an empty tier has no preset", func(c *contract.Contract) { c.Review.Tier = "" }, "/review/tier"},
		{"a review job needs a pool", func(c *contract.Contract) { c.Review.RunsOn = "" }, "/review/runsOn"},
		{"a review job needs a limit", func(c *contract.Contract) { c.Review.TimeoutMinutes = 0 }, "/review/timeoutMinutes"},
		{"a negative limit is not a limit", func(c *contract.Contract) { c.Review.TimeoutMinutes = -1 }, "/review/timeoutMinutes"},
		{"minio needs an endpoint", func(c *contract.Contract) { c.Review.StreamEndpoint = "" }, "/review/streamEndpoint"},
		{"minio needs a bucket", func(c *contract.Contract) { c.Review.StreamBucket = "" }, "/review/streamBucket"},
		{"an unknown store is refused", func(c *contract.Contract) { c.Review.StreamStore = "s3" }, "/review/streamStore"},
		{"an empty store is refused", func(c *contract.Contract) { c.Review.StreamStore = "" }, "/review/streamStore"},
		{
			// Dead configuration reads as intent: a reader seeing an endpoint beside
			// `none` will believe something is archived.
			"an opt-out carries no settings",
			func(c *contract.Contract) { c.Review.StreamStore = contract.ReviewStreamNone },
			"/review/streamStore",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := reviewBase()
			tc.mut(c)
			res := validation.Validate(c, nil)
			if res.OK() {
				t.Fatalf("the validator accepted it; %s should have been reported", tc.where)
			}
			found := false
			for _, p := range res.Problems {
				if p.Where == tc.where {
					found = true
				}
			}
			if !found {
				t.Fatalf("no problem at %s; got %v", tc.where, res.Error())
			}
		})
	}
}
