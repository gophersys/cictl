package schema_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gophersys/eden/tools/cictl/internal/schema"
)

// TestEmit_IsValidJSON asserts the emitted schema parses as JSON and carries the
// draft-2020-12 marker and the apiVersion const derived from the contract.
func TestEmit_IsValidJSON(t *testing.T) {
	t.Parallel()
	doc, err := schema.Emit()
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(doc, &root); err != nil {
		t.Fatalf("emitted schema is not valid JSON: %v", err)
	}
	if got := root["$schema"]; got != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %v, want draft 2020-12", got)
	}
	props, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties object")
	}
	apiVersion, ok := props["apiVersion"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no apiVersion property")
	}
	if apiVersion["const"] != "eden.ci/v1" {
		t.Fatalf("apiVersion const = %v, want eden.ci/v1", apiVersion["const"])
	}
}

// TestEmit_RequiredTopLevel asserts every load-bearing top-level field is required.
func TestEmit_RequiredTopLevel(t *testing.T) {
	t.Parallel()
	doc, err := schema.Emit()
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(doc, &root); err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]bool{
		"apiVersion": false, "repo": false, "kind": false, "image": false,
		"languages": false, "tiers": false, "providers": false, "toolMatrix": false,
	}
	req, ok := root["required"].([]any)
	if !ok {
		t.Fatalf("schema has no required array")
	}
	for _, r := range req {
		if s, ok := r.(string); ok {
			want[s] = true
		}
	}
	for field, seen := range want {
		if !seen {
			t.Errorf("required field %q missing from schema's required list", field)
		}
	}
}

// TestEmit_Deterministic asserts two emits are byte-identical (the artifact must
// be reproducible so it can be committed and diffed).
func TestEmit_Deterministic(t *testing.T) {
	t.Parallel()
	a, err := schema.Emit()
	if err != nil {
		t.Fatalf("Emit a: %v", err)
	}
	b, err := schema.Emit()
	if err != nil {
		t.Fatalf("Emit b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("schema emit is not deterministic")
	}
	if !strings.HasSuffix(string(a), "\n") {
		t.Fatalf("schema must end with a single trailing newline")
	}
}
