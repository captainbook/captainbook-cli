package api

// Endpoint defines the metadata for a statistics API endpoint.
type Endpoint struct {
	Name              string
	Path              string // URL path suffix under /api/v1/cli/statistics/
	Description       string
	ExtraFlags        []ExtraFlag
	ExcludeCommonFlags []string // Common flag names to exclude (e.g., "product_id" for gift-certificates)
}

// ExtraFlag defines an endpoint-specific CLI flag.
type ExtraFlag struct {
	Name    string
	Type    string // "string", "int", "bool"
	Desc    string
	Enum    []string
	Default string
}

// Endpoints is the metadata table for all 11 statistics endpoints.
var Endpoints = []Endpoint{
	{
		Name:        "revenue",
		Path:        "/revenue",
		Description: "Revenue statistics (gross/net revenue, commissions, tips, refunds)",
		ExtraFlags: []ExtraFlag{
			{Name: "payment-method", Type: "string", Desc: "Filter by payment method", Enum: []string{"card", "cash", "paypal", "gift", "voucher"}},
			{Name: "origin", Type: "string", Desc: "Filter by booking origin", Enum: []string{"WIDGET", "BACK_OFFICE", "MARKETPLACE", "WEBSITE_BUILDER", "CHANNEL_MANAGER"}},
			{Name: "day-of-week", Type: "string", Desc: "Comma-separated days (mon,tue,wed,thu,fri,sat,sun)"},
		},
	},
	{
		Name:        "bookings",
		Path:        "/bookings",
		Description: "Booking volume and status breakdown",
		ExtraFlags: []ExtraFlag{
			{Name: "status", Type: "string", Desc: "Filter by booking status", Enum: []string{"confirmed", "cancelled", "pending", "expired"}},
			{Name: "product-option-id", Type: "int", Desc: "Filter by product option ID"},
			{Name: "day-of-week", Type: "string", Desc: "Comma-separated days (mon,tue,wed,thu,fri,sat,sun)"},
			{Name: "time-from", Type: "string", Desc: "Filter availabilities starting from this time (HH:MM)"},
			{Name: "time-to", Type: "string", Desc: "Filter availabilities ending before this time (HH:MM)"},
		},
	},
	{
		Name:        "products",
		Path:        "/products",
		Description: "Product ranking statistics",
		ExtraFlags: []ExtraFlag{
			{Name: "sort-by", Type: "string", Desc: "Sort field", Enum: []string{"bookings", "revenue", "guests", "cancellation_rate"}, Default: "bookings"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10"},
		},
	},
	{
		Name:        "resources",
		Path:        "/resources",
		Description: "Resource utilisation rankings",
		ExtraFlags: []ExtraFlag{
			{Name: "resource-category", Type: "string", Desc: "Filter by resource category", Enum: []string{"GUIDE", "ASSET", "EQUIPMENT", "AUXILIARY"}},
			{Name: "sort-by", Type: "string", Desc: "Sort field", Enum: []string{"bookings", "revenue"}, Default: "bookings"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10"},
		},
	},
	{
		Name:        "customers",
		Path:        "/customers",
		Description: "Customer acquisition, retention, and top spenders",
		ExtraFlags: []ExtraFlag{
			{Name: "sort-by", Type: "string", Desc: "Sort top customers by", Enum: []string{"spending", "bookings", "recent"}, Default: "spending"},
			{Name: "returning-only", Type: "bool", Desc: "Only include returning customers in top list"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10"},
		},
	},
	{
		Name:        "channels",
		Path:        "/channels",
		Description: "Booking channel distribution",
	},
	{
		Name:        "occupancy",
		Path:        "/occupancy",
		Description: "Slot occupancy and capacity utilisation",
		ExtraFlags: []ExtraFlag{
			{Name: "product-option-id", Type: "int", Desc: "Filter by product option ID"},
		},
	},
	{
		Name:        "extras",
		Path:        "/extras",
		Description: "Extra/add-on sales performance",
		ExtraFlags: []ExtraFlag{
			{Name: "sort-by", Type: "string", Desc: "Sort field", Enum: []string{"times_sold", "revenue"}, Default: "times_sold"},
			{Name: "sort-direction", Type: "string", Desc: "Sort direction", Enum: []string{"asc", "desc"}, Default: "desc"},
			{Name: "limit", Type: "int", Desc: "Max items to return (1-50)", Default: "10"},
		},
	},
	{
		Name:        "discounts",
		Path:        "/discounts",
		Description: "Discount code usage statistics",
	},
	{
		Name:              "gift-certs",
		Path:              "/gift-certificates",
		Description:       "Gift certificate issuance and redemption metrics",
		ExcludeCommonFlags: []string{"product_id"},
	},
	{
		Name:        "summary",
		Path:        "/summary",
		Description: "Dashboard summary (aggregates key metrics in one call)",
	},
}

// HasExcludedFlag returns true if the given common flag name should be excluded for this endpoint.
func (e *Endpoint) HasExcludedFlag(flagName string) bool {
	for _, f := range e.ExcludeCommonFlags {
		if f == flagName {
			return true
		}
	}
	return false
}
