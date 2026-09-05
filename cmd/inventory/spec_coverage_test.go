package inventory

// Spec→CLI coverage. Every other drift test in this package runs the OTHER
// direction: it takes what the CLI already has and proves the spec agrees.
// That direction cannot see absence. A spec can gain whole endpoints and
// request fields and the suite stays green, because nothing the CLI declares
// has drifted — there is simply less of it than there should be.
//
// This was not hypothetical. Syncing api/inventory/cli-v1.yaml from 1.4.0 to
// 1.6.0 (+368 spec lines, +550 generated) and running the full suite produced
// zero failures, while `bookings set-resources` still modelled two resource
// kinds against a three-kind server and the entire product ticketing surface
// (delivery_method, redemption_method, confirm_ticket_reissue) was missing.
// Green tests were not evidence of sync.
//
// The two tests below close that hole:
//
//	TestSpecCoverage_EveryOperationIsBound  — every spec operation resolves to
//	    a command that binds its verb + path.
//	TestSpecCoverage_EveryRequestFieldIsReachable — every property of every
//	    request body is reachable through some flag's JSON key.
//
// Both are allow-list driven. An entry in the allow-list is a DECISION, not a
// suppression: it records that the CLI deliberately does not expose something,
// with the reason. Adding an entry should feel like writing a small ADR. That
// is the point — the default is coverage, and opting out costs a sentence.

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// unboundOps are spec operations the CLI deliberately does not expose.
// Key: "VERB /path". Value: why not.
//
// Keep this empty unless there is a real reason. "Not implemented yet" is not
// a reason to add an entry — it is the failure this test exists to report.
var unboundOps = map[string]string{}

// unreachableRequestFields are request-body properties no flag maps to.
// Key: "VERB /path.field". Value: why not.
var unreachableRequestFields = map[string]string{
	// The CLI declares --currency on exactly the three request schemas where
	// the spec lists currency in `required:`, and on none of the six that
	// declare it optionally. That asymmetry is deliberate and is locked from
	// the other direction by TestSpecDrift_CurrencyFlagTracksSpecRequirement:
	// none of those tables has a currency column, so the value is dropped on
	// persist, and since 1.4.0 it is validated against the account currency —
	// so on an optional endpoint sending it can only no-op or 422 the whole
	// write. --data reaches the field if a caller truly needs it.
	"PATCH /products/{id}.currency":             "optional-currency: see TestSpecDrift_CurrencyFlagTracksSpecRequirement",
	"PATCH /extras/{id}.currency":               "optional-currency: see TestSpecDrift_CurrencyFlagTracksSpecRequirement",
	"PATCH /pricing-tiers/{id}.currency":        "optional-currency: see TestSpecDrift_CurrencyFlagTracksSpecRequirement",
	"POST /pricing-tiers.currency":              "optional-currency: see TestSpecDrift_CurrencyFlagTracksSpecRequirement",
	"PATCH /gift-certs/available/{id}.currency": "optional-currency: see TestSpecDrift_CurrencyFlagTracksSpecRequirement",
	"POST /gift-certs/issued.currency":          "optional-currency: see TestSpecDrift_CurrencyFlagTracksSpecRequirement",

	// dry_run is not a user field: it is bound by the --dry-run global flag and
	// injected by JSONBodyFromArgs, never by a per-command field map.
	"*.dry_run": "bound by the global --dry-run flag, injected by JSONBodyFromArgs",

	// Accepted by the server and then thrown away. A flag here would be worse
	// than no flag: it would read as "set the location's timezone" in --help
	// while the value never lands anywhere. Spec: "Accepted but not stored —
	// no column." Use --data if you need to prove the round trip.
	"POST /locations.timezone":       "accepted but not stored — no column",
	"POST /locations.notes":          "accepted but not stored — no column",
	"PATCH /locations/{id}.timezone": "accepted but not stored — no column",
	"PATCH /locations/{id}.notes":    "accepted but not stored — no column",

	// Same shape: legacy aliases the controller accepts and ignores. Tiers
	// belong to a PricingCategory, and the pricing_tiers table has no name,
	// product_option_id or availability_id column at all.
	"POST /pricing-tiers.name":              "legacy alias, ignored by the controller",
	"POST /pricing-tiers.product_option_id": "legacy alias, ignored by the controller",
	"POST /pricing-tiers.availability_id":   "legacy alias, ignored by the controller",
	"PATCH /pricing-tiers/{id}.name":        "legacy alias, ignored by the controller",

	// Free-form object (additionalProperties: true). A scalar flag cannot
	// express it; --data is the honest route, and documenting a
	// --custom-attributes flag that never existed is a mistake this repo has
	// already made once (caught by TestSkillsDocDrift).
	"PATCH /guests/{id}.custom_attributes": "free-form object — --data only",

	// Reachable, just not as flags: `bulk-update` splits into five per-setting
	// subcommands, so `setting` IS the subcommand name and `new_value` is that
	// subcommand's typed flag (--is-bookable, --price, ...). Collapsing them
	// into two stringly-typed flags would lose the per-setting validation.
	"POST /availabilities/bulk-update.setting":   "encoded as the subcommand name",
	"POST /availabilities/bulk-update.new_value": "encoded as each subcommand's typed flag",
}

// boundOp is one (verb, path) pair the live command tree binds.
type boundOp struct {
	verb string
	path string
	cmd  string // command path, for error messages
}

// collectBoundOps walks the live cobra tree from Cmd() and returns every
// verb+path a leaf command binds. Walking the real tree (rather than the
// CommandDef AST) is deliberate: it is the tree the user actually gets, so a
// command that exists as a literal but is never wired in does not count as
// coverage.
func collectBoundOps(t *testing.T) map[string]boundOp {
	t.Helper()
	out := map[string]boundOp{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		verb, path := c.Annotations["verb"], c.Annotations["path"]
		if verb != "" && path != "" {
			out[verb+" "+path] = boundOp{verb: verb, path: path, cmd: c.CommandPath()}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(Cmd())
	return out
}

// TestSpecCoverage_EveryOperationIsBound fails when the spec declares an
// operation no command binds. This is the check that would have caught the
// 1.6.0 sync landing without `GET /bookings/{id}/resources/equipment/available`.
func TestSpecCoverage_EveryOperationIsBound(t *testing.T) {
	doc := loadSpecDoc(t)
	bound := collectBoundOps(t)

	var missing []string
	for key := range doc.ops {
		if _, ok := bound[key]; ok {
			continue
		}
		if reason, allowed := unboundOps[key]; allowed {
			t.Logf("allowed unbound operation: %s (%s)", key, reason)
			continue
		}
		missing = append(missing, key)
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("%d spec operation(s) have no command binding them:\n  %s\n\n"+
			"The spec is the contract; an operation with no command is CLI surface that "+
			"silently does not exist. Either wire the command, or add the operation to "+
			"unboundOps with the reason it is deliberately unexposed.",
			len(missing), strings.Join(missing, "\n  "))
	}

	// Guard the guard: if the spec parser or the tree walker silently returns
	// nothing, every assertion above passes vacuously.
	if len(doc.ops) == 0 {
		t.Fatal("parsed 0 operations from the spec — the parser is broken, not the CLI")
	}
	if len(bound) == 0 {
		t.Fatal("walked 0 bound operations from Cmd() — the tree walker is broken, not the spec")
	}
	t.Logf("verified %d spec operations against %d bound commands", len(doc.ops), len(bound))
}

// TestSpecCoverage_EveryRequestFieldIsReachable fails when a request-body
// property has no flag mapping to it. This is the check that would have caught
// 1.5.0 landing without --delivery-method / --redemption-method.
//
// It reads the SAME field maps TestSpecDrift_FieldMapKeysExistInSpec reads, so
// the two together are a biconditional: that test says every key the CLI sends
// is in the spec, this one says every field the spec accepts is sendable.
func TestSpecCoverage_EveryRequestFieldIsReachable(t *testing.T) {
	doc := loadSpecDoc(t)
	cmds := walkInventoryCmdLits(t)

	// Reachable fields per "VERB /path", by either route:
	//
	//   1. the FieldMap explicitly maps a flag to that JSON key, or
	//   2. a declared flag's name matches the field once kebab-case is
	//      converted to snake_case (--product-option-id -> product_option_id).
	//
	// Rule 2 exists because several commands build their body by hand rather
	// than through JSONBodyFromArgs — `availabilities bulk-delete` assigns
	// body["product_option_id"] directly, and bulk-update splits into five
	// per-setting subcommands. Those fields ARE reachable; they just never
	// appear in a field map. Matching on the flag name asks the question this
	// test actually cares about ("can a user set this without --data?")
	// instead of the narrower "is it in a field map?".
	//
	// Limitation, stated rather than hidden: rule 2 proves a flag EXISTS, not
	// that it reaches the wire. A declared-but-unwired flag would satisfy it.
	// That is a different bug class, and the field-map direction
	// (TestSpecDrift_FieldMapKeysExistInSpec) is what guards it.
	sent := map[string]map[string]bool{}
	for _, c := range cmds {
		if c.Verb == "" || c.Path == "" {
			continue
		}
		key := c.Verb + " " + c.Path
		if sent[key] == nil {
			sent[key] = map[string]bool{}
		}
		for _, jsonKey := range c.FieldMap {
			sent[key][jsonKey] = true
		}
		for _, f := range c.Flags {
			sent[key][strings.ReplaceAll(f.Name, "-", "_")] = true
		}
	}

	// Union in the LIVE tree's flags. Some commands are built by hand rather
	// than from a CommandDef literal (`availabilities bulk-update`, which splits
	// into five per-setting subcommands, and the multipart upload command), so
	// the AST walker above cannot see them at all — their fields would read as
	// missing when the flags are right there in --help.
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		verb, path := c.Annotations["verb"], c.Annotations["path"]
		if verb != "" && path != "" {
			key := verb + " " + path
			if sent[key] == nil {
				sent[key] = map[string]bool{}
			}
			c.LocalFlags().VisitAll(func(f *pflag.Flag) {
				sent[key][strings.ReplaceAll(f.Name, "-", "_")] = true
			})
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(Cmd())

	var missing []string
	checked := 0
	for key, op := range doc.ops {
		props := requestBodyProps(doc, op)
		if len(props) == 0 {
			continue
		}
		// An operation with a body but no command at all is already reported by
		// TestSpecCoverage_EveryOperationIsBound; don't double-report it here.
		if _, bound := sent[key]; !bound {
			continue
		}
		for _, field := range props {
			checked++
			if sent[key][field] {
				continue
			}
			if _, allowed := unreachableRequestFields["*."+field]; allowed {
				continue
			}
			if _, allowed := unreachableRequestFields[key+"."+field]; allowed {
				continue
			}
			missing = append(missing, fmt.Sprintf("%s → %s", key, field))
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("%d request-body field(s) the spec accepts have no flag:\n  %s\n\n"+
			"A field with no flag is only reachable through --data, which no agent "+
			"discovers from --help. Either add the flag, or add the field to "+
			"unreachableRequestFields with the reason it is deliberately omitted.",
			len(missing), strings.Join(missing, "\n  "))
	}

	if checked == 0 {
		t.Fatal("checked 0 request-body fields — the extractor is broken, not the CLI")
	}
	t.Logf("verified %d request-body fields across %d operations", checked, len(sent))
}

// requestBodyProps returns the flat property names of an operation's request
// body, resolving a $ref through the component schemas.
func requestBodyProps(doc *specDoc, op *opDef) []string {
	var flat map[string]*specField
	switch {
	case op.BodyRef != "":
		flat = doc.schemas[op.BodyRef]
	case len(op.BodyInline) > 0:
		flat = op.BodyInline
	}
	if len(flat) == 0 {
		return nil
	}
	out := make([]string, 0, len(flat))
	for name := range flat {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
