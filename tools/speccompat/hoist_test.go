package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The construct rule 3 exists for: an array branch whose `items` is an inline
// schema carrying an enum, which oapi-codegen must name and then names
// identically to the branch itself.
const collidingParam = `
paths:
  /bookings:
    get:
      operationId: listBookings
      parameters:
        - name: include
          in: query
          schema:
            oneOf:
              - type: string
                enum: [resources]
              - type: array
                items:
                  type: string
                  enum: [resources]
components:
  schemas:
    Existing:
      type: string
`

func parse(t *testing.T, in string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(in), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &doc
}

func marshal(t *testing.T, doc *yaml.Node) string {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestHoist_MovesTheCollidingParamSchema is the happy path: the parameter ends
// up holding a $ref and the schema itself lands under components.schemas with a
// name derived from the operationId and the parameter name.
func TestHoist_MovesTheCollidingParamSchema(t *testing.T) {
	doc := parse(t, collidingParam)

	if n := hoistCollidingParamSchemas(doc); n != 1 {
		t.Fatalf("hoisted %d; want 1", n)
	}

	out := marshal(t, doc)
	if !strings.Contains(out, "#/components/schemas/ListBookingsIncludeParam") {
		t.Errorf("parameter does not reference the hoisted schema:\n%s", out)
	}
	if !strings.Contains(out, "ListBookingsIncludeParam:") {
		t.Errorf("hoisted parameter schema is not under components.schemas:\n%s", out)
	}
	// The item type must be named too. Without it codegen derives one name for
	// the array branch AND its items and reports a duplicate typename, so the
	// parameter hoist alone does not build.
	if !strings.Contains(out, "ListBookingsIncludeItem:") {
		t.Errorf("inline array items were not hoisted to their own schema:\n%s", out)
	}
	if !strings.Contains(out, "#/components/schemas/ListBookingsIncludeItem") {
		t.Errorf("array branch does not reference the hoisted item schema:\n%s", out)
	}
	// Both branches must survive. Collapsing is what this rule exists NOT to do:
	// the array spelling is part of the published contract.
	if got := strings.Count(out, "enum:"); got != 2 {
		t.Errorf("document carries %d enum keys; want 2 (both branches kept):\n%s", got, out)
	}
	if !strings.Contains(out, "Existing:") {
		t.Errorf("pre-existing component schema was lost:\n%s", out)
	}
}

// TestHoist_IsIdempotent mirrors TestNormalize_TypeArrayIsIdempotent. Rule 3 is
// the first rule that APPENDS a node rather than rewriting one in place, so a
// second pass that still matched would add another component on every run — and
// `make codegen-check` diffs the generated tree, so that would fail the gate at
// random.
func TestHoist_IsIdempotent(t *testing.T) {
	doc := parse(t, collidingParam)

	if n := hoistCollidingParamSchemas(doc); n != 1 {
		t.Fatalf("first pass hoisted %d; want 1", n)
	}
	first := marshal(t, doc)

	if n := hoistCollidingParamSchemas(doc); n != 0 {
		t.Errorf("second pass hoisted %d; want 0", n)
	}
	second := marshal(t, doc)

	if first != second {
		t.Errorf("second pass changed the document:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	for _, name := range []string{"ListBookingsIncludeParam:", "ListBookingsIncludeItem:"} {
		if got := strings.Count(second, name); got != 1 {
			t.Errorf("document carries %d %s after two passes; want 1:\n%s", got, name, second)
		}
	}
}

// TestHoist_IsDeterministic pins the requirement `make codegen-check` imposes:
// the same input must produce byte-identical output across independent runs. A
// name derived from map iteration order would pass the idempotency test above
// and still fail this one.
func TestHoist_IsDeterministic(t *testing.T) {
	runOnce := func() string {
		doc := parse(t, collidingParam)
		if n := hoistCollidingParamSchemas(doc); n != 1 {
			t.Fatalf("hoisted %d; want 1", n)
		}
		return marshal(t, doc)
	}
	first := runOnce()
	for i := 0; i < 5; i++ {
		if got := runOnce(); got != first {
			t.Fatalf("run %d differs from run 1:\nfirst:\n%s\ngot:\n%s", i+2, first, got)
		}
	}
}

// TestHoist_LeavesEverythingElseAlone is the F1 regression guard. Two unions in
// the real spec look like the colliding one and generate correct code today:
// bulk-update's product_option_id (primitive items, inlined as int) and
// Answer.answer_raw (items {} -> []interface{}). Flattening either would be a
// silent regression, so the document must come back byte-identical.
func TestHoist_LeavesEverythingElseAlone(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{
			// Items is a $ref, so codegen already has a name for it.
			name: "array branch with $ref items",
			in: "paths:\n  /x:\n    get:\n      operationId: getX\n      parameters:\n" +
				"        - name: ids\n          in: query\n          schema:\n            oneOf:\n" +
				"              - $ref: '#/components/schemas/Id'\n" +
				"              - type: array\n                items:\n                  $ref: '#/components/schemas/Id'\n",
		},
		{
			// Items is a bare primitive; codegen inlines it as int. This is
			// bulk-update's product_option_id shape.
			name: "array branch with primitive items",
			in: "paths:\n  /x:\n    get:\n      operationId: getX\n      parameters:\n" +
				"        - name: option\n          in: query\n          schema:\n            oneOf:\n" +
				"              - type: integer\n" +
				"              - type: array\n                items:\n                  type: integer\n",
		},
		{
			// Answer.answer_raw's shape, and not a parameter either.
			name: "open union in a component property",
			in: "components:\n  schemas:\n    Answer:\n      properties:\n        answer_raw:\n          oneOf:\n" +
				"            - type: string\n" +
				"            - type: array\n              items: {}\n" +
				"            - type: boolean\n",
		},
		{
			// The colliding shape, but in a request body rather than a
			// parameter. Rule 3 never walks request bodies.
			name: "colliding shape in a request body is out of reach",
			in: "paths:\n  /x:\n    post:\n      operationId: postX\n      requestBody:\n        content:\n" +
				"          application/json:\n            schema:\n              properties:\n                inc:\n                  oneOf:\n" +
				"                    - type: string\n                      enum: [a]\n" +
				"                    - type: array\n                      items:\n                        type: string\n                        enum: [a]\n",
		},
		{
			name: "plain scalar parameter",
			in: "paths:\n  /x:\n    get:\n      operationId: getX\n      parameters:\n" +
				"        - name: q\n          in: query\n          schema:\n            type: string\n",
		},
		{
			// A $ref parameter is skipped here; the component it points at is
			// visited on its own, so handling both would hoist twice.
			name: "$ref parameter",
			in: "paths:\n  /x:\n    get:\n      operationId: getX\n      parameters:\n" +
				"        - $ref: '#/components/parameters/Inc'\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := parse(t, tc.in)
			before := marshal(t, doc)
			if n := hoistCollidingParamSchemas(doc); n != 0 {
				t.Errorf("hoisted %d; want 0", n)
			}
			if after := marshal(t, doc); after != before {
				t.Errorf("document was mutated:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestHoist_LeavesAnchoredSchemasAlone mirrors the anchor guard on rule 2.
// components.schemas sits below paths in this spec, so relocating a subtree that
// owns an anchor can place that anchor after an alias pointing at it, and the
// emitted file then fails to re-parse at all.
func TestHoist_LeavesAnchoredSchemasAlone(t *testing.T) {
	const in = `
paths:
  /bookings:
    get:
      operationId: listBookings
      parameters:
        - name: include
          in: query
          schema: &incschema
            oneOf:
              - type: string
                enum: [resources]
              - type: array
                items:
                  type: string
                  enum: [resources]
    post:
      operationId: createBooking
      parameters:
        - name: include
          in: query
          schema: *incschema
`
	doc := parse(t, in)
	before := marshal(t, doc)

	if n := hoistCollidingParamSchemas(doc); n != 0 {
		t.Errorf("hoisted %d anchored schema(s); want 0", n)
	}
	if after := marshal(t, doc); after != before {
		t.Errorf("an anchored schema was mutated:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestHoist_HandlesComponentParametersAndNameCollisions covers the other place
// OpenAPI allows a parameter, and proves the derived name steps aside rather
// than overwriting an existing schema.
func TestHoist_HandlesComponentParametersAndNameCollisions(t *testing.T) {
	const in = `
components:
  parameters:
    Inc:
      name: include
      in: query
      schema:
        oneOf:
          - type: string
            enum: [resources]
          - type: array
            items:
              type: string
              enum: [resources]
  schemas:
    IncIncludeParam:
      type: string
`
	doc := parse(t, in)
	if n := hoistCollidingParamSchemas(doc); n != 1 {
		t.Fatalf("hoisted %d; want 1", n)
	}
	out := marshal(t, doc)

	// The derived name was taken, so the hoist must pick the next one and leave
	// the existing schema intact.
	if !strings.Contains(out, "IncIncludeParam2:") {
		t.Errorf("collision was not sidestepped:\n%s", out)
	}
	if !strings.Contains(out, "IncIncludeItem:") {
		t.Errorf("item schema was not hoisted for a components.parameters entry:\n%s", out)
	}
	if !strings.Contains(out, "IncIncludeParam:\n        type: string") &&
		!strings.Contains(out, "IncIncludeParam:\n            type: string") {
		t.Errorf("pre-existing IncIncludeParam was overwritten:\n%s", out)
	}
}

// TestHoist_RunsAfterNullableNormalization proves the ordering main() relies on:
// a union carrying BOTH a null member and a colliding array branch is first
// normalized by rule 1 (null stripped, nullable stamped) and only then hoisted,
// so the two rules do not race for the same mapping.
func TestHoist_RunsAfterNullableNormalization(t *testing.T) {
	const in = `
paths:
  /bookings:
    get:
      operationId: listBookings
      parameters:
        - name: include
          in: query
          schema:
            oneOf:
              - type: string
                enum: [resources]
              - type: array
                items:
                  type: string
                  enum: [resources]
              - type: 'null'
`
	doc := parse(t, in)

	if n := normalize(doc); n != 1 {
		t.Fatalf("normalize rewrote %d; want 1 (the null member)", n)
	}
	if n := hoistCollidingParamSchemas(doc); n != 1 {
		t.Fatalf("hoisted %d after normalization; want 1", n)
	}

	out := marshal(t, doc)
	if !strings.Contains(out, "nullable: true") {
		t.Errorf("nullability was lost across the two rules:\n%s", out)
	}
	if strings.Contains(out, "'null'") {
		t.Errorf("the null member survived:\n%s", out)
	}
	if !strings.Contains(out, "ListBookingsIncludeParam:") {
		t.Errorf("schema was not hoisted:\n%s", out)
	}
}

// TestHoist_VendoredSpecLeavesNoCollidingParamSchemas runs the rule over the
// spec that is actually checked in, which is the assertion the unit cases cannot
// make: `make codegen` fails with "duplicate typename" or emits a
// self-referential alias on any colliding parameter that survives, and that
// surfaces at regeneration time rather than in CI here.
//
// It also asserts the rule still has work to do. If a future sync gave the
// upstream `include` parameter a $ref'd item type, rule 3 would be dead code and
// this test says so rather than passing silently.
func TestHoist_VendoredSpecLeavesNoCollidingParamSchemas(t *testing.T) {
	data, err := os.ReadFile("../../api/inventory/cli-v1.yaml")
	if err != nil {
		t.Fatalf("read vendored spec: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse vendored spec: %v", err)
	}

	before := countCollidingParamSchemas(&doc)
	if before == 0 {
		t.Error("the vendored spec has no colliding parameter schemas left; " +
			"hoistCollidingParamSchemas is now dead code — delete it or re-check the sync")
	}

	normalize(&doc)
	if n := hoistCollidingParamSchemas(&doc); n != before {
		t.Errorf("hoisted %d of %d colliding parameter schema(s)", n, before)
	}

	if after := countCollidingParamSchemas(&doc); after != 0 {
		t.Errorf("%d colliding parameter schema(s) survive the rewrite — codegen will report "+
			"a duplicate typename or emit a self-referential alias", after)
	}

	// Idempotency on the real document, not just the fixture.
	if n := hoistCollidingParamSchemas(&doc); n != 0 {
		t.Errorf("a second pass over the vendored spec hoisted %d more; want 0", n)
	}
}

// countCollidingParamSchemas counts parameter schemas whose array branch carries
// inline enum items — the exact construct oapi-codegen cannot name twice.
func countCollidingParamSchemas(doc *yaml.Node) int {
	root := documentRoot(doc)
	if root == nil {
		return 0
	}
	count := 0
	for _, p := range eachParameter(root) {
		if s := mapValue(p.param, "schema"); s != nil && hasCollidingInlineArrayItems(s) {
			count++
		}
	}
	return count
}

// TestHoist_DetectorAndRewriterCannotDisagree pins the coupling between the two
// independent copies of the "colliding inline array items" predicate.
//
// hasCollidingInlineArrayItems decides WHICH parameters to hoist;
// hoistInlineEnumItems decides which item schemas to NAME. Each evaluates the
// same four clauses on its own. Should they diverge, a parameter schema moves to
// components.schemas with its item type still inline, the name collision travels
// with it, and codegen fails on a duplicate typename.
//
// The vendored-spec sweep cannot catch that: once the parameter holds a $ref,
// countCollidingParamSchemas scores 0, so the guard passes while `make codegen`
// breaks — the failure lands in the test job rather than the codegen gate, which
// is the diagnosis-in-the-wrong-place problem this package exists to avoid.
func TestHoist_DetectorAndRewriterCannotDisagree(t *testing.T) {
	// A parameter the detector matches. If the rewriter is ever narrowed so it
	// skips this shape, hoistCollidingParamSchemas must fail loudly rather than
	// emit a spec that only breaks later.
	const src = `
openapi: 3.1.0
paths:
  /things:
    get:
      operationId: listThings
      parameters:
        - name: include
          in: query
          schema:
            oneOf:
              - type: string
                enum: [resources]
              - type: array
                items:
                  type: string
                  enum: [resources]
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	// Sanity: the detector must match, or the fixture proves nothing.
	root := documentRoot(&doc)
	matched := 0
	for _, p := range eachParameter(root) {
		if s := mapValue(p.param, "schema"); s != nil && hasCollidingInlineArrayItems(s) {
			matched++
		}
	}
	if matched != 1 {
		t.Fatalf("detector matched %d parameters, want 1 — the fixture no longer exercises the predicate", matched)
	}

	if got := hoistCollidingParamSchemas(&doc); got != 1 {
		t.Fatalf("hoistCollidingParamSchemas = %d, want 1", got)
	}

	// Both halves must have happened: the parameter now holds a $ref, AND the
	// hoisted component's array branch holds a $ref to a named item type.
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "ListThingsIncludeParam") {
		t.Errorf("the parameter schema was not hoisted:\n%s", got)
	}
	if !strings.Contains(got, "ListThingsIncludeItem") {
		t.Errorf("the inline enum items were NOT named — this is the exact divergence that makes codegen fail on a duplicate typename:\n%s", got)
	}

	// And the result must be idempotent: a second pass has nothing left to do.
	if again := hoistCollidingParamSchemas(&doc); again != 0 {
		t.Errorf("second pass hoisted %d more — the rewrite does not converge", again)
	}
}

// TestNormalize_DropsRedundantNullEnumMember covers the second half of the
// nullable-union rewrite.
//
// A nullable enum spells its null TWICE: once in `type: [string, "null"]` and
// once as a member of `enum`. Collapsing the type array moves nullability onto
// `nullable: true`, which oapi-codegen expresses as a pointer — leaving the enum's
// own null member redundant. Left in place, codegen emits a constant named
// ...LessThannil whose value is the literal string "<nil>", and its Valid()
// method then reports "<nil>" as a legal enum value. cli-v1 1.29.0 produced 42 of
// those; 1.6.0 produced none, so the construct arrived with the sync.
//
// Dropping it is lossless: a JSON null unmarshals to a nil pointer, never to the
// string "<nil>".
func TestNormalize_DropsRedundantNullEnumMember(t *testing.T) {
	const src = `
openapi: 3.1.0
components:
  schemas:
    Thing:
      type: object
      properties:
        schedule_type:
          type: [string, "null"]
          enum: [date, datetime, null]
        # Not a nullable union, so the enum must be left exactly as written.
        plain:
          type: string
          enum: [a, b]
        # enum: [null] alone says "may only be null" — emptying it would change
        # the meaning, so it stays.
        only_null:
          type: [string, "null"]
          enum: [null]
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if n := normalize(&doc); n == 0 {
		t.Fatal("normalize rewrote nothing — the fixture no longer exercises the nullable path")
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	if strings.Contains(got, "enum: [date, datetime, null]") {
		t.Errorf("the redundant null enum member survived:\n%s", got)
	}
	if !strings.Contains(got, "date") || !strings.Contains(got, "datetime") {
		t.Errorf("real enum members were lost:\n%s", got)
	}
	// nullability must still be expressed, just once
	if !strings.Contains(got, "nullable: true") {
		t.Errorf("nullability was dropped entirely:\n%s", got)
	}
	// The non-nullable enum is untouched.
	if !strings.Contains(got, "- a") && !strings.Contains(got, "[a, b]") {
		t.Errorf("a plain enum was altered:\n%s", got)
	}
	// An enum that is ONLY null keeps its member: emptying it would say
	// something different.
	if !strings.Contains(got, "only_null") {
		t.Fatalf("fixture lost:\n%s", got)
	}
	onlyNull := got[strings.Index(got, "only_null"):]
	if !strings.Contains(onlyNull, "null") {
		t.Errorf("enum: [null] was emptied, which changes its meaning:\n%s", onlyNull)
	}
}

// TestNormalize_VendoredSpecHasNoNullEnumMembers sweeps the real spec, so a
// future upstream nullable enum cannot reintroduce the bogus constant silently.
func TestNormalize_VendoredSpecHasNoNullEnumMembers(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "inventory", "cli-v1.yaml"))
	if err != nil {
		t.Fatalf("read vendored spec: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse vendored spec: %v", err)
	}
	normalize(&doc)
	hoistCollidingParamSchemas(&doc)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Any `enum:` line still carrying a bare null after normalization would
	// become a ...LessThannil constant valued "<nil>".
	bad := 0
	for i, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "enum:") && !strings.HasPrefix(trimmed, "- null") {
			continue
		}
		if strings.Contains(trimmed, "null") && strings.HasPrefix(trimmed, "enum:") {
			t.Errorf("line %d after normalization still has a null enum member, which codegen renders as \"<nil>\": %s", i+1, trimmed)
			bad++
		}
	}
	if bad > 0 {
		t.Logf("%d null enum members survived normalization", bad)
	}
}
