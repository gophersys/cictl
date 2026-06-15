// Package schema emits the Eden CI contract's JSON Schema (draft 2020-12) by
// reflecting over the contract Go structs. The structs are the source; this
// package is the deterministic projection into the universal JSON-Schema
// artifact that the validator and any external tool consume.
package schema

import (
	"bytes"
	"encoding/json"

	"github.com/gophersys/eden/tools/cictl/internal/contract"
	"github.com/gophersys/eden/tools/cictl/internal/failure"
	"github.com/invopop/jsonschema"
)

// Emit reflects over contract.Contract and returns the JSON Schema as indented,
// newline-terminated bytes. The reflector is configured for a stable, inlined,
// draft-2020-12 schema so two runs over the same struct shape are byte-identical.
func Emit() ([]byte, error) {
	r := &jsonschema.Reflector{
		// Inline every definition rather than emitting $defs/$ref so the schema is
		// a single self-contained document the validator can compile from bytes.
		DoNotReference: true,
		// Treat every exported field as required unless it is explicitly omitempty,
		// matching the contract's "all fields load-bearing" intent.
		RequiredFromJSONSchemaTags: false,
		// Use the json tag for property names (the YAML and JSON tags agree).
		FieldNameTag: "json",
	}
	s := r.Reflect(&contract.Contract{})
	s.Version = "https://json-schema.org/draft/2020-12/schema"
	s.ID = "" // keep the artifact path-independent / reproducible across machines

	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, failure.Wrap(err, "marshal schema")
	}
	// Normalise to a single trailing newline for a stable on-disk artifact.
	out := bytes.TrimRight(raw, "\n")
	out = append(out, '\n')
	return out, nil
}
