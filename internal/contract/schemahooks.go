package contract

import (
	"bytes"

	"github.com/invopop/jsonschema"
)

// SchemaEnum is the schema fragment an enum type contributes to the emitted
// JSON Schema. It is the invopop schema type; aliasing it here keeps the
// jsonschema import out of contract.go's enum declarations and gives the enum
// JSONSchema() methods a house-local return type.
type SchemaEnum = jsonschema.Schema

// enumSchema builds a string-enum schema fragment from a typed slice of allowed
// values, in declaration order, with a human description. invopop calls each
// enum type's JSONSchema() method during reflection and inlines the result, so
// the closed set in the Go declaration is the single source of the schema enum
// — they cannot drift.
func enumSchema[T ~string](description string, values []T) *SchemaEnum {
	s := &SchemaEnum{Type: "string", Description: description}
	for _, v := range values {
		s.Enum = append(s.Enum, string(v))
	}
	return s
}

// JSONSchemaExtend is invoked by the invopop reflector after it builds the
// Contract schema, letting us pin constraints the struct tags cannot express.
// Here it nails apiVersion to the const APIVersion value — derived from the same
// constant the validator checks, so the schema and the semantic rule cannot
// drift. This keeps the apiVersion's allowed value in exactly one home.
func (Contract) JSONSchemaExtend(s *SchemaEnum) {
	if s.Properties == nil {
		return
	}
	if av, ok := s.Properties.Get("apiVersion"); ok {
		av.Const = APIVersion
	}
}

// bytesReader adapts a byte slice to an io.Reader for the strict YAML decoder
// without pulling another import into contract.go.
func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
