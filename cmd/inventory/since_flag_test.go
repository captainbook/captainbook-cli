package inventory

import (
	"sort"
	"strings"
	"testing"
)

// TestSinceFlagTracksSpecQueryParam is the guard for the --since churn this
// spec sync carried: five list endpoints DROPPED the parameter
// (categories, guests, pricing-categories, pricing-tiers, questions — their
// tables carry no created_at/updated_at at all, so the server now answers
// 422) and one GAINED it (product media, bounding media_updated_at).
//
// Both directions are silent failures, which is why this asserts equality
// rather than one-way coverage:
//
//   - A flag the spec no longer has is the worse half. Re-adding
//     `{Name: "since"}` to a FlagDef list compiles even when the closure
//     can't use it (gen.ListCategoriesParams has no Since field), so the
//     value is accepted, dropped on the floor, and the caller gets a full
//     unfiltered page that a polling client reads as "nothing changed".
//     TestSpecQueryParamsAreExposedAsFlags only looks for MISSING flags, so
//     nothing else in the suite sees this.
//   - A spec param with no flag is the reverse drift: media list shipped
//     without --since for exactly as long as nobody checked.
//
// The test is deliberately scoped to `since` rather than every query param:
// it is the one parameter this sync moved in both directions, and a
// blanket check would need an exemption list that hides the interesting case.
func TestSinceFlagTracksSpecQueryParam(t *testing.T) {
	spec := loadSpecDoc(t)

	var withFlag, withParam []string
	for _, cl := range walkInventoryCmdLits(t) {
		if cl.Verb != "GET" || cl.Path == "" {
			continue
		}
		op := spec.findOp(cl.Verb, cl.Path)
		if op == nil {
			t.Errorf("%s: %s %s is not in the spec at all", cl.Use, cl.Verb, cl.Path)
			continue
		}

		declared := false
		for _, f := range cl.Flags {
			if f.Name == "since" {
				declared = true
			}
		}
		_, inSpec := op.QueryParams["since"]

		label := cl.Use + " (" + cl.Verb + " " + cl.Path + ")"
		if declared {
			withFlag = append(withFlag, label)
		}
		if inSpec {
			withParam = append(withParam, label)
		}

		switch {
		case declared && !inSpec:
			t.Errorf("%s declares --since but the spec has no `since` query param.\n"+
				"    The server removed it and now answers 422; a flag left behind is "+
				"parsed, silently dropped, and the caller gets an unfiltered page they "+
				"read as an incremental sync.", label)
		case !declared && inSpec:
			t.Errorf("%s has a `since` query param in the spec but no --since flag.\n"+
				"    Incremental sync is unreachable from the CLI for this resource.", label)
		}
	}

	// The five removals are the point of this test; if the walker stopped
	// seeing GET CommandDefs the loop above would pass vacuously.
	for _, gone := range []string{
		"categories list", "guests list", "pricing-categories list",
		"pricing-tiers list", "questions list",
	} {
		for _, have := range withFlag {
			if strings.HasPrefix(have, gone+" ") {
				t.Errorf("%s must not declare --since (server 422s on it)", gone)
			}
		}
	}
	if len(withFlag) == 0 || len(withParam) == 0 {
		t.Fatalf("found %d commands with --since and %d spec params — the AST or spec "+
			"walker is broken, not the code", len(withFlag), len(withParam))
	}
	sort.Strings(withFlag)
	t.Logf("commands carrying --since (%d): %s", len(withFlag), strings.Join(withFlag, ", "))
}
