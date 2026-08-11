// Command speccompat rewrites OpenAPI 3.1 nullable-ref constructs into the
// 3.0 form that oapi-codegen understands, emitting a throwaway spec copy for
// the codegen step only.
//
// Why this exists: api/inventory/cli-v1.yaml is vendored byte-identical from
// the server repo so `make codegen` and the spec-drift tests both read exactly
// what the API team publishes. Upstream authors nullable `$ref`s the 3.1 way:
//
//	trigger:
//	  oneOf:
//	    - $ref: "#/components/schemas/WorkflowStep"
//	    - { type: "null" }
//
// oapi-codegen has no 3.1 support (oapi-codegen/oapi-codegen#373) and dies on
// the `{type: "null"}` member with "unhandled Schema type: &[null]". The 3.0
// spelling of the same thing is:
//
//	trigger:
//	  allOf:
//	    - $ref: "#/components/schemas/WorkflowStep"
//	  nullable: true
//
// Both mean "WorkflowStep or null" and both generate `*WorkflowStep`, so the
// rewrite is semantics-preserving for our purposes.
//
// Scope is deliberately narrow: only `oneOf`/`anyOf` sequences containing a
// `type: "null"` member are touched. Everything else passes through untouched,
// so an unexpected upstream construct fails loudly in codegen rather than
// being silently mangled here.
//
// Usage: speccompat <input.yaml> <output.yaml>
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: speccompat <input.yaml> <output.yaml>")
		os.Exit(2)
	}
	in, out := os.Args[1], os.Args[2]

	raw, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "speccompat: reading %s: %v\n", in, err)
		os.Exit(1)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "speccompat: parsing %s: %v\n", in, err)
		os.Exit(1)
	}

	n := normalize(&doc)

	encoded, err := yaml.Marshal(&doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "speccompat: encoding: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, encoded, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "speccompat: writing %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "speccompat: rewrote %d nullable-union schema(s) to the 3.0 form\n", n)
}

// normalize walks the tree and rewrites every nullable union it finds,
// returning how many it rewrote.
func normalize(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	count := 0
	if n.Kind == yaml.MappingNode {
		count += rewriteNullableUnion(n)
	}
	for _, c := range n.Content {
		count += normalize(c)
	}
	return count
}

// rewriteNullableUnion converts `oneOf`/`anyOf` sequences that include a
// `{type: "null"}` member into the 3.0 `nullable: true` form on the mapping
// that owns them. A single surviving member collapses to `allOf` (which
// oapi-codegen resolves to the referenced type); multiple survivors keep their
// original union keyword, since dropping to allOf there would change meaning.
//
// Returns 1 if it rewrote this mapping, 0 otherwise.
//
// Collapsing a single-survivor union renames its key to `allOf`, which would
// produce a duplicate key — invalid YAML — if the mapping already carries an
// `allOf`, or carries both `oneOf` and `anyOf`. Both are pathological schemas,
// and silently emitting a broken document is the one outcome worse than not
// helping: the mapping is left untouched so codegen rejects it loudly.
func rewriteNullableUnion(m *yaml.Node) int {
	keyAt := func(name string) int {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == name {
				return i
			}
		}
		return -1
	}

	oneOfIdx, anyOfIdx := keyAt("oneOf"), keyAt("anyOf")
	if oneOfIdx >= 0 && anyOfIdx >= 0 {
		return 0 // both union keywords — can't collapse without colliding
	}
	i := oneOfIdx
	if i < 0 {
		i = anyOfIdx
	}
	if i < 0 {
		return 0
	}

	key, val := m.Content[i], m.Content[i+1]
	if val.Kind != yaml.SequenceNode {
		return 0
	}

	survivors := make([]*yaml.Node, 0, len(val.Content))
	sawNull := false
	for _, member := range val.Content {
		if isNullTypeSchema(member) {
			sawNull = true
			continue
		}
		survivors = append(survivors, member)
	}
	if !sawNull || len(survivors) == 0 {
		return 0
	}
	if len(survivors) == 1 && keyAt("allOf") >= 0 {
		return 0 // renaming would collide with an existing allOf
	}

	val.Content = survivors
	if len(survivors) == 1 {
		key.Value = "allOf"
	}
	setNullable(m)
	return 1
}

// isNullTypeSchema reports whether node is exactly `{type: "null"}` — the 3.1
// spelling of the null branch of a nullable union. A mapping carrying any
// other key (say `{type: "null", description: ...}`) is left alone so we never
// silently discard information.
//
// JSON Schema wants the quoted string, but YAML has three spellings that all
// reach the same place, and a spec author picking the wrong one shouldn't get a
// baffling codegen failure:
//
//	type: "null"   → !!str  "null"   (canonical)
//	type: null     → !!null "null"
//	type: ~        → !!null "~"
//
// All three are accepted, because `type:` holding a YAML null has no other
// possible meaning in OpenAPI. Anything else — including a `type: nullish`
// typo — is not our business and falls through to codegen.
func isNullTypeSchema(node *yaml.Node) bool {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return false
	}
	key, val := node.Content[0], node.Content[1]
	if key.Kind != yaml.ScalarNode || key.Value != "type" || val.Kind != yaml.ScalarNode {
		return false
	}
	return val.Tag == "!!null" || val.Value == "null"
}

// setNullable adds `nullable: true` to a mapping, or overwrites an existing
// nullable key.
func setNullable(m *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == "nullable" {
			m.Content[i+1].Kind = yaml.ScalarNode
			m.Content[i+1].Tag = "!!bool"
			m.Content[i+1].Value = "true"
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "nullable"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
	)
}
