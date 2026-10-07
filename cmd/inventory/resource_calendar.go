package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	invpkg "github.com/captainbook/captainbook-cli/internal/inventory"
	"github.com/captainbook/captainbook-cli/internal/inventory/gen"
)

// resourceCalendarDefs declares the resource calendar: one read endpoint
// answering "when is this resource busy".
//
// --resource-id is the only required flag and the spec marks it required at the
// query level, so it is gated client-side: without it the server answers 422,
// and the round trip buys nothing.
func resourceCalendarDefs() []CommandDef {
	return []CommandDef{
		{
			Use: "resource-calendar list", Short: "List a resource's busy time",
			Kind: KindRead, Verb: "GET", Path: "/resource-calendar", Ability: invpkg.Read,
			Long: "Lists the windows one resource is occupied, across bookings and " +
				"explicit unavailability. Ids come from `resources list`.",
			Flags: []FlagDef{
				{Name: "resource-id", Type: "string", Required: true, Description: "The resource whose calendar to read"},
				{Name: "from", Type: "string", Description: "Window start (YYYY-MM-DD)"},
				{Name: "to", Type: "string", Description: "Window end (YYYY-MM-DD)"},
				// Comma-separated subset, not one value: the spec's own example is
				// `busy,unavailable`. See FlagDef.EnumList.
				{Name: "event-types", Type: "string", EnumList: true, Description: "booking|busy|unavailable (comma-separated subset; omit for all three)"},
				{Name: "product-option-id", Type: "int", Min: 1, Description: "Narrow to events of one product option"},
				{Name: "limit", Type: "int", Min: 1, Description: "Page size"},
				{Name: "cursor", Type: "string", Description: "Pagination cursor"},
			},
			Run: func(ctx context.Context, r *Runner, args RunArgs) (*RunResult, error) {
				rid := args.FlagString("resource-id")
				if rid == "" {
					return nil, fmt.Errorf("--resource-id is required")
				}
				p := &gen.ListResourceCalendarParams{ResourceId: rid}
				if v := args.FlagString("from"); v != "" {
					d, err := parseDate(v)
					if err != nil {
						return nil, fmt.Errorf("--from: %w", err)
					}
					p.From = &d
				}
				if v := args.FlagString("to"); v != "" {
					d, err := parseDate(v)
					if err != nil {
						return nil, fmt.Errorf("--to: %w", err)
					}
					p.To = &d
				}
				if v := args.FlagString("event-types"); v != "" {
					et := gen.ListResourceCalendarParamsEventTypes(v)
					p.EventTypes = &et
				}
				if args.FlagSet("product-option-id") {
					v := args.FlagInt("product-option-id")
					// Spec pins this to minimum 1; a 0 would otherwise be
					// dropped by the unset-guard and silently widen the page.
					p.ProductOptionId = &v
				}
				if v := args.FlagInt("limit"); v != 0 {
					p.Limit = &v
				}
				if v := args.FlagString("cursor"); v != "" {
					p.Cursor = &v
				}
				resp, err := r.Client.ListResourceCalendarWithResponse(ctx, p)
				if err != nil {
					return nil, err
				}
				res, perr := ParseGenResponse(resp.Body, resp.HTTPResponse, "ResourceCalendarEvent", "")
				if res != nil {
					// `unavailable_resources` sits OUTSIDE `data`, so the table and
					// csv renderers drop it — and the spec is explicit that its
					// presence "means the page is not the whole story for the ids it
					// names". Without a signal, a disconnected calendar or a window
					// past sync coverage renders as an empty list, which reads as
					// "this resource is free". That is the one answer this command
					// must never give by accident.
					res.Warnings = append(res.Warnings, incompleteCalendarWarnings(resp.Body)...)
				}
				return res, perr
			},
		},
	}
}

// incompleteCalendarWarnings reads the response's `unavailable_resources` map and
// returns one machine-readable stderr line per affected resource.
//
// Absence of the field means every requested resource was answered completely, so
// the quiet path stays quiet.
func incompleteCalendarWarnings(body []byte) []string {
	var envelope struct {
		UnavailableResources map[string][]string `json:"unavailable_resources"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.UnavailableResources) == 0 {
		return nil
	}

	// Sorted so the output is stable across runs; a map would otherwise reorder.
	ids := make([]string, 0, len(envelope.UnavailableResources))
	for id := range envelope.UnavailableResources {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		reasons := envelope.UnavailableResources[id]
		out = append(out, fmt.Sprintf(
			"RESOURCE_CALENDAR_INCOMPLETE resource_id=%s reasons=%s", id, strings.Join(reasons, ",")))
	}
	return out
}
