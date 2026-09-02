package inventory

// TestSpecDrift catches the bug class that has shipped four times during
// inventory CLI v1 review: drift between flag-name → JSON-key mappings
// and the spec, plus drift between enum tokens in flag descriptions and
// the spec enum. The test is fully static: it walks the AST of
// cmd/inventory/*.go to extract every CommandDef literal and the field
// map inside its Run closure, then cross-checks against the spec at
// api/inventory/cli-v1.yaml.
//
// Two assertions per command:
//   1. Every JSON key in JSONBodyFromArgs's third arg must be either a
//      property of the spec's request body schema OR a query parameter
//      on the operation. A typo (e.g. send_email vs send_now) fails
//      loudly.
//   2. Every FlagDef.Description with a leading "tok|tok|tok" run is
//      compared to the spec's enum list for the matching field
//      (case-sensitive, set-equal). A drifted description (e.g.
//      "confirmed|pending|cancelled" vs spec "ON_HOLD|CONFIRMED|...")
//      fails loudly.
//
// Bypasses: read-only commands without a JSONBodyFromArgs call are
// covered by Check 2 only. Hand-written outliers (bulk-update,
// uploadCmd) are covered separately because they don't sit inside a
// CommandDef literal.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// -----------------------------------------------------------------------------
// AST walker — extracts CommandDef literals from cmd/inventory/*.go.
// -----------------------------------------------------------------------------

type cmdLit struct {
	File     string
	Use      string
	Verb     string
	Path     string
	Ability  string // "cli:read" / "cli:write" / "cli:cs"; "" when ungated (whoami)
	Flags    []flagLit
	FieldMap map[string]string // flag name → JSON key, from JSONBodyFromArgs map literal
}

type flagLit struct {
	Name        string
	Description string
}

func walkInventoryCmdLits(t *testing.T) []cmdLit {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		// Skip _test.go files; they're not production CommandDefs.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.AllErrors)
	if err != nil {
		t.Fatalf("parser.ParseDir: %v", err)
	}
	var out []cmdLit
	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			short := filepath.Base(fname)
			ast.Inspect(file, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				switch {
				case isSliceOfCommandDefType(cl.Type):
					// `[]CommandDef{ {…}, {…} }` — inner Elts are
					// CommandDef literals with Type == nil; recurse.
					for _, elt := range cl.Elts {
						if inner, ok := elt.(*ast.CompositeLit); ok {
							if lit := extractCmdLit(short, inner); lit.Verb != "" {
								out = append(out, lit)
							}
						}
					}
					return false
				case isCommandDefType(cl.Type):
					// Standalone `CommandDef{…}` — used inside
					// bulkUpdateDef and similar helpers.
					if lit := extractCmdLit(short, cl); lit.Verb != "" {
						out = append(out, lit)
					}
					return false
				}
				return true
			})
		}
	}
	return out
}

func extractCmdLit(file string, cl *ast.CompositeLit) cmdLit {
	lit := cmdLit{File: file}
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch name.Name {
		case "Use":
			lit.Use = stringLit(kv.Value)
		case "Verb":
			lit.Verb = stringLit(kv.Value)
		case "Path":
			lit.Path = stringLit(kv.Value)
		case "Ability":
			lit.Ability = abilityConstValue(kv.Value)
		case "Flags":
			lit.Flags = parseFlagsLit(kv.Value)
		case "Run":
			lit.FieldMap = parseRunFieldMap(kv.Value)
		}
	}
	return lit
}

func isCommandDefType(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "CommandDef"
}

// isSliceOfCommandDefType matches `[]CommandDef`.
func isSliceOfCommandDefType(e ast.Expr) bool {
	at, ok := e.(*ast.ArrayType)
	if !ok || at.Len != nil {
		return false
	}
	return isCommandDefType(at.Elt)
}

func stringLit(e ast.Expr) string {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return ""
	}
	// Strip quotes; doesn't handle escape sequences but our descriptions
	// are plain ASCII enums so it's fine.
	s := bl.Value
	if len(s) >= 2 && (s[0] == '"' || s[0] == '`') {
		s = s[1 : len(s)-1]
	}
	return s
}

func parseFlagsLit(e ast.Expr) []flagLit {
	cl, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []flagLit
	for _, elt := range cl.Elts {
		fl, ok := elt.(*ast.CompositeLit)
		if !ok {
			continue
		}
		var f flagLit
		for _, fkv := range fl.Elts {
			kv, ok := fkv.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			id, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch id.Name {
			case "Name":
				f.Name = stringLit(kv.Value)
			case "Description":
				f.Description = stringLit(kv.Value)
			}
		}
		if f.Name != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseRunFieldMap walks a Run func literal's body, finds the first call
// to a body-builder helper that takes a flag-name → json-key map, and
// extracts the map. Recognized helpers:
//   - JSONBodyFromArgs(args, dryRun, fieldMap)              // arg[2]
//   - triggerOrStepBody(args, fieldMap)                     // arg[1]
//
// New helpers must be registered here, otherwise their field maps
// silently skip TestSpecDrift_FieldMapKeysExistInSpec.
func parseRunFieldMap(e ast.Expr) map[string]string {
	fl, ok := e.(*ast.FuncLit)
	if !ok {
		return nil
	}
	// Helper name → index of the field-map arg in the call's Args slice.
	helpers := map[string]int{
		"JSONBodyFromArgs":  2,
		"triggerOrStepBody": 1,
	}
	var found map[string]string
	ast.Inspect(fl.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := ce.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		mapArgIdx, ok := helpers[ident.Name]
		if !ok {
			return true
		}
		if len(ce.Args) <= mapArgIdx {
			return true
		}
		mapLit, ok := ce.Args[mapArgIdx].(*ast.CompositeLit)
		if !ok {
			// nil literal (Ident "nil") or a non-literal expression — no
			// statically-checkable field map. Treat as empty.
			return true
		}
		out := map[string]string{}
		for _, elt := range mapLit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k := stringLit(kv.Key)
			v := stringLit(kv.Value)
			if k != "" && v != "" {
				out[k] = v
			}
		}
		found = out
		return false
	})
	return found
}

// -----------------------------------------------------------------------------
// Spec parser — flattens api/inventory/cli-v1.yaml into op + field maps.
// -----------------------------------------------------------------------------

type specDoc struct {
	ops     map[string]*opDef                 // "VERB /path" → op
	schemas map[string]map[string]*specField  // ref name → field map
}

type opDef struct {
	Verb            string
	Path            string
	QueryParams     map[string]*specField // query-param name → field
	BodyRef         string                // "#/components/schemas/X" (empty if inline)
	BodyInline      map[string]*specField // populated when body is inline (not a $ref)
}

type specField struct {
	Type string
	Enum []string
	Ref  string // present when this property is itself a $ref
}

func loadSpecDoc(t *testing.T) *specDoc {
	t.Helper()
	data, err := os.ReadFile("../../api/inventory/cli-v1.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	doc := &specDoc{
		ops:     map[string]*opDef{},
		schemas: map[string]map[string]*specField{},
	}

	// Components / schemas — flatten properties for direct lookups.
	if comps, ok := raw["components"].(map[string]any); ok {
		if schemas, ok := comps["schemas"].(map[string]any); ok {
			for name, schema := range schemas {
				m, ok := schema.(map[string]any)
				if !ok {
					continue
				}
				flat := flattenSchema(m, raw)
				doc.schemas["#/components/schemas/"+name] = flat
			}
		}
	}

	// Paths × verbs.
	paths, _ := raw["paths"].(map[string]any)
	for path, pv := range paths {
		verbs, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		for verb, op := range verbs {
			if !isHTTPVerb(verb) {
				continue
			}
			opMap, ok := op.(map[string]any)
			if !ok {
				continue
			}
			d := &opDef{
				Verb:        strings.ToUpper(verb),
				Path:        path,
				QueryParams: map[string]*specField{},
			}
			// Parameters (query, path, header).
			if params, ok := opMap["parameters"].([]any); ok {
				for _, p := range params {
					pm := resolveRefMaybe(p, raw)
					if pm == nil {
						continue
					}
					if in, _ := pm["in"].(string); in != "query" {
						continue
					}
					name, _ := pm["name"].(string)
					if name == "" {
						continue
					}
					schema, _ := pm["schema"].(map[string]any)
					d.QueryParams[name] = parseSpecField(schema)
				}
			}
			// Request body.
			if rb, ok := opMap["requestBody"].(map[string]any); ok {
				if content, ok := rb["content"].(map[string]any); ok {
					if appj, ok := content["application/json"].(map[string]any); ok {
						if schema, ok := appj["schema"].(map[string]any); ok {
							if ref, _ := schema["$ref"].(string); ref != "" {
								d.BodyRef = ref
							} else {
								d.BodyInline = flattenSchema(schema, raw)
							}
						}
					}
				}
			}
			doc.ops[d.Verb+" "+d.Path] = d
		}
	}
	return doc
}

func isHTTPVerb(s string) bool {
	switch strings.ToLower(s) {
	case "get", "post", "patch", "put", "delete", "options", "head":
		return true
	}
	return false
}

func resolveRefMaybe(node any, root map[string]any) map[string]any {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	if ref, ok := m["$ref"].(string); ok && ref != "" {
		// Single-hop ref: "#/components/parameters/IdempotencyKey" etc.
		parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
		cur := any(root)
		for _, p := range parts {
			cm, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = cm[p]
		}
		if rm, ok := cur.(map[string]any); ok {
			return rm
		}
		return nil
	}
	return m
}

func flattenSchema(schema map[string]any, root map[string]any) map[string]*specField {
	out := map[string]*specField{}
	// Resolve $ref if the schema itself is a ref.
	schema = resolveRefMaybe(schema, root)
	if schema == nil {
		return out
	}
	// Direct properties.
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, prop := range props {
			pm, ok := prop.(map[string]any)
			if !ok {
				continue
			}
			out[name] = parseSpecField(pm)
		}
	}
	// allOf composition: merge each sub-schema's properties.
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, sub := range allOf {
			subMap := resolveRefMaybe(sub, root)
			for k, v := range flattenSchema(subMap, root) {
				out[k] = v
			}
		}
	}
	return out
}

func parseSpecField(schema map[string]any) *specField {
	if schema == nil {
		return &specField{}
	}
	f := &specField{}
	if t, ok := schema["type"].(string); ok {
		f.Type = t
	}
	if ref, ok := schema["$ref"].(string); ok {
		f.Ref = ref
	}
	if enum, ok := schema["enum"].([]any); ok {
		for _, v := range enum {
			if s, ok := v.(string); ok {
				f.Enum = append(f.Enum, s)
			}
		}
	}
	return f
}

func (s *specDoc) findOp(verb, path string) *opDef {
	return s.ops[verb+" "+path]
}

// bodyField returns the spec field for a JSON key on the request body,
// resolving the BodyRef into components/schemas if needed.
func (s *specDoc) bodyField(op *opDef, jsonKey string) *specField {
	if op == nil {
		return nil
	}
	if op.BodyInline != nil {
		if f, ok := op.BodyInline[jsonKey]; ok {
			return f
		}
	}
	if op.BodyRef != "" {
		if schema, ok := s.schemas[op.BodyRef]; ok {
			if f, ok := schema[jsonKey]; ok {
				return f
			}
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Helpers — set comparison. extractEnumTokens lives in inventory.go now
// because makeRunE uses it for client-side flag validation.
// -----------------------------------------------------------------------------

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func kebabToSnake(s string) string { return strings.ReplaceAll(s, "-", "_") }

// -----------------------------------------------------------------------------
// The actual tests.
// -----------------------------------------------------------------------------

// TestSpecDrift_FieldMapKeysExistInSpec asserts every JSON key in every
// JSONBodyFromArgs map literal is a real property of the corresponding
// spec request body (or, where the closure misuses the map for query
// params, a query parameter on the operation). dry_run is exempt — it's
// injected by the helper independent of the field map.
func TestSpecDrift_FieldMapKeysExistInSpec(t *testing.T) {
	spec := loadSpecDoc(t)
	cmds := walkInventoryCmdLits(t)
	if len(cmds) == 0 {
		t.Fatal("no CommandDef literals found — AST walker broken")
	}
	for _, c := range cmds {
		// Look up the spec op for EVERY command (not just ones with a
		// non-empty FieldMap). A typo in Verb/Path is its own bug class
		// — the audit log records a non-existent endpoint and forensic
		// correlation with server access logs breaks. Skipping
		// FieldMap-less commands here would let those slip through.
		op := spec.findOp(c.Verb, c.Path)
		if op == nil {
			t.Errorf("[%s] %q: no spec operation for %s %s (verb/path typo? — audit will record a non-existent endpoint)",
				c.File, c.Use, c.Verb, c.Path)
			continue
		}
		for flagName, jsonKey := range c.FieldMap {
			if jsonKey == "dry_run" {
				continue
			}
			if spec.bodyField(op, jsonKey) != nil {
				continue
			}
			if _, ok := op.QueryParams[jsonKey]; ok {
				continue
			}
			t.Errorf("[%s] %q (%s %s): flag --%s maps to JSON key %q but spec has no such body property or query param",
				c.File, c.Use, c.Verb, c.Path, flagName, jsonKey)
		}
	}
}

// TestSpecDrift_IdempotencyKeyThreaded asserts every gen.<Mutation>Params
// literal in cmd/inventory/*.go sets the IdempotencyKey field. Without
// it, the transport's idempotencyKeyRT mints a SECOND UUIDv7 on the
// wire, audit logs a different key from what hits the server, and
// forensic correlation breaks. This is the structural shape of the
// "every mutation closure must thread the key" rule.
//
// The set of "mutation Params" is derived statically from the gen
// package: any Params struct that declares a field literally typed
// `*IdempotencyKey` is a mutation params and MUST have the field set
// at construction.
func TestSpecDrift_IdempotencyKeyThreaded(t *testing.T) {
	mutationParams := mutationParamsTypes(t)
	if len(mutationParams) == 0 {
		t.Fatal("no mutation Params types found — gen package walker broken")
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.AllErrors)
	if err != nil {
		t.Fatalf("parser.ParseDir: %v", err)
	}
	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			short := filepath.Base(fname)
			ast.Inspect(file, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				name, isGen := genParamsName(cl.Type)
				if !isGen {
					return true
				}
				if !mutationParams[name] {
					return true
				}
				// Found a mutation Params literal — does it set
				// IdempotencyKey to a non-trivial value? `nil`,
				// missing, or a wrong value all break audit/wire
				// correlation; only `args.IdempotencyKeyUUID` (used
				// from runMutation) and `&parsedKey` (the multipart
				// upload outlier's local) are accepted.
				var keyValue ast.Expr
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "IdempotencyKey" {
						keyValue = kv.Value
						break
					}
				}
				pos := fset.Position(cl.Pos())
				if keyValue == nil {
					t.Errorf("[%s:%d] gen.%s literal does NOT set IdempotencyKey — audit/wire keys will diverge",
						short, pos.Line, name)
					return true
				}
				if !isAcceptedIdempotencyKeyExpr(keyValue) {
					t.Errorf("[%s:%d] gen.%s.IdempotencyKey is %s — must be args.IdempotencyKeyUUID (or the multipart outlier's &parsedKey)",
						short, pos.Line, name, exprToString(keyValue))
				}
				return true
			})
		}
	}
}

// isAcceptedIdempotencyKeyExpr returns true when the given expression
// is one of the two known-correct value expressions for Params.IdempotencyKey:
//   - args.IdempotencyKeyUUID (the runMutation thread)
//   - &parsedKey (the uploadCmd multipart outlier's local)
// Anything else (nil, &someOtherVar, helper-call, etc.) is a bug.
func isAcceptedIdempotencyKeyExpr(e ast.Expr) bool {
	if se, ok := e.(*ast.SelectorExpr); ok {
		if x, ok := se.X.(*ast.Ident); ok && x.Name == "args" && se.Sel.Name == "IdempotencyKeyUUID" {
			return true
		}
	}
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op.String() == "&" {
		if id, ok := u.X.(*ast.Ident); ok && id.Name == "parsedKey" {
			return true
		}
	}
	return false
}

func exprToString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprToString(v.X) + "." + v.Sel.Name
	case *ast.UnaryExpr:
		return v.Op.String() + exprToString(v.X)
	case *ast.BasicLit:
		return v.Value
	}
	return "<unrecognized expr>"
}

// mutationParamsTypes returns the set of gen.<Name>Params type names
// that declare an IdempotencyKey field. Statically derived from
// internal/inventory/gen/inventory.gen.go via a small AST walk so the
// test stays accurate as the spec evolves.
func mutationParamsTypes(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../internal/inventory/gen/inventory.gen.go", nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse gen file: %v", err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		if !strings.HasSuffix(ts.Name.Name, "Params") {
			return true
		}
		for _, field := range st.Fields.List {
			if len(field.Names) == 0 || field.Names[0].Name != "IdempotencyKey" {
				continue
			}
			// Must be *IdempotencyKey to count.
			star, ok := field.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); ok && id.Name == "IdempotencyKey" {
				out[ts.Name.Name] = true
			}
		}
		return true
	})
	return out
}

// genParamsName matches `gen.NameParams` in a CompositeLit.Type and
// returns "NameParams". Returns "", false when the type isn't a gen
// package selector.
func genParamsName(e ast.Expr) (string, bool) {
	se, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := se.X.(*ast.Ident)
	if !ok || pkg.Name != "gen" {
		return "", false
	}
	if !strings.HasSuffix(se.Sel.Name, "Params") {
		return "", false
	}
	return se.Sel.Name, true
}

// TestSpecDrift_FlagDescriptionEnumsMatchSpec asserts every FlagDef
// whose Description starts with a "tok|tok|tok" run matches the spec
// enum at the corresponding field. Catches the booking-status /
// gift-cert-status / transaction-type drift class.
func TestSpecDrift_FlagDescriptionEnumsMatchSpec(t *testing.T) {
	spec := loadSpecDoc(t)
	cmds := walkInventoryCmdLits(t)
	for _, c := range cmds {
		op := spec.findOp(c.Verb, c.Path)
		if op == nil {
			continue // already reported by the FieldMap test
		}
		for _, f := range c.Flags {
			tokens := extractEnumTokens(f.Description)
			if tokens == nil {
				continue
			}
			// Resolve the flag to a spec field.
			jsonKey := c.FieldMap[f.Name]
			if jsonKey == "" {
				jsonKey = kebabToSnake(f.Name)
			}
			var specEnum []string
			if sf := spec.bodyField(op, jsonKey); sf != nil && sf.Enum != nil {
				specEnum = sf.Enum
			} else if qp, ok := op.QueryParams[jsonKey]; ok && qp.Enum != nil {
				specEnum = qp.Enum
			}
			if specEnum == nil {
				// Spec doesn't constrain this field with an enum — the
				// description's pipes are documenting valid client values
				// but the server doesn't enforce. Skip.
				continue
			}
			if !sameSet(tokens, specEnum) {
				t.Errorf("[%s] %q (%s %s): flag --%s description tokens %v don't match spec enum %v",
					c.File, c.Use, c.Verb, c.Path, f.Name, tokens, specEnum)
			}
		}
	}
}

// abilityConstValue maps the `Ability:` field's AST expression to the
// wire string the constant carries. The field is written as a qualified
// constant (`invpkg.CS`) in every production CommandDef; anything else
// returns "" and is reported as unrecognized by the caller rather than
// silently passing.
func abilityConstValue(e ast.Expr) string {
	se, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if x, ok := se.X.(*ast.Ident); !ok || x.Name != "invpkg" {
		return ""
	}
	switch se.Sel.Name {
	case "Read":
		return "cli:read"
	case "Write":
		return "cli:write"
	case "CS":
		return "cli:cs"
	}
	return ""
}

// TestSpecDrift_AbilitiesMatchSpec locks each CommandDef's Ability to the
// spec. Abilities are hand-mirrored (D34) because the spec states them in
// prose rather than as an enum, and that hand-mirroring has now drifted
// twice: bookings cancel (issue #19) and gift-certs void (spec 1.1.0 moved
// it to cli:cs while the CommandDef kept cli:write and a comment citing a
// spec line number that had since moved).
//
// A wrong Ability breaks in both directions. Too permissive and preflight
// waves a token through into a server 403, wasting the local gate that
// exists precisely to avoid that round trip. Too restrictive and we refuse
// a call the server would have allowed.
//
// Two checks, because the spec exposes the cli:cs set two different ways
// and neither alone is sufficient:
//
//  1. Per-operation: an operation whose `description` says "Requires the
//     `cli:X` ability" pins every CommandDef bound to that (verb, path).
//     Exact, but the spec only annotates a couple of operations.
//  2. Cardinality: the securitySchemes prose commits to a specific number
//     of cli:cs routes ("the five routes gated on `abilities:cli:cs`").
//     Coarse, but it covers the routes check 1 says nothing about — a
//     route silently entering or leaving the CS set moves the count.
//
// Check 2 counts distinct (verb, path) pairs, not CommandDefs: resend
// confirmation is deliberately exposed as two commands (`bookings
// resend-confirmation` and `notifications resend`) over one route.
func TestSpecDrift_AbilitiesMatchSpec(t *testing.T) {
	cmds := walkInventoryCmdLits(t)
	if len(cmds) == 0 {
		t.Fatal("no CommandDef literals found — AST walker broken")
	}

	// --- Check 1: operations that name their required ability in prose. ---
	for opKey, ability := range specDeclaredAbilities(t) {
		matched := 0
		for _, c := range cmds {
			if c.Verb+" "+c.Path != opKey {
				continue
			}
			matched++
			if c.Ability != ability {
				got := c.Ability
				if got == "" {
					got = "(unset or unrecognized)"
				}
				t.Errorf("[%s] %q (%s): binds Ability %s, but the spec's operation description requires %s",
					c.File, c.Use, opKey, got, ability)
			}
		}
		if matched == 0 {
			t.Errorf("spec declares %s on %s but no CommandDef binds that route", ability, opKey)
		}
	}

	// --- Check 2: the cli:cs route count the spec commits to. ---
	want := specCSRouteCount(t)
	routes := map[string]bool{}
	for _, c := range cmds {
		if c.Ability == "cli:cs" {
			routes[c.Verb+" "+c.Path] = true
		}
	}
	if len(routes) != want {
		var got []string
		for r := range routes {
			got = append(got, r)
		}
		sort.Strings(got)
		t.Errorf("CLI binds %d distinct cli:cs routes, spec says %d.\nBound: %v\n"+
			"Either a route moved between cli:write and cli:cs, or the spec's count changed — reconcile against the securitySchemes ability table.",
			len(routes), want, got)
	}
}

// specDeclaredAbilities returns "VERB /path" → "cli:x" for every operation
// whose description states its required ability outright.
func specDeclaredAbilities(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("../../api/inventory/cli-v1.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	out, err := parseSpecDeclaredAbilities(data)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

// declaredAbilityRe matches the phrasing the spec uses when it pins an
// operation's ability inline: "Requires the `cli:cs` ability".
var declaredAbilityRe = regexp.MustCompile("Requires the `(cli:[a-z]+)` ability")

// parseSpecDeclaredAbilities is the pure half of specDeclaredAbilities: spec
// bytes in, "VERB /path" → "cli:x" out. Split from the testing.T wrapper so
// the regexp and the empty-result guard are directly unit-testable — the
// guard exists to catch a spec rewording, and a guard that has never been
// exercised is a guard nobody has checked.
func parseSpecDeclaredAbilities(data []byte) (map[string]string, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("yaml.Unmarshal: %w", err)
	}
	out := map[string]string{}
	paths, _ := raw["paths"].(map[string]any)
	for path, pv := range paths {
		verbs, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		for verb, op := range verbs {
			if !isHTTPVerb(verb) {
				continue
			}
			opMap, ok := op.(map[string]any)
			if !ok {
				continue
			}
			desc, _ := opMap["description"].(string)
			if m := declaredAbilityRe.FindStringSubmatch(desc); m != nil {
				out[strings.ToUpper(verb)+" "+path] = m[1]
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no operation description declares a required ability — the spec's phrasing changed; re-anchor declaredAbilityRe")
	}
	return out, nil
}

// specCSRouteCount reads the number of cli:cs routes out of the ability
// table in the securitySchemes description ("the five routes gated on
// `abilities:cli:cs`"). Written as an English numeral in prose, so it is
// matched against a small word list.
//
// A miss is a hard failure, not a skip: the whole point of this test is
// that an unnoticed change in the CS set ships. If the spec rewords the
// sentence, that is exactly when a human should re-read the table.
func specCSRouteCount(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("../../api/inventory/cli-v1.yaml")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	n, err := parseCSRouteCount(data)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return n
}

// csRouteCountRe matches the sentence in the securitySchemes ability table
// that commits to a specific number of cli:cs routes.
var csRouteCountRe = regexp.MustCompile("the ([a-z]+) routes gated on `abilities:cli:cs`")

// numeralWords maps the English numerals the spec plausibly uses. Both this
// map and csRouteCountRe are the failure-prone parts of the cardinality
// check, so parseCSRouteCount is split out and unit-tested directly.
var numeralWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
	"six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10,
}

func parseCSRouteCount(data []byte) (int, error) {
	m := csRouteCountRe.FindSubmatch(data)
	if m == nil {
		return 0, errors.New("could not find the cli:cs route count in the spec's ability table — the wording changed; re-read the securitySchemes table and re-anchor csRouteCountRe")
	}
	n, ok := numeralWords[string(m[1])]
	if !ok {
		return 0, fmt.Errorf("cli:cs route count %q is not a numeral this test knows — extend numeralWords", m[1])
	}
	return n, nil
}

// TestSpecDriftHelpers_ParseAbilityAnnotations covers the parsing that
// TestSpecDrift_AbilitiesMatchSpec depends on, including the guards that
// only fire when the spec's prose changes. Those guards are the reason the
// drift check can't silently pass on a reworded spec, so they get fixtures
// rather than waiting for a real rewording to exercise them.
func TestSpecDriftHelpers_ParseAbilityAnnotations(t *testing.T) {
	t.Run("declared abilities: happy path", func(t *testing.T) {
		got, err := parseSpecDeclaredAbilities([]byte(`
paths:
  /gift-certs/issued/{id}/void:
    post:
      description: |
        Requires the ` + "`cli:cs`" + ` ability. Voiding kills an instrument.
    get:
      description: No ability stated here.
  /products:
    post:
      description: |
        Requires the ` + "`cli:write`" + ` ability.
`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]string{
			"POST /gift-certs/issued/{id}/void": "cli:cs",
			"POST /products":                    "cli:write",
		}
		if len(got) != len(want) {
			t.Fatalf("got %d entries %v, want %d %v", len(got), got, len(want), want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("got[%q] = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("declared abilities: rewording is an error, not an empty pass", func(t *testing.T) {
		// The exact failure mode the guard exists for: a spec that still
		// describes the ability, in words the regexp no longer matches.
		_, err := parseSpecDeclaredAbilities([]byte(`
paths:
  /gift-certs/issued/{id}/void:
    post:
      description: This endpoint needs the cli:cs scope.
`))
		if err == nil {
			t.Fatal("expected an error when no description matches the phrasing, got nil — a reworded spec would silently pass the drift check")
		}
		if !strings.Contains(err.Error(), "re-anchor") {
			t.Errorf("error %q should tell the maintainer to re-anchor the regexp", err)
		}
	})

	t.Run("declared abilities: malformed yaml", func(t *testing.T) {
		if _, err := parseSpecDeclaredAbilities([]byte("paths: [unclosed")); err == nil {
			t.Fatal("expected a yaml error, got nil")
		}
	})

	t.Run("cs route count: happy path", func(t *testing.T) {
		n, err := parseCSRouteCount([]byte("plus issued gift-cert **void** — the five routes gated on `abilities:cli:cs` (they sit in two groups)."))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 5 {
			t.Errorf("got %d, want 5", n)
		}
	})

	t.Run("cs route count: rewording is an error", func(t *testing.T) {
		_, err := parseCSRouteCount([]byte("the 5 routes gated on `abilities:cli:cs`"))
		if err == nil {
			t.Fatal("expected an error when the numeral is a digit, got nil")
		}
		if !strings.Contains(err.Error(), "re-anchor") {
			t.Errorf("error %q should tell the maintainer to re-anchor the regexp", err)
		}
	})

	t.Run("cs route count: unknown numeral", func(t *testing.T) {
		_, err := parseCSRouteCount([]byte("the eleven routes gated on `abilities:cli:cs`"))
		if err == nil {
			t.Fatal("expected an error for a numeral outside the word list, got nil")
		}
		if !strings.Contains(err.Error(), "numeralWords") {
			t.Errorf("error %q should name the map to extend", err)
		}
	})
}

// TestSpecDriftHelpers_AbilityConstValue covers the AST→wire-string mapping,
// including the unrecognized-expression fallback that no production
// CommandDef reaches today (all of them write invpkg.Read/Write/CS). A
// CommandDef that stopped using the qualified constant would otherwise read
// as ability "" and slip past the mismatch check as a false pass.
func TestSpecDriftHelpers_AbilityConstValue(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"invpkg.Read", "cli:read"},
		{"invpkg.Write", "cli:write"},
		{"invpkg.CS", "cli:cs"},
		{"invpkg.Unknown", ""}, // new constant the mapper doesn't know
		{"inventory.CS", ""},   // different package qualifier
		{"CS", ""},             // dot-imported / unqualified
		{`"cli:cs"`, ""},       // raw string instead of the constant
	}
	for _, tc := range cases {
		e, err := parser.ParseExpr(tc.expr)
		if err != nil {
			t.Fatalf("ParseExpr(%q): %v", tc.expr, err)
		}
		if got := abilityConstValue(e); got != tc.want {
			t.Errorf("abilityConstValue(%s) = %q, want %q", tc.expr, got, tc.want)
		}
	}
}
