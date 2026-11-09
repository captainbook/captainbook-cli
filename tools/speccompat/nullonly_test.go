package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Rule 4: a schema whose type is NULL AND NOTHING ELSE.
//
// 1.56.0's `POST /bookings` describes its dry-run branch with
// `booking: {type: ["null"], description: "Always null on a dry run."}`, which
// is legal 3.1 and which oapi-codegen rejects with the same "unhandled Schema
// type: &[null]" that rule 1 exists to prevent. rewriteNullableTypeArray
// declines it on purpose — it collapses `[T, "null"]` to T and there is no T —
// so this is its own pass.
//
// Value: protects=a null-only type array is rewritten to an untyped `nullable: true` so codegen can parse it; fails_when=the rule stops firing (codegen dies on the vendored spec again) or starts firing on a bare union branch or a multi-type array, which would silently erase a type; why_new=the construct is new in this spec sync and the existing type-array rule documents that it skips it; seam=none

// TestRewriteNullOnlyType_DropsTheTypeAndMarksNullable is the happy path: the
// `type` key goes, `nullable: true` arrives, and every sibling key survives —
// `description` above all, which is the only thing that says WHY the field is
// always null.
func TestRewriteNullOnlyType_DropsTheTypeAndMarksNullable(t *testing.T) {
	const in = `
booking:
  type: ["null"]
  description: Always null on a dry run.
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil {
		t.Fatalf("parse input: %v", err)
	}
	if n := normalize(&doc); n != 1 {
		t.Fatalf("normalize rewrote %d schema(s); want 1", n)
	}
	out := marshal(t, &doc)
	if strings.Contains(out, "type:") {
		t.Errorf("the null-only type survived; codegen will still reject it:\n%s", out)
	}
	if !strings.Contains(out, "nullable: true") {
		t.Errorf("nullable: true was not added:\n%s", out)
	}
	if !strings.Contains(out, "Always null on a dry run.") {
		t.Errorf("the description was dropped — the only thing explaining the null:\n%s", out)
	}
}

// TestRewriteNullOnlyType_IsIdempotent. `make codegen` runs the transform once
// on a fresh copy, but a pass that leaves a matchable shape behind would grow
// the mapping on every repeat — the same trap TestNormalize_TypeArrayIsIdempotent
// guards for rule 2. Here convergence comes from the `type` key being REMOVED,
// so there is nothing left to match.
func TestRewriteNullOnlyType_IsIdempotent(t *testing.T) {
	const in = `
payment_url:
  type: ["null"]
  description: Always null on a dry run.
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil {
		t.Fatalf("parse input: %v", err)
	}
	if n := normalize(&doc); n != 1 {
		t.Fatalf("first pass rewrote %d; want 1", n)
	}
	first := marshal(t, &doc)
	if n := normalize(&doc); n != 0 {
		t.Errorf("second pass rewrote %d schema(s); want 0", n)
	}
	second := marshal(t, &doc)
	if first != second {
		t.Errorf("second pass changed the document:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if got := strings.Count(second, "nullable"); got != 1 {
		t.Errorf("document carries %d nullable keys after two passes; want exactly 1:\n%s", got, second)
	}
}

// TestRewriteNullOnlyType_LeavesEverythingElseAlone covers the bail-outs. The
// contract of this package is that anything it does not fully understand
// reaches codegen untouched, so codegen fails loudly rather than speccompat
// silently emitting a schema that generates the wrong Go type.
func TestRewriteNullOnlyType_LeavesEverythingElseAlone(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int // expected normalize() count
		why  string
	}{
		{
			name: "a bare union branch belongs to rule 1",
			// A mapping whose only key is `type` is a union member.
			// rewriteNullableUnion consumes it from the parent; this rule must
			// not reach in and turn the branch into an untyped schema, because
			// where rule 1 DECLINED a union (a pathological oneOf+anyOf pair)
			// codegen should still reject it rather than have the branch
			// quietly erased.
			in:   "trigger:\n  oneOf:\n    - type: \"null\"\n",
			want: 0,
			why:  "the lone-key mapping is rule 1's business",
		},
		{
			name: "a nullable type array belongs to rule 2",
			in:   "answers:\n  type: [array, 'null']\n  items: { type: string }\n",
			want: 1, // rewritten by rule 2, which keeps `type: array`
			why:  "rule 2 collapses it and leaves a real type behind",
		},
		{
			name: "a multi-type union has no 3.0 spelling",
			in:   "weird:\n  type: [string, integer, \"null\"]\n  description: x\n",
			want: 0,
			why:  "picking a winner would generate the wrong Go type",
		},
		{
			name: "an example payload is data, not schema",
			// normalizeAt refuses to descend into data blocks, and this is the
			// case that makes the position tracking necessary rather than
			// nice: `type` is an ordinary word inside an example.
			in:   "example:\n  type: [\"null\"]\n  description: x\n",
			want: 0,
			why:  "rewriting it would stamp a fabricated nullable into a payload",
		},
		{
			name: "an anchored sequence must not be spliced out",
			in:   "a:\n  type: &nullonly [\"null\"]\n  description: x\nb:\n  type: *nullonly\n",
			want: 0,
			why:  "swapping it orphans the alias and the file stops parsing",
		},
		{
			name: "an empty type sequence names nothing",
			in:   "a:\n  type: []\n  description: x\n",
			want: 0,
			why:  "there is no null to act on",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tc.in), &doc); err != nil {
				t.Fatalf("parse input: %v", err)
			}
			if n := normalize(&doc); n != tc.want {
				t.Errorf("normalize() = %d, want %d (%s)\n%s", n, tc.want, tc.why, marshal(t, &doc))
			}
		})
	}
}

// TestRewriteNullOnlyType_VendoredSpecCarriesNoneAfterNormalize is the pin that
// actually matters: it reads the REAL spec rather than a fixture, so it fails
// the day upstream adds a null-only type in a shape the rule does not reach —
// which is exactly how this construct arrived in the first place, as a codegen
// error nothing in the suite predicted.
func TestRewriteNullOnlyType_VendoredSpecCarriesNoneAfterNormalize(t *testing.T) {
	raw, err := os.ReadFile("../../api/inventory/cli-v1.yaml")
	if err != nil {
		t.Fatalf("read vendored spec: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse vendored spec: %v", err)
	}

	before := countNullOnlyTypes(&doc)
	if before == 0 {
		t.Skip("the vendored spec carries no null-only type array; the rule has nothing to prove here")
	}
	normalize(&doc)
	if after := countNullOnlyTypes(&doc); after != 0 {
		t.Errorf("%d null-only type array(s) survived normalize (of %d); codegen will reject the spec",
			after, before)
	}
}

// countNullOnlyTypes counts `type:` sequences holding nothing but null,
// wherever they appear. Deliberately NOT position-aware: this is the detector
// the rewriter's own guards are measured against, so it must be able to see a
// construct the rewriter declines — that disagreement is the finding, not a
// false positive.
func countNullOnlyTypes(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	count := 0
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Value != "type" || val.Kind != yaml.SequenceNode {
				continue
			}
			if len(val.Content) == 0 {
				continue
			}
			allNull := true
			for _, m := range val.Content {
				if m.Kind != yaml.ScalarNode || (m.Tag != "!!null" && m.Value != "null") {
					allNull = false
					break
				}
			}
			if allNull {
				count++
			}
		}
	}
	for _, c := range n.Content {
		count += countNullOnlyTypes(c)
	}
	return count
}
