package api

import (
	"slices"
	"strings"
)

// Endpoint defines the metadata for a statistics API endpoint.
type Endpoint struct {
	Name string
	// Path is the URL suffix under /api/v1/cli/statistics/. The spec documents
	// only the canonical /api/v1/statistics/* mount; routes/api_statistics.php
	// mounts both, and the CLI uses the /cli alias so it shares one base URL
	// with the inventory client.
	Path        string
	Description string
	// Filters is the per-metric filter vocabulary this endpoint actually
	// applies. It is a positive list, not a set of exclusions, because that is
	// how the server models it — see the Filters block below.
	Filters    []string
	ExtraFlags []ExtraFlag
}

// ExtraFlag defines an endpoint-specific CLI flag.
type ExtraFlag struct {
	Name    string
	Type    string // "string", "int", "bool"
	Desc    string
	Enum    []string
	Default string

	// Min and Max bound an int flag, mirroring the spec's minimum/maximum.
	// Both are inclusive; zero means unbounded on that side.
	//
	// Max exists because the lower bound alone was enforced: `--limit` advertised
	// "(1-50)" and refused 0, but passed 100 straight through to a server whose
	// spec caps it at 50. The caller then got exit 18 with a truncated body for a
	// value the CLI's own help text had already told them was out of range.
	Min int
	Max int
}

// -----------------------------------------------------------------------------
// Filters
//
// The server REFUSES a filter a metric does not implement — 400, not a silent
// drop. `MetricFilters::assertSupported` was added precisely because dropping
// in silence produced wrong answers: `metric=browsing, product_id=12` used to
// answer with every product's funnel labelled as that one product's.
//
// So this is not advisory. A flag offered on a metric that refuses it turns a
// deliberate server-side refusal into a routine user error, which is why each
// endpoint below names the filters it supports and `stats` declares flags from
// that list rather than uniformly.
//
// The vocabulary and the per-metric sets mirror
// app/Support/Statistics/MetricFilters.php in the server repo. They are
// asserted against the vendored spec by the statistics drift tests rather than
// trusted, because a hand-kept copy is correct only on the day it is typed.
// -----------------------------------------------------------------------------

// Filter names, spelled exactly as the statistics API's query parameters.
const (
	FilterBusinessUnitID = "business_unit_id"
	FilterProductID      = "product_id"
	FilterChannelID      = "channel_id"
	FilterLocationID     = "location_id"
	FilterResourceID     = "resource_id"
	FilterDayOfWeek      = "day_of_week"
	FilterTimeOfDayFrom  = "time_of_day_from"
	FilterTimeOfDayTo    = "time_of_day_to"
	FilterDateBasis      = "date_basis"
)

// FilterDef describes how one filter reaches the CLI surface.
type FilterDef struct {
	Type string // "int" or "string"
	Desc string
	Enum []string
}

// FilterDefs is the whole filter vocabulary.
//
// channel_id is the one id that is a STRING in the spec, not an integer. An int
// flag here would reject legitimate values, which is the same class of mistake
// the inventory lane hit when its flags claimed `string` against integer params.
var FilterDefs = map[string]FilterDef{
	FilterBusinessUnitID: {Type: "int", Desc: "Filter by business unit"},
	FilterProductID:      {Type: "int", Desc: "Filter by product"},
	FilterChannelID:      {Type: "string", Desc: "Filter by channel (string id, from the channels metric)"},
	FilterLocationID:     {Type: "int", Desc: "Filter by location"},
	FilterResourceID:     {Type: "int", Desc: "Filter by resource (ids come from the resources metric)"},
	FilterDayOfWeek:      {Type: "string", Desc: "Comma-separated days (mon,tue,wed,thu,fri,sat,sun). It is the weekday of whatever the metric counts: the trip departs for bookings and channels, the payment arrives for revenue, the visit happens for browsing"},
	FilterTimeOfDayFrom:  {Type: "string", Desc: "Departure time lower bound (HH:MM), read off the trip's departure"},
	FilterTimeOfDayTo:    {Type: "string", Desc: "Departure time upper bound (HH:MM), read off the trip's departure"},
	FilterDateBasis:      {Type: "string", Desc: "Which date the period bounds", Enum: []string{"booking", "trip"}},
}

// Common filter groupings, named so the per-metric lists below read as the
// server's own groupings rather than as repeated literals.
var (
	// What applyBookingFilters() narrows on, plus the resource join.
	filtersBookingGrain = []string{
		FilterBusinessUnitID, FilterProductID, FilterChannelID, FilterLocationID, FilterResourceID,
	}
	// Booking-grain metrics that additionally honour the trip/booking basis.
	filtersWithDateBasis = append(append([]string{}, filtersBookingGrain...), FilterDateBasis)
)

// Endpoints is the metadata table for all 13 statistics endpoints.
var Endpoints = []Endpoint{
	{
		Name:        "revenue",
		Path:        "/revenue",
		Description: "Revenue statistics (gross/net revenue, commissions, tips, refunds)",
		// No date_basis: every figure is dated by booking_transactions.created_at,
		// the instant the money arrived. A trip-dated revenue total is a
		// different metric, reachable as the revenue column of products or
		// resources under --date-basis trip.
		Filters: append(append([]string{}, filtersBookingGrain...), FilterDayOfWeek),
		ExtraFlags: []ExtraFlag{
			{Name: "payment-method", Type: "string", Desc: "Filter by payment method", Enum: []string{"card", "cash", "paypal", "gift", "voucher"}},
			{Name: "origin", Type: "string", Desc: "Filter by booking origin", Enum: []string{"WIDGET", "BACK_OFFICE", "MARKETPLACE", "WEBSITE_BUILDER", "CHANNEL_MANAGER"}},
		},
	},
	{
		Name:        "bookings",
		Path:        "/bookings",
		Description: "Booking volume and status breakdown",
		// The only metric that takes the time-of-day window; it reads the
		// departure via availabilities.from.
		Filters: append(append([]string{}, filtersWithDateBasis...),
			FilterDayOfWeek, FilterTimeOfDayFrom, FilterTimeOfDayTo),
		ExtraFlags: []ExtraFlag{
			{Name: "status", Type: "string", Desc: "Filter by booking status", Enum: []string{"confirmed", "cancelled", "pending", "expired"}},
			{Name: "product-option-id", Type: "int", Desc: "Filter by product option ID"},
		},
	},
	{
		Name:        "products",
		Path:        "/products",
		Description: "Product ranking statistics",
		Filters:     filtersWithDateBasis,
		ExtraFlags: []ExtraFlag{
			{Name: "sort-by", Type: "string", Desc: "Sort field", Enum: []string{"bookings", "revenue", "guests", "cancellation_rate"}, Default: "bookings"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10", Min: 1, Max: 50},
		},
	},
	{
		Name:        "resources",
		Path:        "/resources",
		Description: "Resource utilisation rankings",
		Filters:     filtersWithDateBasis,
		ExtraFlags: []ExtraFlag{
			{Name: "resource-category", Type: "string", Desc: "Filter by resource category", Enum: []string{"GUIDE", "ASSET", "EQUIPMENT", "AUXILIARY"}},
			{Name: "sort-by", Type: "string", Desc: "Sort field", Enum: []string{"bookings", "revenue"}, Default: "bookings"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10", Min: 1, Max: 50},
		},
	},
	{
		Name:        "customers",
		Path:        "/customers",
		Description: "Customer acquisition, retention, and top spenders",
		Filters:     filtersWithDateBasis,
		ExtraFlags: []ExtraFlag{
			{Name: "sort-by", Type: "string", Desc: "Sort top customers by", Enum: []string{"spending", "bookings", "recent"}, Default: "spending"},
			{Name: "returning-only", Type: "bool", Desc: "Only include returning customers in top list"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10", Min: 1, Max: 50},
		},
	},
	{
		Name:        "locations",
		Path:        "/locations",
		Description: "Location ranking statistics",
		Filters:     filtersWithDateBasis,
		ExtraFlags: []ExtraFlag{
			{Name: "sort-by", Type: "string", Desc: "Sort field", Enum: []string{"bookings", "revenue", "guests"}, Default: "bookings"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10", Min: 1, Max: 50},
		},
	},
	{
		Name:        "channels",
		Path:        "/channels",
		Description: "Booking channel distribution",
		// day_of_week here is the DEPARTURE's, matching bookings rather than
		// inventing a third reading beside it and revenue's. No date_basis: it
		// is booking-grain and the trip basis is deferred upstream.
		Filters: append(append([]string{}, filtersBookingGrain...), FilterDayOfWeek),
	},
	{
		Name:        "occupancy",
		Path:        "/occupancy",
		Description: "Slot occupancy and capacity utilisation",
		// Counts SLOTS, so it joins no bookings: no channel to filter on, and no
		// resource either (booking_resource records who WORKED a booking, and an
		// empty slot has nobody). It refuses date_basis because it has no second
		// basis to offer — it is dated by the departure already — and day_of_week
		// because it GROUPS by weekday.
		Filters: []string{FilterBusinessUnitID, FilterProductID, FilterLocationID},
		ExtraFlags: []ExtraFlag{
			{Name: "product-option-id", Type: "int", Desc: "Filter by product option ID"},
		},
	},
	{
		Name:        "extras",
		Path:        "/extras",
		Description: "Extra/add-on sales performance",
		Filters:     filtersBookingGrain,
		ExtraFlags: []ExtraFlag{
			{Name: "sort-by", Type: "string", Desc: "Sort field", Enum: []string{"times_sold", "revenue"}, Default: "times_sold"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10", Min: 1, Max: 50},
		},
	},
	{
		Name:        "discounts",
		Path:        "/discounts",
		Description: "Discount code usage statistics",
		Filters:     filtersBookingGrain,
	},
	{
		Name:        "gift-certs",
		Path:        "/gift-certificates",
		Description: "Gift certificate issuance and redemption metrics",
		// A certificate is an amount, not a trip: no product, channel, location
		// or resource, and nothing to date by a departure.
		Filters: []string{FilterBusinessUnitID},
	},
	{
		Name:        "browsing",
		Path:        "/browsing",
		Description: "Browsing funnel and visitor behaviour",
		// Reads customer_events, which joins no bookings, so there is no product,
		// channel, location or resource to scope by. day_of_week is the exception
		// and the only one it could take: a visit has its own instant, so the
		// weekday is a property of the row rather than of something joined to it.
		Filters: []string{FilterBusinessUnitID, FilterDayOfWeek},
	},
	{
		Name:        "summary",
		Path:        "/summary",
		Description: "Dashboard summary (aggregates key metrics in one call)",
		// The INTERSECTION of its sections, not their union. Five would honour
		// channel_id and occupancy cannot, so a channel-scoped summary would
		// report the whole business's occupancy rate beside channel-scoped
		// revenue in one object. An internally inconsistent answer is worse than
		// a refused one; resource_id and date_basis are refused for the same
		// reason.
		Filters: []string{FilterBusinessUnitID, FilterProductID, FilterLocationID},
	},
}

// SupportsFilter reports whether this endpoint applies the named filter.
//
// cmd/stats.go ranges over Filters directly in both the flag-declaration and the
// value-collection loop, so this exists for callers asking about a single filter —
// today, the tests that pin the matrix against the vendored spec.
func (e *Endpoint) SupportsFilter(name string) bool {
	return slices.Contains(e.Filters, name)
}

// FlagName converts a filter's API parameter name to its CLI flag name.
// `time_of_day_from` becomes `time-of-day-from`.
func FlagName(filter string) string {
	return strings.ReplaceAll(filter, "_", "-")
}
