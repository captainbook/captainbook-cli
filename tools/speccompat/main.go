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
// The same idea has a second 3.1 spelling — a type *array* rather than a
// union of subschemas (Booking.answers uses it):
//
//	answers:
//	  type: [array, 'null']
//	  items: { $ref: "#/components/schemas/Answer" }
//
// which codegen rejects with "unhandled Schema type: &[array null]". The 3.0
// equivalent is `type: array` + `nullable: true`.
//
// A THIRD construct needs help, and it is not a nullability problem at all.
// `GET /bookings` spells its `include` parameter as "one value or a list of
// them":
//
//	include:
//	  oneOf:
//	    - { type: string, enum: [resources] }
//	    - { type: array, items: { type: string, enum: [resources] } }
//
// oapi-codegen names the array branch from the parameter (`...Include1`) and
// then needs a name for its ITEM type too. The items schema is inline and
// carries an enum, so it must become a named Go type — and codegen derives the
// SAME name for it, emitting
//
//	type ListBookingsParamsInclude1 = []ListBookingsParamsInclude1
//	type ListBookingsParamsInclude1 string
//
// a self-referential alias plus a redeclaration. The package does not compile,
// and `make codegen` still exits 0, so the breakage surfaces later as a
// confusing build failure.
//
// What matters is the ITEMS shape, not the "scalar or array" shape. Two other
// unions in this spec look similar and generate perfectly good code:
// `BulkUpdateAvailabilityRequest.product_option_id` (items is a bare
// `{type: integer}`, which codegen inlines as `int`) and `Answer.answer_raw`
// (`items: {}` → `[]interface{}`). Collapsing either would be a silent
// regression — the first would lose its documented list form, the second is a
// deliberately open union for an any-JSON column. So this rewrite does NOT
// collapse anything, and it only ever looks at PARAMETER schemas, which is
// where the colliding construct lives. Both of those are request-body /
// component-property schemas and are never visited.
//
// The fix hoists the whole parameter schema into `components.schemas` and
// leaves a `$ref` behind. Codegen then derives the item name from the new
// component instead of from the parameter, the collision disappears, and both
// branches survive: the generated field becomes the named union
// `*ListBookingsIncludeParam`, with As/From helpers, so either wire spelling is
// still expressible and the item enum keeps its own named type.
//
// Both hoists are needed, and each fixes a different failure. Naming the ITEMS
// removes the collision. Naming the PARAMETER SCHEMA makes the result usable: a
// union left inline on a parameter generates an anonymous
// `struct{ union json.RawMessage }` whose field is unexported and which, being
// anonymous, cannot carry the As/From helpers either, so no other package can
// set it. Hoisting only the parameter schema fails outright — the collision
// moves with it and codegen reports a duplicate typename.
// See hoistCollidingParamSchemas.
//
// Scope is deliberately narrow: only `oneOf`/`anyOf` sequences containing a
// `type: "null"` member, `type:` sequences of exactly one real type plus
// null, and parameter schemas whose array branch has inline enum items are
// touched. Everything else passes through untouched, so an
// unexpected upstream construct fails loudly in codegen rather than being
// silently mangled here.
//
// Usage: speccompat <input.yaml> <output.yaml>
package main

import (
	"fmt"
	"os"
	"strings"

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
	// Runs after normalize so a parameter whose union also carried a
	// `{type: "null"}` member has already been through rewriteNullableUnion;
	// this pass then sees whatever survived rather than racing it.
	h := hoistCollidingParamSchemas(&doc)

	encoded, err := yaml.Marshal(&doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "speccompat: encoding: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, encoded, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "speccompat: writing %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "speccompat: rewrote %d nullable-union schema(s) to the 3.0 form, "+
		"hoisted %d colliding parameter schema(s)\n", n, h)
}

// normalize walks the tree and rewrites every nullable union it finds,
// returning how many it rewrote.
func normalize(n *yaml.Node) int {
	return normalizeAt(n, false)
}

// normalizeAt is normalize plus the one piece of context the rewrites cannot
// recover on their own: whether this subtree is SCHEMA or DATA.
//
// `type`, `oneOf` and `anyOf` are schema keywords, but they are also ordinary
// words that appear as payload inside `example:`, `default:` and `x-` blocks.
// Filtering on the value is not enough to tell them apart: this spec's own
// `Answer.type` / `Question.type` enums include `number` and `boolean`, which
// are simultaneously legal data values AND JSON Schema type names. So
// `example: {type: [number, 'null']}` is indistinguishable from a real schema
// by value alone — and rewriting it would collapse the sequence and stamp a
// fabricated `nullable: true` into somebody's example payload.
//
// Position is the only thing that separates them, so it is tracked here rather
// than guessed at in the rewrites.
func normalizeAt(n *yaml.Node, inData bool) int {
	if n == nil {
		return 0
	}
	count := 0
	if n.Kind == yaml.MappingNode {
		if !inData {
			count += rewriteNullableUnion(n)
			count += rewriteNullableTypeArray(n)
		}
		// Descend key by key so a data block can be recognised by the key
		// that introduces it. A sequence or scalar just inherits the flag.
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			childInData := inData
			if key.Kind == yaml.ScalarNode && isDataBlockKey(key.Value) {
				childInData = true
			}
			count += normalizeAt(key, inData)
			count += normalizeAt(val, childInData)
		}
		return count
	}
	for _, c := range n.Content {
		count += normalizeAt(c, inData)
	}
	return count
}

// isDataBlockKey reports whether a mapping key introduces a subtree of DATA
// rather than schema. `example` / `examples` / `default` hold payload shaped
// like the thing being described, and `x-` extensions are vendor-defined and
// off-limits by definition.
func isDataBlockKey(k string) bool {
	switch k {
	case "example", "examples", "default", "enum":
		return true
	}
	return strings.HasPrefix(k, "x-")
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

// rewriteNullableTypeArray converts the 3.1 type-array spelling of a nullable
// scalar/array/object — `type: [array, "null"]` — into the 3.0 pair
// `type: array` + `nullable: true`.
//
// Returns 1 if it rewrote this mapping, 0 otherwise.
//
// Only a sequence of exactly one real type plus null is collapsed. A genuine
// multi-type union (`type: [string, integer, "null"]`) has no 3.0 spelling, so
// rewriting it would have to pick a winner and silently generate the wrong Go
// type for every consumer; it is left alone for codegen to reject. Likewise a
// lone `type: ["null"]` — nothing survives to name a type with.
func rewriteNullableTypeArray(m *yaml.Node) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i], m.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Value != "type" {
			continue
		}
		if val.Kind != yaml.SequenceNode {
			continue
		}
		var survivors []*yaml.Node
		sawNull := false
		wellFormed := true
		for _, member := range val.Content {
			if member.Kind != yaml.ScalarNode {
				wellFormed = false
				break
			}
			if member.Tag == "!!null" || member.Value == "null" {
				sawNull = true
				continue
			}
			survivors = append(survivors, member)
		}
		if !wellFormed || !sawNull || len(survivors) != 1 {
			continue
		}
		// Second, narrower gate: the survivor must NAME a JSON Schema type.
		// Position is already handled by normalizeAt, which never calls this
		// inside a data block; this catches the rest — a `type:` sequence in
		// schema position holding something that is not a type name is not a
		// nullable type-array, whatever else it is.
		//
		// This gate alone would NOT be enough, and it is worth being precise
		// about why: it filters by value, and some values are both. This
		// spec's `Answer.type` enum contains `number` and `boolean`, which
		// are legal data AND JSON Schema type names, so `example: {type:
		// [number, "null"]}` is invisible to a value filter. Position is what
		// separates those; this is the belt to that pair of braces.
		if !isJSONSchemaTypeName(survivors[0].Value) {
			continue
		}
		// An anchored sequence is left alone. Swapping the node out orphans
		// every `*alias` pointing at it, and the emitted file then fails to
		// re-parse at all ("unknown anchor") — the one outcome this package
		// calls worse than not helping. Fail loudly in codegen instead.
		if val.Anchor != "" {
			continue
		}
		// Replace the sequence node in place with the surviving scalar. The
		// mapping's other entries, and their comments, are untouched.
		m.Content[i+1] = survivors[0]
		setNullable(m)
		// A nullable enum spells its null twice: once in the type array, once as
		// a member of `enum`. Collapsing the type array moves nullability onto
		// `nullable: true`, which codegen expresses as a POINTER — so the enum's
		// own null member is now redundant, and oapi-codegen renders it as a
		// bogus constant named ...LessThannil with the literal value "<nil>".
		// 1.29.0 made 42 of them. Dropping it is lossless: a JSON null
		// unmarshals to a nil pointer, never to the string "<nil>", so no legal
		// value stops round-tripping.
		dropNullEnumMember(m)
		return 1
	}
	return 0
}

// dropNullEnumMember removes a `null` member from the mapping's `enum`, if it
// has one. Called only after a nullable type-array was collapsed on the SAME
// mapping, so nullability is already carried by `nullable: true`.
//
// Leaves an anchored enum alone, for the reason the rest of this package does:
// swapping an anchored node out orphans its aliases and the emitted file fails
// to re-parse, which is worse than not helping. Never empties an enum either —
// `enum: [null]` alone carries real information (the field may only be null),
// and an empty enum means something different.
func dropNullEnumMember(m *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		key, val := m.Content[i], m.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Value != "enum" {
			continue
		}
		if val.Kind != yaml.SequenceNode || val.Anchor != "" {
			return
		}
		kept := make([]*yaml.Node, 0, len(val.Content))
		for _, member := range val.Content {
			// Key on the NULL TAG alone. Matching Value == "null" as well would
			// also delete a quoted `"null"`, which YAML tags !!str — a perfectly
			// legal member of a string enum, and a plausible one here since this
			// runs only on a nullable string enum. The loss would be silent: codegen
			// emits an enum missing a valid value and the CLI's description-derived
			// gate then refuses a value the server accepts.
			if member.Kind == yaml.ScalarNode && member.Anchor == "" && member.Tag == "!!null" {
				continue
			}
			kept = append(kept, member)
		}
		if len(kept) > 0 && len(kept) < len(val.Content) {
			val.Content = kept
		}
		return
	}
}

// isJSONSchemaTypeName reports whether s is one of the seven type names JSON
// Schema defines. Anything else in a `type:` sequence means the key is data,
// not a schema keyword — see rewriteNullableTypeArray.
func isJSONSchemaTypeName(s string) bool {
	switch s {
	case "string", "number", "integer", "boolean", "array", "object", "null":
		return true
	}
	return false
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

// -----------------------------------------------------------------------------
// Rule 3: hoist a colliding parameter schema into components.schemas.
//
// Unlike rules 1 and 2 this one RELOCATES a node instead of rewriting it in
// place, and it is scoped structurally rather than by a value matcher: it walks
// only parameter objects. That is deliberate. The two look-alike unions in this
// spec live in a request body and a component property, so a pass that cannot
// reach them cannot regress them, which is a stronger guarantee than a matcher
// that is careful enough to skip them.
// -----------------------------------------------------------------------------

// hoistCollidingParamSchemas rewrites every parameter whose inline schema would
// make oapi-codegen derive one name for both a union branch and that branch's
// item type. The schema moves to components.schemas under a derived name and
// the parameter keeps a $ref to it. Returns how many it moved.
//
// Convergence: after a hoist the parameter's schema is a `$ref`, so a second
// pass finds no `oneOf` there and moves nothing. The relocated schema now lives
// under components.schemas, which this pass never visits.
func hoistCollidingParamSchemas(doc *yaml.Node) int {
	root := documentRoot(doc)
	if root == nil {
		return 0
	}

	// Collect the work before touching anything: appending to
	// components.schemas while still walking paths would mutate a tree we are
	// iterating.
	type pending struct {
		param *yaml.Node // the parameter mapping
		stem  string     // derived name stem, e.g. "ListBookingsInclude"
	}
	var work []pending

	for _, p := range eachParameter(root) {
		schema := mapValue(p.param, "schema")
		if schema == nil || schema.Kind != yaml.MappingNode {
			continue
		}
		if !hasCollidingInlineArrayItems(schema) {
			continue
		}
		// Moving a subtree that owns an anchor can place that anchor AFTER an
		// alias pointing at it (components.schemas sits below paths in this
		// spec), and the emitted file then fails to re-parse — the one outcome
		// this package calls worse than not helping. Leave it for codegen.
		if containsAnchor(schema) {
			continue
		}
		work = append(work, pending{param: p.param, stem: p.stem()})
	}
	if len(work) == 0 {
		return 0
	}

	schemas := ensureComponentsSchemas(root)
	moved := 0
	for _, w := range work {
		schema := mapValue(w.param, "schema")
		if schema == nil {
			continue
		}

		// BOTH hoists are required, and each fixes a different failure.
		//
		// Naming the ITEMS removes the collision itself: codegen stops deriving
		// one name for the array branch and its item type. Verified: with only
		// this hoist the package compiles.
		//
		// Naming the PARAMETER SCHEMA makes the result usable. A union left
		// inline on a parameter generates an anonymous `struct{ union
		// json.RawMessage }` — an unexported field on a type that, being
		// anonymous, cannot carry the As/From helpers either, so no other
		// package can set it and the flag would be unreachable. Moved to a
		// component it becomes a NAMED type with full As/From/Merge helpers.
		//
		// Doing only the second one fails outright: the collision simply moves
		// with the schema and codegen reports a duplicate typename.
		// The detector (hasCollidingInlineArrayItems) and this rewriter each
		// evaluate the same four-clause predicate independently, and nothing
		// couples them. If they ever disagree, the parameter schema moves to
		// components.schemas WITHOUT its item type being named — the collision
		// travels with it and codegen reports a duplicate typename.
		//
		// The vendored-spec guard cannot see that: after the hoist the parameter
		// holds a $ref, so countCollidingParamSchemas scores 0 and the test
		// passes while `make codegen` breaks. So fail here instead, loudly, at
		// the one point where both verdicts are known.
		if hoistInlineEnumItems(schemas, schema, w.stem) == 0 {
			panic(fmt.Sprintf(
				"speccompat: %s was selected for hoisting but no inline enum items were moved — "+
					"hasCollidingInlineArrayItems and hoistInlineEnumItems disagree. Hoisting the parameter "+
					"schema alone moves the name collision with it and codegen fails on a duplicate typename.",
				w.stem))
		}

		name := uniqueSchemaName(schemas, w.stem+"Param")
		// Append `name: <the schema node>` to components.schemas, then leave a
		// $ref behind. The schema node itself is reused, not copied, so its
		// comments and ordering survive.
		schemas.Content = append(schemas.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
			schema,
		)
		setMapValue(w.param, "schema", &yaml.Node{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "$ref"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "#/components/schemas/" + name},
			},
		})
		moved++
	}
	return moved
}

// hoistInlineEnumItems moves every colliding inline `items` schema under this
// schema's array branches into components.schemas and leaves a $ref behind.
// Naming the item type is what actually removes the collision.
func hoistInlineEnumItems(schemas, schema *yaml.Node, stem string) int {
	moved := 0
	for _, kw := range []string{"oneOf", "anyOf"} {
		branches := mapValue(schema, kw)
		if branches == nil || branches.Kind != yaml.SequenceNode {
			continue
		}
		for _, b := range branches.Content {
			if b.Kind != yaml.MappingNode || scalarOf(mapValue(b, "type")) != "array" {
				continue
			}
			items := mapValue(b, "items")
			if items == nil || items.Kind != yaml.MappingNode || mapValue(items, "$ref") != nil {
				continue
			}
			e := mapValue(items, "enum")
			if e == nil || e.Kind != yaml.SequenceNode || len(e.Content) == 0 {
				continue
			}
			itemName := uniqueSchemaName(schemas, stem+"Item")
			schemas.Content = append(schemas.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: itemName},
				items,
			)
			setMapValue(b, "items", &yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "$ref"},
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "#/components/schemas/" + itemName},
				},
			})
			moved++
		}
	}
	return moved
}

// paramSite is one parameter object plus the context needed to name it.
type paramSite struct {
	param   *yaml.Node
	opID    string // operationId, when the parameter is declared on an operation
	method  string
	path    string
	compKey string // key under components.parameters, when declared there
}

// stem derives the name stem both hoisted schemas are built from: the parameter
// schema becomes <stem>Param and its item type <stem>Item. It depends only on
// identifiers already in the document and on ordered traversal, never on map
// iteration, so repeated runs over the same input produce byte-identical output
// — which `make codegen-check` requires, since it diffs the generated tree.
func (p paramSite) stem() string {
	pname := camelize(scalarOf(mapValue(p.param, "name")))
	switch {
	case p.opID != "":
		return camelize(p.opID) + pname
	case p.compKey != "":
		return camelize(p.compKey) + pname
	default:
		return camelize(p.method) + camelize(p.path) + pname
	}
}

// eachParameter yields every parameter object in the document, from both places
// OpenAPI allows one: inline on an operation, and shared under
// components.parameters. A `$ref` parameter is skipped — the thing it points at
// is visited directly, so handling it twice would be the bug.
func eachParameter(root *yaml.Node) []paramSite {
	var out []paramSite

	collect := func(seq *yaml.Node, opID, method, path string) {
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return
		}
		for _, p := range seq.Content {
			if p.Kind != yaml.MappingNode || mapValue(p, "$ref") != nil {
				continue
			}
			out = append(out, paramSite{param: p, opID: opID, method: method, path: path})
		}
	}

	if paths := mapValue(root, "paths"); paths != nil && paths.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(paths.Content); i += 2 {
			pathKey, item := paths.Content[i], paths.Content[i+1]
			if item.Kind != yaml.MappingNode {
				continue
			}
			// Path-level parameters apply to every operation under the path.
			collect(mapValue(item, "parameters"), "", "", pathKey.Value)
			for j := 0; j+1 < len(item.Content); j += 2 {
				mKey, op := item.Content[j], item.Content[j+1]
				if !isHTTPMethod(mKey.Value) || op.Kind != yaml.MappingNode {
					continue
				}
				collect(mapValue(op, "parameters"), scalarOf(mapValue(op, "operationId")), mKey.Value, pathKey.Value)
			}
		}
	}

	if comps := mapValue(root, "components"); comps != nil && comps.Kind == yaml.MappingNode {
		if params := mapValue(comps, "parameters"); params != nil && params.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(params.Content); i += 2 {
				key, p := params.Content[i], params.Content[i+1]
				if p.Kind != yaml.MappingNode {
					continue
				}
				out = append(out, paramSite{param: p, compKey: key.Value})
			}
		}
	}
	return out
}

// hasCollidingInlineArrayItems reports whether a schema is a union with an
// array branch whose `items` is an inline schema that oapi-codegen must give a
// generated NAME to. An enum is the case that occurs here and the only one
// verified to break; `$ref` items and bare primitives both generate fine, so
// they are left alone rather than guessed about.
func hasCollidingInlineArrayItems(schema *yaml.Node) bool {
	for _, kw := range []string{"oneOf", "anyOf"} {
		branches := mapValue(schema, kw)
		if branches == nil || branches.Kind != yaml.SequenceNode {
			continue
		}
		for _, b := range branches.Content {
			if b.Kind != yaml.MappingNode || scalarOf(mapValue(b, "type")) != "array" {
				continue
			}
			items := mapValue(b, "items")
			if items == nil || items.Kind != yaml.MappingNode {
				continue
			}
			if mapValue(items, "$ref") != nil {
				continue // named already; no collision
			}
			if e := mapValue(items, "enum"); e != nil && e.Kind == yaml.SequenceNode && len(e.Content) > 0 {
				return true
			}
		}
	}
	return false
}

// containsAnchor reports whether any node in the subtree carries a YAML anchor.
func containsAnchor(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Anchor != "" {
		return true
	}
	for _, c := range n.Content {
		if containsAnchor(c) {
			return true
		}
	}
	return false
}

// uniqueSchemaName returns base, or base with the smallest integer suffix that
// is not already a key under components.schemas. The scan is over an ordered
// Content slice, so the result is stable across runs.
func uniqueSchemaName(schemas *yaml.Node, base string) string {
	taken := func(name string) bool {
		for i := 0; i+1 < len(schemas.Content); i += 2 {
			if schemas.Content[i].Value == name {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s%d", base, i)
		if !taken(candidate) {
			return candidate
		}
	}
}

// ensureComponentsSchemas returns the components.schemas mapping, creating
// `components` and/or `schemas` if the document lacks them.
func ensureComponentsSchemas(root *yaml.Node) *yaml.Node {
	comps := mapValue(root, "components")
	if comps == nil || comps.Kind != yaml.MappingNode {
		comps = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapValue(root, "components", comps)
	}
	schemas := mapValue(comps, "schemas")
	if schemas == nil || schemas.Kind != yaml.MappingNode {
		schemas = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setMapValue(comps, "schemas", schemas)
	}
	return schemas
}

// documentRoot unwraps a DocumentNode to the mapping underneath it.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

// mapValue returns the value node for key in a mapping, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// setMapValue replaces key's value in place, or appends the pair when absent.
func setMapValue(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		val,
	)
}

// scalarOf returns a scalar node's value, or "" for anything else.
func scalarOf(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// isHTTPMethod reports whether a path-item key is an operation rather than
// `parameters`, `summary`, `$ref` or an extension.
func isHTTPMethod(k string) bool {
	switch k {
	case "get", "put", "post", "delete", "options", "head", "patch", "trace":
		return true
	}
	return false
}

// camelize turns an identifier or path fragment into UpperCamelCase, dropping
// every character a Go type name cannot carry. `/bookings/{id}` becomes
// `BookingsId`; `listBookings` becomes `ListBookings`.
func camelize(s string) string {
	var b strings.Builder
	upcomingUpper := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			if upcomingUpper {
				b.WriteRune(r - 32)
				upcomingUpper = false
			} else {
				b.WriteRune(r)
			}
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			upcomingUpper = false
		default:
			upcomingUpper = true
		}
	}
	return b.String()
}
