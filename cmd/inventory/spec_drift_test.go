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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
// to JSONBodyFromArgs (or to plain map[string]string{...}), and extracts
// the flag-name → json-key map.
func parseRunFieldMap(e ast.Expr) map[string]string {
	fl, ok := e.(*ast.FuncLit)
	if !ok {
		return nil
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
		if !ok || ident.Name != "JSONBodyFromArgs" {
			return true
		}
		if len(ce.Args) < 3 {
			return true
		}
		mapLit, ok := ce.Args[2].(*ast.CompositeLit)
		if !ok {
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
// Helpers — enum-token extraction, set comparison.
// -----------------------------------------------------------------------------

// extractEnumTokens returns the leading "tok|tok|tok" run of a description,
// split into individual tokens. Returns nil if the leading run is not
// pipe-delimited. Tokens accept [A-Za-z0-9_].
func extractEnumTokens(desc string) []string {
	desc = strings.TrimSpace(desc)
	if !strings.Contains(desc, "|") {
		return nil
	}
	end := 0
	for end < len(desc) {
		c := desc[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '|' {
			end++
			continue
		}
		break
	}
	head := desc[:end]
	if !strings.Contains(head, "|") {
		return nil
	}
	parts := strings.Split(head, "|")
	// Drop empty tokens (caused by a trailing/leading pipe).
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

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
				// IdempotencyKey?
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "IdempotencyKey" {
						return true // OK, key is set
					}
				}
				pos := fset.Position(cl.Pos())
				t.Errorf("[%s:%d] gen.%s literal does NOT set IdempotencyKey — audit/wire keys will diverge",
					short, pos.Line, name)
				return true
			})
		}
	}
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
