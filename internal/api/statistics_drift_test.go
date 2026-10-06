package api

// Statistics spec drift, both directions.
//
// Until this file existed, the statistics side of the CLI had NO external pin:
// `internal/api/endpoints.go` was a hand-written table and the only test over it
// asserted a literal count against the table that produced it. Nothing in the
// repo read a statistics spec. The cost was measured, not hypothetical — by the
// time it was noticed the CLI was missing 2 whole endpoints and ~30 filters, and
// `stats bookings --time-from` had been sending a query key the server never
// accepted since the first statistics commit, returning HTTP 200 with the period
// UNFILTERED and presenting it as filtered.
//
// TODOS.md records that exact lesson being learned once already and applied to
// the inventory lane only. These tests close it on this side, in the same shape:
//
//	TestStatisticsDrift_EveryEndpointIsBound  — spec→CLI: every documented metric
//	    has a subcommand, and every filter the metric applies is reachable.
//	TestStatisticsDrift_EveryFlagExistsInSpec — CLI→spec: every flag the CLI
//	    declares is a real parameter of that metric, with the right type and enum.
//
// Together they are a biconditional: neither direction alone can see absence.
//
// Both are allow-list driven, and an entry in an allow-list is a DECISION with a
// reason attached, not a suppression.

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/captainbook/captainbook-cli/internal/docscan"
)

// specPathPrefix is the canonical mount the spec documents. The CLI calls the
// `/api/v1/cli/statistics/*` alias of the same controllers (both are mounted in
// routes/api_statistics.php), so only the suffix is compared.
const specPathPrefix = "/api/v1/statistics"

// commonSpecParams are accepted by every metric and are not a per-metric
// question, so they are excluded from the filter comparison. The CLI declares
// them unconditionally in makeEndpointCmd.
var commonSpecParams = map[string]bool{
	"from":         true,
	"to":           true,
	"granularity":  true,
	"compare_from": true,
	"compare_to":   true,
}

// unexposedSpecParams are spec parameters the CLI deliberately does not offer.
// Key: "metric.param". Value: why not.
//
// Keep this empty unless there is a real reason. "Not implemented yet" is not a
// reason — it is the failure this test exists to report.
var unexposedSpecParams = map[string]string{}

type specParam struct {
	Name string
	Type string
	Enum []string
}

// loadStatisticsSpec returns metric path suffix → param name → param.
func loadStatisticsSpec(t *testing.T) map[string]map[string]specParam {
	t.Helper()
	raw, err := os.ReadFile("../../api/statistics/statistics-openapi.yml")
	if err != nil {
		t.Fatalf("read vendored statistics spec: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse vendored statistics spec: %v", err)
	}

	resolve := func(node any) map[string]any {
		m, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		for i := 0; i < 8; i++ {
			ref, ok := m["$ref"].(string)
			if !ok {
				return m
			}
			cur := any(doc)
			for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				cm, ok := cur.(map[string]any)
				if !ok {
					return nil
				}
				cur = cm[seg]
			}
			m, ok = cur.(map[string]any)
			if !ok {
				return nil
			}
		}
		return m
	}

	paths, _ := doc["paths"].(map[string]any)
	out := map[string]map[string]specParam{}
	for path, item := range paths {
		pm, _ := item.(map[string]any)
		op := resolve(pm["get"])
		if op == nil {
			continue
		}
		suffix := strings.TrimPrefix(path, specPathPrefix)
		params := map[string]specParam{}
		rawParams, _ := op["parameters"].([]any)
		for _, p := range rawParams {
			pr := resolve(p)
			if pr == nil {
				continue
			}
			name, _ := pr["name"].(string)
			if name == "" {
				continue
			}
			sch := resolve(pr["schema"])
			sp := specParam{Name: name}
			if sch != nil {
				sp.Type, _ = sch["type"].(string)
				if e, ok := sch["enum"].([]any); ok {
					for _, v := range e {
						sp.Enum = append(sp.Enum, fmt.Sprint(v))
					}
				}
			}
			params[name] = sp
		}
		out[suffix] = params
	}
	if len(out) == 0 {
		t.Fatal("no GET operations found in the vendored statistics spec; the loader is broken")
	}
	return out
}

// goType maps a spec parameter type to the CLI flag type used for it.
func goType(specType string) string {
	switch specType {
	case "integer":
		return "int"
	case "boolean":
		return "bool"
	default:
		return "string"
	}
}

func TestStatisticsDrift_EveryEndpointIsBound(t *testing.T) {
	spec := loadStatisticsSpec(t)

	byPath := map[string]*Endpoint{}
	for i := range Endpoints {
		byPath[Endpoints[i].Path] = &Endpoints[i]
	}

	// --- Direction 1a: every documented metric has a subcommand. ---
	var missing []string
	for suffix := range spec {
		if byPath[suffix] == nil {
			missing = append(missing, suffix)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d documented statistics endpoint(s) have no CLI subcommand:\n      %s\n\n"+
			"The spec is the contract; a metric with no subcommand is CLI surface that silently "+
			"does not exist. Wire it in internal/api/endpoints.go.",
			len(missing), strings.Join(missing, "\n      "))
	}

	// --- Direction 1b: and no subcommand invents a metric. ---
	var invented []string
	for path := range byPath {
		if _, ok := spec[path]; !ok {
			invented = append(invented, path)
		}
	}
	sort.Strings(invented)
	if len(invented) > 0 {
		t.Errorf("%d CLI subcommand(s) name a metric the spec does not document: %s",
			len(invented), strings.Join(invented, ", "))
	}

	// --- Direction 1c: every filter the metric applies is reachable. ---
	for suffix, params := range spec {
		ep := byPath[suffix]
		if ep == nil {
			continue
		}
		var unreachable []string
		for name := range params {
			if commonSpecParams[name] {
				continue
			}
			if _, isFilter := FilterDefs[name]; !isFilter {
				continue
			}
			if ep.SupportsFilter(name) {
				continue
			}
			if _, allowed := unexposedSpecParams[ep.Name+"."+name]; allowed {
				continue
			}
			unreachable = append(unreachable, name)
		}
		sort.Strings(unreachable)
		if len(unreachable) > 0 {
			t.Errorf("stats %s: spec documents filter(s) the CLI does not offer: %s\n"+
				"      Add them to this endpoint's Filters, or record them in unexposedSpecParams with a reason.",
				ep.Name, strings.Join(unreachable, ", "))
		}
	}

	// --- Direction 1d: non-filter parameters are exposed as ExtraFlags. ---
	for suffix, params := range spec {
		ep := byPath[suffix]
		if ep == nil {
			continue
		}
		declared := map[string]bool{}
		for _, f := range ep.ExtraFlags {
			declared[strings.ReplaceAll(f.Name, "-", "_")] = true
		}
		var unexposed []string
		for name := range params {
			if commonSpecParams[name] {
				continue
			}
			if _, isFilter := FilterDefs[name]; isFilter {
				continue
			}
			if declared[name] {
				continue
			}
			if _, allowed := unexposedSpecParams[ep.Name+"."+name]; allowed {
				continue
			}
			unexposed = append(unexposed, name)
		}
		sort.Strings(unexposed)
		if len(unexposed) > 0 {
			t.Errorf("stats %s: spec parameter(s) with no flag: %s\n"+
				"      Add an ExtraFlag, or record them in unexposedSpecParams with a reason.",
				ep.Name, strings.Join(unexposed, ", "))
		}
	}
}

func TestStatisticsDrift_EveryFlagExistsInSpec(t *testing.T) {
	spec := loadStatisticsSpec(t)

	for i := range Endpoints {
		ep := &Endpoints[i]
		params, ok := spec[ep.Path]
		if !ok {
			continue // reported by the other direction
		}

		// --- Direction 2a: every declared filter is a real parameter here. ---
		for _, f := range ep.Filters {
			sp, exists := params[f]
			if !exists {
				t.Errorf("stats %s declares filter %q, which the spec does NOT accept on this metric. "+
					"The server refuses an unsupported filter with 400, so this flag can only fail.",
					ep.Name, f)
				continue
			}
			def, known := FilterDefs[f]
			if !known {
				t.Errorf("stats %s declares filter %q with no entry in FilterDefs", ep.Name, f)
				continue
			}
			// Type agreement. channel_id is the one id the spec types as a
			// string; declaring it int would reject legitimate values.
			if want := goType(sp.Type); def.Type != want {
				t.Errorf("filter %q is declared %q but the spec types it %q (want %q) on stats %s",
					f, def.Type, sp.Type, want, ep.Name)
			}
			if len(sp.Enum) > 0 && !docscan.SameSet(def.Enum, sp.Enum) {
				t.Errorf("filter %q enum %v does not match the spec's %v on stats %s",
					f, def.Enum, sp.Enum, ep.Name)
			}
		}

		// --- Direction 2b: every ExtraFlag is a real parameter here. ---
		for _, f := range ep.ExtraFlags {
			param := strings.ReplaceAll(f.Name, "-", "_")
			sp, exists := params[param]
			if !exists {
				t.Errorf("stats %s declares --%s, which maps to %q — not a parameter the spec accepts on this metric",
					ep.Name, f.Name, param)
				continue
			}
			if want := goType(sp.Type); f.Type != want {
				t.Errorf("stats %s --%s is declared %q but the spec types it %q (want %q)",
					ep.Name, f.Name, f.Type, sp.Type, want)
			}
			if len(sp.Enum) > 0 && !docscan.SameSet(f.Enum, sp.Enum) {
				t.Errorf("stats %s --%s enum %v does not match the spec's %v",
					ep.Name, f.Name, f.Enum, sp.Enum)
			}
		}
	}
}

// TestStatisticsDrift_FilterDefsCoverTheVocabulary checks the vocabulary table
// itself: every filter named by any endpoint must have a FilterDef, and no
// FilterDef may go unused. An unused entry means a filter was dropped from every
// metric and the definition was left behind.
func TestStatisticsDrift_FilterDefsCoverTheVocabulary(t *testing.T) {
	used := map[string]bool{}
	for i := range Endpoints {
		for _, f := range Endpoints[i].Filters {
			used[f] = true
			if _, ok := FilterDefs[f]; !ok {
				t.Errorf("stats %s declares filter %q with no FilterDefs entry", Endpoints[i].Name, f)
			}
		}
	}
	for f := range FilterDefs {
		if !used[f] {
			t.Errorf("FilterDefs has an entry for %q that no endpoint uses — delete it or wire it up", f)
		}
	}
}

// TestStatisticsDrift_CommonParamsAreCommon verifies the ASSUMPTION the other
// two directions rest on.
//
// commonSpecParams excludes five parameters from both directions of the
// biconditional on the grounds that every metric accepts them. Nothing checked
// that. The CLI declares all five unconditionally and sends `granularity` on
// every single request — its default "day" is never empty — so if upstream
// narrowed any of them to a subset of metrics, every call to an excluded metric
// would fail while both drift directions stayed green. An exclusion list is a
// claim about the spec, and an unverified claim about the spec is the thing this
// file exists to make impossible.
//
// Value: protects=the five always-sent common params really are accepted by every metric in the spec; fails_when=upstream narrows from/to/granularity/compare_from/compare_to to a subset of metrics, which would 400 every call to the metrics that lost it; why_new=both existing directions SKIP these names, so neither can see the exclusion going wrong; seam=none
func TestStatisticsDrift_CommonParamsAreCommon(t *testing.T) {
	spec := loadStatisticsSpec(t)
	if len(spec) == 0 {
		t.Fatal("parsed no operations out of the vendored statistics spec")
	}

	checked := 0
	for _, ep := range Endpoints {
		params, ok := spec[ep.Path]
		if !ok {
			t.Errorf("%s: no GET operation at %q in the vendored spec", ep.Name, ep.Path)
			continue
		}
		for name := range commonSpecParams {
			if _, ok := params[name]; !ok {
				t.Errorf("%s (%s): commonSpecParams treats %q as accepted by every metric, but the spec does not declare it here.\n"+
					"      Both drift directions SKIP this name, so neither would notice. The CLI sends it anyway "+
					"(granularity unconditionally, its default \"day\" never being empty), so every call to this metric would be refused.",
					ep.Name, ep.Path, name)
				continue
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("verified 0 common params — the spec loader or the endpoint table is broken, not the spec")
	}
	t.Logf("verified %d metric/common-param pairs", checked)
}
