package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestNormalize_TypeArrayIsIdempotent extends the idempotency guard to the
// new type-array rewrite. `make codegen` runs the transform on a fresh copy,
// but chained or repeated invocations must converge: the union rewrite is
// self-limiting because it renames its key, whereas this one leaves `type:`
// in place and APPENDS `nullable`, so a second pass that still matched would
// grow the mapping every time.
func TestNormalize_TypeArrayIsIdempotent(t *testing.T) {
	const in = `
answers:
  type: [array, 'null']
  items: { $ref: "#/components/schemas/Answer" }
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil {
		t.Fatalf("parse input: %v", err)
	}

	if n := normalize(&doc); n != 1 {
		t.Fatalf("first pass rewrote %d; want 1", n)
	}
	first, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal after first pass: %v", err)
	}

	if n := normalize(&doc); n != 0 {
		t.Errorf("second pass rewrote %d type array(s); want 0", n)
	}
	second, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal after second pass: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("second pass changed the document:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if got := strings.Count(string(second), "nullable"); got != 1 {
		t.Errorf("document carries %d nullable keys after two passes; want exactly 1:\n%s", got, second)
	}
}

// TestRewriteNullableTypeArray_LeavesUnrecognisedShapesAlone covers the
// bail-outs. The transform's contract is that anything it does not fully
// understand reaches codegen untouched, so codegen fails loudly rather than
// this tool silently emitting a schema that generates the wrong Go type.
func TestRewriteNullableTypeArray_LeavesUnrecognisedShapesAlone(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantCount int
	}{
		{
			// No null member: this is a genuine multi-type union with no 3.0
			// spelling and nothing to strip. Collapsing it would have to pick
			// a winner.
			name:      "type array without null is untouched",
			in:        "x:\n  type: [string, integer]\n",
			wantCount: 0,
		},
		{
			// A non-scalar member means this isn't the simple type-array
			// spelling at all (someone has nested a schema under `type:`).
			// Whatever it is, it is not ours to rewrite.
			name:      "type array with a mapping member is untouched",
			in:        "x:\n  type: [{ $ref: \"#/components/schemas/Y\" }, 'null']\n",
			wantCount: 0,
		},
		{
			// Only the surviving type may be duplicated away; `[array,
			// 'null', 'null']` leaves one real type, so it IS collapsible —
			// but three real types are not.
			name:      "three real types plus null is untouched",
			in:        "x:\n  type: [string, integer, boolean, 'null']\n",
			wantCount: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tc.in), &doc); err != nil {
				t.Fatalf("parse: %v", err)
			}
			before, err := yaml.Marshal(&doc)
			if err != nil {
				t.Fatalf("marshal before: %v", err)
			}
			if n := normalize(&doc); n != tc.wantCount {
				t.Errorf("normalize rewrote %d; want %d", n, tc.wantCount)
			}
			after, err := yaml.Marshal(&doc)
			if err != nil {
				t.Fatalf("marshal after: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("document was mutated:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if strings.Contains(string(after), "nullable") {
				t.Errorf("a bail-out path still stamped nullable on the mapping:\n%s", after)
			}
		})
	}
}

// TestNormalize_VendoredSpecLeavesNoTypeSequences runs the transform over the
// spec that is actually checked in, which is the assertion the unit cases
// cannot make: `make codegen` dies with "unhandled Schema type: &[array
// null]" on any type sequence that survives, and that failure surfaces at
// regeneration time — after the sync commit — rather than in CI here.
//
// It also asserts the transform still has work to do: if a future sync
// switched Booking.answers back to the 3.0 `nullable` idiom, the type-array
// rewrite would be dead code and this test would say so rather than passing
// silently.
func TestNormalize_VendoredSpecLeavesNoTypeSequences(t *testing.T) {
	data, err := os.ReadFile("../../api/inventory/cli-v1.yaml")
	if err != nil {
		t.Fatalf("read vendored spec: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse vendored spec: %v", err)
	}

	if before := countTypeSequences(&doc); before == 0 {
		t.Error("the vendored spec has no `type: [x, null]` mappings left; " +
			"rewriteNullableTypeArray is now dead code — delete it or re-check the sync")
	}

	if n := normalize(&doc); n == 0 {
		t.Error("normalize rewrote nothing in the vendored spec; the walk is broken")
	}

	if after := countTypeSequences(&doc); after != 0 {
		t.Errorf("%d `type:` sequence(s) survive normalization — codegen will fail with "+
			"\"unhandled Schema type\". A multi-type union has no 3.0 spelling and needs "+
			"a decision in the spec, not here.", after)
	}
}

// countTypeSequences counts mappings whose `type` key holds a sequence —
// the exact construct oapi-codegen cannot consume.
func countTypeSequences(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	count := 0
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind == yaml.ScalarNode && k.Value == "type" && v.Kind == yaml.SequenceNode {
				count++
			}
		}
	}
	for _, c := range n.Content {
		count += countTypeSequences(c)
	}
	return count
}
