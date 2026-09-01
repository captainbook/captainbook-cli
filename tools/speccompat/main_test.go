package main

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestNormalize covers the transform that keeps `make codegen` working against
// an OpenAPI 3.1 spec. This tool sits upstream of the entire generated API
// client, so a bug here doesn't fail loudly — it silently emits wrong Go types
// for every endpoint. Each case below is a shape the vendored spec either has
// today or could grow when the API team adds another nullable field.
func TestNormalize(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		want      string
		wantCount int
	}{
		{
			// The exact shape in cli-v1.yaml today (Workflow.trigger,
			// WorkflowExecution.failed_log). oapi-codegen dies on the null
			// member; allOf + nullable generates the same *WorkflowStep.
			name: "oneOf ref plus null collapses to allOf + nullable",
			in: `
trigger:
  oneOf:
    - $ref: "#/components/schemas/WorkflowStep"
    - { type: "null" }
  description: Singleton TRIGGER step.
`,
			want: `
trigger:
  allOf:
    - $ref: "#/components/schemas/WorkflowStep"
  description: Singleton TRIGGER step.
  nullable: true
`,
			wantCount: 1,
		},
		{
			// anyOf is the other legal 3.1 spelling of the same thing.
			name: "anyOf ref plus null collapses the same way",
			in: `
failed_log:
  anyOf:
    - $ref: "#/components/schemas/WorkflowExecutionLog"
    - { type: "null" }
`,
			want: `
failed_log:
  allOf:
    - $ref: "#/components/schemas/WorkflowExecutionLog"
  nullable: true
`,
			wantCount: 1,
		},
		{
			// Two real branches + null. Collapsing to allOf here would mean
			// "A AND B" instead of "A OR B" — a silent semantic change. Keep
			// the union keyword, only strip the null member.
			name: "multi-survivor union keeps oneOf and only drops null",
			in: `
payload:
  oneOf:
    - $ref: "#/components/schemas/Booking"
    - $ref: "#/components/schemas/Availability"
    - { type: "null" }
`,
			want: `
payload:
  oneOf:
    - $ref: "#/components/schemas/Booking"
    - $ref: "#/components/schemas/Availability"
  nullable: true
`,
			wantCount: 1,
		},
		{
			// A union with no null member is a plain 3.0-legal discriminated
			// union. Touching it would corrupt a spec oapi-codegen already
			// handles.
			name: "union without a null member is left untouched",
			in: `
payload:
  oneOf:
    - $ref: "#/components/schemas/Booking"
    - $ref: "#/components/schemas/Availability"
`,
			want: `
payload:
  oneOf:
    - $ref: "#/components/schemas/Booking"
    - $ref: "#/components/schemas/Availability"
`,
			wantCount: 0,
		},
		{
			// A null member carrying extra keys is documentation we'd be
			// throwing away. Narrow-scope rule: don't touch it, let codegen
			// fail loudly so a human looks.
			name: "annotated null member is not swallowed",
			in: `
trigger:
  oneOf:
    - $ref: "#/components/schemas/WorkflowStep"
    - { type: "null", description: "explicitly cleared" }
`,
			want: `
trigger:
  oneOf:
    - $ref: "#/components/schemas/WorkflowStep"
    - { type: "null", description: "explicitly cleared" }
`,
			wantCount: 0,
		},
		{
			// A hand-written `nullable: false` alongside a null union member
			// is contradictory; the union wins.
			name: "existing nullable key is overwritten rather than duplicated",
			in: `
trigger:
  oneOf:
    - $ref: "#/components/schemas/WorkflowStep"
    - { type: "null" }
  nullable: false
`,
			want: `
trigger:
  allOf:
    - $ref: "#/components/schemas/WorkflowStep"
  nullable: true
`,
			wantCount: 1,
		},
		{
			// Degenerate: nothing but null. Rewriting would emit an empty
			// allOf, which is worse than leaving it for codegen to reject.
			name: "null-only union is left untouched",
			in: `
trigger:
  oneOf:
    - { type: "null" }
`,
			want: `
trigger:
  oneOf:
    - { type: "null" }
`,
			wantCount: 0,
		},
		{
			// Collapsing both would rename both keys to allOf — a duplicate
			// mapping key, i.e. invalid YAML. Emitting a broken spec is worse
			// than not helping, so leave it for codegen to reject.
			name: "mapping with both union keywords is left untouched",
			in: `
weird:
  oneOf:
    - $ref: "#/components/schemas/A"
    - { type: "null" }
  anyOf:
    - $ref: "#/components/schemas/B"
    - { type: "null" }
`,
			want: `
weird:
  oneOf:
    - $ref: "#/components/schemas/A"
    - { type: "null" }
  anyOf:
    - $ref: "#/components/schemas/B"
    - { type: "null" }
`,
			wantCount: 0,
		},
		{
			// Same collision, likelier shape: a schema that already composes
			// with allOf and adds a nullable ref alongside it.
			name: "single-survivor collapse that would collide with an existing allOf is skipped",
			in: `
mixed:
  allOf:
    - $ref: "#/components/schemas/Base"
  oneOf:
    - $ref: "#/components/schemas/A"
    - { type: "null" }
`,
			want: `
mixed:
  allOf:
    - $ref: "#/components/schemas/Base"
  oneOf:
    - $ref: "#/components/schemas/A"
    - { type: "null" }
`,
			wantCount: 0,
		},
		{
			// The collision guard is specific to the single-survivor rename.
			// A multi-survivor union keeps its own keyword, so an existing
			// allOf is no obstacle and the null member is still stripped.
			name: "multi-survivor union alongside allOf still drops null",
			in: `
mixed:
  allOf:
    - $ref: "#/components/schemas/Base"
  oneOf:
    - $ref: "#/components/schemas/A"
    - $ref: "#/components/schemas/B"
    - { type: "null" }
`,
			want: `
mixed:
  allOf:
    - $ref: "#/components/schemas/Base"
  oneOf:
    - $ref: "#/components/schemas/A"
    - $ref: "#/components/schemas/B"
  nullable: true
`,
			wantCount: 1,
		},
		{
			// The 3.1 type-array spelling, and the exact shape in
			// cli-v1.yaml today (Booking.answers). `nullable` is not a
			// keyword in 3.1, so the API team spells nullability this way
			// on fields that genuinely return null; codegen dies on the
			// sequence with "unhandled Schema type: &[array null]".
			name: "type array plus null collapses to the scalar type + nullable",
			in: `
answers:
  type: [array, 'null']
  items: { $ref: "#/components/schemas/Answer" }
`,
			want: `
answers:
  type: array
  items: { $ref: "#/components/schemas/Answer" }
  nullable: true
`,
			wantCount: 1,
		},
		{
			// Same rewrite on a scalar, the likelier shape the next
			// nullable field will arrive in.
			name: "type array works for scalars too",
			in: `
label:
  type: [string, 'null']
`,
			want: `
label:
  type: string
  nullable: true
`,
			wantCount: 1,
		},
		{
			// A genuine multi-type union has no 3.0 spelling. Rewriting it
			// would have to pick a winner and silently generate the wrong Go
			// type for every consumer, so it is left for codegen to reject.
			name: "multi-type union with null is left untouched",
			in: `
answer_raw:
  type: [string, integer, 'null']
`,
			want: `
answer_raw:
  type: [string, integer, 'null']
`,
			wantCount: 0,
		},
		{
			// Degenerate: nothing survives to name a type with.
			name: "null-only type array is left untouched",
			in: `
cleared:
  type: ['null']
`,
			want: `
cleared:
  type: ['null']
`,
			wantCount: 0,
		},
		{
			// A plain scalar type is the overwhelmingly common case and must
			// pass through without acquiring a spurious nullable.
			name: "plain scalar type is left untouched",
			in: `
label:
  type: string
`,
			want: `
label:
  type: string
`,
			wantCount: 0,
		},
		{
			// A `type:` key is only a SCHEMA keyword when its value names a
			// JSON Schema type. Under an `example:` it is ordinary data, and
			// rewriting it would both collapse the sequence and inject a
			// fabricated `nullable: true` into the operator's example — the
			// exact silent mangling this package promises never to do. Not
			// contrived for this spec: `granularity` is enum [booking, guest,
			// extra], so `[guest, null]` is a shape upstream could write.
			name: "type sequence under example: is data, not a schema keyword",
			in: `
example:
  type: [guest, 'null']
  name: bob
`,
			want: `
example:
  type: [guest, 'null']
  name: bob
`,
			wantCount: 0,
		},
		{
			name: "type sequence naming a non-schema value is left alone",
			in: `
default:
  type: [booking, 'null']
`,
			want: `
default:
  type: [booking, 'null']
`,
			wantCount: 0,
		},
		{
			// Every JSON Schema type name must still rewrite — the guard
			// must not be so tight that it breaks the real case.
			name: "every JSON Schema type name still rewrites",
			in: `
a: {type: [string, 'null']}
b: {type: [number, 'null']}
c: {type: [integer, 'null']}
d: {type: [boolean, 'null']}
e: {type: [array, 'null']}
f: {type: [object, 'null']}
`,
			want: `
a: {type: string, nullable: true}
b: {type: number, nullable: true}
c: {type: integer, nullable: true}
d: {type: boolean, nullable: true}
e: {type: array, nullable: true}
f: {type: object, nullable: true}
`,
			wantCount: 6,
		},
		{
			// POSITION, not value. `number` is a JSON Schema type name AND a
			// legal value of this spec's own Answer.type / Question.type
			// enums, so a value filter cannot see that this is data. Only
			// the enclosing `example:` key can. This is the case the
			// value-only guard missed.
			name: "schema-NAME data value under example: is still left alone",
			in: `
Answer:
  example:
    id: a_1
    type: [number, 'null']
`,
			want: `
Answer:
  example:
    id: a_1
    type: [number, 'null']
`,
			wantCount: 0,
		},
		{
			// Same for the union form, which previously had no guard at all.
			name: "oneOf under example: is data too",
			in: `
Booking:
  example:
    trigger:
      oneOf:
        - $ref: "#/components/schemas/WorkflowStep"
        - { type: "null" }
`,
			want: `
Booking:
  example:
    trigger:
      oneOf:
        - $ref: "#/components/schemas/WorkflowStep"
        - { type: "null" }
`,
			wantCount: 0,
		},
		{
			name: "default: and x- extensions are data as well",
			in: `
a:
  default: {type: [array, 'null']}
b:
  x-vendor: {type: [string, 'null']}
`,
			want: `
a:
  default: {type: [array, 'null']}
b:
  x-vendor: {type: [string, 'null']}
`,
			wantCount: 0,
		},
		{
			// Swapping the node out orphans every alias pointing at it, and
			// the emitted document then fails to re-parse entirely with
			// "unknown anchor" — worse than not helping.
			name: "anchored type sequence is left alone rather than orphaning its aliases",
			in: `
a:
  type: &tt [array, 'null']
b:
  type: *tt
`,
			want: `
a:
  type: &tt [array, 'null']
b:
  type: *tt
`,
			wantCount: 0,
		},
		{
			// The real spec nests these many levels down under
			// components.schemas.X.properties.y — the walk has to recurse
			// through mappings AND sequences to reach them.
			name: "deeply nested unions are found through mappings and sequences",
			in: `
components:
  schemas:
    Workflow:
      properties:
        trigger:
          oneOf:
            - $ref: "#/components/schemas/WorkflowStep"
            - { type: "null" }
    Bundle:
      allOf:
        - properties:
            log:
              oneOf:
                - $ref: "#/components/schemas/WorkflowExecutionLog"
                - { type: "null" }
`,
			want: `
components:
  schemas:
    Workflow:
      properties:
        trigger:
          allOf:
            - $ref: "#/components/schemas/WorkflowStep"
          nullable: true
    Bundle:
      allOf:
        - properties:
            log:
              allOf:
                - $ref: "#/components/schemas/WorkflowExecutionLog"
              nullable: true
`,
			wantCount: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tc.in), &doc); err != nil {
				t.Fatalf("parse input: %v", err)
			}

			got := normalize(&doc)
			if got != tc.wantCount {
				t.Errorf("normalize rewrote %d union(s); want %d", got, tc.wantCount)
			}

			// Compare semantically, not byte-wise: the tool's output is a
			// build artifact and yaml.Marshal is free to reflow it.
			assertSameYAML(t, &doc, tc.want)
		})
	}
}

// TestNormalizeIsIdempotent guards the re-run path. `make codegen` runs the
// transform on a fresh copy every time, but a second pass over already-rewritten
// output must be a no-op — if it weren't, chained invocations would keep
// mutating the schema.
func TestNormalizeIsIdempotent(t *testing.T) {
	const in = `
trigger:
  oneOf:
    - $ref: "#/components/schemas/WorkflowStep"
    - { type: "null" }
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
		t.Errorf("second pass rewrote %d union(s); want 0 (transform must be idempotent)", n)
	}
	second, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal after second pass: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("second pass changed the document:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestIsNullTypeSchema covers the predicate directly — it's the guard that
// decides what gets dropped, so its false cases matter as much as its true one.
func TestIsNullTypeSchema(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// All three YAML spellings of null are accepted — see the doc comment
		// on isNullTypeSchema. Accepting only the canonical quoted form would
		// mean `type: ~` silently failed codegen with an opaque error.
		{"canonical quoted null", `{ type: "null" }`, true},
		{"unquoted null", `{ type: null }`, true},
		{"tilde null", `{ type: ~ }`, true},
		{"a real ref", `{ $ref: "#/components/schemas/X" }`, false},
		{"null with extra keys", `{ type: "null", description: x }`, false},
		{"a string type", `{ type: string }`, false},
		{"a near-miss typo", `{ type: nullish }`, false},
		{"a sequence", `[1, 2]`, false},
		{"a scalar", `hello`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tc.in), &doc); err != nil {
				t.Fatalf("parse: %v", err)
			}
			// doc is a DocumentNode; the schema under test is its only child.
			if len(doc.Content) != 1 {
				t.Fatalf("expected one root node, got %d", len(doc.Content))
			}
			if got := isNullTypeSchema(doc.Content[0]); got != tc.want {
				t.Errorf("isNullTypeSchema(%s) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// assertSameYAML compares a node tree against expected YAML by value, so the
// assertion survives any reflowing yaml.Marshal does.
func assertSameYAML(t *testing.T, got *yaml.Node, wantSrc string) {
	t.Helper()

	gotBytes, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var gotVal, wantVal interface{}
	if err := yaml.Unmarshal(gotBytes, &gotVal); err != nil {
		t.Fatalf("re-parse result: %v", err)
	}
	if err := yaml.Unmarshal([]byte(wantSrc), &wantVal); err != nil {
		t.Fatalf("parse expected: %v", err)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Errorf("result mismatch:\ngot:\n%s\nwant:\n%s", gotBytes, wantSrc)
	}
}
